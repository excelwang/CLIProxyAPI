package management

import "strings"

func (h *Handler) selectedCodexAuthID() string {
	if h == nil {
		return ""
	}
	if h.authManager != nil {
		if selected := strings.TrimSpace(h.authManager.SelectedAuthID("codex")); selected != "" {
			return selected
		}
	}
	state := h.codexUsageStateRef()
	if state == nil {
		return ""
	}
	state.codexUsageMu.RLock()
	defer state.codexUsageMu.RUnlock()
	if selected := strings.TrimSpace(state.codexUsageSelected); selected != "" {
		return selected
	}
	return strings.TrimSpace(state.codexUsageSummary.SelectedAuthID)
}
