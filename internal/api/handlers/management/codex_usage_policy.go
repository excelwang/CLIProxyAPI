package management

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// codexUsagePollPolicy encapsulates how usage polling is applied to cached auth statuses.
// The current policy is intentionally strict: only the selected auth may trigger an upstream
// usage query, and only when its per-auth TTL has expired.
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
	if selectedAuthID == "" {
		return changed
	}
	auth, ok := codexAuths[selectedAuthID]
	if !ok || auth == nil {
		return changed
	}

	status := applyCodexAuthUsageIdentity(current[selectedAuthID], auth)
	if codexUsageTTLExpired(status, now) {
		var polled bool
		status, polled = p.pollAuthUsage(ctx, now, status, auth)
		if polled {
			changed = true
		}
	}
	current[selectedAuthID] = status

	return changed
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
		normalized := applyCodexAuthUsageIdentity(status, auth)
		if !codexAuthUsageIdentityEqual(status, normalized) {
			changed = true
		}
		current[authID] = normalized
	}
	return changed
}

func codexAuthUsageIdentityEqual(left, right codexAuthUsageStatus) bool {
	return strings.TrimSpace(left.AuthID) == strings.TrimSpace(right.AuthID) &&
		strings.TrimSpace(left.FileName) == strings.TrimSpace(right.FileName) &&
		strings.TrimSpace(left.Email) == strings.TrimSpace(right.Email) &&
		strings.TrimSpace(left.PlanType) == strings.TrimSpace(right.PlanType) &&
		left.Priority == right.Priority &&
		strings.TrimSpace(left.AccountID) == strings.TrimSpace(right.AccountID)
}

func applyCodexAuthUsageIdentity(status codexAuthUsageStatus, auth *coreauth.Auth) codexAuthUsageStatus {
	if auth == nil {
		return status
	}
	authID := strings.TrimSpace(auth.ID)
	status.AuthID = authID
	status.FileName = strings.TrimSpace(auth.FileName)
	if status.FileName == "" && strings.HasSuffix(strings.ToLower(authID), ".json") {
		status.FileName = authID
	}
	status.Email = authEmail(auth)
	status.PlanType = inferCodexPlanType(auth, status)
	status.Priority = codexAuthPriority(auth)
	status.AccountID = extractCodexAccountID(auth)
	if status.Status == "" {
		status.Status = "skipped"
	}
	return status
}

func codexAuthPriority(auth *coreauth.Auth) int {
	if auth == nil {
		return 0
	}
	if raw := strings.TrimSpace(auth.Attributes["priority"]); raw != "" {
		if priority, err := strconv.Atoi(raw); err == nil {
			return priority
		}
	}
	if auth.Metadata != nil {
		if priority, ok := codexAuthPriorityValue(auth.Metadata["priority"]); ok {
			return priority
		}
	}
	path := strings.TrimSpace(auth.Attributes["path"])
	if path == "" {
		path = strings.TrimSpace(auth.FileName)
	}
	if path == "" {
		return 0
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var metadata map[string]any
	if err := json.Unmarshal(content, &metadata); err != nil {
		return 0
	}
	if priority, ok := codexAuthPriorityValue(metadata["priority"]); ok {
		return priority
	}
	return 0
}

func codexAuthPriorityValue(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case string:
		priority, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return priority, true
		}
	}
	return 0, false
}
