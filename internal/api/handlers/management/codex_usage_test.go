package management

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func seedTestCodexUsageState(h *Handler, state *codexUsageState) *Handler {
	if h == nil || state == nil {
		return h
	}
	runtime := h.codexUsageStateRef()
	if runtime == nil {
		return h
	}
	runtime.codexUsageByAuth = state.codexUsageByAuth
	runtime.codexUsageCompat = state.codexUsageCompat
	runtime.codexUsageSummary = state.codexUsageSummary
	runtime.codexUsageHasData = state.codexUsageHasData
	runtime.codexUsageSelected = state.codexUsageSelected
	runtime.codexObservedServiceTierAuthID = state.codexObservedServiceTierAuthID
	runtime.codexObservedServiceTier = state.codexObservedServiceTier
	runtime.codexObservedServiceTierAt = state.codexObservedServiceTierAt
	if state.codexUsageAsyncPoll.Load() {
		runtime.codexUsageAsyncPoll.Store(true)
	}
	return h
}

func TestCodexUsagePollIntervalMatchesCodexCLI(t *testing.T) {
	if codexUsagePollInterval != 60*time.Second {
		t.Fatalf("expected poll interval 60s, got %s", codexUsagePollInterval)
	}
}

func TestHydrateCodexAuthUsageStatuses_MarksChangedWhenPriorityDiffers(t *testing.T) {
	current := map[string]codexAuthUsageStatus{
		"auth-1": {AuthID: "auth-1", Priority: 0},
	}
	auths := map[string]*coreauth.Auth{
		"auth-1": {ID: "auth-1", FileName: "codex-auth-1.json", Attributes: map[string]string{"priority": "2"}},
	}
	changed := hydrateCodexAuthUsageStatuses(current, auths)
	if !changed {
		t.Fatal("expected hydrate to report changed when priority differs")
	}
	if got := current["auth-1"].Priority; got != 2 {
		t.Fatalf("expected priority 2 after hydrate, got %d", got)
	}
}

func TestBuildCodexUsageExtensions_PopulatesPriorityFromAuthFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "codex-auth.json")
	if err := os.WriteFile(file, []byte(`{"priority":5}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	exts := buildCodexUsageExtensions(map[string]codexAuthUsageStatus{
		"codex-auth.json": {
			AuthID:       "codex-auth.json",
			FileName:     "codex-auth.json",
			PlanType:     "team",
			Status:       "ok",
			Priority:     0,
			LastPolledAt: time.Now().UTC(),
		},
	}, time.Now().UTC(), 0.2, 6.0, dir, nil, "", "", "", time.Time{})
	if exts == nil || len(exts.ActiveAuthFiles) != 1 {
		t.Fatal("expected one active auth file extension item")
	}
	if got := exts.ActiveAuthFiles[0].Priority; got != 5 {
		t.Fatalf("expected extension priority 5, got %d", got)
	}
}

func TestCloneCodexUsageExtensions_CopiesPriority(t *testing.T) {
	input := &codexUsageExtensions{
		ActiveAuthFiles: []codexUsageAuthFileExtensionItem{{
			AuthID:   "auth-1",
			FileName: "auth-1.json",
			Account:  "priority@example.com",
			PlanType: "team",
			Priority: 9,
			Status:   "ok",
		}},
	}
	cloned := cloneCodexUsageExtensions(input)
	if cloned == nil || len(cloned.ActiveAuthFiles) != 1 {
		t.Fatal("expected one cloned auth file extension item")
	}
	if got := cloned.ActiveAuthFiles[0].Priority; got != 9 {
		t.Fatalf("expected cloned priority 9, got %d", got)
	}
}

func TestBuildCodexUsageExtensions_PopulatesPriorityFromAuthLookup(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "codex-auth.json",
		FileName: "codex-auth.json",
		Metadata: map[string]any{"priority": 7},
	}
	exts := buildCodexUsageExtensions(map[string]codexAuthUsageStatus{
		"codex-auth.json": {
			AuthID:       "codex-auth.json",
			FileName:     "codex-auth.json",
			PlanType:     "team",
			Status:       "ok",
			Priority:     0,
			LastPolledAt: time.Now().UTC(),
		},
	}, time.Now().UTC(), 0.2, 6.0, "", map[string]*coreauth.Auth{"codex-auth.json": auth}, "", "", "", time.Time{})
	if exts == nil || len(exts.ActiveAuthFiles) != 1 {
		t.Fatal("expected one active auth file extension item")
	}
	if got := exts.ActiveAuthFiles[0].Priority; got != 7 {
		t.Fatalf("expected extension priority 7 from auth lookup, got %d", got)
	}
}

func TestApplyCodexAuthUsageIdentity_PopulatesPriorityFromFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "codex-auth.json")
	if err := os.WriteFile(file, []byte(`{"priority":5}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	auth := &coreauth.Auth{ID: "auth-3", FileName: file, Attributes: map[string]string{"path": file}}
	status := applyCodexAuthUsageIdentity(codexAuthUsageStatus{}, auth)
	if status.Priority != 5 {
		t.Fatalf("expected priority 5 from file, got %d", status.Priority)
	}
}

func TestApplyCodexAuthUsageIdentity_PopulatesPriorityFromMetadata(t *testing.T) {
	auth := &coreauth.Auth{ID: "auth-2", FileName: "codex-auth-2.json", Metadata: map[string]any{"priority": float64(3)}}
	status := applyCodexAuthUsageIdentity(codexAuthUsageStatus{}, auth)
	if status.Priority != 3 {
		t.Fatalf("expected priority 3 from metadata, got %d", status.Priority)
	}
}

func TestApplyCodexAuthUsageIdentity_PopulatesPriority(t *testing.T) {
	auth := &coreauth.Auth{ID: "auth-1", FileName: "codex-auth-1.json", Attributes: map[string]string{"priority": "7"}}
	status := applyCodexAuthUsageIdentity(codexAuthUsageStatus{}, auth)
	if status.Priority != 7 {
		t.Fatalf("expected priority 7, got %d", status.Priority)
	}
}

func TestCodexPlanWeight_FreeIsPointTwo(t *testing.T) {
	if got := codexPlanWeight("free", 0.2, 6.0); got != 0.2 {
		t.Fatalf("expected free plan weight 0.2, got %v", got)
	}
	if got := codexPlanWeight("business", 0.2, 6.0); got != 1.0 {
		t.Fatalf("expected business plan weight 1.0, got %v", got)
	}
	if got := codexPlanWeight("pro", 0.2, 6.0); got != 6.0 {
		t.Fatalf("expected pro plan weight 6.0, got %v", got)
	}
}

func TestCodexEffectiveMainRateLimit_WeeklyExhaustedForcesBothWindowsDepleted(t *testing.T) {
	status := codexAuthUsageStatus{
		Status: "ok",
		Usage: &codexUsagePayload{
			PlanType: "team",
			RateLimit: &codexUsageRateLimit{
				Allowed:      false,
				LimitReached: true,
				PrimaryWindow: &codexUsageWindow{
					UsedPercent:        0,
					LimitWindowSeconds: 18000,
				},
				SecondaryWindow: &codexUsageWindow{
					UsedPercent:        100,
					LimitWindowSeconds: 604800,
					ResetAfterSeconds:  12345,
					ResetAt:            1890000000,
				},
			},
		},
	}

	got := codexEffectiveMainRateLimit(time.Now().UTC(), status)
	if got == nil || got.PrimaryWindow == nil || got.SecondaryWindow == nil {
		t.Fatalf("expected both windows, got %+v", got)
	}
	if got.PrimaryWindow.UsedPercent != 100 || got.SecondaryWindow.UsedPercent != 100 {
		t.Fatalf("expected both windows used_percent=100, got primary=%d secondary=%d", got.PrimaryWindow.UsedPercent, got.SecondaryWindow.UsedPercent)
	}
}

func TestCodexEffectiveMainRateLimit_AuthFailureForcesBothWindowsDepleted(t *testing.T) {
	status := codexAuthUsageStatus{
		Status: "error",
		Error:  "usage request failed: status=401 body={\"error\":{\"code\":\"token_invalidated\"}}",
	}

	got := codexEffectiveMainRateLimit(time.Now().UTC(), status)
	if got == nil || got.PrimaryWindow == nil || got.SecondaryWindow == nil {
		t.Fatalf("expected both windows for auth failure, got %+v", got)
	}
	if got.PrimaryWindow.UsedPercent != 100 || got.SecondaryWindow.UsedPercent != 100 {
		t.Fatalf("expected both windows used_percent=100, got primary=%d secondary=%d", got.PrimaryWindow.UsedPercent, got.SecondaryWindow.UsedPercent)
	}
}

func TestCodexEffectiveMainRateLimit_NonAuthErrorKeepsCachedUsage(t *testing.T) {
	status := codexAuthUsageStatus{
		Status: "error",
		Error:  "Get \"https://chatgpt.com/backend-api/wham/usage\": EOF",
		Usage: &codexUsagePayload{
			PlanType: "free",
			RateLimit: &codexUsageRateLimit{
				PrimaryWindow: &codexUsageWindow{
					UsedPercent:        20,
					LimitWindowSeconds: 18000,
				},
				SecondaryWindow: &codexUsageWindow{
					UsedPercent:        90,
					LimitWindowSeconds: 604800,
				},
			},
		},
	}

	got := codexEffectiveMainRateLimit(time.Now().UTC(), status)
	if got == nil || got.PrimaryWindow == nil || got.SecondaryWindow == nil {
		t.Fatalf("expected cached windows for non-auth error, got %+v", got)
	}
	if got.PrimaryWindow.UsedPercent != 20 || got.SecondaryWindow.UsedPercent != 90 {
		t.Fatalf("expected cached used_percent primary=20 secondary=90, got primary=%d secondary=%d", got.PrimaryWindow.UsedPercent, got.SecondaryWindow.UsedPercent)
	}
}

func TestCodexEffectiveMainRateLimit_ResetDueRestoresFullQuota(t *testing.T) {
	now := time.Unix(1_900_000_000, 0).UTC()
	status := codexAuthUsageStatus{
		Status: "ok",
		Usage: &codexUsagePayload{
			PlanType: "team",
			RateLimit: &codexUsageRateLimit{
				Allowed:      false,
				LimitReached: true,
				PrimaryWindow: &codexUsageWindow{
					UsedPercent:        100,
					LimitWindowSeconds: codexFiveHourWindowSecs,
					ResetAfterSeconds:  0,
					ResetAt:            now.Unix() - 5,
				},
				SecondaryWindow: &codexUsageWindow{
					UsedPercent:        100,
					LimitWindowSeconds: codexWeeklyWindowSecs,
					ResetAfterSeconds:  0,
					ResetAt:            now.Unix() - 1,
				},
			},
		},
	}

	got := codexEffectiveMainRateLimit(now, status)
	if got == nil || got.PrimaryWindow == nil || got.SecondaryWindow == nil {
		t.Fatalf("expected recovered windows, got %+v", got)
	}
	if got.PrimaryWindow.UsedPercent != 0 || got.SecondaryWindow.UsedPercent != 0 {
		t.Fatalf("expected both windows used_percent=0 after reset, got primary=%d secondary=%d", got.PrimaryWindow.UsedPercent, got.SecondaryWindow.UsedPercent)
	}
	if !got.Allowed || got.LimitReached {
		t.Fatalf("expected allowed=true limit_reached=false after reset, got allowed=%v limit_reached=%v", got.Allowed, got.LimitReached)
	}
}

func TestBuildCodexUsageExtensions_ResetDueShowsRecoveredWindow(t *testing.T) {
	now := time.Unix(1_900_000_000, 0).UTC()
	exts := buildCodexUsageExtensions(map[string]codexAuthUsageStatus{
		"auth-1": {
			AuthID:   "auth-1",
			FileName: "codex-auth-1.json",
			PlanType: "team",
			Status:   "ok",
			Usage: &codexUsagePayload{
				RateLimit: &codexUsageRateLimit{
					Allowed:      false,
					LimitReached: true,
					PrimaryWindow: &codexUsageWindow{
						UsedPercent:        100,
						LimitWindowSeconds: codexFiveHourWindowSecs,
						ResetAt:            now.Unix() - 10,
					},
					SecondaryWindow: &codexUsageWindow{
						UsedPercent:        100,
						LimitWindowSeconds: codexWeeklyWindowSecs,
						ResetAt:            now.Unix() - 10,
					},
				},
			},
		},
	}, now, 0.2, 6.0, "", nil, "", "", "", time.Time{})
	if exts == nil || len(exts.ActiveAuthFiles) != 1 {
		t.Fatal("expected one active auth file extension item")
	}
	item := exts.ActiveAuthFiles[0]
	if item.FiveHour == nil || item.Week == nil {
		t.Fatalf("expected both windows on active auth item, got %+v", item)
	}
	if item.FiveHour.UsedPercent != 0 || item.Week.UsedPercent != 0 {
		t.Fatalf("expected auth file item windows to recover to full quota, got five_hour=%d week=%d", item.FiveHour.UsedPercent, item.Week.UsedPercent)
	}
}

func TestBuildCodexUsageExtensions_IncludesSelectedAuthServiceTier(t *testing.T) {
	now := time.Unix(1_900_000_000, 0).UTC()
	exts := buildCodexUsageExtensions(map[string]codexAuthUsageStatus{
		"auth-1": {
			AuthID:   "auth-1",
			FileName: "codex-auth-1.json",
			PlanType: "team",
			Status:   "ok",
		},
	}, now, 0.2, 6.0, "", nil, "auth-1", "auth-1", "priority", now)
	if exts == nil || exts.SelectedAuth == nil {
		t.Fatalf("expected selected auth extension, got %#v", exts)
	}
	if exts.SelectedAuth.AuthID != "auth-1" {
		t.Fatalf("expected selected auth id auth-1, got %q", exts.SelectedAuth.AuthID)
	}
	if exts.SelectedAuth.ServiceTier != "priority" {
		t.Fatalf("expected selected auth service tier priority, got %q", exts.SelectedAuth.ServiceTier)
	}
	if exts.SelectedAuth.ObservedAt == nil || !exts.SelectedAuth.ObservedAt.Equal(now) {
		t.Fatalf("expected selected auth observed_at=%s, got %#v", now, exts.SelectedAuth.ObservedAt)
	}
}

func TestBuildCodexUsageExtensions_OmitsSelectedAuthServiceTierWhenObservedAuthDiffers(t *testing.T) {
	now := time.Unix(1_900_000_000, 0).UTC()
	exts := buildCodexUsageExtensions(map[string]codexAuthUsageStatus{
		"auth-1": {
			AuthID:   "auth-1",
			FileName: "codex-auth-1.json",
			PlanType: "team",
			Status:   "ok",
		},
	}, now, 0.2, 6.0, "", nil, "auth-1", "auth-2", "priority", now)
	if exts == nil {
		t.Fatal("expected extensions")
	}
	if exts.SelectedAuth != nil {
		t.Fatalf("expected selected auth extension omitted when observed auth differs, got %#v", exts.SelectedAuth)
	}
}

func TestBuildCodexUsageExtensions_FallsBackToObservedAuthWhenSelectedAuthMissing(t *testing.T) {
	now := time.Unix(1_900_000_000, 0).UTC()
	exts := buildCodexUsageExtensions(map[string]codexAuthUsageStatus{
		"auth-1": {
			AuthID:   "auth-1",
			FileName: "codex-auth-1.json",
			PlanType: "team",
			Status:   "ok",
		},
	}, now, 0.2, 6.0, "", nil, "", "auth-1", "default", now)
	if exts == nil || exts.SelectedAuth == nil {
		t.Fatalf("expected selected auth extension from observed auth fallback, got %#v", exts)
	}
	if exts.SelectedAuth.AuthID != "auth-1" {
		t.Fatalf("expected fallback auth id auth-1, got %q", exts.SelectedAuth.AuthID)
	}
	if exts.SelectedAuth.ServiceTier != "default" {
		t.Fatalf("expected fallback service tier default, got %q", exts.SelectedAuth.ServiceTier)
	}
}

func TestCodexEffectiveMainRateLimit_PartialRecoveryKeepsFiveHourBlocked(t *testing.T) {
	now := time.Unix(1_900_000_000, 0).UTC()
	status := codexAuthUsageStatus{
		Status: "ok",
		Usage: &codexUsagePayload{
			PlanType: "team",
			RateLimit: &codexUsageRateLimit{
				Allowed:      false,
				LimitReached: true,
				PrimaryWindow: &codexUsageWindow{
					UsedPercent:        100,
					LimitWindowSeconds: codexFiveHourWindowSecs,
					ResetAt:            now.Unix() + 1800,
				},
				SecondaryWindow: &codexUsageWindow{
					UsedPercent:        100,
					LimitWindowSeconds: codexWeeklyWindowSecs,
					ResetAt:            now.Unix() - 10,
				},
			},
		},
	}

	got := codexEffectiveMainRateLimit(now, status)
	if got == nil || got.PrimaryWindow == nil || got.SecondaryWindow == nil {
		t.Fatalf("expected both windows, got %+v", got)
	}
	if got.PrimaryWindow.UsedPercent != 100 || got.SecondaryWindow.UsedPercent != 0 {
		t.Fatalf("expected 5h blocked and week recovered, got primary=%d secondary=%d", got.PrimaryWindow.UsedPercent, got.SecondaryWindow.UsedPercent)
	}
	if got.Allowed || !got.LimitReached {
		t.Fatalf("expected allowed=false limit_reached=true while 5h remains blocked, got allowed=%v limit_reached=%v", got.Allowed, got.LimitReached)
	}
}

func TestCodexEffectiveMainRateLimit_WeeklyDepletedAlignsForcedResetTime(t *testing.T) {
	now := time.Unix(1_900_000_000, 0).UTC()
	status := codexAuthUsageStatus{
		Status: "ok",
		Usage: &codexUsagePayload{
			PlanType: "team",
			RateLimit: &codexUsageRateLimit{
				Allowed:      false,
				LimitReached: true,
				PrimaryWindow: &codexUsageWindow{
					UsedPercent:        100,
					LimitWindowSeconds: codexFiveHourWindowSecs,
					ResetAt:            now.Unix() - 60,
				},
				SecondaryWindow: &codexUsageWindow{
					UsedPercent:        100,
					LimitWindowSeconds: codexWeeklyWindowSecs,
					ResetAfterSeconds:  3600,
					ResetAt:            now.Unix() + 3600,
				},
			},
		},
	}

	got := codexEffectiveMainRateLimit(now, status)
	if got == nil || got.PrimaryWindow == nil || got.SecondaryWindow == nil {
		t.Fatalf("expected both windows, got %+v", got)
	}
	if got.PrimaryWindow.ResetAt != status.Usage.RateLimit.SecondaryWindow.ResetAt || got.SecondaryWindow.ResetAt != status.Usage.RateLimit.SecondaryWindow.ResetAt {
		t.Fatalf("expected forced depleted windows to align with weekly reset_at, got primary=%d secondary=%d week=%d", got.PrimaryWindow.ResetAt, got.SecondaryWindow.ResetAt, status.Usage.RateLimit.SecondaryWindow.ResetAt)
	}
	if got.PrimaryWindow.ResetAfterSeconds != status.Usage.RateLimit.SecondaryWindow.ResetAfterSeconds || got.SecondaryWindow.ResetAfterSeconds != status.Usage.RateLimit.SecondaryWindow.ResetAfterSeconds {
		t.Fatalf("expected forced depleted windows to align with weekly reset_after_seconds, got primary=%d secondary=%d week=%d", got.PrimaryWindow.ResetAfterSeconds, got.SecondaryWindow.ResetAfterSeconds, status.Usage.RateLimit.SecondaryWindow.ResetAfterSeconds)
	}
}

func TestAggregateCodexUsage_AppliesFreeWeight(t *testing.T) {
	statuses := map[string]codexAuthUsageStatus{
		"free-auth": {
			Status: "ok",
			Usage: &codexUsagePayload{
				PlanType: "free",
				RateLimit: &codexUsageRateLimit{
					PrimaryWindow: &codexUsageWindow{
						UsedPercent:        100,
						LimitWindowSeconds: 18000,
					},
				},
			},
		},
		"business-auth": {
			Status: "ok",
			Usage: &codexUsagePayload{
				PlanType: "business",
				RateLimit: &codexUsageRateLimit{
					PrimaryWindow: &codexUsageWindow{
						UsedPercent:        0,
						LimitWindowSeconds: 18000,
					},
				},
			},
		},
	}

	compat, totals, withUsage := aggregateCodexUsage(statuses, 0.2, 6.0)
	if withUsage != 2 {
		t.Fatalf("expected withUsage=2, got %d", withUsage)
	}
	if compat.RateLimit == nil || compat.RateLimit.PrimaryWindow == nil {
		t.Fatalf("expected primary window in compat payload")
	}
	if compat.RateLimit.PrimaryWindow.UsedPercent != 17 {
		t.Fatalf("expected weighted used_percent=17, got %d", compat.RateLimit.PrimaryWindow.UsedPercent)
	}
	if totals.PrimaryWindow == nil {
		t.Fatalf("expected primary totals")
	}
	if totals.PrimaryWindow.ProgressPercent != 16.67 {
		t.Fatalf("expected weighted progress 16.67, got %.2f", totals.PrimaryWindow.ProgressPercent)
	}
	if compat.TotalUsageMultiplier != 1.2 {
		t.Fatalf("expected total usage multiplier 1.2, got %.2f", compat.TotalUsageMultiplier)
	}
}

func TestAggregateCodexUsage_AppliesProWeight(t *testing.T) {
	statuses := map[string]codexAuthUsageStatus{
		"pro-auth": {
			Status: "ok",
			Usage: &codexUsagePayload{
				PlanType: "pro",
				RateLimit: &codexUsageRateLimit{
					SecondaryWindow: &codexUsageWindow{
						UsedPercent:        50,
						LimitWindowSeconds: 604800,
					},
				},
			},
		},
		"plus-auth": {
			Status: "ok",
			Usage: &codexUsagePayload{
				PlanType: "plus",
				RateLimit: &codexUsageRateLimit{
					SecondaryWindow: &codexUsageWindow{
						UsedPercent:        50,
						LimitWindowSeconds: 604800,
					},
				},
			},
		},
	}

	compat, totals, withUsage := aggregateCodexUsage(statuses, 0.2, 6.0)
	if withUsage != 2 {
		t.Fatalf("expected withUsage=2, got %d", withUsage)
	}
	if compat.TotalUsageMultiplier != 7.0 {
		t.Fatalf("expected total usage multiplier 7.0, got %.2f", compat.TotalUsageMultiplier)
	}
	if totals.TotalUsageMultiplier != 7.0 {
		t.Fatalf("expected summary total usage multiplier 7.0, got %.2f", totals.TotalUsageMultiplier)
	}
}

func TestAggregateCodexUsage_NormalizesWindowOrderByDuration(t *testing.T) {
	statuses := map[string]codexAuthUsageStatus{
		"auth-1": {
			Status: "ok",
			Usage: &codexUsagePayload{
				PlanType: "plus",
				RateLimit: &codexUsageRateLimit{
					PrimaryWindow: &codexUsageWindow{
						UsedPercent:        35,
						LimitWindowSeconds: 604800,
					},
					SecondaryWindow: &codexUsageWindow{
						UsedPercent:        80,
						LimitWindowSeconds: 18000,
					},
				},
			},
		},
	}

	compat, _, withUsage := aggregateCodexUsage(statuses, 0.2, 6.0)
	if withUsage != 1 {
		t.Fatalf("expected withUsage=1, got %d", withUsage)
	}
	if compat.RateLimit == nil || compat.RateLimit.PrimaryWindow == nil || compat.RateLimit.SecondaryWindow == nil {
		t.Fatalf("expected both windows in compat payload")
	}
	if compat.RateLimit.PrimaryWindow.LimitWindowSeconds != 18000 || compat.RateLimit.PrimaryWindow.UsedPercent != 80 {
		t.Fatalf("expected primary to represent 5h window, got %+v", compat.RateLimit.PrimaryWindow)
	}
	if compat.RateLimit.SecondaryWindow.LimitWindowSeconds != 604800 || compat.RateLimit.SecondaryWindow.UsedPercent != 35 {
		t.Fatalf("expected secondary to represent weekly window, got %+v", compat.RateLimit.SecondaryWindow)
	}
}

func TestAggregateCodexUsage_WeeklyOnlyFreeDoesNotPolluteFiveHourWindow(t *testing.T) {
	statuses := map[string]codexAuthUsageStatus{
		"team-auth": {
			Status: "ok",
			Usage: &codexUsagePayload{
				PlanType: "team",
				RateLimit: &codexUsageRateLimit{
					PrimaryWindow: &codexUsageWindow{
						UsedPercent:        80,
						LimitWindowSeconds: 18000,
					},
					SecondaryWindow: &codexUsageWindow{
						UsedPercent:        35,
						LimitWindowSeconds: 604800,
					},
				},
			},
		},
		"free-auth": {
			Status: "ok",
			Usage: &codexUsagePayload{
				PlanType: "free",
				RateLimit: &codexUsageRateLimit{
					PrimaryWindow: &codexUsageWindow{
						UsedPercent:        50,
						LimitWindowSeconds: 604800,
					},
				},
			},
		},
	}

	compat, totals, withUsage := aggregateCodexUsage(statuses, 0.2, 6.0)
	if withUsage != 2 {
		t.Fatalf("expected withUsage=2, got %d", withUsage)
	}
	if compat.RateLimit == nil || compat.RateLimit.PrimaryWindow == nil || compat.RateLimit.SecondaryWindow == nil {
		t.Fatalf("expected both windows in compat payload")
	}
	if compat.RateLimit.PrimaryWindow.LimitWindowSeconds != 18000 {
		t.Fatalf("expected 5h primary window, got %d", compat.RateLimit.PrimaryWindow.LimitWindowSeconds)
	}
	if compat.RateLimit.SecondaryWindow.LimitWindowSeconds != 604800 {
		t.Fatalf("expected weekly secondary window, got %d", compat.RateLimit.SecondaryWindow.LimitWindowSeconds)
	}
	if compat.RateLimit.PrimaryWindow.UsedPercent != 67 {
		t.Fatalf("expected compat 5h used_percent=67, got %d", compat.RateLimit.PrimaryWindow.UsedPercent)
	}
	if compat.RateLimit.SecondaryWindow.UsedPercent != 38 {
		t.Fatalf("expected compat weekly used_percent=38, got %d", compat.RateLimit.SecondaryWindow.UsedPercent)
	}
	if totals.PrimaryWindow == nil || totals.SecondaryWindow == nil {
		t.Fatalf("expected both total windows")
	}
	if totals.PrimaryWindow.ProgressPercent != 66.67 {
		t.Fatalf("expected 5h progress 66.67, got %.2f", totals.PrimaryWindow.ProgressPercent)
	}
	if totals.SecondaryWindow.ProgressPercent != 37.5 {
		t.Fatalf("expected weekly progress 37.50, got %.2f", totals.SecondaryWindow.ProgressPercent)
	}
}

func TestAggregateCodexUsage_DenominatorIncludesAllCodexAuthFiles(t *testing.T) {
	statuses := map[string]codexAuthUsageStatus{
		"free-auth": {
			PlanType: "free",
			Status:   "ok",
			Usage: &codexUsagePayload{
				PlanType: "free",
				RateLimit: &codexUsageRateLimit{
					PrimaryWindow: &codexUsageWindow{
						UsedPercent:        100,
						LimitWindowSeconds: 18000,
					},
				},
			},
		},
		"business-auth": {
			PlanType: "business",
			Status:   "ok",
			Usage: &codexUsagePayload{
				PlanType: "business",
				RateLimit: &codexUsageRateLimit{
					PrimaryWindow: &codexUsageWindow{
						UsedPercent:        0,
						LimitWindowSeconds: 18000,
					},
				},
			},
		},
		"no-usage-auth": {
			PlanType: "business",
			Status:   "error",
			Usage:    nil,
		},
	}

	_, totals, withUsage := aggregateCodexUsage(statuses, 0.2, 6.0)
	if withUsage != 2 {
		t.Fatalf("expected withUsage=2, got %d", withUsage)
	}
	if totals.PrimaryWindow == nil {
		t.Fatalf("expected primary totals")
	}
	if totals.PrimaryWindow.AuthFiles != 3 {
		t.Fatalf("expected denominator auth_files=3, got %d", totals.PrimaryWindow.AuthFiles)
	}
	if totals.PrimaryWindow.TotalPercent != 220 {
		t.Fatalf("expected weighted total_percent=220, got %d", totals.PrimaryWindow.TotalPercent)
	}
	if totals.PrimaryWindow.UsedPercentSum != 20 {
		t.Fatalf("expected weighted used_percent_sum=20, got %d", totals.PrimaryWindow.UsedPercentSum)
	}
	if totals.PrimaryWindow.ProgressPercent != 9.09 {
		t.Fatalf("expected weighted progress 9.09, got %.2f", totals.PrimaryWindow.ProgressPercent)
	}
	if totals.TotalUsageMultiplier != 2.2 {
		t.Fatalf("expected total usage multiplier 2.2, got %.2f", totals.TotalUsageMultiplier)
	}
}

func TestRefreshCodexUsageFromCacheTTL_PollsSelectedAuthPerTTL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var whamCalls int32
	whamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&whamCalls, 1)
		if r.URL.Path != "/backend-api/wham/usage" {
			t.Fatalf("unexpected wham path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token-wham" {
			t.Fatalf("unexpected auth header: %s", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "acc-wham" {
			t.Fatalf("unexpected account id header: %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(codexUsagePayload{
			PlanType: "plus",
			RateLimit: &codexUsageRateLimit{
				Allowed:      true,
				LimitReached: false,
				PrimaryWindow: &codexUsageWindow{
					UsedPercent:        20,
					LimitWindowSeconds: 18000,
					ResetAfterSeconds:  1200,
					ResetAt:            1900000000,
				},
				SecondaryWindow: &codexUsageWindow{
					UsedPercent:        40,
					LimitWindowSeconds: 604800,
					ResetAfterSeconds:  3600,
					ResetAt:            1900003600,
				},
			},
			AdditionalRateLimits: []codexUsageAdditionalRateLimit{{
				LimitName:      "messages",
				MeteredFeature: "cloud",
				RateLimit: &codexUsageRateLimit{
					Allowed:      true,
					LimitReached: false,
					PrimaryWindow: &codexUsageWindow{
						UsedPercent:        10,
						LimitWindowSeconds: 86400,
						ResetAfterSeconds:  600,
						ResetAt:            1900000600,
					},
				},
			}},
		})
	}))
	defer whamServer.Close()

	var apiCalls int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&apiCalls, 1)
		if r.URL.Path != "/api/codex/usage" {
			t.Fatalf("unexpected codex api path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token-api" {
			t.Fatalf("unexpected auth header: %s", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "acc-api" {
			t.Fatalf("unexpected account id header: %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(codexUsagePayload{
			PlanType: "plus",
			RateLimit: &codexUsageRateLimit{
				Allowed:      false,
				LimitReached: true,
				PrimaryWindow: &codexUsageWindow{
					UsedPercent:        60,
					LimitWindowSeconds: 18000,
					ResetAfterSeconds:  1800,
					ResetAt:            1900001800,
				},
				SecondaryWindow: &codexUsageWindow{
					UsedPercent:        80,
					LimitWindowSeconds: 604800,
					ResetAfterSeconds:  7200,
					ResetAt:            1900007200,
				},
			},
			AdditionalRateLimits: []codexUsageAdditionalRateLimit{{
				LimitName:      "messages",
				MeteredFeature: "cloud",
				RateLimit: &codexUsageRateLimit{
					Allowed:      false,
					LimitReached: true,
					PrimaryWindow: &codexUsageWindow{
						UsedPercent:        50,
						LimitWindowSeconds: 86400,
						ResetAfterSeconds:  900,
						ResetAt:            1900000900,
					},
				},
			}},
		})
	}))
	defer apiServer.Close()

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	ctx := context.Background()
	_, _ = manager.Register(ctx, &coreauth.Auth{
		ID:       "codex-wham",
		Provider: "codex",
		FileName: "codex-wham.json",
		Attributes: map[string]string{
			"base_url": whamServer.URL + "/backend-api",
		},
		Metadata: map[string]any{
			"access_token": "token-wham",
			"account_id":   "acc-wham",
			"email":        "wham@example.com",
		},
	})
	_, _ = manager.Register(ctx, &coreauth.Auth{
		ID:       "codex-api",
		Provider: "codex",
		FileName: "codex-api.json",
		Attributes: map[string]string{
			"base_url": apiServer.URL,
		},
		Metadata: map[string]any{
			"access_token": "token-api",
			"account_id":   "acc-api",
			"email":        "api@example.com",
		},
	})
	_, _ = manager.Register(ctx, &coreauth.Auth{
		ID:       "codex-apikey",
		Provider: "codex",
		FileName: "codex-apikey.json",
		Attributes: map[string]string{
			"api_key": "sk-test",
		},
	})

	h := seedTestCodexUsageState(&Handler{
		cfg:            &config.Config{},
		authManager:    manager,
		configFilePath: t.TempDir() + "/config.yaml",
	}, &codexUsageState{
		codexUsageByAuth: make(map[string]codexAuthUsageStatus),
		codexUsageCompat: defaultCodexUsagePayload(),
	})

	h.setSelectedCodexAuthID("codex-wham")
	h.refreshCodexUsageFromCacheTTL(context.Background())
	compat, summary, hasData := h.codexUsageSnapshot()

	if !hasData {
		t.Fatal("expected usage data after polling")
	}
	if atomic.LoadInt32(&whamCalls) != 1 {
		t.Fatalf("expected 1 wham call, got %d", whamCalls)
	}
	if atomic.LoadInt32(&apiCalls) != 0 {
		t.Fatalf("expected no api call for non-selected auth, got %d", apiCalls)
	}
	if summary.SelectedAuthID != "codex-wham" {
		t.Fatalf("expected selected auth codex-wham, got %q", summary.SelectedAuthID)
	}
	if compat.PlanType != "plus" {
		t.Fatalf("expected plan_type plus, got %q", compat.PlanType)
	}
	if compat.RateLimit == nil || compat.RateLimit.PrimaryWindow == nil {
		t.Fatalf("expected aggregated primary window")
	}
	snapshot := h.codexUsageByAuthSnapshot()
	if got := snapshot["codex-wham"].Usage.RateLimit.PrimaryWindow.UsedPercent; got != 20 {
		t.Fatalf("expected selected auth cached 5h used_percent=20, got %d", got)
	}
	if snapshot["codex-api"].Usage != nil {
		t.Fatal("expected non-selected auth to remain cache-only before it is selected")
	}
	if summary.AuthFilesTotal != 3 {
		t.Fatalf("expected all codex auth files cached, got %d", summary.AuthFilesTotal)
	}
	if summary.AuthFilesWithUsage != 1 {
		t.Fatalf("expected only selected auth to have usage after first refresh, got %d", summary.AuthFilesWithUsage)
	}

	// Within TTL no upstream calls should be issued.
	h.refreshCodexUsageFromCacheTTL(context.Background())
	if atomic.LoadInt32(&whamCalls) != 1 {
		t.Fatalf("expected still 1 wham call due to TTL, got %d", whamCalls)
	}
	if atomic.LoadInt32(&apiCalls) != 0 {
		t.Fatalf("expected still 0 api calls for non-selected auth due to cache-only policy, got %d", apiCalls)
	}

	// Selection changes poll the newly selected auth if it has never been refreshed.
	h.setSelectedCodexAuthID("codex-api")
	h.refreshCodexUsageFromCacheTTL(context.Background())
	if atomic.LoadInt32(&whamCalls) != 1 {
		t.Fatalf("expected wham call count unchanged on selection change, got %d", whamCalls)
	}
	if atomic.LoadInt32(&apiCalls) != 1 {
		t.Fatalf("expected first poll for newly selected auth, got %d", apiCalls)
	}
	_, summary, _ = h.codexUsageSnapshot()
	if summary.SelectedAuthID != "codex-api" {
		t.Fatalf("expected selected auth codex-api, got %q", summary.SelectedAuthID)
	}
	if summary.AuthFilesWithUsage != 2 {
		t.Fatalf("expected both selected auths with cached usage after second selection, got %d", summary.AuthFilesWithUsage)
	}

	// Force selected auth TTL to expire; only selected auth should be refreshed.
	h.codexUsageStateRef().codexUsageMu.Lock()
	st := h.codexUsageStateRef().codexUsageByAuth["codex-api"]
	st.LastPolledAt = time.Now().Add(-61 * time.Second).UTC()
	h.codexUsageStateRef().codexUsageByAuth["codex-api"] = st
	h.codexUsageStateRef().codexUsageMu.Unlock()
	h.refreshCodexUsageFromCacheTTL(context.Background())
	if atomic.LoadInt32(&whamCalls) != 1 {
		t.Fatalf("expected wham call count unchanged when wham is not selected, got %d", whamCalls)
	}
	if atomic.LoadInt32(&apiCalls) != 2 {
		t.Fatalf("expected one additional api poll after selected auth ttl expiry, got %d", apiCalls)
	}
}

func TestCodexUsageStatePersistence(t *testing.T) {
	tmp := t.TempDir()
	configPath := tmp + "/config.yaml"
	h := seedTestCodexUsageState(&Handler{
		configFilePath: configPath,
	}, &codexUsageState{
		codexUsageByAuth: make(map[string]codexAuthUsageStatus),
		codexUsageCompat: defaultCodexUsagePayload(),
	})
	now := time.Now().UTC()
	h.updateCodexUsageState(map[string]codexAuthUsageStatus{
		"codex-a": {
			AuthID:       "codex-a",
			Status:       "ok",
			LastPolledAt: now,
			HasUsage:     true,
			Usage: &codexUsagePayload{
				PlanType: "plus",
				RateLimit: &codexUsageRateLimit{
					PrimaryWindow: &codexUsageWindow{
						UsedPercent:        33,
						LimitWindowSeconds: 18000,
					},
				},
			},
		},
	}, "codex-a", now, true)

	h2 := seedTestCodexUsageState(&Handler{
		configFilePath: configPath,
	}, &codexUsageState{
		codexUsageByAuth: make(map[string]codexAuthUsageStatus),
		codexUsageCompat: defaultCodexUsagePayload(),
	})
	h2.loadCodexUsageState()
	compat, summary, hasData := h2.codexUsageSnapshot()
	if !hasData {
		t.Fatal("expected persisted usage data after reload")
	}
	if summary.SelectedAuthID != "codex-a" {
		t.Fatalf("expected selected auth codex-a, got %q", summary.SelectedAuthID)
	}
	if compat.RateLimit == nil || compat.RateLimit.PrimaryWindow == nil || compat.RateLimit.PrimaryWindow.UsedPercent != 33 {
		t.Fatalf("unexpected persisted compat payload: %+v", compat)
	}
	if compat.Extensions != nil && compat.Extensions.SelectedAuth != nil {
		t.Fatalf("expected persisted payload to omit runtime selected auth service tier, got %#v", compat.Extensions.SelectedAuth)
	}
}

func TestCodexUsageSnapshot_InjectsSelectedAuthServiceTierLive(t *testing.T) {
	now := time.Unix(1_900_000_000, 0).UTC()
	h := seedTestCodexUsageState(&Handler{}, &codexUsageState{
		codexUsageByAuth: map[string]codexAuthUsageStatus{
			"auth-1": {
				AuthID:   "auth-1",
				FileName: "codex-auth-1.json",
				Email:    "selected@example.com",
				PlanType: "team",
				Status:   "ok",
				HasUsage: true,
				Usage: &codexUsagePayload{
					PlanType: "team",
					RateLimit: &codexUsageRateLimit{
						PrimaryWindow: &codexUsageWindow{
							UsedPercent:        20,
							LimitWindowSeconds: codexFiveHourWindowSecs,
						},
						SecondaryWindow: &codexUsageWindow{
							UsedPercent:        40,
							LimitWindowSeconds: codexWeeklyWindowSecs,
						},
					},
				},
			},
		},
		codexUsageCompat: defaultCodexUsagePayload(),
		codexUsageSummary: codexUsageSummaryResponse{
			SelectedAuthID: "auth-1",
		},
		codexUsageHasData:              true,
		codexUsageSelected:             "auth-1",
		codexObservedServiceTierAuthID: "auth-1",
		codexObservedServiceTier:       "priority",
		codexObservedServiceTierAt:     now,
	})

	compat, summary, hasData := h.codexUsageSnapshot()
	if !hasData {
		t.Fatal("expected hasData=true")
	}
	if compat.Extensions == nil || compat.Extensions.SelectedAuth == nil {
		t.Fatalf("expected compat selected auth extension, got %#v", compat.Extensions)
	}
	if compat.Extensions.SelectedAuth.ServiceTier != "priority" {
		t.Fatalf("expected compat selected auth service tier priority, got %q", compat.Extensions.SelectedAuth.ServiceTier)
	}
	if summary.CompatPayload.Extensions == nil || summary.CompatPayload.Extensions.SelectedAuth == nil {
		t.Fatalf("expected summary compat selected auth extension, got %#v", summary.CompatPayload.Extensions)
	}
	if summary.CompatPayload.Extensions.SelectedAuth.ServiceTier != "priority" {
		t.Fatalf("expected summary compat selected auth service tier priority, got %q", summary.CompatPayload.Extensions.SelectedAuth.ServiceTier)
	}
}

func TestGetCodexUsageCompatDefaultsToGuest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/codex/usage", nil)

	h.GetCodexUsageCompat(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload codexUsagePayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.PlanType != "guest" {
		t.Fatalf("expected plan_type guest, got %q", payload.PlanType)
	}
}

func TestGetCodexUsageCompat_ReturnsAggregatedPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := seedTestCodexUsageState(&Handler{}, &codexUsageState{
		codexUsageByAuth: map[string]codexAuthUsageStatus{
			"selected-auth": {
				AuthID:    "selected-auth",
				Email:     "selected-fallback@example.com",
				AccountID: "acc-fallback",
				HasUsage:  true,
				Usage: &codexUsagePayload{
					UserID:              "user-1",
					AccountID:           "acc-1",
					Email:               "selected@example.com",
					PlanType:            "team",
					RateLimit:           &codexUsageRateLimit{Allowed: true, LimitReached: false},
					CodeReviewRateLimit: &codexUsageRateLimit{Allowed: true, LimitReached: false},
					Promo:               json.RawMessage("null"),
					Extra: map[string]json.RawMessage{
						"new_field": json.RawMessage(`{"x":1}`),
					},
				},
			},
		},
		codexUsageSelected: "selected-auth",
		codexUsageCompat: codexUsagePayload{
			PlanType: "free",
			Email:    "compat@example.com",
		},
		codexUsageSummary: codexUsageSummaryResponse{
			SelectedAuthID: "selected-auth",
		},
		codexUsageHasData: true,
	})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/codex/usage", nil)
	h.GetCodexUsageCompat(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := payload["email"]; got != "compat@example.com" {
		t.Fatalf("expected aggregated/compat email, got %#v", got)
	}
	if _, ok := payload["account_id"]; ok {
		t.Fatalf("expected no selected account_id in aggregated payload, got %#v", payload["account_id"])
	}
	if _, ok := payload["user_id"]; ok {
		t.Fatalf("expected no selected user_id in aggregated payload, got %#v", payload["user_id"])
	}
	if _, ok := payload["code_review_rate_limit"]; ok {
		t.Fatal("did not expect selected-only code_review_rate_limit in aggregated payload")
	}
	if _, ok := payload["new_field"]; ok {
		t.Fatal("did not expect selected-only extra top-level fields in aggregated payload")
	}
}

func TestGetCodexUsageCompat_IncludesActiveAuthFilesExtension(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC()
	older := now.Add(-2 * time.Minute)
	mid := now.Add(-1 * time.Minute)

	h := seedTestCodexUsageState(&Handler{}, &codexUsageState{
		codexUsageByAuth: map[string]codexAuthUsageStatus{
			"active-auth": {
				AuthID:        "active-auth",
				FileName:      "codex-active.json",
				Email:         "active@example.com",
				PlanType:      "team",
				Status:        "ok",
				HasUsage:      true,
				AccountID:     "acc-active",
				LastSuccessAt: &now,
				Usage: &codexUsagePayload{
					PlanType: "team",
					RateLimit: &codexUsageRateLimit{
						PrimaryWindow: &codexUsageWindow{
							UsedPercent:        22,
							LimitWindowSeconds: 18000,
							ResetAfterSeconds:  1111,
							ResetAt:            1900000001,
						},
						SecondaryWindow: &codexUsageWindow{
							UsedPercent:        44,
							LimitWindowSeconds: 604800,
							ResetAfterSeconds:  2222,
							ResetAt:            1900000002,
						},
					},
				},
			},
			"soft-error-auth": {
				AuthID:        "soft-error-auth",
				FileName:      "codex-soft-error.json",
				Email:         "soft-error@example.com",
				PlanType:      "free",
				Status:        "error",
				HasUsage:      true,
				Error:         "Get \"https://chatgpt.com/backend-api/wham/usage\": EOF",
				LastSuccessAt: &older,
				Usage: &codexUsagePayload{
					PlanType: "free",
					RateLimit: &codexUsageRateLimit{
						PrimaryWindow: &codexUsageWindow{
							UsedPercent:        99,
							LimitWindowSeconds: 604800,
						},
					},
				},
			},
			"hard-error-auth": {
				AuthID:       "hard-error-auth",
				FileName:     "codex-hard-error.json",
				Email:        "hard-error@example.com",
				PlanType:     "free",
				Status:       "error",
				HasUsage:     false,
				Error:        "usage request failed: status=401 body={\"error\":{\"code\":\"token_invalidated\"}}",
				LastPolledAt: mid,
			},
		},
		codexUsageCompat: codexUsagePayload{
			PlanType: "team",
			RateLimit: &codexUsageRateLimit{
				PrimaryWindow: &codexUsageWindow{
					UsedPercent:        22,
					LimitWindowSeconds: 18000,
				},
				SecondaryWindow: &codexUsageWindow{
					UsedPercent:        44,
					LimitWindowSeconds: 604800,
				},
			},
		},
		codexUsageSummary: codexUsageSummaryResponse{
			AuthFiles: []codexAuthUsageStatus{
				{AuthID: "active-auth", FileName: "codex-active.json"},
				{AuthID: "soft-error-auth", FileName: "codex-soft-error.json"},
				{AuthID: "hard-error-auth", FileName: "codex-hard-error.json"},
			},
		},
		codexUsageHasData: true,
	})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/codex/usage", nil)
	h.GetCodexUsageCompat(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload codexUsagePayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Extensions == nil {
		t.Fatal("expected extensions in compat payload")
	}
	if len(payload.Extensions.ActiveAuthFiles) != 3 {
		t.Fatalf("expected 3 auth items including soft/hard errors, got %d", len(payload.Extensions.ActiveAuthFiles))
	}
	if payload.Extensions.ActiveAuthFiles[0].AuthID != "active-auth" {
		t.Fatalf("expected latest item active-auth, got %q", payload.Extensions.ActiveAuthFiles[0].AuthID)
	}
	if payload.Extensions.ActiveAuthFiles[1].AuthID != "hard-error-auth" {
		t.Fatalf("expected second item hard-error-auth, got %q", payload.Extensions.ActiveAuthFiles[1].AuthID)
	}
	if payload.Extensions.ActiveAuthFiles[2].AuthID != "soft-error-auth" {
		t.Fatalf("expected third item soft-error-auth, got %q", payload.Extensions.ActiveAuthFiles[2].AuthID)
	}

	item := payload.Extensions.ActiveAuthFiles[0]
	if item.Account != "active@example.com" {
		t.Fatalf("expected account from email, got %q", item.Account)
	}
	if item.FiveHour == nil || item.FiveHour.UsedPercent != 22 {
		t.Fatalf("expected five_hour used_percent=22, got %+v", item.FiveHour)
	}
	if item.Week == nil || item.Week.UsedPercent != 44 {
		t.Fatalf("expected week used_percent=44, got %+v", item.Week)
	}

	hard := payload.Extensions.ActiveAuthFiles[1]
	if hard.Status != "error" {
		t.Fatalf("expected hard error status=error, got %q", hard.Status)
	}
	if hard.ErrorSummary == "" {
		t.Fatal("expected hard error summary")
	}
	if hard.FiveHour == nil || hard.FiveHour.UsedPercent != 100 {
		t.Fatalf("expected hard error five_hour used_percent=100, got %+v", hard.FiveHour)
	}
	if hard.Week == nil || hard.Week.UsedPercent != 100 {
		t.Fatalf("expected hard error week used_percent=100, got %+v", hard.Week)
	}

	soft := payload.Extensions.ActiveAuthFiles[2]
	if soft.Status != "error" || soft.ErrorSummary == "" {
		t.Fatalf("expected soft error with summary, got status=%q summary=%q", soft.Status, soft.ErrorSummary)
	}
}

func TestCodexUsagePayload_PreservesUnknownTopLevelFields(t *testing.T) {
	raw := []byte(`{
		"user_id":"user-1",
		"account_id":"acc-1",
		"email":"user@example.com",
		"plan_type":"team",
		"rate_limit":{"allowed":true,"limit_reached":false},
		"code_review_rate_limit":{"allowed":true,"limit_reached":false},
		"credits":{"has_credits":false,"unlimited":false,"balance":null,"approx_local_messages":null,"approx_cloud_messages":null},
		"additional_rate_limits":[],
		"promo":null,
		"unknown_obj":{"a":1},
		"unknown_num":2
	}`)

	var payload codexUsagePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Email != "user@example.com" {
		t.Fatalf("expected email parsed, got %q", payload.Email)
	}
	if _, ok := payload.Extra["unknown_obj"]; !ok {
		t.Fatal("expected unknown_obj to be preserved")
	}
	if _, ok := payload.Extra["unknown_num"]; !ok {
		t.Fatal("expected unknown_num to be preserved")
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("unmarshal encoded payload: %v", err)
	}
	if _, ok := roundTrip["unknown_obj"]; !ok {
		t.Fatal("expected unknown_obj after marshal")
	}
	if _, ok := roundTrip["unknown_num"]; !ok {
		t.Fatal("expected unknown_num after marshal")
	}
}

func TestEvaluateCodexOAuthQuota_UsesConfiguredAvailableTotal(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("codex-oauth-available-totals:\n  - 3\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	h := seedTestCodexUsageState(&Handler{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				APIKeys: []string{"k1", "k2"},
			},
		},
		configFilePath: configPath,
	}, &codexUsageState{
		codexUsageByAuth: make(map[string]codexAuthUsageStatus),
		codexUsageCompat: defaultCodexUsagePayload(),
		codexUsageSummary: codexUsageSummaryResponse{
			Total: codexUsageTotalSummary{
				PrimaryWindow:   &codexUsageWindowTotals{ProgressPercent: 90},
				SecondaryWindow: &codexUsageWindowTotals{ProgressPercent: 60, TotalPercent: 600},
			},
		},
	})

	exceeded, progress, limit, checked := h.EvaluateCodexOAuthQuota(context.Background(), "k1")
	if !checked {
		t.Fatal("expected checked=true for configured api key")
	}
	if !exceeded {
		t.Fatal("expected exceeded=true when progress exceeds configured limit")
	}
	if math.Abs(progress-3.6) > 1e-9 {
		t.Fatalf("expected used weekly units=3.6, got %v", progress)
	}
	if limit != 3 {
		t.Fatalf("expected configured limit units=3, got %v", limit)
	}

	exceeded, progress, limit, checked = h.EvaluateCodexOAuthQuota(context.Background(), "k2")
	if !checked {
		t.Fatal("expected checked=true for second api key")
	}
	if exceeded {
		t.Fatal("expected exceeded=false with default full quota")
	}
	if math.Abs(progress-3.6) > 1e-9 {
		t.Fatalf("expected used weekly units=3.6, got %v", progress)
	}
	if limit != 6 {
		t.Fatalf("expected default full-system units limit=6, got %v", limit)
	}

	_, _, _, checked = h.EvaluateCodexOAuthQuota(context.Background(), "k-not-found")
	if checked {
		t.Fatal("expected checked=false for unknown api key")
	}
}

func TestEvaluateCodexOAuthQuota_IgnoresPrimaryWindow(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("codex-oauth-available-totals:\n  - 2\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	h := seedTestCodexUsageState(&Handler{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				APIKeys: []string{"k1"},
			},
		},
		configFilePath: configPath,
	}, &codexUsageState{
		codexUsageByAuth: make(map[string]codexAuthUsageStatus),
		codexUsageCompat: defaultCodexUsagePayload(),
		codexUsageSummary: codexUsageSummaryResponse{
			Total: codexUsageTotalSummary{
				PrimaryWindow:   &codexUsageWindowTotals{ProgressPercent: 95},
				SecondaryWindow: &codexUsageWindowTotals{ProgressPercent: 40, TotalPercent: 300},
			},
		},
	})

	exceeded, progress, limit, checked := h.EvaluateCodexOAuthQuota(context.Background(), "k1")
	if !checked {
		t.Fatal("expected checked=true")
	}
	if exceeded {
		t.Fatal("expected exceeded=false when only primary(5h) exceeds limit")
	}
	if math.Abs(progress-1.2) > 1e-9 {
		t.Fatalf("expected weekly used units=1.2, got %v", progress)
	}
	if limit != 2 {
		t.Fatalf("expected limit units=2, got %v", limit)
	}
}

func TestRefreshCodexUsageFromCacheTTL_ClearsUsageOnUnauthorized(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var statusCode int32 = http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			t.Fatalf("unexpected wham path: %s", r.URL.Path)
		}
		if code := int(atomic.LoadInt32(&statusCode)); code != http.StatusOK {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(codexUsagePayload{
			PlanType: "plus",
			RateLimit: &codexUsageRateLimit{
				PrimaryWindow: &codexUsageWindow{
					UsedPercent:        25,
					LimitWindowSeconds: 18000,
				},
			},
		})
	}))
	defer server.Close()

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	ctx := context.Background()
	_, _ = manager.Register(ctx, &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		FileName: "codex-auth.json",
		Attributes: map[string]string{
			"base_url": server.URL + "/backend-api",
		},
		Metadata: map[string]any{
			"access_token": "token-auth",
			"account_id":   "acc-auth",
		},
	})

	h := seedTestCodexUsageState(&Handler{
		cfg:            &config.Config{},
		authManager:    manager,
		configFilePath: t.TempDir() + "/config.yaml",
	}, &codexUsageState{
		codexUsageByAuth: make(map[string]codexAuthUsageStatus),
		codexUsageCompat: defaultCodexUsagePayload(),
	})

	h.setSelectedCodexAuthID("codex-auth")
	h.refreshCodexUsageFromCacheTTL(context.Background())
	compat, summary, hasData := h.codexUsageSnapshot()
	if !hasData {
		t.Fatal("expected hasData=true after successful poll")
	}
	if summary.AuthFilesWithUsage != 1 {
		t.Fatalf("expected withUsage=1 after success, got %d", summary.AuthFilesWithUsage)
	}
	if compat.PlanType != "plus" {
		t.Fatalf("expected plan_type plus after success, got %q", compat.PlanType)
	}

	atomic.StoreInt32(&statusCode, http.StatusUnauthorized)
	h.codexUsageStateRef().codexUsageMu.Lock()
	st := h.codexUsageStateRef().codexUsageByAuth["codex-auth"]
	st.LastPolledAt = time.Now().Add(-61 * time.Second).UTC()
	h.codexUsageStateRef().codexUsageByAuth["codex-auth"] = st
	h.codexUsageStateRef().codexUsageMu.Unlock()

	h.refreshCodexUsageFromCacheTTL(context.Background())
	compat, summary, hasData = h.codexUsageSnapshot()
	if hasData {
		t.Fatal("expected hasData=false after unauthorized response clears usage")
	}
	if summary.AuthFilesWithUsage != 0 {
		t.Fatalf("expected withUsage=0 after unauthorized response, got %d", summary.AuthFilesWithUsage)
	}
	if compat.PlanType != "guest" {
		t.Fatalf("expected plan_type guest after usage clear, got %q", compat.PlanType)
	}
	authStatus, ok := h.codexUsageByAuthSnapshot()["codex-auth"]
	if !ok {
		t.Fatal("expected auth status to remain present")
	}
	if authStatus.Status != "error" {
		t.Fatalf("expected auth status error, got %q", authStatus.Status)
	}
	if authStatus.Usage != nil {
		t.Fatal("expected usage to be nil after unauthorized response")
	}
	if authStatus.HasUsage {
		t.Fatal("expected has_usage=false after unauthorized response")
	}
}

func TestRefreshCodexUsageFromCacheTTL_SoftErrorUsesCacheUntilTTLExpires(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var mode int32
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/backend-api/wham/usage" {
			t.Fatalf("unexpected wham path: %s", r.URL.Path)
		}
		switch atomic.LoadInt32(&mode) {
		case 1:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"temporary upstream failure"}`))
			return
		case 2:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(codexUsagePayload{
				PlanType: "plus",
				RateLimit: &codexUsageRateLimit{
					PrimaryWindow: &codexUsageWindow{
						UsedPercent:        40,
						LimitWindowSeconds: 18000,
					},
				},
			})
			return
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(codexUsagePayload{
				PlanType: "plus",
				RateLimit: &codexUsageRateLimit{
					PrimaryWindow: &codexUsageWindow{
						UsedPercent:        25,
						LimitWindowSeconds: 18000,
					},
				},
			})
			return
		}
	}))
	defer server.Close()

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	ctx := context.Background()
	_, _ = manager.Register(ctx, &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		FileName: "codex-auth.json",
		Attributes: map[string]string{
			"base_url": server.URL + "/backend-api",
		},
		Metadata: map[string]any{
			"access_token": "token-auth",
			"account_id":   "acc-auth",
		},
	})

	h := seedTestCodexUsageState(&Handler{
		cfg:            &config.Config{},
		authManager:    manager,
		configFilePath: t.TempDir() + "/config.yaml",
	}, &codexUsageState{
		codexUsageByAuth: make(map[string]codexAuthUsageStatus),
		codexUsageCompat: defaultCodexUsagePayload(),
	})

	h.setSelectedCodexAuthID("codex-auth")
	h.refreshCodexUsageFromCacheTTL(context.Background())
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected first successful poll call count=1, got %d", calls)
	}

	initial := h.codexUsageByAuthSnapshot()["codex-auth"]
	if initial.Usage == nil || initial.Usage.RateLimit == nil || initial.Usage.RateLimit.PrimaryWindow == nil {
		t.Fatal("expected initial cached usage")
	}
	if initial.Usage.RateLimit.PrimaryWindow.UsedPercent != 25 {
		t.Fatalf("expected initial used_percent=25, got %d", initial.Usage.RateLimit.PrimaryWindow.UsedPercent)
	}

	// Inside TTL, soft errors must keep using cache and must not repoll.
	h.codexUsageStateRef().codexUsageMu.Lock()
	st := h.codexUsageStateRef().codexUsageByAuth["codex-auth"]
	st.Status = "error"
	st.Error = "Get \"https://chatgpt.com/backend-api/wham/usage\": EOF"
	st.LastPolledAt = time.Now().UTC()
	h.codexUsageStateRef().codexUsageByAuth["codex-auth"] = st
	h.codexUsageStateRef().codexUsageMu.Unlock()

	atomic.StoreInt32(&mode, 1)
	h.refreshCodexUsageFromCacheTTL(context.Background())
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected no repoll before ttl expiry, calls=%d", calls)
	}

	stillCached := h.codexUsageByAuthSnapshot()["codex-auth"]
	if stillCached.Status != "error" {
		t.Fatalf("expected status to remain error while serving cache, got %q", stillCached.Status)
	}
	if !stillCached.HasUsage || stillCached.Usage == nil || stillCached.Usage.RateLimit == nil || stillCached.Usage.RateLimit.PrimaryWindow == nil {
		t.Fatal("expected cached usage to be kept while ttl is active")
	}
	if stillCached.Usage.RateLimit.PrimaryWindow.UsedPercent != 25 {
		t.Fatalf("expected cached used_percent=25 before ttl expiry, got %d", stillCached.Usage.RateLimit.PrimaryWindow.UsedPercent)
	}
	_, _, hasData := h.codexUsageSnapshot()
	if !hasData {
		t.Fatal("expected hasData=true while cache is still usable")
	}

	// Once TTL expires, the selected auth may be retried.
	h.codexUsageStateRef().codexUsageMu.Lock()
	st = h.codexUsageStateRef().codexUsageByAuth["codex-auth"]
	st.LastPolledAt = time.Now().Add(-(codexUsagePollInterval + time.Second)).UTC()
	h.codexUsageStateRef().codexUsageByAuth["codex-auth"] = st
	h.codexUsageStateRef().codexUsageMu.Unlock()

	h.refreshCodexUsageFromCacheTTL(context.Background())
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected one retry after ttl expiry, calls=%d", calls)
	}

	afterFailure := h.codexUsageByAuthSnapshot()["codex-auth"]
	if afterFailure.Status != "error" {
		t.Fatalf("expected status to remain error on soft failure, got %q", afterFailure.Status)
	}
	if !afterFailure.HasUsage || afterFailure.Usage == nil || afterFailure.Usage.RateLimit == nil || afterFailure.Usage.RateLimit.PrimaryWindow == nil {
		t.Fatal("expected cached usage to be kept after soft failure")
	}
	if afterFailure.Usage.RateLimit.PrimaryWindow.UsedPercent != 25 {
		t.Fatalf("expected cached used_percent=25 after failure, got %d", afterFailure.Usage.RateLimit.PrimaryWindow.UsedPercent)
	}

	// After another TTL interval, the next poll may recover the auth.
	atomic.StoreInt32(&mode, 2)
	h.codexUsageStateRef().codexUsageMu.Lock()
	st = h.codexUsageStateRef().codexUsageByAuth["codex-auth"]
	st.LastPolledAt = time.Now().Add(-(codexUsagePollInterval + time.Second)).UTC()
	h.codexUsageStateRef().codexUsageByAuth["codex-auth"] = st
	h.codexUsageStateRef().codexUsageMu.Unlock()

	h.refreshCodexUsageFromCacheTTL(context.Background())
	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("expected recovery poll after ttl expiry, calls=%d", calls)
	}

	afterRecovery := h.codexUsageByAuthSnapshot()["codex-auth"]
	if afterRecovery.Status != "ok" {
		t.Fatalf("expected status ok after upstream recovery, got %q", afterRecovery.Status)
	}
	if afterRecovery.Usage == nil || afterRecovery.Usage.RateLimit == nil || afterRecovery.Usage.RateLimit.PrimaryWindow == nil {
		t.Fatal("expected refreshed usage after upstream recovery")
	}
	if afterRecovery.Usage.RateLimit.PrimaryWindow.UsedPercent != 40 {
		t.Fatalf("expected refreshed used_percent=40, got %d", afterRecovery.Usage.RateLimit.PrimaryWindow.UsedPercent)
	}
}

func TestRefreshCodexUsageFromCacheTTL_DropsDisabledAuthFromAggregation(t *testing.T) {
	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	ctx := context.Background()

	_, _ = manager.Register(ctx, &coreauth.Auth{
		ID:       "codex-active",
		Provider: "codex",
		FileName: "codex-active.json",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"access_token": "token-active",
		},
	})
	_, _ = manager.Register(ctx, &coreauth.Auth{
		ID:       "codex-removed",
		Provider: "codex",
		FileName: "codex-removed.json",
		Status:   coreauth.StatusDisabled,
		Disabled: true,
		Metadata: map[string]any{
			"access_token": "token-removed",
		},
	})
	h := seedTestCodexUsageState(&Handler{
		cfg:         &config.Config{},
		authManager: manager,
	}, &codexUsageState{
		codexUsageByAuth: map[string]codexAuthUsageStatus{
			"codex-active": {
				AuthID:   "codex-active",
				FileName: "codex-active.json",
				Status:   "ok",
				HasUsage: true,
				Usage: &codexUsagePayload{
					PlanType: "plus",
					RateLimit: &codexUsageRateLimit{
						PrimaryWindow: &codexUsageWindow{
							UsedPercent:        20,
							LimitWindowSeconds: 18000,
						},
					},
				},
			},
			"codex-removed": {
				AuthID:   "codex-removed",
				FileName: "codex-removed.json",
				Status:   "ok",
				HasUsage: true,
				Usage: &codexUsagePayload{
					PlanType: "plus",
					RateLimit: &codexUsageRateLimit{
						PrimaryWindow: &codexUsageWindow{
							UsedPercent:        80,
							LimitWindowSeconds: 18000,
						},
					},
				},
			},
		},
		codexUsageCompat: defaultCodexUsagePayload(),
	})
	h.setSelectedCodexAuthID("")

	h.refreshCodexUsageFromCacheTTL(context.Background())
	compat, summary, _ := h.codexUsageSnapshot()

	if summary.AuthFilesTotal != 1 {
		t.Fatalf("expected disabled auth to be excluded, got auth_files_total=%d", summary.AuthFilesTotal)
	}
	if summary.AuthFilesWithUsage != 1 {
		t.Fatalf("expected with_usage=1, got %d", summary.AuthFilesWithUsage)
	}
	if compat.Extensions == nil || len(compat.Extensions.ActiveAuthFiles) != 1 {
		t.Fatalf("expected exactly one active auth file in extensions, got %#v", compat.Extensions)
	}
	if compat.Extensions.ActiveAuthFiles[0].AuthID != "codex-active" {
		t.Fatalf("expected remaining auth to be codex-active, got %q", compat.Extensions.ActiveAuthFiles[0].AuthID)
	}
}

func TestRefreshCodexUsageFromCacheTTL_DropsMissingFileBackedAuthFromAggregation(t *testing.T) {
	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	ctx := context.Background()

	missingPath := filepath.Join(t.TempDir(), "removed.json")
	if err := os.WriteFile(missingPath, []byte(`{"type":"codex"}`), 0o600); err != nil {
		t.Fatalf("write temp auth file: %v", err)
	}
	if err := os.Remove(missingPath); err != nil {
		t.Fatalf("remove temp auth file: %v", err)
	}

	_, _ = manager.Register(ctx, &coreauth.Auth{
		ID:       "codex-missing",
		Provider: "codex",
		FileName: "removed.json",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": missingPath,
		},
		Metadata: map[string]any{
			"access_token": "token-missing",
		},
	})
	h := seedTestCodexUsageState(&Handler{
		cfg:         &config.Config{},
		authManager: manager,
	}, &codexUsageState{
		codexUsageByAuth: map[string]codexAuthUsageStatus{
			"codex-missing": {
				AuthID:   "codex-missing",
				FileName: "removed.json",
				Status:   "ok",
				HasUsage: true,
				Usage: &codexUsagePayload{
					PlanType: "plus",
					RateLimit: &codexUsageRateLimit{
						PrimaryWindow: &codexUsageWindow{
							UsedPercent:        70,
							LimitWindowSeconds: 18000,
						},
					},
				},
			},
		},
		codexUsageCompat: defaultCodexUsagePayload(),
	})
	h.setSelectedCodexAuthID("")

	h.refreshCodexUsageFromCacheTTL(context.Background())
	compat, summary, hasData := h.codexUsageSnapshot()

	if summary.AuthFilesTotal != 0 {
		t.Fatalf("expected missing file-backed auth to be excluded, got auth_files_total=%d", summary.AuthFilesTotal)
	}
	if hasData {
		t.Fatal("expected hasData=false after removing missing file-backed auth")
	}
	if compat.Extensions != nil && len(compat.Extensions.ActiveAuthFiles) > 0 {
		t.Fatalf("expected no active auth files in extensions, got %#v", compat.Extensions.ActiveAuthFiles)
	}
}

func TestRefreshCodexUsageFromCacheTTL_PrunesRemovedAuthFromCache(t *testing.T) {
	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	ctx := context.Background()

	_, _ = manager.Register(ctx, &coreauth.Auth{
		ID:       "codex-live",
		Provider: "codex",
		FileName: "codex-live.json",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"access_token": "token-live",
		},
	})
	now := time.Now().UTC()
	h := seedTestCodexUsageState(&Handler{
		cfg:         &config.Config{},
		authManager: manager,
	}, &codexUsageState{
		codexUsageByAuth: map[string]codexAuthUsageStatus{
			"codex-live": {
				AuthID:       "codex-live",
				FileName:     "codex-live.json",
				Status:       "ok",
				HasUsage:     true,
				LastPolledAt: now,
				Usage: &codexUsagePayload{
					PlanType: "plus",
					RateLimit: &codexUsageRateLimit{
						PrimaryWindow: &codexUsageWindow{UsedPercent: 20, LimitWindowSeconds: 18000},
					},
				},
			},
			"codex-stale": {
				AuthID:       "codex-stale",
				FileName:     "codex-stale.json",
				Status:       "ok",
				HasUsage:     true,
				LastPolledAt: now,
				Usage: &codexUsagePayload{
					PlanType: "plus",
					RateLimit: &codexUsageRateLimit{
						PrimaryWindow: &codexUsageWindow{UsedPercent: 80, LimitWindowSeconds: 18000},
					},
				},
			},
		},
		codexUsageCompat: defaultCodexUsagePayload(),
	})
	h.setSelectedCodexAuthID("")

	h.refreshCodexUsageFromCacheTTL(context.Background())

	snapshot := h.codexUsageByAuthSnapshot()
	if _, ok := snapshot["codex-stale"]; ok {
		t.Fatalf("expected stale auth usage to be pruned, got %#v", snapshot["codex-stale"])
	}
	if _, ok := snapshot["codex-live"]; !ok {
		t.Fatal("expected live auth usage to remain")
	}
	_, summary, _ := h.codexUsageSnapshot()
	if summary.AuthFilesTotal != 1 {
		t.Fatalf("expected auth_files_total=1 after pruning stale auth, got %d", summary.AuthFilesTotal)
	}
}

func TestBuildCodexUsageRecovery_CombinedTracksSignificantRecovery(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	recovery := buildCodexUsageRecovery(map[string]codexAuthUsageStatus{
		"team-active": {
			PlanType: "team",
			Status:   "ok",
			Usage: &codexUsagePayload{
				PlanType: "team",
				RateLimit: &codexUsageRateLimit{
					PrimaryWindow:   &codexUsageWindow{UsedPercent: 20, LimitWindowSeconds: 18000, ResetAt: now.Add(2 * time.Hour).Unix()},
					SecondaryWindow: &codexUsageWindow{UsedPercent: 40, LimitWindowSeconds: 604800, ResetAt: now.Add(4 * 24 * time.Hour).Unix()},
				},
			},
		},
		"team-week-exhausted": {
			PlanType: "team",
			Status:   "ok",
			Usage: &codexUsagePayload{
				PlanType: "team",
				RateLimit: &codexUsageRateLimit{
					PrimaryWindow:   &codexUsageWindow{UsedPercent: 100, LimitWindowSeconds: 18000, ResetAt: now.Add(1 * time.Hour).Unix()},
					SecondaryWindow: &codexUsageWindow{UsedPercent: 100, LimitWindowSeconds: 604800, ResetAt: now.Add(48 * time.Hour).Unix()},
				},
			},
		},
		"free-five-blocked": {
			PlanType: "free",
			Status:   "ok",
			Usage: &codexUsagePayload{
				PlanType: "free",
				RateLimit: &codexUsageRateLimit{
					PrimaryWindow:   &codexUsageWindow{UsedPercent: 100, LimitWindowSeconds: 18000, ResetAt: now.Add(30 * time.Minute).Unix()},
					SecondaryWindow: &codexUsageWindow{UsedPercent: 50, LimitWindowSeconds: 604800, ResetAt: now.Add(72 * time.Hour).Unix()},
				},
			},
		},
		"hard-failed": {
			PlanType: "team",
			Status:   "error",
			Error:    "usage request failed: status=401 body={\"error\":{\"code\":\"token_invalidated\"}}",
			Usage: &codexUsagePayload{
				PlanType: "team",
				RateLimit: &codexUsageRateLimit{
					PrimaryWindow:   &codexUsageWindow{UsedPercent: 0, LimitWindowSeconds: 18000},
					SecondaryWindow: &codexUsageWindow{UsedPercent: 0, LimitWindowSeconds: 604800},
				},
			},
		},
	}, now, 0.2, 6.0)

	if recovery == nil || recovery.Combined == nil {
		t.Fatal("expected combined recovery summary")
	}
	combined := recovery.Combined
	if math.Abs(combined.TotalUnits-2.2) > 1e-9 {
		t.Fatalf("expected total_units=2.2 without hard failures, got %v", combined.TotalUnits)
	}
	if math.Abs(combined.AvailableUnitsNow-0.6) > 1e-9 {
		t.Fatalf("expected available_now=0.6, got %v", combined.AvailableUnitsNow)
	}
	if math.Abs(combined.FiveHourBlockedUnitsNow-0.1) > 1e-9 {
		t.Fatalf("expected five_hour_blocked_units_now=0.1, got %v", combined.FiveHourBlockedUnitsNow)
	}
	if combined.FiveHourNextWaitSeconds != 1800 {
		t.Fatalf("expected five_hour_next_wait_seconds=1800, got %d", combined.FiveHourNextWaitSeconds)
	}
	if combined.NextWaitSeconds != 1800 {
		t.Fatalf("expected next_wait_seconds=1800, got %d", combined.NextWaitSeconds)
	}
	if math.Abs(combined.NextAvailableUnits-0.7) > 1e-9 {
		t.Fatalf("expected next_available_units=0.7, got %v", combined.NextAvailableUnits)
	}
	if math.Abs(combined.SignificantDeltaUnits-1.0) > 1e-9 {
		t.Fatalf("expected significant_delta_units=1.0, got %v", combined.SignificantDeltaUnits)
	}
	if combined.SignificantWaitSeconds != 48*3600 {
		t.Fatalf("expected significant_wait_seconds=172800, got %d", combined.SignificantWaitSeconds)
	}
	if math.Abs(combined.SignificantAvailableUnits-1.7) > 1e-9 {
		t.Fatalf("expected significant_available_units=1.7, got %v", combined.SignificantAvailableUnits)
	}
	if combined.FullWaitSeconds != 96*3600 {
		t.Fatalf("expected full_wait_seconds=345600, got %d", combined.FullWaitSeconds)
	}
	if math.Abs(combined.FullAvailableUnits-2.2) > 1e-9 {
		t.Fatalf("expected full_available_units=2.2, got %v", combined.FullAvailableUnits)
	}
	if len(combined.Events) != 4 {
		t.Fatalf("expected 4 combined recovery events, got %d", len(combined.Events))
	}
	if math.Abs(combined.Events[0].AvailableUnits-0.7) > 1e-9 {
		t.Fatalf("expected first event available_units=0.7, got %v", combined.Events[0].AvailableUnits)
	}
}

func TestGetCodexUsageCompat_IncludesCombinedRecoveryExtension(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC()
	h := seedTestCodexUsageState(&Handler{}, &codexUsageState{
		codexUsageByAuth: map[string]codexAuthUsageStatus{
			"ok-auth": {
				AuthID:        "ok-auth",
				Email:         "ok@example.com",
				PlanType:      "team",
				Status:        "ok",
				HasUsage:      true,
				LastSuccessAt: &now,
				Usage: &codexUsagePayload{
					PlanType: "team",
					RateLimit: &codexUsageRateLimit{
						PrimaryWindow:   &codexUsageWindow{UsedPercent: 100, LimitWindowSeconds: 18000, ResetAt: now.Add(15 * time.Minute).Unix()},
						SecondaryWindow: &codexUsageWindow{UsedPercent: 25, LimitWindowSeconds: 604800, ResetAt: now.Add(7 * 24 * time.Hour).Unix()},
					},
				},
			},
			"hard-auth": {
				AuthID:   "hard-auth",
				Email:    "hard@example.com",
				PlanType: "team",
				Status:   "error",
				Error:    "usage request failed: status=401 body={\"error\":{\"code\":\"token_invalidated\"}}",
			},
		},
		codexUsageCompat: codexUsagePayload{
			PlanType: "team",
			RateLimit: &codexUsageRateLimit{
				PrimaryWindow:   &codexUsageWindow{UsedPercent: 100, LimitWindowSeconds: 18000},
				SecondaryWindow: &codexUsageWindow{UsedPercent: 25, LimitWindowSeconds: 604800},
			},
		},
		codexUsageSummary: codexUsageSummaryResponse{AuthFiles: []codexAuthUsageStatus{{AuthID: "ok-auth"}, {AuthID: "hard-auth"}}},
		codexUsageHasData: true,
	})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/codex/usage", nil)
	h.GetCodexUsageCompat(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload codexUsagePayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Extensions == nil || payload.Extensions.Recovery == nil || payload.Extensions.Recovery.Combined == nil {
		t.Fatal("expected combined recovery extension in compat payload")
	}
	combined := payload.Extensions.Recovery.Combined
	if math.Abs(combined.TotalUnits-1.0) > 1e-9 {
		t.Fatalf("expected hard auth excluded from combined total, got %v", combined.TotalUnits)
	}
	if combined.NextWaitSeconds <= 0 {
		t.Fatalf("expected positive next_wait_seconds, got %d", combined.NextWaitSeconds)
	}
	if math.Abs(combined.AvailableUnitsNow-0.0) > 1e-9 {
		t.Fatalf("expected five-hour gate to block current available units, got %v", combined.AvailableUnitsNow)
	}
	if math.Abs(combined.FiveHourBlockedUnitsNow-0.75) > 1e-9 {
		t.Fatalf("expected blocked weekly availability 0.75, got %v", combined.FiveHourBlockedUnitsNow)
	}
}
