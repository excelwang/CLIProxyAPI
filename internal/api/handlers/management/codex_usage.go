package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	codexUsagePollInterval   = 60 * time.Second
	codexUsageSoftErrorRetry = 15 * time.Second
	codexUsageRequestTimeout = 20 * time.Second
	codexUsageDefaultBaseURL = "https://chatgpt.com/backend-api"
	codexFreePlanWeight      = 0.2
	codexFiveHourWindowSecs  = 5 * 60 * 60
	codexWeeklyWindowSecs    = 7 * 24 * 60 * 60
	// OpenAI official Codex Pricing indicates Pro usage is 6x Plus usage.
	// Keep this as default unless overridden by config.
	codexProPlanWeight      = 6.0
	codexUsageStateFileName = ".codex-usage-cache.json"
)

type codexUsageWindow struct {
	UsedPercent        int   `json:"used_percent"`
	LimitWindowSeconds int   `json:"limit_window_seconds"`
	ResetAfterSeconds  int   `json:"reset_after_seconds"`
	ResetAt            int64 `json:"reset_at"`
}

type codexUsageRateLimit struct {
	Allowed         bool              `json:"allowed"`
	LimitReached    bool              `json:"limit_reached"`
	PrimaryWindow   *codexUsageWindow `json:"primary_window,omitempty"`
	SecondaryWindow *codexUsageWindow `json:"secondary_window,omitempty"`
}

type codexUsageCredits struct {
	HasCredits          bool    `json:"has_credits"`
	Unlimited           bool    `json:"unlimited"`
	Balance             *string `json:"balance"`
	ApproxLocalMessages []any   `json:"approx_local_messages"`
	ApproxCloudMessages []any   `json:"approx_cloud_messages"`
}

type codexUsageAdditionalRateLimit struct {
	LimitName      string               `json:"limit_name"`
	MeteredFeature string               `json:"metered_feature"`
	RateLimit      *codexUsageRateLimit `json:"rate_limit,omitempty"`
}

type codexUsageAuthFileExtensionItem struct {
	AuthID       string            `json:"auth_id,omitempty"`
	FileName     string            `json:"file_name,omitempty"`
	Account      string            `json:"account,omitempty"`
	PlanType     string            `json:"plan_type,omitempty"`
	Priority     int               `json:"priority,omitempty"`
	Status       string            `json:"status,omitempty"`
	Error        string            `json:"error,omitempty"`
	ErrorSummary string            `json:"error_summary,omitempty"`
	LastUsedAt   time.Time         `json:"last_used_at,omitempty"`
	FiveHour     *codexUsageWindow `json:"five_hour,omitempty"`
	Week         *codexUsageWindow `json:"week,omitempty"`
}

type codexUsageRecoveryEvent struct {
	ResetAt                  int64   `json:"reset_at,omitempty"`
	WaitSeconds              int     `json:"wait_seconds,omitempty"`
	ReleaseUnits             float64 `json:"release_units,omitempty"`
	CumulativeReleaseUnits   float64 `json:"cumulative_release_units,omitempty"`
	CumulativeReleasePercent float64 `json:"cumulative_release_percent,omitempty"`
	AvailableUnits           float64 `json:"available_units,omitempty"`
	AvailablePercent         float64 `json:"available_percent,omitempty"`
}

type codexUsageRecoveryWindow struct {
	TotalUnits                  float64                   `json:"total_units,omitempty"`
	LockedUnits                 float64                   `json:"locked_units,omitempty"`
	AvailableUnitsNow           float64                   `json:"available_units_now,omitempty"`
	AvailablePercentNow         float64                   `json:"available_percent_now,omitempty"`
	NextWaitSeconds             int                       `json:"next_wait_seconds,omitempty"`
	NextResetAt                 int64                     `json:"next_reset_at,omitempty"`
	NextReleaseUnits            float64                   `json:"next_release_units,omitempty"`
	NextAvailableUnits          float64                   `json:"next_available_units,omitempty"`
	NextAvailablePercent        float64                   `json:"next_available_percent,omitempty"`
	SignificantWaitSeconds      int                       `json:"significant_wait_seconds,omitempty"`
	SignificantResetAt          int64                     `json:"significant_reset_at,omitempty"`
	SignificantTargetUnits      float64                   `json:"significant_target_units,omitempty"`
	SignificantReleaseUnits     float64                   `json:"significant_release_units,omitempty"`
	SignificantAvailableUnits   float64                   `json:"significant_available_units,omitempty"`
	SignificantAvailablePercent float64                   `json:"significant_available_percent,omitempty"`
	FullWaitSeconds             int                       `json:"full_wait_seconds,omitempty"`
	FullResetAt                 int64                     `json:"full_reset_at,omitempty"`
	FullReleaseUnits            float64                   `json:"full_release_units,omitempty"`
	FullAvailableUnits          float64                   `json:"full_available_units,omitempty"`
	FullAvailablePercent        float64                   `json:"full_available_percent,omitempty"`
	Events                      []codexUsageRecoveryEvent `json:"events,omitempty"`
}

type codexUsageCombinedRecovery struct {
	TotalUnits                  float64                   `json:"total_units,omitempty"`
	AvailableUnitsNow           float64                   `json:"available_units_now,omitempty"`
	AvailablePercentNow         float64                   `json:"available_percent_now,omitempty"`
	NextWaitSeconds             int                       `json:"next_wait_seconds,omitempty"`
	NextResetAt                 int64                     `json:"next_reset_at,omitempty"`
	NextAvailableUnits          float64                   `json:"next_available_units,omitempty"`
	NextAvailablePercent        float64                   `json:"next_available_percent,omitempty"`
	SignificantDeltaUnits       float64                   `json:"significant_delta_units,omitempty"`
	SignificantWaitSeconds      int                       `json:"significant_wait_seconds,omitempty"`
	SignificantResetAt          int64                     `json:"significant_reset_at,omitempty"`
	SignificantAvailableUnits   float64                   `json:"significant_available_units,omitempty"`
	SignificantAvailablePercent float64                   `json:"significant_available_percent,omitempty"`
	FullWaitSeconds             int                       `json:"full_wait_seconds,omitempty"`
	FullResetAt                 int64                     `json:"full_reset_at,omitempty"`
	FullAvailableUnits          float64                   `json:"full_available_units,omitempty"`
	FullAvailablePercent        float64                   `json:"full_available_percent,omitempty"`
	FiveHourBlockedUnitsNow     float64                   `json:"five_hour_blocked_units_now,omitempty"`
	FiveHourBlockedPercentNow   float64                   `json:"five_hour_blocked_percent_now,omitempty"`
	FiveHourNextWaitSeconds     int                       `json:"five_hour_next_wait_seconds,omitempty"`
	FiveHourNextResetAt         int64                     `json:"five_hour_next_reset_at,omitempty"`
	Events                      []codexUsageRecoveryEvent `json:"events,omitempty"`
}

type codexUsageRecovery struct {
	FiveHour *codexUsageRecoveryWindow   `json:"five_hour,omitempty"`
	Week     *codexUsageRecoveryWindow   `json:"week,omitempty"`
	Combined *codexUsageCombinedRecovery `json:"combined,omitempty"`
}

type codexUsageExtensions struct {
	ActiveAuthFiles []codexUsageAuthFileExtensionItem `json:"active_auth_files,omitempty"`
	Recovery        *codexUsageRecovery               `json:"recovery,omitempty"`
}

type codexUsagePayload struct {
	UserID               string                          `json:"user_id,omitempty"`
	AccountID            string                          `json:"account_id,omitempty"`
	Email                string                          `json:"email,omitempty"`
	PlanType             string                          `json:"plan_type"`
	TotalUsageMultiplier float64                         `json:"total_usage_multiplier,omitempty"`
	RateLimit            *codexUsageRateLimit            `json:"rate_limit,omitempty"`
	CodeReviewRateLimit  *codexUsageRateLimit            `json:"code_review_rate_limit,omitempty"`
	Credits              *codexUsageCredits              `json:"credits,omitempty"`
	AdditionalRateLimits []codexUsageAdditionalRateLimit `json:"additional_rate_limits"`
	Extensions           *codexUsageExtensions           `json:"extensions,omitempty"`
	Promo                json.RawMessage                 `json:"promo,omitempty"`
	Extra                map[string]json.RawMessage      `json:"-"`
}

type codexAuthUsageStatus struct {
	AuthID        string             `json:"auth_id"`
	FileName      string             `json:"file_name,omitempty"`
	Email         string             `json:"email,omitempty"`
	PlanType      string             `json:"plan_type,omitempty"`
	Priority      int                `json:"priority,omitempty"`
	AccountID     string             `json:"account_id,omitempty"`
	BaseURL       string             `json:"base_url,omitempty"`
	PathStyle     string             `json:"path_style,omitempty"`
	Status        string             `json:"status"`
	Error         string             `json:"error,omitempty"`
	LastPolledAt  time.Time          `json:"last_polled_at,omitempty"`
	LastSuccessAt *time.Time         `json:"last_success_at,omitempty"`
	HasUsage      bool               `json:"has_usage"`
	Usage         *codexUsagePayload `json:"usage,omitempty"`
}

type codexUsageWindowTotals struct {
	AuthFiles           int     `json:"auth_files"`
	UsedPercentSum      int     `json:"used_percent_sum"`
	TotalPercent        int     `json:"total_percent"`
	RemainingPercentSum int     `json:"remaining_percent_sum"`
	AverageUsedPercent  int     `json:"average_used_percent"`
	ProgressPercent     float64 `json:"progress_percent"`
	MinResetAfterSecond int     `json:"min_reset_after_seconds,omitempty"`
	MinResetAt          int64   `json:"min_reset_at,omitempty"`
}

type codexAdditionalRateLimitTotals struct {
	LimitName       string                  `json:"limit_name"`
	MeteredFeature  string                  `json:"metered_feature"`
	PrimaryWindow   *codexUsageWindowTotals `json:"primary_window,omitempty"`
	SecondaryWindow *codexUsageWindowTotals `json:"secondary_window,omitempty"`
}

type codexUsageTotalSummary struct {
	TotalUsageMultiplier float64                          `json:"total_usage_multiplier,omitempty"`
	PrimaryWindow        *codexUsageWindowTotals          `json:"primary_window,omitempty"`
	SecondaryWindow      *codexUsageWindowTotals          `json:"secondary_window,omitempty"`
	AdditionalRateLimits []codexAdditionalRateLimitTotals `json:"additional_rate_limits,omitempty"`
}

type codexUsageSummaryResponse struct {
	UpdatedAt           time.Time              `json:"updated_at"`
	PollIntervalSeconds int                    `json:"poll_interval_seconds"`
	AuthFilesTotal      int                    `json:"auth_files_total"`
	AuthFilesWithUsage  int                    `json:"auth_files_with_usage"`
	AuthFilesWithErrors int                    `json:"auth_files_with_errors"`
	SelectedAuthID      string                 `json:"selected_auth_id,omitempty"`
	CompatPayload       codexUsagePayload      `json:"compat_payload"`
	Total               codexUsageTotalSummary `json:"total"`
	AuthFiles           []codexAuthUsageStatus `json:"auth_files"`
}

type codexUsagePersistentState struct {
	UpdatedAt      time.Time                       `json:"updated_at"`
	SelectedAuthID string                          `json:"selected_auth_id,omitempty"`
	ByAuth         map[string]codexAuthUsageStatus `json:"by_auth"`
	CompatPayload  codexUsagePayload               `json:"compat_payload"`
	Summary        codexUsageSummaryResponse       `json:"summary"`
	HasData        bool                            `json:"has_data"`
}

type codexUsageHTTPError struct {
	StatusCode int
	Preview    string
}

func (p *codexUsagePayload) UnmarshalJSON(data []byte) error {
	type payloadAlias codexUsagePayload
	var parsed payloadAlias
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, key := range []string{
		"user_id",
		"account_id",
		"email",
		"plan_type",
		"total_usage_multiplier",
		"rate_limit",
		"code_review_rate_limit",
		"credits",
		"additional_rate_limits",
		"extensions",
		"promo",
	} {
		delete(raw, key)
	}
	*p = codexUsagePayload(parsed)
	if len(raw) == 0 {
		p.Extra = nil
	} else {
		p.Extra = raw
	}
	return nil
}

func (p codexUsagePayload) MarshalJSON() ([]byte, error) {
	type payloadAlias codexUsagePayload
	base := payloadAlias(p)
	base.Extra = nil
	encoded, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	if len(p.Extra) == 0 {
		return encoded, nil
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return nil, err
	}
	for key, value := range p.Extra {
		if _, exists := merged[key]; exists {
			continue
		}
		if len(value) == 0 {
			continue
		}
		merged[key] = value
	}
	return json.Marshal(merged)
}

func (e *codexUsageHTTPError) Error() string {
	if e == nil {
		return "usage request failed"
	}
	if strings.TrimSpace(e.Preview) == "" {
		return fmt.Sprintf("usage request failed: status=%d", e.StatusCode)
	}
	return fmt.Sprintf("usage request failed: status=%d body=%s", e.StatusCode, e.Preview)
}

type codexWindowAccumulator struct {
	count                  int
	denominatorCount       int
	weightSum              float64
	denominatorWeightSum   float64
	usedPercentWeightedSum float64
	limitWindowWeightedSum float64
	minResetAfter          int
	minResetAt             int64
}

func (a *codexWindowAccumulator) addDenominator(weight float64) {
	if weight <= 0 {
		weight = 1
	}
	a.denominatorCount++
	a.denominatorWeightSum += weight
}

func (a *codexWindowAccumulator) add(window *codexUsageWindow, weight float64) {
	if window == nil {
		return
	}
	if weight <= 0 {
		weight = 1
	}
	a.count++
	a.weightSum += weight
	a.usedPercentWeightedSum += float64(window.UsedPercent) * weight
	a.limitWindowWeightedSum += float64(window.LimitWindowSeconds) * weight
	if window.ResetAfterSeconds > 0 && (a.minResetAfter == 0 || window.ResetAfterSeconds < a.minResetAfter) {
		a.minResetAfter = window.ResetAfterSeconds
	}
	if window.ResetAt > 0 && (a.minResetAt == 0 || window.ResetAt < a.minResetAt) {
		a.minResetAt = window.ResetAt
	}
}

func (a *codexWindowAccumulator) averageWindow() *codexUsageWindow {
	if a == nil || a.count == 0 || a.weightSum <= 0 {
		return nil
	}
	return &codexUsageWindow{
		UsedPercent:        int(math.Round(a.usedPercentWeightedSum / a.weightSum)),
		LimitWindowSeconds: int(math.Round(a.limitWindowWeightedSum / a.weightSum)),
		ResetAfterSeconds:  a.minResetAfter,
		ResetAt:            a.minResetAt,
	}
}

func (a *codexWindowAccumulator) totals() *codexUsageWindowTotals {
	if a == nil || a.denominatorCount == 0 || a.denominatorWeightSum <= 0 {
		return nil
	}
	totalPercentFloat := a.denominatorWeightSum * 100
	totalPercent := int(math.Round(totalPercentFloat))
	usedPercentSum := int(math.Round(a.usedPercentWeightedSum))
	progress := 0.0
	if totalPercentFloat > 0 {
		progress = (a.usedPercentWeightedSum / totalPercentFloat) * 100
	}
	averageUsed := 0
	if a.denominatorWeightSum > 0 {
		averageUsed = int(math.Round(a.usedPercentWeightedSum / a.denominatorWeightSum))
	}
	return &codexUsageWindowTotals{
		AuthFiles:           a.denominatorCount,
		UsedPercentSum:      usedPercentSum,
		TotalPercent:        totalPercent,
		RemainingPercentSum: totalPercent - usedPercentSum,
		AverageUsedPercent:  averageUsed,
		ProgressPercent:     math.Round(progress*100) / 100,
		MinResetAfterSecond: a.minResetAfter,
		MinResetAt:          a.minResetAt,
	}
}

type codexRateLimitAccumulator struct {
	hasAny          bool
	allowedAny      bool
	limitReachedAll bool
	primaryWindow   codexWindowAccumulator
	secondaryWindow codexWindowAccumulator
}

func (a *codexRateLimitAccumulator) add(rate *codexUsageRateLimit, weight float64) {
	if rate == nil {
		return
	}
	if !a.hasAny {
		a.limitReachedAll = true
	}
	a.hasAny = true
	a.allowedAny = a.allowedAny || rate.Allowed
	if !rate.LimitReached {
		a.limitReachedAll = false
	}
	a.primaryWindow.add(rate.PrimaryWindow, weight)
	a.secondaryWindow.add(rate.SecondaryWindow, weight)
}

func (a *codexRateLimitAccumulator) addDenominator(weight float64) {
	a.primaryWindow.addDenominator(weight)
	a.secondaryWindow.addDenominator(weight)
}

func (a *codexRateLimitAccumulator) averageRateLimit() *codexUsageRateLimit {
	if a == nil || !a.hasAny {
		return nil
	}
	return &codexUsageRateLimit{
		Allowed:         a.allowedAny,
		LimitReached:    a.limitReachedAll,
		PrimaryWindow:   a.primaryWindow.averageWindow(),
		SecondaryWindow: a.secondaryWindow.averageWindow(),
	}
}

type codexAdditionalAccumulator struct {
	limitName      string
	meteredFeature string
	rate           codexRateLimitAccumulator
}

func defaultCodexUsagePayload() codexUsagePayload {
	return codexUsagePayload{PlanType: "guest"}
}

// refreshCodexUsageFromCacheTTL updates codex usage cache on demand.
// It never polls upstream unless a specific auth file cache TTL has expired.
func (h *Handler) refreshCodexUsageFromCacheTTL(ctx context.Context) {
	if h == nil {
		return
	}
	h.ensureUsageRuntimeInitialized()
	state := h.codexUsageStateRef()
	if state == nil {
		return
	}
	state.codexUsagePollMu.Lock()
	defer state.codexUsagePollMu.Unlock()

	now := time.Now().UTC()
	manager := h.authManager
	if manager == nil {
		return
	}

	currentSelectedAuthID := h.selectedCodexAuthID()

	previous := h.codexUsageByAuthSnapshot()
	current := make(map[string]codexAuthUsageStatus, len(previous))
	for key, value := range previous {
		current[key] = value
	}

	auths := manager.List()
	codexAuths := make(map[string]*coreauth.Auth)
	for _, auth := range auths {
		if !shouldTrackCodexUsageAuth(auth) {
			continue
		}
		codexAuths[strings.TrimSpace(auth.ID)] = auth
	}
	changed := false
	for authID := range current {
		if _, ok := codexAuths[authID]; !ok {
			delete(current, authID)
			changed = true
		}
	}

	selectedAuthID := currentSelectedAuthID
	if selectedAuthID != "" {
		if _, ok := codexAuths[selectedAuthID]; !ok {
			selectedAuthID = ""
		}
	}
	selectedChanged := currentSelectedAuthID != selectedAuthID

	if len(codexAuths) == 0 {
		if changed || selectedChanged {
			h.updateCodexUsageState(current, selectedAuthID, now, true)
		}
		return
	}

	changed = h.codexUsagePollPolicy().Refresh(ctx, now, selectedAuthID, current, codexAuths) || changed

	if changed || selectedChanged {
		h.updateCodexUsageState(current, selectedAuthID, now, true)
	}
}

func shouldTrackCodexUsageAuth(auth *coreauth.Auth) bool {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return false
	}
	// If a file-backed auth has been removed from disk, drop it from usage aggregation.
	path := strings.TrimSpace(authAttribute(auth, "path"))
	if path != "" {
		if _, err := os.Stat(path); err != nil && os.IsNotExist(err) {
			return false
		}
	}
	return true
}

func (h *Handler) updateCodexUsageState(current map[string]codexAuthUsageStatus, selectedAuthID string, now time.Time, persist bool) {
	if h == nil {
		return
	}
	state := h.codexUsageStateRef()
	if state == nil {
		return
	}
	compatPayload, totalSummary, withUsage := aggregateCodexUsage(current, h.codexFreePlanWeight(), h.codexProPlanWeight())
	fillCodexCompatAccountEmailFromSelected(&compatPayload, current, selectedAuthID)
	compatPayload.Extensions = buildCodexUsageExtensions(current, now, h.codexFreePlanWeight(), h.codexProPlanWeight(), h.codexUsageAuthBaseDir(), h.codexUsageAuthLookup())
	authErrors := 0
	authList := make([]codexAuthUsageStatus, 0, len(current))
	for _, item := range current {
		if item.Status == "error" {
			authErrors++
		}
		authList = append(authList, cloneCodexAuthUsageStatus(item))
	}
	sort.Slice(authList, func(i, j int) bool {
		left := strings.TrimSpace(authList[i].FileName)
		right := strings.TrimSpace(authList[j].FileName)
		if left == right {
			return authList[i].AuthID < authList[j].AuthID
		}
		if left == "" {
			return false
		}
		if right == "" {
			return true
		}
		return left < right
	})

	summary := codexUsageSummaryResponse{
		UpdatedAt:           now,
		PollIntervalSeconds: int(codexUsagePollInterval / time.Second),
		AuthFilesTotal:      len(current),
		AuthFilesWithUsage:  withUsage,
		AuthFilesWithErrors: authErrors,
		SelectedAuthID:      selectedAuthID,
		CompatPayload:       compatPayload,
		Total:               totalSummary,
		AuthFiles:           authList,
	}

	state.codexUsageMu.Lock()
	state.codexUsageByAuth = current
	state.codexUsageCompat = compatPayload
	state.codexUsageSummary = summary
	state.codexUsageHasData = withUsage > 0
	state.codexUsageSelected = selectedAuthID
	state.codexUsageMu.Unlock()
	if persist {
		h.persistCodexUsageState()
	}
}

func (h *Handler) removeCodexUsageAuthState(authID string) {
	if h == nil {
		return
	}
	h.ensureUsageRuntimeInitialized()
	state := h.codexUsageStateRef()
	if state == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}

	state.codexUsagePollMu.Lock()
	defer state.codexUsagePollMu.Unlock()

	current := h.codexUsageByAuthSnapshot()
	selectedAuthID := strings.TrimSpace(state.codexUsageSelected)
	if selectedAuthID == "" {
		selectedAuthID = strings.TrimSpace(state.codexUsageSummary.SelectedAuthID)
	}
	changed := false
	if _, ok := current[authID]; ok {
		delete(current, authID)
		changed = true
	}

	if selectedAuthID == authID {
		selectedAuthID = ""
		changed = true
	}

	if !changed {
		return
	}
	h.updateCodexUsageState(current, selectedAuthID, time.Now().UTC(), true)
}

func fillCodexCompatAccountEmailFromSelected(compat *codexUsagePayload, current map[string]codexAuthUsageStatus, selectedAuthID string) {
	if compat == nil {
		return
	}
	if strings.TrimSpace(compat.Email) != "" {
		return
	}
	selectedAuthID = strings.TrimSpace(selectedAuthID)
	if selectedAuthID == "" || current == nil {
		return
	}
	status, ok := current[selectedAuthID]
	if !ok {
		return
	}
	email := strings.TrimSpace(status.Email)
	if email == "" && status.Usage != nil {
		email = strings.TrimSpace(status.Usage.Email)
	}
	if email != "" {
		compat.Email = email
	}
}

func buildCodexUsageExtensions(current map[string]codexAuthUsageStatus, now time.Time, freePlanWeight, proPlanWeight float64, authBaseDir string, authLookup map[string]*coreauth.Auth) *codexUsageExtensions {
	if len(current) == 0 {
		return nil
	}
	items := make([]codexUsageAuthFileExtensionItem, 0, len(current))
	for _, status := range current {
		normalized := codexEffectiveMainRateLimit(status)

		account := strings.TrimSpace(status.Email)
		if account == "" && status.Usage != nil {
			account = strings.TrimSpace(status.Usage.Email)
		}
		if account == "" {
			account = strings.TrimSpace(status.AccountID)
		}
		if account == "" && status.Usage != nil {
			account = strings.TrimSpace(status.Usage.AccountID)
		}

		fileName := strings.TrimSpace(status.FileName)
		if fileName == "" {
			if authID := strings.TrimSpace(status.AuthID); strings.HasSuffix(strings.ToLower(authID), ".json") {
				fileName = authID
			}
		}
		item := codexUsageAuthFileExtensionItem{
			AuthID:       strings.TrimSpace(status.AuthID),
			FileName:     fileName,
			Account:      account,
			PlanType:     strings.TrimSpace(status.PlanType),
			Priority:     codexStatusPriority(status, authLookup[strings.TrimSpace(status.AuthID)], authBaseDir),
			Status:       strings.TrimSpace(status.Status),
			Error:        strings.TrimSpace(status.Error),
			ErrorSummary: codexUsageErrorSummary(status.Error),
			LastUsedAt:   codexAuthUsageRecentTime(status),
		}
		if item.PlanType == "" && status.Usage != nil {
			item.PlanType = strings.TrimSpace(status.Usage.PlanType)
		}
		if normalized != nil {
			item.FiveHour = cloneCodexUsageWindow(normalized.PrimaryWindow)
			item.Week = cloneCodexUsageWindow(normalized.SecondaryWindow)
		}
		if item.FiveHour == nil && item.Week != nil && strings.EqualFold(strings.TrimSpace(item.PlanType), "free") {
			// Free plan may expose a single weekly window on upstream; mirror it for 5h display compatibility.
			item.FiveHour = cloneCodexUsageWindow(item.Week)
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].LastUsedAt.Equal(items[j].LastUsedAt) {
			return items[i].LastUsedAt.After(items[j].LastUsedAt)
		}
		left := strings.TrimSpace(items[i].FileName)
		right := strings.TrimSpace(items[j].FileName)
		if left == right {
			return items[i].AuthID < items[j].AuthID
		}
		if left == "" {
			return false
		}
		if right == "" {
			return true
		}
		return left < right
	})
	return &codexUsageExtensions{
		ActiveAuthFiles: items,
		Recovery:        buildCodexUsageRecovery(current, now, freePlanWeight, proPlanWeight),
	}
}

func (h *Handler) codexUsageAuthBaseDir() string {
	if h == nil {
		return ""
	}
	if h.cfg != nil {
		if baseDir := strings.TrimSpace(h.cfg.AuthDir); baseDir != "" {
			return baseDir
		}
	}
	if cfgPath := strings.TrimSpace(h.configFilePath); cfgPath != "" {
		return filepath.Dir(cfgPath)
	}
	return ""
}

func (h *Handler) codexUsageAuthLookup() map[string]*coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}
	lookup := make(map[string]*coreauth.Auth)
	for _, auth := range h.authManager.List() {
		if !shouldTrackCodexUsageAuth(auth) {
			continue
		}
		authID := strings.TrimSpace(auth.ID)
		if authID == "" {
			continue
		}
		lookup[authID] = auth
	}
	return lookup
}

func codexStatusPriority(status codexAuthUsageStatus, auth *coreauth.Auth, authBaseDir string) int {
	if status.Priority != 0 {
		return status.Priority
	}
	if auth != nil {
		if priority := codexAuthPriority(auth); priority != 0 {
			return priority
		}
	}
	fileName := strings.TrimSpace(status.FileName)
	if fileName == "" && auth != nil {
		fileName = strings.TrimSpace(auth.FileName)
	}
	if fileName == "" {
		if authID := strings.TrimSpace(status.AuthID); strings.HasSuffix(strings.ToLower(authID), ".json") {
			fileName = authID
		}
	}
	if fileName == "" {
		return 0
	}
	path := fileName
	if !filepath.IsAbs(path) && strings.TrimSpace(authBaseDir) != "" {
		path = filepath.Join(strings.TrimSpace(authBaseDir), path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var metadata map[string]any
	if err := json.Unmarshal(content, &metadata); err != nil {
		return 0
	}
	if value, ok := metadata["priority"]; ok {
		if priority, okPriority := codexStatusPriorityValue(value); okPriority {
			return priority
		}
	}
	return 0
}

func codexStatusPriorityValue(value any) (int, bool) {
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

func codexAuthUsageRecentTime(status codexAuthUsageStatus) time.Time {
	if status.LastSuccessAt != nil && !status.LastSuccessAt.IsZero() {
		return status.LastSuccessAt.UTC()
	}
	if !status.LastPolledAt.IsZero() {
		return status.LastPolledAt.UTC()
	}
	return time.Time{}
}

func codexUsageErrorSummary(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	parts := strings.Fields(text)
	text = strings.Join(parts, " ")
	const maxLen = 120
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "..."
}

func codexEffectiveMainRateLimit(status codexAuthUsageStatus) *codexUsageRateLimit {
	var normalized *codexUsageRateLimit
	if status.Usage != nil {
		normalized = normalizeCodexMainRateLimitWindows(status.Usage.RateLimit)
	}
	if codexIsAuthFailureStatus(status) || codexIsWeeklyExhaustedRateLimit(normalized) {
		return codexForceDepletedRateLimit(normalized)
	}
	return normalized
}

func codexIsWeeklyExhaustedRateLimit(rate *codexUsageRateLimit) bool {
	if rate == nil {
		return false
	}
	week := rate.SecondaryWindow
	if week == nil && rate.PrimaryWindow != nil && isLikelyCodexWeeklyWindow(rate.PrimaryWindow.LimitWindowSeconds) {
		week = rate.PrimaryWindow
	}
	if week == nil {
		return false
	}
	if week.UsedPercent >= 100 {
		return true
	}
	if week.UsedPercent >= 99 && rate.LimitReached && !rate.Allowed {
		return true
	}
	return false
}

func codexForceDepletedRateLimit(rate *codexUsageRateLimit) *codexUsageRateLimit {
	out := cloneCodexUsageRateLimit(rate)
	if out == nil {
		out = &codexUsageRateLimit{}
	}

	weekRef := cloneCodexUsageWindow(out.SecondaryWindow)
	if weekRef == nil {
		weekRef = cloneCodexUsageWindow(out.PrimaryWindow)
	}

	if out.PrimaryWindow == nil {
		out.PrimaryWindow = &codexUsageWindow{}
	}
	if out.PrimaryWindow.LimitWindowSeconds <= 0 {
		out.PrimaryWindow.LimitWindowSeconds = codexFiveHourWindowSecs
	}
	out.PrimaryWindow.UsedPercent = 100
	if weekRef != nil {
		if out.PrimaryWindow.ResetAfterSeconds <= 0 {
			out.PrimaryWindow.ResetAfterSeconds = weekRef.ResetAfterSeconds
		}
		if out.PrimaryWindow.ResetAt <= 0 {
			out.PrimaryWindow.ResetAt = weekRef.ResetAt
		}
	}

	if out.SecondaryWindow == nil {
		out.SecondaryWindow = &codexUsageWindow{}
	}
	if out.SecondaryWindow.LimitWindowSeconds <= 0 {
		out.SecondaryWindow.LimitWindowSeconds = codexWeeklyWindowSecs
	}
	out.SecondaryWindow.UsedPercent = 100
	if out.SecondaryWindow.ResetAfterSeconds <= 0 && out.PrimaryWindow != nil {
		out.SecondaryWindow.ResetAfterSeconds = out.PrimaryWindow.ResetAfterSeconds
	}
	if out.SecondaryWindow.ResetAt <= 0 && out.PrimaryWindow != nil {
		out.SecondaryWindow.ResetAt = out.PrimaryWindow.ResetAt
	}

	out.Allowed = false
	out.LimitReached = true
	return out
}

func codexIsAuthFailureStatus(status codexAuthUsageStatus) bool {
	if !strings.EqualFold(strings.TrimSpace(status.Status), "error") {
		return false
	}
	signals := strings.ToLower(strings.TrimSpace(status.Error))
	if signals == "" {
		return false
	}
	markers := []string{
		"status=401",
		"status=403",
		"http 401",
		"http 403",
		"unauthorized",
		"forbidden",
		"permission denied",
		"access denied",
		"authentication failed",
		"auth failed",
		"token_invalidated",
		"invalid_token",
		"invalid access token",
		"expired token",
		"missing access_token",
		"missing api key",
		"invalid_api_key",
		"invalid api key",
		"not authorized",
		"认证失败",
		"鉴权失败",
		"权限不足",
		"访问被拒绝",
		"令牌失效",
	}
	for _, marker := range markers {
		if strings.Contains(signals, marker) {
			return true
		}
	}
	return false
}

type codexRecoveryContribution struct {
	weight float64
	window *codexUsageWindow
}

func buildCodexUsageRecovery(current map[string]codexAuthUsageStatus, now time.Time, freePlanWeight, proPlanWeight float64) *codexUsageRecovery {
	if len(current) == 0 {
		return nil
	}
	fiveHour := make([]codexRecoveryContribution, 0, len(current))
	week := make([]codexRecoveryContribution, 0, len(current))
	for _, status := range current {
		planType := strings.TrimSpace(status.PlanType)
		if planType == "" && status.Usage != nil {
			planType = strings.TrimSpace(status.Usage.PlanType)
		}
		weight := codexPlanWeight(planType, freePlanWeight, proPlanWeight)
		normalized := codexEffectiveMainRateLimit(status)

		var fiveWindow *codexUsageWindow
		var weekWindow *codexUsageWindow
		if normalized != nil {
			fiveWindow = normalized.PrimaryWindow
			weekWindow = normalized.SecondaryWindow
		}
		if fiveWindow == nil && weekWindow != nil && strings.EqualFold(strings.TrimSpace(planType), "free") {
			fiveWindow = weekWindow
		}
		fiveHour = append(fiveHour, codexRecoveryContribution{
			weight: weight,
			window: fiveWindow,
		})
		week = append(week, codexRecoveryContribution{
			weight: weight,
			window: weekWindow,
		})
	}
	out := &codexUsageRecovery{
		FiveHour: buildCodexUsageRecoveryWindow(now, fiveHour),
		Week:     buildCodexUsageRecoveryWindow(now, week),
		Combined: buildCodexUsageCombinedRecovery(now, current, freePlanWeight, proPlanWeight),
	}
	if out.FiveHour == nil && out.Week == nil && out.Combined == nil {
		return nil
	}
	return out
}

func buildCodexUsageRecoveryWindow(now time.Time, contributions []codexRecoveryContribution) *codexUsageRecoveryWindow {
	if len(contributions) == 0 {
		return nil
	}
	totalUnits := 0.0
	lockedUnits := 0.0
	releaseByReset := make(map[int64]float64)
	for _, item := range contributions {
		weight := item.weight
		if weight <= 0 {
			weight = 1.0
		}
		totalUnits += weight
		if item.window == nil {
			continue
		}
		usedPercent := float64(item.window.UsedPercent)
		if usedPercent < 0 {
			usedPercent = 0
		}
		if usedPercent > 100 {
			usedPercent = 100
		}
		locked := weight * usedPercent / 100.0
		if locked <= 0 {
			continue
		}
		lockedUnits += locked

		resetAt, ok := codexWindowResetAt(item.window, now)
		if !ok {
			continue
		}
		releaseByReset[resetAt] += locked
	}

	if totalUnits <= 0 {
		return nil
	}
	availableNow := math.Max(totalUnits-lockedUnits, 0)
	window := &codexUsageRecoveryWindow{
		TotalUnits:             roundFloat(totalUnits, 2),
		LockedUnits:            roundFloat(lockedUnits, 2),
		AvailableUnitsNow:      roundFloat(availableNow, 2),
		AvailablePercentNow:    roundFloat((availableNow/totalUnits)*100, 2),
		SignificantTargetUnits: roundFloat(math.Max(totalUnits*0.05, 0.5), 2),
		FullAvailableUnits:     roundFloat(availableNow, 2),
		FullAvailablePercent:   roundFloat((availableNow/totalUnits)*100, 2),
	}
	if len(releaseByReset) == 0 {
		return window
	}

	resetAts := make([]int64, 0, len(releaseByReset))
	for resetAt := range releaseByReset {
		resetAts = append(resetAts, resetAt)
	}
	sort.Slice(resetAts, func(i, j int) bool { return resetAts[i] < resetAts[j] })

	events := make([]codexUsageRecoveryEvent, 0, len(resetAts))
	cumulative := 0.0
	significantTarget := math.Max(totalUnits*0.05, 0.5)
	for idx, resetAt := range resetAts {
		release := releaseByReset[resetAt]
		cumulative += release
		wait := int(resetAt - now.Unix())
		if wait < 0 {
			wait = 0
		}
		availableUnits := availableNow + cumulative
		event := codexUsageRecoveryEvent{
			ResetAt:                  resetAt,
			WaitSeconds:              wait,
			ReleaseUnits:             roundFloat(release, 2),
			CumulativeReleaseUnits:   roundFloat(cumulative, 2),
			CumulativeReleasePercent: roundFloat((cumulative/totalUnits)*100, 2),
			AvailableUnits:           roundFloat(availableUnits, 2),
			AvailablePercent:         roundFloat((availableUnits/totalUnits)*100, 2),
		}
		events = append(events, event)
		if idx == 0 {
			window.NextWaitSeconds = wait
			window.NextResetAt = resetAt
			window.NextReleaseUnits = roundFloat(release, 2)
			window.NextAvailableUnits = roundFloat(availableUnits, 2)
			window.NextAvailablePercent = roundFloat((availableUnits/totalUnits)*100, 2)
		}
		if window.SignificantResetAt == 0 && cumulative >= significantTarget {
			window.SignificantWaitSeconds = wait
			window.SignificantResetAt = resetAt
			window.SignificantReleaseUnits = roundFloat(cumulative, 2)
			window.SignificantAvailableUnits = roundFloat(availableUnits, 2)
			window.SignificantAvailablePercent = roundFloat((availableUnits/totalUnits)*100, 2)
		}
		window.FullWaitSeconds = wait
		window.FullResetAt = resetAt
		window.FullReleaseUnits = roundFloat(cumulative, 2)
		window.FullAvailableUnits = roundFloat(availableUnits, 2)
		window.FullAvailablePercent = roundFloat((availableUnits/totalUnits)*100, 2)
	}
	if window.SignificantResetAt == 0 {
		window.SignificantWaitSeconds = window.FullWaitSeconds
		window.SignificantResetAt = window.FullResetAt
		window.SignificantReleaseUnits = window.FullReleaseUnits
		window.SignificantAvailableUnits = window.FullAvailableUnits
		window.SignificantAvailablePercent = window.FullAvailablePercent
	}
	window.Events = events
	return window
}

type codexCombinedRecoveryContribution struct {
	weight         float64
	weekAvailable  float64
	weekLocked     float64
	weekResetAt    int64
	fiveBlockedNow bool
	fiveResetAt    int64
}

func buildCodexUsageCombinedRecovery(now time.Time, current map[string]codexAuthUsageStatus, freePlanWeight, proPlanWeight float64) *codexUsageCombinedRecovery {
	if len(current) == 0 {
		return nil
	}
	contributions := make([]codexCombinedRecoveryContribution, 0, len(current))
	for _, status := range current {
		if codexIsAuthFailureStatus(status) {
			continue
		}
		planType := strings.TrimSpace(status.PlanType)
		if planType == "" && status.Usage != nil {
			planType = strings.TrimSpace(status.Usage.PlanType)
		}
		weight := codexPlanWeight(planType, freePlanWeight, proPlanWeight)
		if weight <= 0 {
			continue
		}
		normalized := codexEffectiveMainRateLimit(status)
		var fiveWindow *codexUsageWindow
		var weekWindow *codexUsageWindow
		if normalized != nil {
			fiveWindow = normalized.PrimaryWindow
			weekWindow = normalized.SecondaryWindow
		}
		if fiveWindow == nil && weekWindow != nil && strings.EqualFold(strings.TrimSpace(planType), "free") {
			fiveWindow = weekWindow
		}
		weekAvailable, weekLocked := codexSplitRecoveryUnits(weight, weekWindow)
		weekResetAt, _ := codexWindowResetAt(weekWindow, now)
		fiveBlockedNow := codexRecoveryWindowBlocked(fiveWindow)
		fiveResetAt, _ := codexWindowResetAt(fiveWindow, now)
		contributions = append(contributions, codexCombinedRecoveryContribution{
			weight:         weight,
			weekAvailable:  weekAvailable,
			weekLocked:     weekLocked,
			weekResetAt:    weekResetAt,
			fiveBlockedNow: fiveBlockedNow,
			fiveResetAt:    fiveResetAt,
		})
	}
	if len(contributions) == 0 {
		return nil
	}

	totalUnits := 0.0
	availableNow := 0.0
	fiveBlockedUnitsNow := 0.0
	fiveHourNextResetAt := int64(0)
	releaseByReset := make(map[int64]float64)

	for _, item := range contributions {
		totalUnits += item.weight
		if !item.fiveBlockedNow {
			availableNow += item.weekAvailable
			if item.weekLocked > 0 && item.weekResetAt > 0 {
				releaseByReset[item.weekResetAt] += item.weekLocked
			}
			continue
		}

		fiveBlockedUnitsNow += item.weekAvailable
		if item.weekAvailable > 0 && item.fiveResetAt > 0 && (fiveHourNextResetAt == 0 || item.fiveResetAt < fiveHourNextResetAt) {
			fiveHourNextResetAt = item.fiveResetAt
		}
		if item.fiveResetAt == 0 {
			continue
		}

		deltaAtUnblock := item.weekAvailable
		weekLocked := item.weekLocked
		if weekLocked > 0 && item.weekResetAt > 0 && item.weekResetAt <= item.fiveResetAt {
			deltaAtUnblock += weekLocked
			weekLocked = 0
		}
		if deltaAtUnblock > 0 {
			releaseByReset[item.fiveResetAt] += deltaAtUnblock
		}
		if weekLocked > 0 && item.weekResetAt > item.fiveResetAt {
			releaseByReset[item.weekResetAt] += weekLocked
		}
	}

	if totalUnits <= 0 {
		return nil
	}

	combined := &codexUsageCombinedRecovery{
		TotalUnits:                roundFloat(totalUnits, 2),
		AvailableUnitsNow:         roundFloat(availableNow, 2),
		AvailablePercentNow:       roundFloat((availableNow/totalUnits)*100, 2),
		FiveHourBlockedUnitsNow:   roundFloat(fiveBlockedUnitsNow, 2),
		FiveHourBlockedPercentNow: roundFloat((fiveBlockedUnitsNow/totalUnits)*100, 2),
		FullAvailableUnits:        roundFloat(availableNow, 2),
		FullAvailablePercent:      roundFloat((availableNow/totalUnits)*100, 2),
	}
	if fiveHourNextResetAt > 0 {
		combined.FiveHourNextResetAt = fiveHourNextResetAt
		combined.FiveHourNextWaitSeconds = maxInt(0, int(fiveHourNextResetAt-now.Unix()))
	}
	if len(releaseByReset) == 0 {
		return combined
	}

	resetAts := make([]int64, 0, len(releaseByReset))
	for resetAt := range releaseByReset {
		resetAts = append(resetAts, resetAt)
	}
	sort.Slice(resetAts, func(i, j int) bool { return resetAts[i] < resetAts[j] })

	events := make([]codexUsageRecoveryEvent, 0, len(resetAts))
	cumulative := 0.0
	maxFutureRelease := 0.0
	for idx, resetAt := range resetAts {
		release := releaseByReset[resetAt]
		if release <= 0 {
			continue
		}
		cumulative += release
		if cumulative > maxFutureRelease {
			maxFutureRelease = cumulative
		}
		wait := maxInt(0, int(resetAt-now.Unix()))
		availableUnits := availableNow + cumulative
		event := codexUsageRecoveryEvent{
			ResetAt:                  resetAt,
			WaitSeconds:              wait,
			ReleaseUnits:             roundFloat(release, 2),
			CumulativeReleaseUnits:   roundFloat(cumulative, 2),
			CumulativeReleasePercent: roundFloat((cumulative/totalUnits)*100, 2),
			AvailableUnits:           roundFloat(availableUnits, 2),
			AvailablePercent:         roundFloat((availableUnits/totalUnits)*100, 2),
		}
		events = append(events, event)
		if idx == 0 {
			combined.NextWaitSeconds = wait
			combined.NextResetAt = resetAt
			combined.NextAvailableUnits = roundFloat(availableUnits, 2)
			combined.NextAvailablePercent = roundFloat((availableUnits/totalUnits)*100, 2)
		}
		combined.FullWaitSeconds = wait
		combined.FullResetAt = resetAt
		combined.FullAvailableUnits = roundFloat(availableUnits, 2)
		combined.FullAvailablePercent = roundFloat((availableUnits/totalUnits)*100, 2)
	}
	combined.Events = events

	significantDelta := math.Min(maxFutureRelease, math.Max(1.0, totalUnits*0.10))
	combined.SignificantDeltaUnits = roundFloat(significantDelta, 2)
	if significantDelta <= 0 {
		return combined
	}
	for _, event := range events {
		if event.CumulativeReleaseUnits+1e-9 < significantDelta {
			continue
		}
		combined.SignificantWaitSeconds = event.WaitSeconds
		combined.SignificantResetAt = event.ResetAt
		combined.SignificantAvailableUnits = event.AvailableUnits
		combined.SignificantAvailablePercent = event.AvailablePercent
		break
	}
	if combined.SignificantResetAt == 0 {
		combined.SignificantWaitSeconds = combined.FullWaitSeconds
		combined.SignificantResetAt = combined.FullResetAt
		combined.SignificantAvailableUnits = combined.FullAvailableUnits
		combined.SignificantAvailablePercent = combined.FullAvailablePercent
	}
	return combined
}

func codexSplitRecoveryUnits(weight float64, window *codexUsageWindow) (available float64, locked float64) {
	if weight <= 0 {
		return 0, 0
	}
	if window == nil {
		return weight, 0
	}
	usedPercent := clampRecoveryPercent(float64(window.UsedPercent))
	locked = weight * usedPercent / 100.0
	available = math.Max(weight-locked, 0)
	return available, locked
}

func codexRecoveryWindowBlocked(window *codexUsageWindow) bool {
	if window == nil {
		return false
	}
	return clampRecoveryPercent(float64(window.UsedPercent)) >= 100
}

func clampRecoveryPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func codexWindowResetAt(window *codexUsageWindow, now time.Time) (int64, bool) {
	if window == nil {
		return 0, false
	}
	if window.ResetAt > 0 {
		return window.ResetAt, true
	}
	if window.ResetAfterSeconds > 0 {
		return now.Unix() + int64(window.ResetAfterSeconds), true
	}
	return 0, false
}

func fillCodexCompatIdentityFromAuthStatus(compat *codexUsagePayload, status codexAuthUsageStatus) {
	if compat == nil {
		return
	}
	email := strings.TrimSpace(compat.Email)
	if email == "" {
		email = strings.TrimSpace(status.Email)
		if email == "" && status.Usage != nil {
			email = strings.TrimSpace(status.Usage.Email)
		}
		if email != "" {
			compat.Email = email
		}
	}
	accountID := strings.TrimSpace(compat.AccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(status.AccountID)
		if accountID == "" && status.Usage != nil {
			accountID = strings.TrimSpace(status.Usage.AccountID)
		}
		if accountID != "" {
			compat.AccountID = accountID
		}
	}
	userID := strings.TrimSpace(compat.UserID)
	if userID == "" && status.Usage != nil {
		userID = strings.TrimSpace(status.Usage.UserID)
		if userID != "" {
			compat.UserID = userID
		}
	}
}

func aggregateCodexUsage(authStatuses map[string]codexAuthUsageStatus, freePlanWeight, proPlanWeight float64) (codexUsagePayload, codexUsageTotalSummary, int) {
	compat := defaultCodexUsagePayload()
	var totals codexUsageTotalSummary

	planCounts := map[string]int{}
	mainRate := codexRateLimitAccumulator{}
	codeReviewRate := codexRateLimitAccumulator{}
	additional := map[string]*codexAdditionalAccumulator{}
	withUsage := 0

	hasCreditsPayload := false
	hasCreditsAny := false
	unlimitedAny := false
	balanceSum := 0.0
	balanceCount := 0

	for _, status := range authStatuses {
		plan := strings.TrimSpace(status.PlanType)
		if plan == "" && status.Usage != nil {
			plan = strings.TrimSpace(status.Usage.PlanType)
		}
		weight := codexPlanWeight(plan, freePlanWeight, proPlanWeight)
		mainRate.addDenominator(weight)
		mainRate.add(codexEffectiveMainRateLimit(status), weight)

		if status.Usage == nil {
			continue
		}
		withUsage++
		usage := status.Usage
		usagePlan := strings.TrimSpace(usage.PlanType)
		if usagePlan == "" {
			usagePlan = plan
		}
		if usagePlan != "" {
			planCounts[usagePlan]++
		}
		codeReviewRate.add(normalizeCodexRateLimitWindows(usage.CodeReviewRateLimit), weight)
		if len(compat.Promo) == 0 && len(usage.Promo) > 0 {
			compat.Promo = cloneRawMessage(usage.Promo)
		}

		if usage.Credits != nil {
			hasCreditsPayload = true
			hasCreditsAny = hasCreditsAny || usage.Credits.HasCredits
			unlimitedAny = unlimitedAny || usage.Credits.Unlimited
			if usage.Credits.Balance != nil {
				if parsed, err := strconv.ParseFloat(strings.TrimSpace(*usage.Credits.Balance), 64); err == nil {
					balanceSum += parsed
					balanceCount++
				}
			}
		}

		for i := range usage.AdditionalRateLimits {
			item := usage.AdditionalRateLimits[i]
			key := strings.TrimSpace(item.LimitName) + "|" + strings.TrimSpace(item.MeteredFeature)
			acc, ok := additional[key]
			if !ok {
				acc = &codexAdditionalAccumulator{
					limitName:      strings.TrimSpace(item.LimitName),
					meteredFeature: strings.TrimSpace(item.MeteredFeature),
				}
				additional[key] = acc
			}
			acc.rate.add(normalizeCodexRateLimitWindows(item.RateLimit), weight)
		}
	}
	if withUsage > 0 {
		compat.PlanType = dominantPlanType(planCounts)
	}
	compat.RateLimit = mainRate.averageRateLimit()
	compat.CodeReviewRateLimit = codeReviewRate.averageRateLimit()
	totals.PrimaryWindow = mainRate.primaryWindow.totals()
	totals.SecondaryWindow = mainRate.secondaryWindow.totals()
	compat.RateLimit = applyCodexMainTotalsToCompatRateLimit(compat.RateLimit, totals.PrimaryWindow, totals.SecondaryWindow)
	totalMultiplier := roundFloat(mainRate.primaryWindow.denominatorWeightSum, 2)
	compat.TotalUsageMultiplier = totalMultiplier
	totals.TotalUsageMultiplier = totalMultiplier

	if hasCreditsPayload {
		credits := &codexUsageCredits{
			HasCredits: hasCreditsAny,
			Unlimited:  unlimitedAny,
		}
		if balanceCount > 0 {
			balance := strconv.FormatFloat(balanceSum, 'f', -1, 64)
			credits.Balance = &balance
		}
		compat.Credits = credits
	}

	if len(additional) > 0 {
		keys := make([]string, 0, len(additional))
		for key := range additional {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		compat.AdditionalRateLimits = make([]codexUsageAdditionalRateLimit, 0, len(keys))
		totals.AdditionalRateLimits = make([]codexAdditionalRateLimitTotals, 0, len(keys))
		for _, key := range keys {
			acc := additional[key]
			compat.AdditionalRateLimits = append(compat.AdditionalRateLimits, codexUsageAdditionalRateLimit{
				LimitName:      acc.limitName,
				MeteredFeature: acc.meteredFeature,
				RateLimit:      acc.rate.averageRateLimit(),
			})
			totals.AdditionalRateLimits = append(totals.AdditionalRateLimits, codexAdditionalRateLimitTotals{
				LimitName:       acc.limitName,
				MeteredFeature:  acc.meteredFeature,
				PrimaryWindow:   acc.rate.primaryWindow.totals(),
				SecondaryWindow: acc.rate.secondaryWindow.totals(),
			})
		}
	}

	return compat, totals, withUsage
}

func dominantPlanType(planCounts map[string]int) string {
	if len(planCounts) == 0 {
		return "guest"
	}
	type pair struct {
		plan  string
		count int
	}
	plans := make([]pair, 0, len(planCounts))
	for plan, count := range planCounts {
		plans = append(plans, pair{plan: plan, count: count})
	}
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].count == plans[j].count {
			return plans[i].plan < plans[j].plan
		}
		return plans[i].count > plans[j].count
	})
	return plans[0].plan
}

func roundFloat(value float64, digits int) float64 {
	if digits < 0 {
		return value
	}
	scale := math.Pow10(digits)
	return math.Round(value*scale) / scale
}

func roundPercentToInt(progress float64) int {
	if progress <= 0 {
		return 0
	}
	if progress >= 100 {
		return 100
	}
	return int(math.Round(progress))
}

func applyCodexMainTotalsToCompatRateLimit(rate *codexUsageRateLimit, primary, secondary *codexUsageWindowTotals) *codexUsageRateLimit {
	if rate == nil && primary == nil && secondary == nil {
		return nil
	}
	out := &codexUsageRateLimit{}
	if rate != nil {
		cloned := *rate
		cloned.PrimaryWindow = cloneCodexUsageWindow(rate.PrimaryWindow)
		cloned.SecondaryWindow = cloneCodexUsageWindow(rate.SecondaryWindow)
		out = &cloned
	}

	if primary != nil {
		if out.PrimaryWindow == nil {
			out.PrimaryWindow = &codexUsageWindow{
				LimitWindowSeconds: codexFiveHourWindowSecs,
			}
		}
		out.PrimaryWindow.UsedPercent = roundPercentToInt(primary.ProgressPercent)
		if out.PrimaryWindow.LimitWindowSeconds <= 0 {
			out.PrimaryWindow.LimitWindowSeconds = codexFiveHourWindowSecs
		}
		if out.PrimaryWindow.ResetAfterSeconds <= 0 {
			out.PrimaryWindow.ResetAfterSeconds = primary.MinResetAfterSecond
		}
		if out.PrimaryWindow.ResetAt <= 0 {
			out.PrimaryWindow.ResetAt = primary.MinResetAt
		}
	}
	if secondary != nil {
		if out.SecondaryWindow == nil {
			out.SecondaryWindow = &codexUsageWindow{
				LimitWindowSeconds: codexWeeklyWindowSecs,
			}
		}
		out.SecondaryWindow.UsedPercent = roundPercentToInt(secondary.ProgressPercent)
		if out.SecondaryWindow.LimitWindowSeconds <= 0 {
			out.SecondaryWindow.LimitWindowSeconds = codexWeeklyWindowSecs
		}
		if out.SecondaryWindow.ResetAfterSeconds <= 0 {
			out.SecondaryWindow.ResetAfterSeconds = secondary.MinResetAfterSecond
		}
		if out.SecondaryWindow.ResetAt <= 0 {
			out.SecondaryWindow.ResetAt = secondary.MinResetAt
		}
	}

	return out
}

func normalizeCodexRateLimitWindows(rate *codexUsageRateLimit) *codexUsageRateLimit {
	if rate == nil {
		return nil
	}
	out := *rate
	primary := cloneCodexUsageWindow(rate.PrimaryWindow)
	secondary := cloneCodexUsageWindow(rate.SecondaryWindow)

	if primary == nil && secondary == nil {
		out.PrimaryWindow = nil
		out.SecondaryWindow = nil
		return &out
	}
	if primary == nil {
		out.PrimaryWindow = nil
		out.SecondaryWindow = secondary
		return &out
	}
	if secondary == nil {
		out.PrimaryWindow = primary
		out.SecondaryWindow = nil
		return &out
	}

	if primary.LimitWindowSeconds > 0 && secondary.LimitWindowSeconds > 0 {
		if primary.LimitWindowSeconds <= secondary.LimitWindowSeconds {
			out.PrimaryWindow = primary
			out.SecondaryWindow = secondary
		} else {
			out.PrimaryWindow = secondary
			out.SecondaryWindow = primary
		}
		return &out
	}

	if isLikelyCodexFiveHourWindow(primary.LimitWindowSeconds) && !isLikelyCodexFiveHourWindow(secondary.LimitWindowSeconds) {
		out.PrimaryWindow = primary
		out.SecondaryWindow = secondary
		return &out
	}
	if isLikelyCodexFiveHourWindow(secondary.LimitWindowSeconds) && !isLikelyCodexFiveHourWindow(primary.LimitWindowSeconds) {
		out.PrimaryWindow = secondary
		out.SecondaryWindow = primary
		return &out
	}

	if isLikelyCodexWeeklyWindow(primary.LimitWindowSeconds) && !isLikelyCodexWeeklyWindow(secondary.LimitWindowSeconds) {
		out.PrimaryWindow = secondary
		out.SecondaryWindow = primary
		return &out
	}
	if isLikelyCodexWeeklyWindow(secondary.LimitWindowSeconds) && !isLikelyCodexWeeklyWindow(primary.LimitWindowSeconds) {
		out.PrimaryWindow = primary
		out.SecondaryWindow = secondary
		return &out
	}

	// Fallback: preserve original primary/secondary order when classification is uncertain.
	out.PrimaryWindow = primary
	out.SecondaryWindow = secondary
	return &out
}

// normalizeCodexMainRateLimitWindows keeps compat payload semantics stable:
// primary_window => 5h window, secondary_window => weekly window.
// For single-window payloads, weekly/unknown windows are treated as secondary
// to avoid contaminating 5h aggregates with weekly-only free accounts.
func normalizeCodexMainRateLimitWindows(rate *codexUsageRateLimit) *codexUsageRateLimit {
	if rate == nil {
		return nil
	}
	out := *rate
	primary := cloneCodexUsageWindow(rate.PrimaryWindow)
	secondary := cloneCodexUsageWindow(rate.SecondaryWindow)

	if primary == nil && secondary == nil {
		out.PrimaryWindow = nil
		out.SecondaryWindow = nil
		return &out
	}

	// Single-window payloads: 5h stays primary, everything else goes secondary.
	if primary != nil && secondary == nil {
		if isLikelyCodexFiveHourWindow(primary.LimitWindowSeconds) {
			out.PrimaryWindow = primary
			out.SecondaryWindow = nil
		} else {
			out.PrimaryWindow = nil
			out.SecondaryWindow = primary
		}
		return &out
	}
	if secondary != nil && primary == nil {
		if isLikelyCodexFiveHourWindow(secondary.LimitWindowSeconds) {
			out.PrimaryWindow = secondary
			out.SecondaryWindow = nil
		} else {
			out.PrimaryWindow = nil
			out.SecondaryWindow = secondary
		}
		return &out
	}

	// Two-window payloads: classify by 5h/week first.
	var fiveHour *codexUsageWindow
	var weekly *codexUsageWindow
	switch {
	case isLikelyCodexFiveHourWindow(primary.LimitWindowSeconds):
		fiveHour = primary
	case isLikelyCodexWeeklyWindow(primary.LimitWindowSeconds):
		weekly = primary
	}
	switch {
	case isLikelyCodexFiveHourWindow(secondary.LimitWindowSeconds) && fiveHour == nil:
		fiveHour = secondary
	case isLikelyCodexWeeklyWindow(secondary.LimitWindowSeconds) && weekly == nil:
		weekly = secondary
	}

	if fiveHour != nil || weekly != nil {
		// Fill the missing side with the remaining window to avoid dropping data.
		if fiveHour == nil {
			if primary != weekly {
				fiveHour = primary
			} else {
				fiveHour = secondary
			}
		}
		if weekly == nil {
			if primary != fiveHour {
				weekly = primary
			} else {
				weekly = secondary
			}
		}
		out.PrimaryWindow = fiveHour
		out.SecondaryWindow = weekly
		return &out
	}

	// Fallback for unknown durations: keep deterministic shortest=>primary order.
	if primary.LimitWindowSeconds > 0 && secondary.LimitWindowSeconds > 0 {
		if primary.LimitWindowSeconds <= secondary.LimitWindowSeconds {
			out.PrimaryWindow = primary
			out.SecondaryWindow = secondary
		} else {
			out.PrimaryWindow = secondary
			out.SecondaryWindow = primary
		}
		return &out
	}

	out.PrimaryWindow = primary
	out.SecondaryWindow = secondary
	return &out
}

func isLikelyCodexFiveHourWindow(seconds int) bool {
	// tolerate provider-side rounding/drift around 5h
	return seconds >= 2*3600 && seconds <= 12*3600
}

func isLikelyCodexWeeklyWindow(seconds int) bool {
	// tolerate provider-side rounding/drift around 7d
	return seconds >= 5*24*3600 && seconds <= 10*24*3600
}

func codexPlanWeight(planType string, freePlanWeight, proPlanWeight float64) float64 {
	if freePlanWeight <= 0 {
		freePlanWeight = codexFreePlanWeight
	}
	if proPlanWeight <= 0 {
		proPlanWeight = codexProPlanWeight
	}
	switch strings.ToLower(strings.TrimSpace(planType)) {
	case "free":
		return freePlanWeight
	case "pro":
		return proPlanWeight
	default:
		return 1.0
	}
}

func inferCodexTotalUsageMultiplier(planType string, freePlanWeight, proPlanWeight float64) float64 {
	switch strings.ToLower(strings.TrimSpace(planType)) {
	case "free":
		return codexPlanWeight("free", freePlanWeight, proPlanWeight)
	case "pro":
		return codexPlanWeight("pro", freePlanWeight, proPlanWeight)
	case "plus", "team", "business", "enterprise", "edu":
		return 1.0
	default:
		return 0
	}
}

func ensureCodexTotalUsageMultiplier(payload *codexUsagePayload, freePlanWeight, proPlanWeight float64) {
	if payload == nil {
		return
	}
	if payload.TotalUsageMultiplier > 0 {
		return
	}
	if inferred := inferCodexTotalUsageMultiplier(payload.PlanType, freePlanWeight, proPlanWeight); inferred > 0 {
		payload.TotalUsageMultiplier = inferred
	}
}

func (h *Handler) codexFreePlanWeight() float64 {
	overrides := h.codexUsageConfigOverrides()
	if overrides.CodexFreePlanWeight <= 0 {
		return codexFreePlanWeight
	}
	return overrides.CodexFreePlanWeight
}

func (h *Handler) codexProPlanWeight() float64 {
	overrides := h.codexUsageConfigOverrides()
	if overrides.CodexProPlanWeight <= 0 {
		return codexProPlanWeight
	}
	return overrides.CodexProPlanWeight
}

func (h *Handler) codexOAuthAvailableTotalForAPIKey(apiKey string) (units float64, configured bool, matched bool) {
	if h == nil || h.cfg == nil {
		return 0, false, false
	}
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return 0, false, false
	}
	index := -1
	for i := range h.cfg.APIKeys {
		if strings.TrimSpace(h.cfg.APIKeys[i]) == key {
			index = i
			break
		}
	}
	if index < 0 {
		return 0, false, false
	}
	overrides := h.codexUsageConfigOverrides()
	if index >= len(overrides.CodexOAuthAvailableTotals) {
		return 0, false, true
	}
	value := overrides.CodexOAuthAvailableTotals[index]
	if value < 0 {
		value = 0
	}
	return value, true, true
}

// EvaluateCodexOAuthQuota checks whether an authenticated proxy API key has exceeded
// its configured available weekly quota for Codex traffic.
//
// Quota unit is "team-member weekly standard units".
func (h *Handler) EvaluateCodexOAuthQuota(ctx context.Context, apiKey string) (exceeded bool, used float64, limit float64, checked bool) {
	h.ensureUsageRuntimeInitialized()
	availableUnits, configured, matched := h.codexOAuthAvailableTotalForAPIKey(apiKey)
	if !matched {
		return false, 0, 0, false
	}

	h.refreshCodexUsageFromCacheTTL(ctx)
	_, summary, _ := h.codexUsageSnapshot()
	weeklyProgressPercent := 0.0
	systemWeeklyUnits := 0.0
	if summary.Total.SecondaryWindow != nil {
		weeklyProgressPercent = summary.Total.SecondaryWindow.ProgressPercent
		systemWeeklyUnits = float64(summary.Total.SecondaryWindow.TotalPercent) / 100.0
		if systemWeeklyUnits < 0 {
			systemWeeklyUnits = 0
		}
	}
	used = (weeklyProgressPercent / 100.0) * systemWeeklyUnits

	if configured {
		limit = availableUnits
		if limit <= 0 {
			return true, used, 0, true
		}
		return used >= limit, used, limit, true
	}

	// Default behavior: full system capacity as quota.
	if systemWeeklyUnits <= 0 {
		return false, used, 0, false
	}
	limit = systemWeeklyUnits
	return used >= limit, used, limit, true
}

func inferCodexPlanType(auth *coreauth.Auth, status codexAuthUsageStatus) string {
	if status.Usage != nil {
		if plan := strings.ToLower(strings.TrimSpace(status.Usage.PlanType)); plan != "" {
			return plan
		}
	}
	if plan := strings.ToLower(strings.TrimSpace(status.PlanType)); plan != "" {
		return plan
	}
	if auth != nil && auth.Metadata != nil {
		if raw, ok := auth.Metadata["plan_type"].(string); ok {
			if plan := strings.ToLower(strings.TrimSpace(raw)); plan != "" {
				return plan
			}
		}
	}
	if claims := extractCodexIDTokenClaims(auth); claims != nil {
		if raw, ok := claims["plan_type"].(string); ok {
			if plan := strings.ToLower(strings.TrimSpace(raw)); plan != "" {
				return plan
			}
		}
	}
	name := ""
	if auth != nil {
		name = strings.ToLower(strings.TrimSpace(auth.FileName))
	}
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(status.FileName))
	}
	switch {
	case strings.Contains(name, "-free"):
		return "free"
	case strings.Contains(name, "-team"):
		return "team"
	case strings.Contains(name, "-business"):
		return "business"
	default:
		return "guest"
	}
}

func (h *Handler) codexUsageStateFilePath() string {
	if h == nil {
		return ""
	}
	baseDir := ""
	if cfgPath := strings.TrimSpace(h.configFilePath); cfgPath != "" {
		baseDir = filepath.Dir(cfgPath)
	} else if h.cfg != nil {
		baseDir = strings.TrimSpace(h.cfg.AuthDir)
	}
	if baseDir == "" {
		return ""
	}
	return filepath.Join(baseDir, codexUsageStateFileName)
}

func (h *Handler) loadCodexUsageState() {
	if h == nil {
		return
	}
	runtime := h.codexUsageStateRef()
	if runtime == nil {
		return
	}
	path := h.codexUsageStateFilePath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var persisted codexUsagePersistentState
	if err := json.Unmarshal(data, &persisted); err != nil {
		log.WithError(err).Debugf("failed to load codex usage cache from %s", path)
		return
	}
	if persisted.ByAuth == nil {
		persisted.ByAuth = make(map[string]codexAuthUsageStatus)
	}
	compat := cloneCodexUsagePayload(&persisted.CompatPayload)
	summary := persisted.Summary
	summary.CompatPayload = cloneCodexUsagePayload(&summary.CompatPayload)
	if summary.PollIntervalSeconds <= 0 {
		summary.PollIntervalSeconds = int(codexUsagePollInterval / time.Second)
	}
	if summary.SelectedAuthID == "" {
		summary.SelectedAuthID = strings.TrimSpace(persisted.SelectedAuthID)
	}
	if len(summary.AuthFiles) == 0 && len(persisted.ByAuth) > 0 {
		authList := make([]codexAuthUsageStatus, 0, len(persisted.ByAuth))
		for _, item := range persisted.ByAuth {
			authList = append(authList, cloneCodexAuthUsageStatus(item))
		}
		sort.Slice(authList, func(i, j int) bool {
			return authList[i].AuthID < authList[j].AuthID
		})
		summary.AuthFiles = authList
	}

	runtime.codexUsageMu.Lock()
	runtime.codexUsageByAuth = make(map[string]codexAuthUsageStatus, len(persisted.ByAuth))
	for key, value := range persisted.ByAuth {
		runtime.codexUsageByAuth[key] = cloneCodexAuthUsageStatus(value)
	}
	runtime.codexUsageCompat = compat
	runtime.codexUsageSummary = summary
	runtime.codexUsageHasData = persisted.HasData
	runtime.codexUsageSelected = strings.TrimSpace(summary.SelectedAuthID)
	runtime.codexUsageMu.Unlock()
}

func (h *Handler) persistCodexUsageState() {
	if h == nil {
		return
	}
	path := h.codexUsageStateFilePath()
	if path == "" {
		return
	}
	compat, summary, hasData := h.codexUsageSnapshot()
	byAuth := h.codexUsageByAuthSnapshot()
	state := codexUsagePersistentState{
		UpdatedAt:      summary.UpdatedAt,
		SelectedAuthID: strings.TrimSpace(summary.SelectedAuthID),
		ByAuth:         byAuth,
		CompatPayload:  compat,
		Summary:        summary,
		HasData:        hasData,
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		log.WithError(err).Debug("failed to marshal codex usage cache")
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.WithError(err).Debugf("failed to create codex usage cache directory %s", dir)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		log.WithError(err).Debugf("failed to write codex usage cache temp file %s", tmp)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.WithError(err).Debugf("failed to commit codex usage cache file %s", path)
		_ = os.Remove(tmp)
	}
}

func (h *Handler) fetchCodexUsagePayload(ctx context.Context, auth *coreauth.Auth, token, accountID string) (codexUsagePayload, string, string, error) {
	payload := defaultCodexUsagePayload()
	baseURL, pathStyle := resolveCodexUsageBaseURL(auth)
	usageURL := buildCodexUsageURL(baseURL, pathStyle)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	if err != nil {
		return payload, baseURL, pathStyle, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "codex-cli")
	if strings.TrimSpace(accountID) != "" {
		req.Header.Set("ChatGPT-Account-Id", strings.TrimSpace(accountID))
	}

	httpClient := &http.Client{
		Timeout:   codexUsageRequestTimeout,
		Transport: h.apiCallTransport(auth),
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return payload, baseURL, pathStyle, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return payload, baseURL, pathStyle, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview := strings.TrimSpace(string(body))
		if len(preview) > 240 {
			preview = preview[:240]
		}
		return payload, baseURL, pathStyle, &codexUsageHTTPError{
			StatusCode: resp.StatusCode,
			Preview:    preview,
		}
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return payload, baseURL, pathStyle, fmt.Errorf("decode usage payload: %w", err)
	}
	if strings.TrimSpace(payload.AccountID) == "" {
		payload.AccountID = strings.TrimSpace(accountID)
	}
	if strings.TrimSpace(payload.Email) == "" {
		payload.Email = authEmail(auth)
	}
	if strings.TrimSpace(payload.PlanType) == "" {
		payload.PlanType = "guest"
	}
	ensureCodexTotalUsageMultiplier(&payload, h.codexFreePlanWeight(), h.codexProPlanWeight())
	return payload, baseURL, pathStyle, nil
}

func resolveCodexUsageBaseURL(auth *coreauth.Auth) (string, string) {
	raw := ""
	if auth != nil && auth.Attributes != nil {
		raw = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if raw == "" && auth != nil && auth.Metadata != nil {
		if v, ok := auth.Metadata["base_url"].(string); ok {
			raw = strings.TrimSpace(v)
		}
	}
	if raw == "" {
		raw = codexUsageDefaultBaseURL
	}
	return normalizeCodexUsageBaseURL(raw)
}

func normalizeCodexUsageBaseURL(base string) (string, string) {
	base = strings.TrimSpace(base)
	if base == "" {
		base = codexUsageDefaultBaseURL
	}
	base = strings.TrimRight(base, "/")

	if (strings.HasPrefix(base, "https://chatgpt.com") || strings.HasPrefix(base, "https://chat.openai.com")) && !strings.Contains(base, "/backend-api") {
		base = base + "/backend-api"
	}

	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/codex") {
		base = strings.TrimSuffix(base, "/codex")
	}

	if strings.Contains(base, "/backend-api") {
		return base, "wham"
	}
	return base, "api"
}

func buildCodexUsageURL(baseURL, pathStyle string) string {
	switch pathStyle {
	case "wham":
		return strings.TrimRight(baseURL, "/") + "/wham/usage"
	default:
		return strings.TrimRight(baseURL, "/") + "/api/codex/usage"
	}
}

func extractCodexAccessToken(auth *coreauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if token := metadataString(auth.Metadata, "access_token", "accessToken"); token != "" {
		return token
	}
	if raw, ok := auth.Metadata["token"]; ok {
		switch typed := raw.(type) {
		case map[string]any:
			return metadataString(typed, "access_token", "accessToken")
		case map[string]string:
			if token := strings.TrimSpace(typed["access_token"]); token != "" {
				return token
			}
			if token := strings.TrimSpace(typed["accessToken"]); token != "" {
				return token
			}
		}
	}
	return ""
}

func metadataString(metadata map[string]any, keys ...string) string {
	if len(metadata) == 0 {
		return ""
	}
	for _, key := range keys {
		if raw, ok := metadata[key]; ok {
			if text, ok := raw.(string); ok {
				if trimmed := strings.TrimSpace(text); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

func extractCodexAccountID(auth *coreauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if accountID := metadataString(auth.Metadata, "account_id", "chatgpt_account_id", "chatgptAccountId"); accountID != "" {
		return accountID
	}
	idToken := metadataString(auth.Metadata, "id_token")
	if idToken == "" {
		return ""
	}
	claims, err := codex.ParseJWTToken(idToken)
	if err != nil || claims == nil {
		return ""
	}
	return strings.TrimSpace(claims.GetAccountID())
}

func cloneCodexUsagePayload(payload *codexUsagePayload) codexUsagePayload {
	if payload == nil {
		return defaultCodexUsagePayload()
	}
	cloned := codexUsagePayload{
		UserID:               strings.TrimSpace(payload.UserID),
		AccountID:            strings.TrimSpace(payload.AccountID),
		Email:                strings.TrimSpace(payload.Email),
		PlanType:             strings.TrimSpace(payload.PlanType),
		TotalUsageMultiplier: payload.TotalUsageMultiplier,
		Extensions:           cloneCodexUsageExtensions(payload.Extensions),
		Promo:                cloneRawMessage(payload.Promo),
		Extra:                cloneRawMap(payload.Extra),
	}
	if cloned.PlanType == "" {
		cloned.PlanType = "guest"
	}
	if payload.RateLimit != nil {
		cloned.RateLimit = cloneCodexUsageRateLimit(payload.RateLimit)
	}
	if payload.CodeReviewRateLimit != nil {
		cloned.CodeReviewRateLimit = cloneCodexUsageRateLimit(payload.CodeReviewRateLimit)
	}
	if payload.Credits != nil {
		clonedCredits := *payload.Credits
		if payload.Credits.Balance != nil {
			b := *payload.Credits.Balance
			clonedCredits.Balance = &b
		}
		if len(payload.Credits.ApproxLocalMessages) > 0 {
			clonedCredits.ApproxLocalMessages = append([]any(nil), payload.Credits.ApproxLocalMessages...)
		}
		if len(payload.Credits.ApproxCloudMessages) > 0 {
			clonedCredits.ApproxCloudMessages = append([]any(nil), payload.Credits.ApproxCloudMessages...)
		}
		cloned.Credits = &clonedCredits
	}
	if payload.AdditionalRateLimits != nil {
		cloned.AdditionalRateLimits = make([]codexUsageAdditionalRateLimit, 0, len(payload.AdditionalRateLimits))
		for i := range payload.AdditionalRateLimits {
			item := payload.AdditionalRateLimits[i]
			copiedItem := codexUsageAdditionalRateLimit{
				LimitName:      item.LimitName,
				MeteredFeature: item.MeteredFeature,
				RateLimit:      cloneCodexUsageRateLimit(item.RateLimit),
			}
			cloned.AdditionalRateLimits = append(cloned.AdditionalRateLimits, copiedItem)
		}
	}
	return cloned
}

func cloneCodexUsageExtensions(input *codexUsageExtensions) *codexUsageExtensions {
	if input == nil {
		return nil
	}
	out := &codexUsageExtensions{
		Recovery: cloneCodexUsageRecovery(input.Recovery),
	}
	if len(input.ActiveAuthFiles) > 0 {
		out.ActiveAuthFiles = make([]codexUsageAuthFileExtensionItem, 0, len(input.ActiveAuthFiles))
		for i := range input.ActiveAuthFiles {
			item := input.ActiveAuthFiles[i]
			out.ActiveAuthFiles = append(out.ActiveAuthFiles, codexUsageAuthFileExtensionItem{
				AuthID:       strings.TrimSpace(item.AuthID),
				FileName:     strings.TrimSpace(item.FileName),
				Account:      strings.TrimSpace(item.Account),
				PlanType:     strings.TrimSpace(item.PlanType),
				Priority:     item.Priority,
				Status:       strings.TrimSpace(item.Status),
				Error:        strings.TrimSpace(item.Error),
				ErrorSummary: strings.TrimSpace(item.ErrorSummary),
				LastUsedAt:   item.LastUsedAt,
				FiveHour:     cloneCodexUsageWindow(item.FiveHour),
				Week:         cloneCodexUsageWindow(item.Week),
			})
		}
	}
	return out
}

func cloneCodexUsageRecovery(input *codexUsageRecovery) *codexUsageRecovery {
	if input == nil {
		return nil
	}
	return &codexUsageRecovery{
		FiveHour: cloneCodexUsageRecoveryWindow(input.FiveHour),
		Week:     cloneCodexUsageRecoveryWindow(input.Week),
		Combined: cloneCodexUsageCombinedRecovery(input.Combined),
	}
}

func cloneCodexUsageCombinedRecovery(input *codexUsageCombinedRecovery) *codexUsageCombinedRecovery {
	if input == nil {
		return nil
	}
	out := *input
	if len(input.Events) > 0 {
		out.Events = make([]codexUsageRecoveryEvent, len(input.Events))
		copy(out.Events, input.Events)
	}
	return &out
}

func cloneCodexUsageRecoveryWindow(input *codexUsageRecoveryWindow) *codexUsageRecoveryWindow {
	if input == nil {
		return nil
	}
	out := *input
	if len(input.Events) > 0 {
		out.Events = make([]codexUsageRecoveryEvent, len(input.Events))
		copy(out.Events, input.Events)
	}
	return &out
}

func cloneCodexUsageRateLimit(input *codexUsageRateLimit) *codexUsageRateLimit {
	if input == nil {
		return nil
	}
	out := *input
	out.PrimaryWindow = cloneCodexUsageWindow(input.PrimaryWindow)
	out.SecondaryWindow = cloneCodexUsageWindow(input.SecondaryWindow)
	return &out
}

func cloneRawMessage(input json.RawMessage) json.RawMessage {
	if len(input) == 0 {
		return nil
	}
	out := make([]byte, len(input))
	copy(out, input)
	return out
}

func cloneRawMap(input map[string]json.RawMessage) map[string]json.RawMessage {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(input))
	for key, value := range input {
		out[key] = cloneRawMessage(value)
	}
	return out
}

func cloneCodexUsageWindow(window *codexUsageWindow) *codexUsageWindow {
	if window == nil {
		return nil
	}
	cloned := *window
	return &cloned
}

func cloneCodexUsageWindowTotals(input *codexUsageWindowTotals) *codexUsageWindowTotals {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}

func cloneCodexUsageTotalSummary(input codexUsageTotalSummary) codexUsageTotalSummary {
	out := codexUsageTotalSummary{
		TotalUsageMultiplier: input.TotalUsageMultiplier,
		PrimaryWindow:        cloneCodexUsageWindowTotals(input.PrimaryWindow),
		SecondaryWindow:      cloneCodexUsageWindowTotals(input.SecondaryWindow),
	}
	if len(input.AdditionalRateLimits) > 0 {
		out.AdditionalRateLimits = make([]codexAdditionalRateLimitTotals, 0, len(input.AdditionalRateLimits))
		for i := range input.AdditionalRateLimits {
			item := input.AdditionalRateLimits[i]
			out.AdditionalRateLimits = append(out.AdditionalRateLimits, codexAdditionalRateLimitTotals{
				LimitName:       item.LimitName,
				MeteredFeature:  item.MeteredFeature,
				PrimaryWindow:   cloneCodexUsageWindowTotals(item.PrimaryWindow),
				SecondaryWindow: cloneCodexUsageWindowTotals(item.SecondaryWindow),
			})
		}
	}
	return out
}

func cloneTimePointer(ts *time.Time) *time.Time {
	if ts == nil {
		return nil
	}
	copied := *ts
	return &copied
}

func cloneCodexAuthUsageStatus(input codexAuthUsageStatus) codexAuthUsageStatus {
	out := input
	out.LastSuccessAt = cloneTimePointer(input.LastSuccessAt)
	if input.Usage != nil {
		cloned := cloneCodexUsagePayload(input.Usage)
		out.Usage = &cloned
	}
	return out
}

func (h *Handler) codexUsageByAuthSnapshot() map[string]codexAuthUsageStatus {
	if h == nil {
		return nil
	}
	h.ensureUsageRuntimeInitialized()
	state := h.codexUsageStateRef()
	if state == nil {
		return nil
	}
	state.codexUsageMu.RLock()
	defer state.codexUsageMu.RUnlock()
	out := make(map[string]codexAuthUsageStatus, len(state.codexUsageByAuth))
	for key, value := range state.codexUsageByAuth {
		out[key] = cloneCodexAuthUsageStatus(value)
	}
	return out
}

func (h *Handler) codexUsageSnapshot() (codexUsagePayload, codexUsageSummaryResponse, bool) {
	if h == nil {
		empty := defaultCodexUsagePayload()
		return empty, codexUsageSummaryResponse{
			PollIntervalSeconds: int(codexUsagePollInterval / time.Second),
			CompatPayload:       empty,
		}, false
	}
	h.ensureUsageRuntimeInitialized()
	state := h.codexUsageStateRef()
	if state == nil {
		empty := defaultCodexUsagePayload()
		return empty, codexUsageSummaryResponse{
			PollIntervalSeconds: int(codexUsagePollInterval / time.Second),
			CompatPayload:       empty,
		}, false
	}
	state.codexUsageMu.RLock()
	defer state.codexUsageMu.RUnlock()

	compat := cloneCodexUsagePayload(&state.codexUsageCompat)
	summary := state.codexUsageSummary
	summary.Total = cloneCodexUsageTotalSummary(state.codexUsageSummary.Total)
	summary.CompatPayload = cloneCodexUsagePayload(&state.codexUsageSummary.CompatPayload)
	if len(state.codexUsageSummary.AuthFiles) > 0 {
		summary.AuthFiles = make([]codexAuthUsageStatus, 0, len(state.codexUsageSummary.AuthFiles))
		for i := range state.codexUsageSummary.AuthFiles {
			summary.AuthFiles = append(summary.AuthFiles, cloneCodexAuthUsageStatus(state.codexUsageSummary.AuthFiles[i]))
		}
	}

	selectedAuthID := strings.TrimSpace(summary.SelectedAuthID)
	if selectedAuthID != "" {
		for i := range summary.AuthFiles {
			if strings.TrimSpace(summary.AuthFiles[i].AuthID) != selectedAuthID {
				continue
			}
			fillCodexCompatIdentityFromAuthStatus(&compat, summary.AuthFiles[i])
			fillCodexCompatIdentityFromAuthStatus(&summary.CompatPayload, summary.AuthFiles[i])
			break
		}
	}
	liveExtensions := buildCodexUsageExtensions(state.codexUsageByAuth, time.Now().UTC(), h.codexFreePlanWeight(), h.codexProPlanWeight(), h.codexUsageAuthBaseDir(), h.codexUsageAuthLookup())
	compat.Extensions = cloneCodexUsageExtensions(liveExtensions)
	summary.CompatPayload.Extensions = cloneCodexUsageExtensions(liveExtensions)
	ensureCodexTotalUsageMultiplier(&compat, h.codexFreePlanWeight(), h.codexProPlanWeight())
	ensureCodexTotalUsageMultiplier(&summary.CompatPayload, h.codexFreePlanWeight(), h.codexProPlanWeight())
	return compat, summary, state.codexUsageHasData
}

func (h *Handler) GetCodexUsageCompat(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusOK, defaultCodexUsagePayload())
		return
	}
	h.ensureUsageRuntimeInitialized()
	h.refreshCodexUsageFromCacheTTL(c.Request.Context())
	compat, _, _ := h.codexUsageSnapshot()
	c.JSON(http.StatusOK, compat)
}

func (h *Handler) GetCodexUsageSummary(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "handler not initialized"})
		return
	}
	h.refreshCodexUsageFromCacheTTL(c.Request.Context())
	_, summary, _ := h.codexUsageSnapshot()
	c.JSON(http.StatusOK, summary)
}
