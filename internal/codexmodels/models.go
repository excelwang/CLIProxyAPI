package codexmodels

import (
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

type ReasoningEffortPreset struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

type TruncationPolicyConfig struct {
	Mode  string `json:"mode"`
	Limit int64  `json:"limit"`
}

type ModelInfo struct {
	Slug                          string                  `json:"slug"`
	DisplayName                   string                  `json:"display_name"`
	Description                   *string                 `json:"description,omitempty"`
	DefaultReasoningLevel         *string                 `json:"default_reasoning_level,omitempty"`
	SupportedReasoningLevels      []ReasoningEffortPreset `json:"supported_reasoning_levels"`
	ShellType                     string                  `json:"shell_type"`
	Visibility                    string                  `json:"visibility"`
	SupportedInAPI                bool                    `json:"supported_in_api"`
	Priority                      int                     `json:"priority"`
	AvailabilityNUX               any                     `json:"availability_nux"`
	Upgrade                       any                     `json:"upgrade"`
	BaseInstructions              string                  `json:"base_instructions"`
	ModelMessages                 any                     `json:"model_messages,omitempty"`
	SupportsReasoningSummaries    bool                    `json:"supports_reasoning_summaries"`
	DefaultReasoningSummary       string                  `json:"default_reasoning_summary"`
	SupportVerbosity              bool                    `json:"support_verbosity"`
	DefaultVerbosity              any                     `json:"default_verbosity"`
	ApplyPatchToolType            any                     `json:"apply_patch_tool_type"`
	WebSearchToolType             string                  `json:"web_search_tool_type"`
	TruncationPolicy              TruncationPolicyConfig  `json:"truncation_policy"`
	SupportsParallelToolCalls     bool                    `json:"supports_parallel_tool_calls"`
	SupportsImageDetailOriginal   bool                    `json:"supports_image_detail_original"`
	ContextWindow                 *int64                  `json:"context_window,omitempty"`
	AutoCompactTokenLimit         *int64                  `json:"auto_compact_token_limit,omitempty"`
	EffectiveContextWindowPercent int64                   `json:"effective_context_window_percent"`
	ExperimentalSupportedTools    []string                `json:"experimental_supported_tools"`
	InputModalities               []string                `json:"input_modalities"`
	PreferWebsockets              bool                    `json:"prefer_websockets"`
}

type ModelsResponse struct {
	Models []ModelInfo `json:"models"`
}

func AvailableCodexModels() ModelsResponse {
	models := registry.GetGlobalRegistry().GetAvailableModelsByProvider("codex")
	if len(models) == 0 {
		return ModelsResponse{Models: []ModelInfo{}}
	}

	cloned := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil || strings.TrimSpace(model.ID) == "" {
			continue
		}
		cloned = append(cloned, enrichModel(model))
	}
	sort.SliceStable(cloned, func(i, j int) bool {
		if cloned[i].Created != cloned[j].Created {
			return cloned[i].Created > cloned[j].Created
		}
		return strings.ToLower(cloned[i].ID) < strings.ToLower(cloned[j].ID)
	})

	response := ModelsResponse{Models: make([]ModelInfo, 0, len(cloned))}
	for idx, model := range cloned {
		response.Models = append(response.Models, toCodexModelInfo(model, len(cloned)-idx))
	}
	return response
}

func enrichModel(model *registry.ModelInfo) *registry.ModelInfo {
	if model == nil {
		return nil
	}
	enriched := *model
	fallback := registry.LookupModelInfo(model.ID, "codex")
	if fallback == nil {
		return &enriched
	}
	if strings.TrimSpace(enriched.DisplayName) == "" {
		enriched.DisplayName = fallback.DisplayName
	}
	if strings.TrimSpace(enriched.Description) == "" {
		enriched.Description = fallback.Description
	}
	if enriched.ContextLength <= 0 {
		enriched.ContextLength = fallback.ContextLength
	}
	if enriched.MaxCompletionTokens <= 0 {
		enriched.MaxCompletionTokens = fallback.MaxCompletionTokens
	}
	if enriched.Thinking == nil && fallback.Thinking != nil {
		copyThinking := *fallback.Thinking
		if len(fallback.Thinking.Levels) > 0 {
			copyThinking.Levels = append([]string(nil), fallback.Thinking.Levels...)
		}
		enriched.Thinking = &copyThinking
	}
	return &enriched
}

func toCodexModelInfo(model *registry.ModelInfo, priority int) ModelInfo {
	displayName := strings.TrimSpace(model.DisplayName)
	if displayName == "" {
		displayName = model.ID
	}

	var description *string
	if desc := strings.TrimSpace(model.Description); desc != "" {
		description = &desc
	}

	reasoningLevels, defaultReasoning := reasoningEfforts(model)
	var contextWindow *int64
	if model.ContextLength > 0 {
		v := int64(model.ContextLength)
		contextWindow = &v
	}

	truncationLimit := int64(100000)
	if model.ContextLength > 0 {
		truncationLimit = int64(model.ContextLength)
	}

	inputModalities := []string{"text"}
	for _, modality := range model.SupportedInputModalities {
		switch strings.ToLower(strings.TrimSpace(modality)) {
		case "image":
			inputModalities = appendIfMissing(inputModalities, "image")
		case "text":
			inputModalities = appendIfMissing(inputModalities, "text")
		}
	}

	return ModelInfo{
		Slug:                          model.ID,
		DisplayName:                   displayName,
		Description:                   description,
		DefaultReasoningLevel:         defaultReasoning,
		SupportedReasoningLevels:      reasoningLevels,
		ShellType:                     "shell_command",
		Visibility:                    "list",
		SupportedInAPI:                true,
		Priority:                      priority,
		AvailabilityNUX:               nil,
		Upgrade:                       nil,
		BaseInstructions:              "You are Codex.",
		ModelMessages:                 nil,
		SupportsReasoningSummaries:    false,
		DefaultReasoningSummary:       "auto",
		SupportVerbosity:              false,
		DefaultVerbosity:              nil,
		ApplyPatchToolType:            nil,
		WebSearchToolType:             "text",
		TruncationPolicy:              TruncationPolicyConfig{Mode: "tokens", Limit: truncationLimit},
		SupportsParallelToolCalls:     contains(model.SupportedParameters, "tools"),
		SupportsImageDetailOriginal:   false,
		ContextWindow:                 contextWindow,
		AutoCompactTokenLimit:         nil,
		EffectiveContextWindowPercent: 95,
		ExperimentalSupportedTools:    []string{},
		InputModalities:               inputModalities,
		PreferWebsockets:              false,
	}
}

func reasoningEfforts(model *registry.ModelInfo) ([]ReasoningEffortPreset, *string) {
	if model != nil && model.Thinking != nil && len(model.Thinking.Levels) > 0 {
		levels := make([]ReasoningEffortPreset, 0, len(model.Thinking.Levels))
		for _, raw := range model.Thinking.Levels {
			level := normalizeReasoningEffort(raw)
			if level == "" {
				continue
			}
			levels = append(levels, ReasoningEffortPreset{
				Effort:      level,
				Description: level,
			})
		}
		if len(levels) > 0 {
			defaultLevel := "medium"
			if !supportsReasoningLevel(levels, defaultLevel) {
				defaultLevel = levels[0].Effort
			}
			return levels, &defaultLevel
		}
	}
	defaultLevel := "medium"
	return []ReasoningEffortPreset{{Effort: defaultLevel, Description: defaultLevel}}, &defaultLevel
}

func supportsReasoningLevel(levels []ReasoningEffortPreset, target string) bool {
	for _, level := range levels {
		if level.Effort == target {
			return true
		}
	}
	return false
}

func normalizeReasoningEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func appendIfMissing(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}
