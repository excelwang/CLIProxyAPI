package management

import (
	"context"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const codexWeeklyQuotaBootstrapPollLimit = 5

func (h *Handler) WeeklyQuotaSnapshots(ctx context.Context, authIDs []string) map[string]coreauth.WeeklyQuotaSnapshot {
	if h == nil || len(authIDs) == 0 {
		return nil
	}

	h.refreshCodexUsageFromCacheTTL(ctx)
	statuses := h.bootstrapWeeklyQuotaStatuses(ctx, authIDs)
	if len(statuses) == 0 {
		return nil
	}

	now := time.Now().UTC()
	out := make(map[string]coreauth.WeeklyQuotaSnapshot)
	seen := make(map[string]struct{}, len(authIDs))
	for _, authID := range authIDs {
		authID = strings.TrimSpace(authID)
		if authID == "" {
			continue
		}
		if _, ok := seen[authID]; ok {
			continue
		}
		seen[authID] = struct{}{}

		status, ok := statuses[authID]
		if !ok {
			continue
		}

		rate := codexEffectiveMainRateLimit(now, status)
		week := codexWeeklyQuotaWindow(rate)
		if week == nil {
			continue
		}
		resetAtUnix, okReset := codexWindowResetAt(week, now)
		if !okReset || resetAtUnix <= 0 {
			continue
		}

		remainingRatio := 1 - clampRecoveryPercent(float64(week.UsedPercent))/100
		if remainingRatio < 0 {
			remainingRatio = 0
		}
		if remainingRatio > 1 {
			remainingRatio = 1
		}

		observedAt := status.LastPolledAt.UTC()
		if status.LastSuccessAt != nil && status.LastSuccessAt.After(observedAt) {
			observedAt = status.LastSuccessAt.UTC()
		}
		if observedAt.IsZero() {
			observedAt = now
		}

		out[authID] = coreauth.WeeklyQuotaSnapshot{
			AuthID:         authID,
			RemainingRatio: remainingRatio,
			ResetAt:        time.Unix(resetAtUnix, 0).UTC(),
			ObservedAt:     observedAt,
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (h *Handler) bootstrapWeeklyQuotaStatuses(ctx context.Context, authIDs []string) map[string]codexAuthUsageStatus {
	if h == nil {
		return nil
	}

	h.ensureUsageRuntimeInitialized()
	state := h.codexUsageStateRef()
	if state == nil {
		return h.codexUsageByAuthSnapshot()
	}

	state.codexUsagePollMu.Lock()
	defer state.codexUsagePollMu.Unlock()

	now := time.Now().UTC()
	current := h.codexUsageByAuthSnapshot()
	manager := h.authManager
	if manager == nil {
		return current
	}

	codexAuths := make(map[string]*coreauth.Auth)
	for _, auth := range manager.List() {
		if !shouldTrackCodexUsageAuth(auth) {
			continue
		}
		codexAuths[strings.TrimSpace(auth.ID)] = auth
	}
	if len(codexAuths) == 0 {
		return current
	}

	changed := hydrateCodexAuthUsageStatuses(current, codexAuths)
	selectedAuthID := strings.TrimSpace(state.codexUsageSelected)
	if selectedAuthID == "" {
		selectedAuthID = strings.TrimSpace(h.selectedCodexAuthID())
	}
	if selectedAuthID != "" {
		if _, ok := codexAuths[selectedAuthID]; !ok {
			selectedAuthID = ""
		}
	}

	poller := selectedAuthOnlyCodexUsagePollPolicy{handler: h}
	polled := 0
	seen := make(map[string]struct{}, len(authIDs))
	for _, authID := range authIDs {
		if polled >= codexWeeklyQuotaBootstrapPollLimit {
			break
		}
		authID = strings.TrimSpace(authID)
		if authID == "" {
			continue
		}
		if _, ok := seen[authID]; ok {
			continue
		}
		seen[authID] = struct{}{}

		auth, ok := codexAuths[authID]
		if !ok || auth == nil {
			continue
		}
		status := applyCodexAuthUsageIdentity(current[authID], auth)
		if !codexUsageNeedsBootstrapPoll(now, status) {
			current[authID] = status
			continue
		}
		updated, didPoll := poller.pollAuthUsage(ctx, now, status, auth)
		current[authID] = updated
		if didPoll {
			changed = true
			polled++
		}
	}

	if changed {
		h.updateCodexUsageState(current, selectedAuthID, now, true)
	}
	return current
}

func codexUsageNeedsBootstrapPoll(now time.Time, status codexAuthUsageStatus) bool {
	if rate := codexEffectiveMainRateLimit(now, status); codexWeeklyQuotaWindow(rate) != nil {
		return false
	}
	if codexIsAuthFailureStatus(status) {
		return false
	}
	return codexUsageTTLExpired(status, now)
}

func codexWeeklyQuotaWindow(rate *codexUsageRateLimit) *codexUsageWindow {
	if rate == nil {
		return nil
	}
	if rate.SecondaryWindow != nil {
		return rate.SecondaryWindow
	}
	if rate.PrimaryWindow != nil && isLikelyCodexWeeklyWindow(rate.PrimaryWindow.LimitWindowSeconds) {
		return rate.PrimaryWindow
	}
	return nil
}
