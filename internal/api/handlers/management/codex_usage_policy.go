package management

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const codexUsageOpportunisticSyncBudget = 1

// codexUsagePollPolicy encapsulates how usage polling is applied to cached auth statuses.
// Keeping policy code in an isolated file reduces merge pressure on the main handler file.
type codexUsagePollPolicy interface {
	Refresh(ctx context.Context, now time.Time, selectedAuthID string, current map[string]codexAuthUsageStatus, codexAuths map[string]*coreauth.Auth) bool
}

type selectedAuthOnlyCodexUsagePollPolicy struct {
	handler *Handler
}

func (h *Handler) codexUsagePollPolicy() codexUsagePollPolicy {
	return selectedAuthOnlyCodexUsagePollPolicy{handler: h}
}

func (p selectedAuthOnlyCodexUsagePollPolicy) Refresh(ctx context.Context, now time.Time, selectedAuthID string, current map[string]codexAuthUsageStatus, codexAuths map[string]*coreauth.Auth) bool {
	h := p.handler
	if h == nil {
		return false
	}

	changed := hydrateCodexAuthUsageStatuses(current, codexAuths)

	selectedAuthID = strings.TrimSpace(selectedAuthID)
	if selectedAuthID != "" {
		if auth, ok := codexAuths[selectedAuthID]; ok && auth != nil {
			status := applyCodexAuthUsageIdentity(current[selectedAuthID], auth)
			shouldPollSelected := status.LastPolledAt.IsZero() ||
				now.Sub(status.LastPolledAt) >= codexUsagePollInterval ||
				(isCodexUsageSoftError(status) && now.Sub(status.LastPolledAt) >= codexUsageSoftErrorRetry)
			if shouldPollSelected {
				var polled bool
				status, polled = p.pollAuthUsage(ctx, now, status, auth)
				if polled {
					changed = true
				}
			}
			current[selectedAuthID] = status
		}
	}

	// Opportunistic correction refresh (no timer):
	// collect high-risk auth IDs first.
	authIDs := make([]string, 0, len(codexAuths))
	for authID := range codexAuths {
		authIDs = append(authIDs, authID)
	}
	sort.Strings(authIDs)

	candidates := make([]string, 0, len(authIDs))
	for _, authID := range authIDs {
		if authID == selectedAuthID {
			continue
		}
		auth := codexAuths[authID]
		if auth == nil {
			continue
		}
		status := applyCodexAuthUsageIdentity(current[authID], auth)
		if !codexUsageTTLExpired(status, now) {
			current[authID] = status
			continue
		}
		if !shouldOpportunisticRefreshCodexUsage(status) {
			current[authID] = status
			continue
		}
		current[authID] = status
		candidates = append(candidates, authID)
	}

	// Keep request latency predictable: perform at most one opportunistic poll synchronously.
	syncBudget := codexUsageOpportunisticSyncBudget
	if syncBudget < 0 {
		syncBudget = 0
	}
	asyncCandidates := make([]string, 0, len(candidates))
	for _, authID := range candidates {
		auth := codexAuths[authID]
		if auth == nil {
			continue
		}
		if syncBudget > 0 {
			status := applyCodexAuthUsageIdentity(current[authID], auth)
			var polled bool
			status, polled = p.pollAuthUsage(ctx, now, status, auth)
			current[authID] = status
			if polled {
				changed = true
			}
			syncBudget--
			continue
		}
		asyncCandidates = append(asyncCandidates, authID)
	}
	if len(asyncCandidates) > 0 {
		h.scheduleCodexUsageAsyncPoll(asyncCandidates)
	}

	return changed
}

func (h *Handler) scheduleCodexUsageAsyncPoll(authIDs []string) {
	if h == nil || len(authIDs) == 0 {
		return
	}
	state := h.codexUsageStateRef()
	if state == nil {
		return
	}
	if !state.codexUsageAsyncPoll.CompareAndSwap(false, true) {
		return
	}
	ids := append([]string(nil), authIDs...)
	go func() {
		defer state.codexUsageAsyncPoll.Store(false)
		h.refreshCodexUsageCandidates(context.Background(), ids)
	}()
}

func (h *Handler) refreshCodexUsageCandidates(ctx context.Context, authIDs []string) {
	if h == nil || len(authIDs) == 0 {
		return
	}
	state := h.codexUsageStateRef()
	if state == nil {
		return
	}
	manager := h.authManager
	if manager == nil {
		return
	}

	ids := make([]string, 0, len(authIDs))
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
		ids = append(ids, authID)
	}
	if len(ids) == 0 {
		return
	}
	sort.Strings(ids)

	codexAuths := make(map[string]*coreauth.Auth)
	for _, auth := range manager.List() {
		if !shouldTrackCodexUsageAuth(auth) {
			continue
		}
		codexAuths[strings.TrimSpace(auth.ID)] = auth
	}

	previous := h.codexUsageByAuthSnapshot()
	current := make(map[string]codexAuthUsageStatus, len(previous))
	changed := false
	for key, value := range previous {
		if _, ok := codexAuths[key]; !ok {
			changed = true
			continue
		}
		current[key] = value
	}

	policy := selectedAuthOnlyCodexUsagePollPolicy{handler: h}
	for _, authID := range ids {
		auth := codexAuths[authID]
		if auth == nil {
			continue
		}
		status := applyCodexAuthUsageIdentity(current[authID], auth)
		now := time.Now().UTC()
		if !codexUsageTTLExpired(status, now) {
			current[authID] = status
			continue
		}
		if !shouldOpportunisticRefreshCodexUsage(status) {
			current[authID] = status
			continue
		}
		var polled bool
		status, polled = policy.pollAuthUsage(ctx, now, status, auth)
		current[authID] = status
		if polled {
			changed = true
		}
	}

	if !changed {
		return
	}

	// Merge touched auth statuses back under poll lock to avoid clobbering
	// concurrent foreground refreshes, while keeping network calls lock-free.
	state.codexUsagePollMu.Lock()
	defer state.codexUsagePollMu.Unlock()

	latest := h.codexUsageByAuthSnapshot()
	for authID := range latest {
		if _, ok := codexAuths[authID]; !ok {
			delete(latest, authID)
		}
	}
	for _, authID := range ids {
		if status, ok := current[authID]; ok {
			latest[authID] = status
		}
	}
	selectedAuthID := h.selectedCodexAuthID()
	if selectedAuthID != "" {
		if _, ok := latest[selectedAuthID]; !ok {
			selectedAuthID = ""
		}
	}
	h.updateCodexUsageState(latest, selectedAuthID, time.Now().UTC(), true)
}

func (p selectedAuthOnlyCodexUsagePollPolicy) pollAuthUsage(ctx context.Context, now time.Time, status codexAuthUsageStatus, auth *coreauth.Auth) (codexAuthUsageStatus, bool) {
	h := p.handler
	if h == nil || auth == nil {
		return status, false
	}

	status.LastPolledAt = now
	token := extractCodexAccessToken(auth)
	if token == "" {
		status.Status = "error"
		status.Error = "missing access_token"
		status.Usage = nil
		status.HasUsage = false
		status.LastSuccessAt = nil
		return status, true
	}

	pollCtx := ctx
	var cancel context.CancelFunc
	if pollCtx == nil {
		pollCtx = context.Background()
	}
	pollCtx, cancel = context.WithTimeout(pollCtx, codexUsageRequestTimeout)
	payload, baseURL, pathStyle, err := h.fetchCodexUsagePayload(pollCtx, auth, token, status.AccountID)
	cancel()

	status.BaseURL = baseURL
	status.PathStyle = pathStyle
	if err != nil {
		status.Status = "error"
		status.Error = err.Error()
		if httpErr, okHTTP := err.(*codexUsageHTTPError); okHTTP && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden) {
			// Credential is no longer valid; clear stale usage cache for this auth.
			status.Usage = nil
			status.HasUsage = false
			status.LastSuccessAt = nil
		}
		log.WithError(err).Debugf("codex usage poll failed for auth %s", status.AuthID)
		return status, true
	}

	status.Status = "ok"
	status.Error = ""
	if accountID := strings.TrimSpace(payload.AccountID); accountID != "" {
		status.AccountID = accountID
	}
	if email := strings.TrimSpace(payload.Email); email != "" {
		status.Email = email
	}
	status.PlanType = strings.TrimSpace(payload.PlanType)
	copied := payload
	status.Usage = &copied
	status.HasUsage = true
	lastSuccess := now
	status.LastSuccessAt = &lastSuccess
	return status, true
}

func codexUsageTTLExpired(status codexAuthUsageStatus, now time.Time) bool {
	if status.LastPolledAt.IsZero() {
		return true
	}
	return now.Sub(status.LastPolledAt) >= codexUsagePollInterval
}

func shouldOpportunisticRefreshCodexUsage(status codexAuthUsageStatus) bool {
	switch strings.ToLower(strings.TrimSpace(status.Status)) {
	case "skipped":
		return true
	case "error":
		return true
	}
	if !status.HasUsage || status.Usage == nil {
		return true
	}
	weeklyUsed, ok := codexWeeklyUsedPercent(status)
	return ok && weeklyUsed >= 95
}

func codexWeeklyUsedPercent(status codexAuthUsageStatus) (int, bool) {
	if status.Usage == nil {
		return 0, false
	}
	normalized := normalizeCodexMainRateLimitWindows(status.Usage.RateLimit)
	if normalized == nil {
		return 0, false
	}
	week := normalized.SecondaryWindow
	if week == nil && normalized.PrimaryWindow != nil && isLikelyCodexWeeklyWindow(normalized.PrimaryWindow.LimitWindowSeconds) {
		week = normalized.PrimaryWindow
	}
	if week == nil {
		return 0, false
	}
	return week.UsedPercent, true
}

func isCodexUsageSoftError(status codexAuthUsageStatus) bool {
	if !strings.EqualFold(strings.TrimSpace(status.Status), "error") {
		return false
	}
	// Soft error: keep stale usage as fallback but force a fresh upstream attempt.
	return status.HasUsage && status.Usage != nil
}

func hydrateCodexAuthUsageStatuses(current map[string]codexAuthUsageStatus, codexAuths map[string]*coreauth.Auth) bool {
	if len(codexAuths) == 0 {
		return false
	}

	changed := false
	authIDs := make([]string, 0, len(codexAuths))
	for authID := range codexAuths {
		authIDs = append(authIDs, authID)
	}
	sort.Strings(authIDs)

	for _, authID := range authIDs {
		auth := codexAuths[authID]
		status, exists := current[authID]
		if !exists {
			status = codexAuthUsageStatus{AuthID: authID}
			changed = true
		}
		current[authID] = applyCodexAuthUsageIdentity(status, auth)
	}
	return changed
}

func applyCodexAuthUsageIdentity(status codexAuthUsageStatus, auth *coreauth.Auth) codexAuthUsageStatus {
	if auth == nil {
		return status
	}
	authID := strings.TrimSpace(auth.ID)
	status.AuthID = authID
	status.FileName = strings.TrimSpace(auth.FileName)
	status.Email = authEmail(auth)
	status.PlanType = inferCodexPlanType(auth, status)
	status.AccountID = extractCodexAccountID(auth)
	if status.Status == "" {
		status.Status = "skipped"
	}
	return status
}
