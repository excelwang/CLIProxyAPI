package codexmodels

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

type ClientVersion struct {
	Major int
	Minor int
	Patch int
}

func (v *ClientVersion) MarshalJSON() ([]byte, error) {
	if v == nil {
		return []byte("null"), nil
	}
	return json.Marshal([]int{v.Major, v.Minor, v.Patch})
}

func (v *ClientVersion) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*v = ClientVersion{}
		return nil
	}

	if strings.HasPrefix(trimmed, "\"") {
		var raw string
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		parsed, err := parseClientVersion(raw)
		if err != nil {
			return err
		}
		*v = *parsed
		return nil
	}

	var parts []int
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) != 3 {
		return fmt.Errorf("client version must contain three elements")
	}
	*v = ClientVersion{Major: parts[0], Minor: parts[1], Patch: parts[2]}
	return nil
}

func (v *ClientVersion) IsZero() bool {
	return v == nil || (v.Major == 0 && v.Minor == 0 && v.Patch == 0)
}

func (v *ClientVersion) Compare(other *ClientVersion) int {
	if v == nil && other == nil {
		return 0
	}
	if v == nil {
		return -1
	}
	if other == nil {
		return 1
	}
	if v.Major != other.Major {
		if v.Major < other.Major {
			return -1
		}
		return 1
	}
	if v.Minor != other.Minor {
		if v.Minor < other.Minor {
			return -1
		}
		return 1
	}
	if v.Patch != other.Patch {
		if v.Patch < other.Patch {
			return -1
		}
		return 1
	}
	return 0
}

func parseClientVersion(raw string) (*ClientVersion, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("empty client version")
	}
	parts := strings.Split(trimmed, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid client version %q", raw)
	}
	values := make([]int, 3)
	for i, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("invalid client version %q: %w", raw, err)
		}
		values[i] = n
	}
	return &ClientVersion{Major: values[0], Minor: values[1], Patch: values[2]}, nil
}

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
	MinimalClientVersion          *ClientVersion          `json:"minimal_client_version,omitempty"`
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

type modelsCatalogDocument struct {
	Models []ModelInfo `json:"models"`
}

const fallbackBaseInstructions = "You are Codex."
const fallbackEffectiveContextWindowPercent = 95

//go:embed catalog/models.json
var embeddedCatalogJSON []byte

var (
	catalogOnce sync.Once
	catalogData map[string]ModelInfo
	catalogErr  error
)

func AvailableCodexModels(clientVersion string) ModelsResponse {
	models := registry.GetGlobalRegistry().GetAvailableModelsByProvider("codex")
	if len(models) == 0 {
		return ModelsResponse{Models: []ModelInfo{}}
	}

	requestedVersion, _ := parseClientVersion(strings.TrimSpace(clientVersion))
	catalog := loadCatalog()
	maxPriority := maxCatalogPriority(catalog)

	response := ModelsResponse{Models: make([]ModelInfo, 0, len(models))}
	for _, model := range models {
		if model == nil || strings.TrimSpace(model.ID) == "" {
			continue
		}

		if catalogModel, ok := catalog[model.ID]; ok {
			if requestedVersion != nil && catalogModel.MinimalClientVersion != nil && requestedVersion.Compare(catalogModel.MinimalClientVersion) < 0 {
				continue
			}
			response.Models = append(response.Models, mergeCatalogModel(model, catalogModel))
			continue
		}

		response.Models = append(response.Models, fallbackModelInfo(enrichModel(model), maxPriority))
	}

	sort.SliceStable(response.Models, func(i, j int) bool {
		if response.Models[i].Priority != response.Models[j].Priority {
			return response.Models[i].Priority < response.Models[j].Priority
		}
		return strings.ToLower(response.Models[i].Slug) < strings.ToLower(response.Models[j].Slug)
	})

	return response
}

func loadCatalog() map[string]ModelInfo {
	catalogOnce.Do(func() {
		catalogData = map[string]ModelInfo{}
		var doc modelsCatalogDocument
		if err := json.Unmarshal(embeddedCatalogJSON, &doc); err != nil {
			catalogErr = err
			return
		}
		for _, model := range doc.Models {
			slug := strings.TrimSpace(model.Slug)
			if slug == "" {
				continue
			}
			catalogData[slug] = cloneModelInfo(model)
		}
	})
	if catalogErr != nil {
		return map[string]ModelInfo{}
	}
	return catalogData
}

func maxCatalogPriority(catalog map[string]ModelInfo) int {
	maxPriority := 0
	for _, model := range catalog {
		if model.Priority > maxPriority {
			maxPriority = model.Priority
		}
	}
	return maxPriority
}

func mergeCatalogModel(runtime *registry.ModelInfo, catalog ModelInfo) ModelInfo {
	merged := cloneModelInfo(catalog)
	if strings.TrimSpace(merged.DisplayName) == "" {
		merged.DisplayName = strings.TrimSpace(runtime.DisplayName)
		if merged.DisplayName == "" {
			merged.DisplayName = runtime.ID
		}
	}
	if merged.Description == nil {
		if desc := strings.TrimSpace(runtime.Description); desc != "" {
			merged.Description = &desc
		}
	}
	if merged.ContextWindow == nil && runtime.ContextLength > 0 {
		v := int64(runtime.ContextLength)
		merged.ContextWindow = &v
	}
	if len(merged.InputModalities) == 0 {
		merged.InputModalities = inputModalities(runtime)
	}
	if len(merged.SupportedReasoningLevels) == 0 {
		levels, defaultLevel := reasoningEfforts(runtime)
		merged.SupportedReasoningLevels = levels
		merged.DefaultReasoningLevel = defaultLevel
	}
	if merged.EffectiveContextWindowPercent <= 0 {
		merged.EffectiveContextWindowPercent = fallbackEffectiveContextWindowPercent
	}
	if merged.TruncationPolicy.Mode == "" {
		merged.TruncationPolicy = fallbackTruncationPolicy(runtime)
	}
	return merged
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
	if len(enriched.SupportedInputModalities) == 0 && len(fallback.SupportedInputModalities) > 0 {
		enriched.SupportedInputModalities = append([]string(nil), fallback.SupportedInputModalities...)
	}
	if len(enriched.SupportedParameters) == 0 && len(fallback.SupportedParameters) > 0 {
		enriched.SupportedParameters = append([]string(nil), fallback.SupportedParameters...)
	}
	return &enriched
}

func fallbackModelInfo(model *registry.ModelInfo, maxKnownPriority int) ModelInfo {
	displayName := strings.TrimSpace(model.DisplayName)
	if displayName == "" {
		displayName = model.ID
	}

	var description *string
	if desc := strings.TrimSpace(model.Description); desc != "" {
		description = &desc
	}

	reasoningLevels, defaultReasoning := reasoningEfforts(model)
	contextWindow := maybeContextWindow(model)

	return ModelInfo{
		Slug:                          model.ID,
		DisplayName:                   displayName,
		Description:                   description,
		DefaultReasoningLevel:         defaultReasoning,
		SupportedReasoningLevels:      reasoningLevels,
		ShellType:                     "shell_command",
		Visibility:                    "list",
		MinimalClientVersion:          nil,
		SupportedInAPI:                true,
		Priority:                      maxKnownPriority + 100,
		AvailabilityNUX:               nil,
		Upgrade:                       nil,
		BaseInstructions:              fallbackBaseInstructions,
		ModelMessages:                 nil,
		SupportsReasoningSummaries:    false,
		DefaultReasoningSummary:       "auto",
		SupportVerbosity:              false,
		DefaultVerbosity:              nil,
		ApplyPatchToolType:            nil,
		WebSearchToolType:             "text",
		TruncationPolicy:              fallbackTruncationPolicy(model),
		SupportsParallelToolCalls:     contains(model.SupportedParameters, "tools"),
		SupportsImageDetailOriginal:   false,
		ContextWindow:                 contextWindow,
		AutoCompactTokenLimit:         nil,
		EffectiveContextWindowPercent: fallbackEffectiveContextWindowPercent,
		ExperimentalSupportedTools:    []string{},
		InputModalities:               inputModalities(model),
		PreferWebsockets:              false,
	}
}

func fallbackTruncationPolicy(model *registry.ModelInfo) TruncationPolicyConfig {
	truncationLimit := int64(10000)
	if model != nil && model.ContextLength > 0 {
		truncationLimit = int64(model.ContextLength)
	}
	return TruncationPolicyConfig{Mode: "tokens", Limit: truncationLimit}
}

func maybeContextWindow(model *registry.ModelInfo) *int64 {
	if model == nil || model.ContextLength <= 0 {
		return nil
	}
	v := int64(model.ContextLength)
	return &v
}

func inputModalities(model *registry.ModelInfo) []string {
	modalities := []string{"text"}
	for _, modality := range model.SupportedInputModalities {
		switch strings.ToLower(strings.TrimSpace(modality)) {
		case "image":
			modalities = appendIfMissing(modalities, "image")
		case "text":
			modalities = appendIfMissing(modalities, "text")
		}
	}
	return modalities
}

func cloneModelInfo(model ModelInfo) ModelInfo {
	cloned := model
	if model.Description != nil {
		description := *model.Description
		cloned.Description = &description
	}
	if model.DefaultReasoningLevel != nil {
		defaultLevel := *model.DefaultReasoningLevel
		cloned.DefaultReasoningLevel = &defaultLevel
	}
	if model.MinimalClientVersion != nil {
		version := *model.MinimalClientVersion
		cloned.MinimalClientVersion = &version
	}
	if len(model.SupportedReasoningLevels) > 0 {
		cloned.SupportedReasoningLevels = append([]ReasoningEffortPreset(nil), model.SupportedReasoningLevels...)
	}
	if model.ContextWindow != nil {
		contextWindow := *model.ContextWindow
		cloned.ContextWindow = &contextWindow
	}
	if model.AutoCompactTokenLimit != nil {
		autoCompact := *model.AutoCompactTokenLimit
		cloned.AutoCompactTokenLimit = &autoCompact
	}
	if len(model.ExperimentalSupportedTools) > 0 {
		cloned.ExperimentalSupportedTools = append([]string(nil), model.ExperimentalSupportedTools...)
	}
	if len(model.InputModalities) > 0 {
		cloned.InputModalities = append([]string(nil), model.InputModalities...)
	}
	return cloned
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
