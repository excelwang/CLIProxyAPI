package management

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestShouldTrackCodexUsageAuthRequiresFileBackedAuth(t *testing.T) {
	t.Parallel()

	fileAuth := &coreauth.Auth{
		ID:       "file-auth.json",
		Provider: "codex",
		FileName: "file-auth.json",
		Status:   coreauth.StatusActive,
	}
	if !shouldTrackCodexUsageAuth(fileAuth) {
		t.Fatalf("shouldTrackCodexUsageAuth(fileAuth) = false, want true")
	}

	configAPIKeyAuth := &coreauth.Auth{
		ID:       "codex:apikey:test123",
		Provider: "codex",
		Label:    "codex-apikey",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"source":   "config:codex[test123]",
			"api_key":  "sk-test",
			"base_url": "https://example.com/v1",
			"priority": "6",
		},
	}
	if shouldTrackCodexUsageAuth(configAPIKeyAuth) {
		t.Fatalf("shouldTrackCodexUsageAuth(configAPIKeyAuth) = true, want false")
	}
}
