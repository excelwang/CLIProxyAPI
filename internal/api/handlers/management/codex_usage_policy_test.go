package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestCodexUsagePollPolicyRefreshRetriesTransientErrors(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour).Unix()
	var (
		mu   sync.Mutex
		hits = map[string]int{}
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			http.NotFound(w, r)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Lock()
		hits[token]++
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"account_id": "acct-" + token,
			"email":      token + "@example.com",
			"plan_type":  "team",
			"rate_limit": map[string]any{
				"allowed":       true,
				"limit_reached": false,
				"secondary_window": map[string]any{
					"used_percent":         24,
					"limit_window_seconds": 604800,
					"reset_after_seconds":  7200,
					"reset_at":             resetAt,
				},
			},
		})
	}))
	defer server.Close()

	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	policy := selectedAuthOnlyCodexUsagePollPolicy{handler: handler}

	selectedAuth := newCodexUsagePolicyTestAuth("selected-auth", "selected-token", server.URL)
	softAuth := newCodexUsagePolicyTestAuth("soft-auth", "soft-token", server.URL)
	hardAuth := newCodexUsagePolicyTestAuth("hard-auth", "hard-token", server.URL)
	codexAuths := map[string]*coreauth.Auth{
		selectedAuth.ID: selectedAuth,
		softAuth.ID:     softAuth,
		hardAuth.ID:     hardAuth,
	}

	lastSuccess := now.Add(-4 * time.Minute)
	current := map[string]codexAuthUsageStatus{
		selectedAuth.ID: {
			AuthID:       selectedAuth.ID,
			Status:       "skipped",
			LastPolledAt: now.Add(-2 * codexUsagePollInterval),
		},
		softAuth.ID: {
			AuthID:        softAuth.ID,
			Status:        "error",
			Error:         `Get "https://chatgpt.com/backend-api/wham/usage": EOF`,
			ErrorKind:     codexUsageErrorKindTransient,
			Transient:     true,
			Stale:         true,
			LastPolledAt:  now.Add(-2 * codexUsagePollInterval),
			LastSuccessAt: &lastSuccess,
			HasUsage:      true,
			Usage:         testCodexUsagePayload("soft@example.com", "team", 61, resetAt),
		},
		hardAuth.ID: {
			AuthID:       hardAuth.ID,
			Status:       "error",
			Error:        "usage request failed: status=401 body=unauthorized",
			ErrorKind:    codexUsageErrorKindAuth,
			LastPolledAt: now.Add(-2 * codexUsagePollInterval),
		},
	}

	changed := policy.Refresh(context.Background(), now, selectedAuth.ID, current, codexAuths)
	if !changed {
		t.Fatalf("Refresh changed = false, want true")
	}

	mu.Lock()
	selectedHits := hits["selected-token"]
	softHits := hits["soft-token"]
	hardHits := hits["hard-token"]
	mu.Unlock()
	if selectedHits != 1 {
		t.Fatalf("selected auth hits = %d, want 1", selectedHits)
	}
	if softHits != 1 {
		t.Fatalf("soft auth hits = %d, want 1", softHits)
	}
	if hardHits != 0 {
		t.Fatalf("hard auth hits = %d, want 0", hardHits)
	}

	selectedStatus := current[selectedAuth.ID]
	if selectedStatus.Status != "ok" {
		t.Fatalf("selected status = %q, want ok", selectedStatus.Status)
	}
	if !selectedStatus.HasUsage || selectedStatus.Usage == nil {
		t.Fatalf("selected status missing usage payload")
	}

	softStatus := current[softAuth.ID]
	if softStatus.Status != "ok" {
		t.Fatalf("soft status = %q, want ok", softStatus.Status)
	}
	if softStatus.Error != "" {
		t.Fatalf("soft error = %q, want empty", softStatus.Error)
	}
	if softStatus.ErrorKind != "" {
		t.Fatalf("soft error kind = %q, want empty", softStatus.ErrorKind)
	}
	if softStatus.Transient {
		t.Fatalf("soft transient = true, want false")
	}
	if softStatus.Stale {
		t.Fatalf("soft stale = true, want false")
	}
	if !softStatus.HasUsage || softStatus.Usage == nil {
		t.Fatalf("soft status missing refreshed usage payload")
	}
	if softStatus.LastSuccessAt == nil || !softStatus.LastSuccessAt.Equal(now) {
		t.Fatalf("soft last success = %v, want %v", softStatus.LastSuccessAt, now)
	}

	hardStatus := current[hardAuth.ID]
	if !hardStatus.LastPolledAt.Equal(now.Add(-2 * codexUsagePollInterval)) {
		t.Fatalf("hard last polled changed to %v, want unchanged", hardStatus.LastPolledAt)
	}
}

func TestCodexUsagePollPolicyRefreshRetriesSoftErrorsWithoutSelectedAuth(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour).Unix()
	var (
		mu   sync.Mutex
		hits = map[string]int{}
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			http.NotFound(w, r)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Lock()
		hits[token]++
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"account_id": "acct-" + token,
			"email":      token + "@example.com",
			"plan_type":  "team",
			"rate_limit": map[string]any{
				"allowed":       true,
				"limit_reached": false,
				"secondary_window": map[string]any{
					"used_percent":         8,
					"limit_window_seconds": 604800,
					"reset_after_seconds":  7200,
					"reset_at":             resetAt,
				},
			},
		})
	}))
	defer server.Close()

	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	policy := selectedAuthOnlyCodexUsagePollPolicy{handler: handler}

	softAuth := newCodexUsagePolicyTestAuth("soft-auth", "soft-token", server.URL)
	hardAuth := newCodexUsagePolicyTestAuth("hard-auth", "hard-token", server.URL)
	current := map[string]codexAuthUsageStatus{
		softAuth.ID: {
			AuthID:       softAuth.ID,
			Status:       "error",
			Error:        `Get "https://chatgpt.com/backend-api/wham/usage": EOF`,
			LastPolledAt: now.Add(-2 * codexUsagePollInterval),
		},
		hardAuth.ID: {
			AuthID:       hardAuth.ID,
			Status:       "error",
			Error:        "usage request failed: status=403 body=forbidden",
			LastPolledAt: now.Add(-2 * codexUsagePollInterval),
		},
	}
	codexAuths := map[string]*coreauth.Auth{
		softAuth.ID: softAuth,
		hardAuth.ID: hardAuth,
	}

	changed := policy.Refresh(context.Background(), now, "", current, codexAuths)
	if !changed {
		t.Fatalf("Refresh changed = false, want true")
	}

	mu.Lock()
	softHits := hits["soft-token"]
	hardHits := hits["hard-token"]
	mu.Unlock()
	if softHits != 1 {
		t.Fatalf("soft auth hits = %d, want 1", softHits)
	}
	if hardHits != 0 {
		t.Fatalf("hard auth hits = %d, want 0", hardHits)
	}
	if current[softAuth.ID].Status != "ok" {
		t.Fatalf("soft status = %q, want ok", current[softAuth.ID].Status)
	}
	if current[hardAuth.ID].Status != "error" {
		t.Fatalf("hard status = %q, want error", current[hardAuth.ID].Status)
	}
}

func TestBuildCodexUsageExtensionsIncludesTransientMarkersAndRecentPollTime(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	lastSuccess := now.Add(-30 * time.Minute)
	lastPolled := now.Add(-2 * time.Minute)
	current := map[string]codexAuthUsageStatus{
		"stale-auth": {
			AuthID:        "stale-auth",
			FileName:      "stale-auth-team.json",
			Email:         "stale@example.com",
			PlanType:      "team",
			Status:        "error",
			Error:         `Get "https://chatgpt.com/backend-api/wham/usage": EOF`,
			ErrorKind:     codexUsageErrorKindTransient,
			Transient:     true,
			Stale:         true,
			LastPolledAt:  lastPolled,
			LastSuccessAt: &lastSuccess,
			HasUsage:      true,
			Usage:         testCodexUsagePayload("stale@example.com", "team", 35, now.Add(2*time.Hour).Unix()),
		},
	}

	extensions := buildCodexUsageExtensions(current, now, codexFreePlanWeight, codexProPlanWeight, "", nil, "", "", "", time.Time{})
	if extensions == nil {
		t.Fatalf("buildCodexUsageExtensions returned nil")
	}
	if len(extensions.ActiveAuthFiles) != 1 {
		t.Fatalf("active auth files len = %d, want 1", len(extensions.ActiveAuthFiles))
	}

	item := extensions.ActiveAuthFiles[0]
	if item.ErrorKind != codexUsageErrorKindTransient {
		t.Fatalf("item.ErrorKind = %q, want %q", item.ErrorKind, codexUsageErrorKindTransient)
	}
	if !item.Transient {
		t.Fatalf("item.Transient = false, want true")
	}
	if !item.Stale {
		t.Fatalf("item.Stale = false, want true")
	}
	if !item.LastUsedAt.Equal(lastPolled) {
		t.Fatalf("item.LastUsedAt = %v, want %v", item.LastUsedAt, lastPolled)
	}
}

func newCodexUsagePolicyTestAuth(id, token, serverURL string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       id,
		Provider: "codex",
		FileName: id + "-team.json",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url":  strings.TrimRight(serverURL, "/") + "/backend-api",
			"plan_type": "team",
			"priority":  "0",
		},
		Metadata: map[string]any{
			"access_token": token,
			"account_id":   "acct-" + id,
			"email":        id + "@example.com",
		},
	}
}

func testCodexUsagePayload(email, planType string, usedPercent int, resetAt int64) *codexUsagePayload {
	return &codexUsagePayload{
		Email:    email,
		PlanType: planType,
		RateLimit: &codexUsageRateLimit{
			Allowed:      usedPercent < 100,
			LimitReached: usedPercent >= 100,
			SecondaryWindow: &codexUsageWindow{
				UsedPercent:        usedPercent,
				LimitWindowSeconds: codexWeeklyWindowSecs,
				ResetAfterSeconds:  int(time.Until(time.Unix(resetAt, 0).UTC()).Seconds()),
				ResetAt:            resetAt,
			},
		},
	}
}
