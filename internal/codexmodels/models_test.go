package codexmodels

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func registerCodexModelForTest(t *testing.T, clientID string, models []*registry.ModelInfo) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, "codex", models)
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})
}

func findModelBySlug(models []ModelInfo, slug string) *ModelInfo {
	for i := range models {
		if models[i].Slug == slug {
			return &models[i]
		}
	}
	return nil
}

func TestAvailableCodexModels_UsesCatalogMetadataForKnownModel(t *testing.T) {
	registerCodexModelForTest(t, "test-codexmodels-catalog", []*registry.ModelInfo{{
		ID:                       "gpt-5.2-codex",
		DisplayName:              "runtime display should not win",
		Description:              "runtime description should not win",
		ContextLength:            400000,
		SupportedParameters:      []string{"tools"},
		SupportedInputModalities: []string{"text", "image"},
		Thinking:                 &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
	}})

	response := AvailableCodexModels("0.111.0")
	found := findModelBySlug(response.Models, "gpt-5.2-codex")
	if found == nil {
		t.Fatalf("expected known model in response, got %+v", response.Models)
	}
	if found.DisplayName != "gpt-5.2-codex" {
		t.Fatalf("expected catalog display_name, got %q", found.DisplayName)
	}
	if found.Priority != 3 {
		t.Fatalf("expected catalog priority 3, got %d", found.Priority)
	}
	if found.MinimalClientVersion == nil || found.MinimalClientVersion.Major != 0 || found.MinimalClientVersion.Minor != 0 || found.MinimalClientVersion.Patch != 1 {
		t.Fatalf("unexpected minimal client version: %+v", found.MinimalClientVersion)
	}
	if found.Visibility != "list" {
		t.Fatalf("unexpected visibility: %q", found.Visibility)
	}
	if found.BaseInstructions == fallbackBaseInstructions {
		t.Fatal("expected catalog base instructions, got fallback instructions")
	}
	if !found.SupportedInAPI {
		t.Fatal("expected supported_in_api=true")
	}
}

func TestAvailableCodexModels_FiltersKnownModelsByClientVersion(t *testing.T) {
	registerCodexModelForTest(t, "test-codexmodels-client-version", []*registry.ModelInfo{{ID: "gpt-5.2-codex"}, {ID: "gpt-5.3-codex"}})

	response := AvailableCodexModels("0.50.0")
	if findModelBySlug(response.Models, "gpt-5.2-codex") == nil {
		t.Fatal("expected gpt-5.2-codex to remain visible for older clients")
	}
	if findModelBySlug(response.Models, "gpt-5.3-codex") != nil {
		t.Fatal("expected gpt-5.3-codex to be filtered by minimal_client_version")
	}
}

func TestAvailableCodexModels_FallbackUnknownModelPreservesRuntimeMetadata(t *testing.T) {
	registerCodexModelForTest(t, "test-codexmodels-runtime", []*registry.ModelInfo{{
		ID:                       "gpt-5.4",
		DisplayName:              "GPT-5.4",
		Description:              "Latest GPT-5.4",
		ContextLength:            400000,
		SupportedParameters:      []string{"tools"},
		SupportedInputModalities: []string{"text", "image"},
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	}})

	response := AvailableCodexModels("0.111.0")
	found := findModelBySlug(response.Models, "gpt-5.4")
	if found == nil {
		t.Fatalf("expected fallback model gpt-5.4 in response, got %+v", response.Models)
	}
	if found.DisplayName != "GPT-5.4" {
		t.Fatalf("unexpected display name: %q", found.DisplayName)
	}
	if !found.SupportedInAPI {
		t.Fatal("expected model to be marked supported_in_api")
	}
	if !found.SupportsParallelToolCalls {
		t.Fatal("expected tools support to map to supports_parallel_tool_calls=true")
	}
	if len(found.InputModalities) != 2 || found.InputModalities[0] != "text" || found.InputModalities[1] != "image" {
		t.Fatalf("unexpected input modalities: %+v", found.InputModalities)
	}
	if found.DefaultReasoningLevel == nil || *found.DefaultReasoningLevel != "medium" {
		t.Fatalf("unexpected default reasoning level: %+v", found.DefaultReasoningLevel)
	}
	if len(found.SupportedReasoningLevels) != 3 {
		t.Fatalf("expected three reasoning levels, got %+v", found.SupportedReasoningLevels)
	}
	if found.Priority <= 3 {
		t.Fatalf("expected fallback priority after known catalog models, got %d", found.Priority)
	}
}
