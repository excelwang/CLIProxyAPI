package codexplugin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
)

func TestBuildAuthJSONProducesChatgptAuthTokensDocument(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	doc, err := BuildAuthJSON("test-api-key", IssueOptions{
		Email:    "plugin@example.com",
		PlanType: "team",
		TTL:      24 * time.Hour,
		Now:      now,
	})
	if err != nil {
		t.Fatalf("BuildAuthJSON returned error: %v", err)
	}

	var parsed struct {
		AuthMode    string `json:"auth_mode"`
		LastRefresh string `json:"last_refresh"`
		Tokens      struct {
			IDToken     string `json:"id_token"`
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatalf("unmarshal auth json: %v", err)
	}
	if parsed.AuthMode != "chatgptAuthTokens" {
		t.Fatalf("unexpected auth_mode: %q", parsed.AuthMode)
	}
	if parsed.LastRefresh == "" {
		t.Fatal("expected last_refresh to be set")
	}
	if parsed.Tokens.IDToken == "" || parsed.Tokens.AccessToken == "" {
		t.Fatal("expected both id_token and access_token to be populated")
	}

	claims, err := codexauth.ParseJWTToken(parsed.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("ParseJWTToken returned error: %v", err)
	}
	if claims.Email != "plugin@example.com" {
		t.Fatalf("unexpected email: %q", claims.Email)
	}
	if claims.CodexAuthInfo.ChatgptPlanType != "team" {
		t.Fatalf("unexpected plan type: %q", claims.CodexAuthInfo.ChatgptPlanType)
	}
	if claims.CodexAuthInfo.ChatgptAccountID != parsed.Tokens.AccountID {
		t.Fatalf("account id mismatch: claims=%q doc=%q", claims.CodexAuthInfo.ChatgptAccountID, parsed.Tokens.AccountID)
	}
}

func TestValidateAccessTokenRejectsRemovedKey(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	token, _, err := IssueAccessToken("test-api-key", IssueOptions{
		Now: now,
	})
	if err != nil {
		t.Fatalf("IssueAccessToken returned error: %v", err)
	}
	validated, err := ValidateAccessToken(token, []string{"test-api-key"}, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("ValidateAccessToken returned error: %v", err)
	}
	if validated.APIKey != "test-api-key" {
		t.Fatalf("unexpected api key: %q", validated.APIKey)
	}

	_, err = ValidateAccessToken(token, []string{"other-key"}, now.Add(time.Hour))
	if err == nil || !strings.Contains(err.Error(), ErrInvalidToken.Error()) {
		t.Fatalf("expected invalid token error, got: %v", err)
	}
}
