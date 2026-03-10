package codexpluginaccess

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/codexplugin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
)

func TestProviderAuthenticateAcceptsDerivedJWT(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	token, _, err := codexplugin.IssueAccessToken("test-key", codexplugin.IssueOptions{Now: now})
	if err != nil {
		t.Fatalf("IssueAccessToken returned error: %v", err)
	}
	p := &provider{
		name: ProviderName,
		keys: []string{"test-key"},
		now:  func() time.Time { return now.Add(time.Hour) },
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	result, authErr := p.Authenticate(req.Context(), req)
	if authErr != nil {
		t.Fatalf("Authenticate returned error: %v", authErr)
	}
	if result == nil {
		t.Fatal("expected authenticate result")
	}
	if result.Principal != "test-key" {
		t.Fatalf("unexpected principal: %q", result.Principal)
	}
	if result.Metadata["plugin_token"] != "true" {
		t.Fatalf("expected plugin_token metadata, got: %#v", result.Metadata)
	}
}

func TestProviderAuthenticateLeavesPlainAPIKeysToConfigProvider(t *testing.T) {
	p := &provider{
		name: ProviderName,
		keys: []string{"test-key"},
		now:  time.Now,
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	_, authErr := p.Authenticate(req.Context(), req)
	if authErr == nil || !sdkaccess.IsAuthErrorCode(authErr, sdkaccess.AuthErrorCodeNotHandled) {
		t.Fatalf("expected not handled error, got: %v", authErr)
	}
}
