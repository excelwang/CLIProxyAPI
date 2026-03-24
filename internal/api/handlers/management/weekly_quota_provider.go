package management

import (
	"context"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func (h *Handler) WeeklyQuotaSnapshots(ctx context.Context, authIDs []string) map[string]coreauth.WeeklyQuotaSnapshot {
	if h == nil || len(authIDs) == 0 {
		return nil
	}

	h.refreshCodexUsageFromCacheTTL(ctx)
	statuses := h.codexUsageByAuthSnapshot()
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
