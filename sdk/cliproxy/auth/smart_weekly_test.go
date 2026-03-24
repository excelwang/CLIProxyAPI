package auth

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type stubWeeklyQuotaProvider struct {
	snapshots map[string]WeeklyQuotaSnapshot
}

func (p *stubWeeklyQuotaProvider) WeeklyQuotaSnapshots(_ context.Context, authIDs []string) map[string]WeeklyQuotaSnapshot {
	if p == nil || len(p.snapshots) == 0 {
		return nil
	}
	out := make(map[string]WeeklyQuotaSnapshot)
	for _, authID := range authIDs {
		if snapshot, ok := p.snapshots[authID]; ok {
			out[authID] = snapshot
		}
	}
	return out
}

func TestSmartWeeklyRespectsPriority(t *testing.T) {
	t.Parallel()

	model := "gpt-5-codex"
	high := newSmartWeeklyTestAuth("smart-weekly-high", 10)
	low := newSmartWeeklyTestAuth("smart-weekly-low", 1)
	registerSmartWeeklyTestModel(t, high.ID, model)
	registerSmartWeeklyTestModel(t, low.ID, model)

	scheduler := newAuthScheduler(&SmartWeeklySelector{})
	scheduler.setWeeklyQuotaProvider(&stubWeeklyQuotaProvider{
		snapshots: map[string]WeeklyQuotaSnapshot{
			high.ID: {
				AuthID:         high.ID,
				RemainingRatio: 0.30,
				ResetAt:        time.Now().UTC().Add(8 * time.Hour),
				ObservedAt:     time.Now().UTC(),
			},
			low.ID: {
				AuthID:         low.ID,
				RemainingRatio: 0.90,
				ResetAt:        time.Now().UTC().Add(30 * time.Minute),
				ObservedAt:     time.Now().UTC(),
			},
		},
	})
	scheduler.upsertAuth(high)
	scheduler.upsertAuth(low)

	picked, errPick := scheduler.pickSingle(context.Background(), "codex", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle returned error: %v", errPick)
	}
	if picked == nil || picked.ID != high.ID {
		t.Fatalf("picked auth = %v, want %s", picked, high.ID)
	}
}

func TestSmartWeeklyProtectsLowWeeklyBalance(t *testing.T) {
	t.Parallel()

	model := "gpt-5-codex"
	protected := newSmartWeeklyTestAuth("smart-weekly-protected", 0)
	preferred := newSmartWeeklyTestAuth("smart-weekly-preferred", 0)
	registerSmartWeeklyTestModel(t, protected.ID, model)
	registerSmartWeeklyTestModel(t, preferred.ID, model)

	now := time.Now().UTC()
	scheduler := newAuthScheduler(&SmartWeeklySelector{})
	scheduler.setWeeklyQuotaProvider(&stubWeeklyQuotaProvider{
		snapshots: map[string]WeeklyQuotaSnapshot{
			protected.ID: {
				AuthID:         protected.ID,
				RemainingRatio: 0.05,
				ResetAt:        now.Add(15 * time.Minute),
				ObservedAt:     now,
			},
			preferred.ID: {
				AuthID:         preferred.ID,
				RemainingRatio: 0.40,
				ResetAt:        now.Add(4 * time.Hour),
				ObservedAt:     now,
			},
		},
	})
	scheduler.upsertAuth(protected)
	scheduler.upsertAuth(preferred)

	picked, errPick := scheduler.pickSingle(context.Background(), "codex", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle returned error: %v", errPick)
	}
	if picked == nil || picked.ID != preferred.ID {
		t.Fatalf("picked auth = %v, want %s", picked, preferred.ID)
	}
}

func TestSmartWeeklyUsesProtectedPoolWhenNecessary(t *testing.T) {
	t.Parallel()

	model := "gpt-5-codex"
	sooner := newSmartWeeklyTestAuth("smart-weekly-sooner", 0)
	later := newSmartWeeklyTestAuth("smart-weekly-later", 0)
	registerSmartWeeklyTestModel(t, sooner.ID, model)
	registerSmartWeeklyTestModel(t, later.ID, model)

	now := time.Now().UTC()
	scheduler := newAuthScheduler(&SmartWeeklySelector{})
	scheduler.setWeeklyQuotaProvider(&stubWeeklyQuotaProvider{
		snapshots: map[string]WeeklyQuotaSnapshot{
			sooner.ID: {
				AuthID:         sooner.ID,
				RemainingRatio: 0.03,
				ResetAt:        now.Add(20 * time.Minute),
				ObservedAt:     now,
			},
			later.ID: {
				AuthID:         later.ID,
				RemainingRatio: 0.04,
				ResetAt:        now.Add(2 * time.Hour),
				ObservedAt:     now,
			},
		},
	})
	scheduler.upsertAuth(sooner)
	scheduler.upsertAuth(later)

	picked, errPick := scheduler.pickSingle(context.Background(), "codex", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle returned error: %v", errPick)
	}
	if picked == nil || picked.ID != sooner.ID {
		t.Fatalf("picked auth = %v, want %s", picked, sooner.ID)
	}
}

func TestSmartWeeklyWarmupOverridesPriorityOnce(t *testing.T) {
	t.Parallel()

	model := "gpt-5-codex"
	high := newSmartWeeklyTestAuth("smart-weekly-warmup-high", 10)
	low := newSmartWeeklyTestAuth("smart-weekly-warmup-low", 1)
	registerSmartWeeklyTestModel(t, high.ID, model)
	registerSmartWeeklyTestModel(t, low.ID, model)

	now := time.Now().UTC()
	initialReset := now.Add(2 * time.Hour)
	provider := &stubWeeklyQuotaProvider{
		snapshots: map[string]WeeklyQuotaSnapshot{
			high.ID: {
				AuthID:         high.ID,
				RemainingRatio: 0.60,
				ResetAt:        initialReset,
				ObservedAt:     now,
			},
			low.ID: {
				AuthID:         low.ID,
				RemainingRatio: 0.60,
				ResetAt:        initialReset,
				ObservedAt:     now,
			},
		},
	}

	scheduler := newAuthScheduler(&SmartWeeklySelector{})
	scheduler.setWeeklyQuotaProvider(provider)
	scheduler.upsertAuth(high)
	scheduler.upsertAuth(low)

	first, errPick := scheduler.pickSingle(context.Background(), "codex", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("first pickSingle returned error: %v", errPick)
	}
	if first == nil || first.ID != high.ID {
		t.Fatalf("first picked auth = %v, want %s", first, high.ID)
	}

	provider.snapshots[low.ID] = WeeklyQuotaSnapshot{
		AuthID:         low.ID,
		RemainingRatio: 0.90,
		ResetAt:        now.Add(7 * 24 * time.Hour),
		ObservedAt:     now.Add(-10 * time.Minute),
	}

	second, errPick := scheduler.pickSingle(context.Background(), "codex", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("second pickSingle returned error: %v", errPick)
	}
	if second == nil || second.ID != low.ID {
		t.Fatalf("second picked auth = %v, want %s", second, low.ID)
	}

	third, errPick := scheduler.pickSingle(context.Background(), "codex", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("third pickSingle returned error: %v", errPick)
	}
	if third == nil || third.ID != high.ID {
		t.Fatalf("third picked auth = %v, want %s", third, high.ID)
	}
}

func newSmartWeeklyTestAuth(id string, priority int) *Auth {
	return &Auth{
		ID:       id,
		Provider: "codex",
		Status:   StatusActive,
		Attributes: map[string]string{
			"priority": strconv.Itoa(priority),
		},
	}
}

func registerSmartWeeklyTestModel(t *testing.T, authID, model string) {
	t.Helper()
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})
}
