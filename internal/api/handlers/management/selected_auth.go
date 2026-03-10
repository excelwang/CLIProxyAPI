package management

import "strings"

// SelectedAuthIDCallback returns a callback that records the currently selected Codex auth.
func (h *Handler) SelectedAuthIDCallback() func(string) {
	if h == nil {
		return nil
	}
	return func(authID string) {
		h.captureSelectedCodexAuthID(authID)
	}
}

func (h *Handler) captureSelectedCodexAuthID(authID string) {
	authID = strings.TrimSpace(authID)
	if authID == "" || !h.isCodexAuthID(authID) {
		return
	}
	h.setSelectedCodexAuthID(authID)
}

func (h *Handler) setSelectedCodexAuthID(authID string) {
	state := h.codexUsageStateRef()
	if state == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	state.codexUsageMu.Lock()
	changed := strings.TrimSpace(state.codexUsageSelected) != authID
	state.codexUsageSelected = authID
	state.codexUsageSummary.SelectedAuthID = authID
	state.codexUsageMu.Unlock()
	if changed {
		h.clearObservedCodexServiceTierIfAuthChanged(authID)
	}
}

func (h *Handler) selectedCodexAuthID() string {
	state := h.codexUsageStateRef()
	if state == nil {
		return ""
	}
	state.codexUsageMu.RLock()
	defer state.codexUsageMu.RUnlock()
	selected := strings.TrimSpace(state.codexUsageSelected)
	if selected != "" {
		return selected
	}
	return strings.TrimSpace(state.codexUsageSummary.SelectedAuthID)
}

func (h *Handler) isCodexAuthID(authID string) bool {
	if h == nil {
		return false
	}
	authID = strings.TrimSpace(authID)
	if authID == "" || h.authManager == nil {
		return false
	}
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if strings.TrimSpace(auth.ID) != authID {
			continue
		}
		return strings.EqualFold(strings.TrimSpace(auth.Provider), "codex")
	}
	return false
}
