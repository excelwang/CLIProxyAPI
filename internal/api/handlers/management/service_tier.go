package management

import (
	"strings"
	"time"
)

// ObservedServiceTierCallback returns a callback that records the upstream-confirmed service tier for a Codex auth.
func (h *Handler) ObservedServiceTierCallback() func(string, string) {
	if h == nil {
		return nil
	}
	return func(authID string, serviceTier string) {
		h.captureObservedCodexServiceTier(authID, serviceTier)
	}
}

func (h *Handler) captureObservedCodexServiceTier(authID string, serviceTier string) {
	if h == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" || !h.isCodexAuthID(authID) {
		return
	}
	state := h.codexUsageStateRef()
	if state == nil {
		return
	}
	serviceTier = strings.ToLower(strings.TrimSpace(serviceTier))
	now := time.Now().UTC()

	state.codexUsageMu.Lock()
	state.codexObservedServiceTierAuthID = authID
	state.codexObservedServiceTier = serviceTier
	state.codexObservedServiceTierAt = now
	state.codexUsageMu.Unlock()
}

func (h *Handler) clearObservedCodexServiceTierIfAuthChanged(authID string) {
	if h == nil {
		return
	}
	state := h.codexUsageStateRef()
	if state == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	state.codexUsageMu.Lock()
	defer state.codexUsageMu.Unlock()
	if authID != "" && authID == strings.TrimSpace(state.codexObservedServiceTierAuthID) {
		return
	}
	state.codexObservedServiceTierAuthID = ""
	state.codexObservedServiceTier = ""
	state.codexObservedServiceTierAt = time.Time{}
}

func (h *Handler) observedCodexServiceTierSnapshot() (string, string, time.Time) {
	state := h.codexUsageStateRef()
	if state == nil {
		return "", "", time.Time{}
	}
	state.codexUsageMu.RLock()
	defer state.codexUsageMu.RUnlock()
	return strings.TrimSpace(state.codexObservedServiceTierAuthID), strings.TrimSpace(state.codexObservedServiceTier), state.codexObservedServiceTierAt
}
