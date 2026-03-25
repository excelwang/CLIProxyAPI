package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestInferCodexPlanTypeUsesAttributesAndTeamFilename(t *testing.T) {
	t.Parallel()

	fromAttributes := &coreauth.Auth{
		Provider:   "codex",
		FileName:   "codex-attr.json",
		Attributes: map[string]string{"plan_type": "team"},
	}
	if got := inferCodexPlanType(fromAttributes, codexAuthUsageStatus{}); got != "team" {
		t.Fatalf("inferCodexPlanType(attributes) = %q, want team", got)
	}

	fromFilename := &coreauth.Auth{
		Provider: "codex",
		FileName: "codex-sample-team.json",
	}
	if got := inferCodexPlanType(fromFilename, codexAuthUsageStatus{}); got != "team" {
		t.Fatalf("inferCodexPlanType(filename) = %q, want team", got)
	}
}

func TestWeeklyQuotaSnapshotsBootstrapUnpolledAuths(t *testing.T) {
	t.Parallel()

	resetAt := time.Now().UTC().Add(2 * time.Hour).Unix()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"account_id": "acct-bootstrap",
			"email":      "bootstrap@example.com",
			"plan_type":  "team",
			"rate_limit": map[string]any{
				"allowed":       true,
				"limit_reached": false,
				"secondary_window": map[string]any{
					"used_percent":         25,
					"limit_window_seconds": 604800,
					"reset_after_seconds":  7200,
					"reset_at":             resetAt,
				},
			},
		})
	}))
	defer server.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	for _, authID := range []string{"bootstrap-auth-1", "bootstrap-auth-2"} {
		_, err := manager.Register(context.Background(), &coreauth.Auth{
			ID:       authID,
			Provider: "codex",
			FileName: authID + "-team.json",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"base_url":  server.URL + "/backend-api",
				"plan_type": "team",
				"priority":  "0",
			},
			Metadata: map[string]any{
				"access_token": "token-" + authID,
				"account_id":   "acct-" + authID,
				"email":        authID + "@example.com",
			},
		})
		if err != nil {
			t.Fatalf("Register(%s) error: %v", authID, err)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	snapshots := h.WeeklyQuotaSnapshots(context.Background(), []string{"bootstrap-auth-1", "bootstrap-auth-2"})
	if len(snapshots) != 2 {
		t.Fatalf("WeeklyQuotaSnapshots len = %d, want 2", len(snapshots))
	}
	if hits.Load() != 2 {
		t.Fatalf("bootstrap poll hits = %d, want 2", hits.Load())
	}

	for _, authID := range []string{"bootstrap-auth-1", "bootstrap-auth-2"} {
		snapshot, ok := snapshots[authID]
		if !ok {
			t.Fatalf("snapshot missing for %s", authID)
		}
		if snapshot.RemainingRatio != 0.75 {
			t.Fatalf("snapshot[%s].RemainingRatio = %v, want 0.75", authID, snapshot.RemainingRatio)
		}
		if snapshot.ResetAt.Unix() != resetAt {
			t.Fatalf("snapshot[%s].ResetAt = %d, want %d", authID, snapshot.ResetAt.Unix(), resetAt)
		}
	}

	statuses := h.codexUsageByAuthSnapshot()
	for _, authID := range []string{"bootstrap-auth-1", "bootstrap-auth-2"} {
		status, ok := statuses[authID]
		if !ok {
			t.Fatalf("status missing for %s", authID)
		}
		if status.Status != "ok" {
			t.Fatalf("status[%s] = %q, want ok", authID, status.Status)
		}
		if !status.HasUsage || status.Usage == nil {
			t.Fatalf("status[%s] missing usage payload", authID)
		}
		if status.PlanType != "team" {
			t.Fatalf("status[%s].PlanType = %q, want team", authID, status.PlanType)
		}
	}
}
