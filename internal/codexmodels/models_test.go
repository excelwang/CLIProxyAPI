package codexmodels

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func TestAvailableCodexModels_UsesRuntimeAvailableSet(t *testing.T) {
	clientID := "test-codexmodels-runtime"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, "codex", []*registry.ModelInfo{
		{
			ID:                       "gpt-5.4",
			DisplayName:              "GPT-5.4",
			Description:              "Latest GPT-5.4",
			ContextLength:            400000,
			SupportedParameters:      []string{"tools"},
			SupportedInputModalities: []string{"text", "image"},
			Thinking: &registry.ThinkingSupport{
				Levels: []string{"low", "medium", "high"},
			},
		},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	response := AvailableCodexModels()
	if len(response.Models) == 0 {
		t.Fatal("expected codex models response to include runtime model")
	}

	var found *ModelInfo
	for index := range response.Models {
		if response.Models[index].Slug == "gpt-5.4" {
			found = &response.Models[index]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected runtime model gpt-5.4 in response, got %+v", response.Models)
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
}
