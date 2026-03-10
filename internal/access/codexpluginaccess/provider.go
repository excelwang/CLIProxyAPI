package codexpluginaccess

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/codexplugin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
)

const (
	AccessProviderTypeCodexPluginJWT = "codex-plugin-jwt"
	ProviderName                     = "codex-plugin-inline"
)

// Register ensures the codex-plugin access provider is available when inline API keys exist.
func Register(cfg *internalconfig.SDKConfig) {
	if cfg == nil || len(cfg.APIKeys) == 0 {
		sdkaccess.UnregisterProvider(AccessProviderTypeCodexPluginJWT)
		return
	}
	keys := normalizeKeys(cfg.APIKeys)
	if len(keys) == 0 {
		sdkaccess.UnregisterProvider(AccessProviderTypeCodexPluginJWT)
		return
	}
	sdkaccess.RegisterProvider(AccessProviderTypeCodexPluginJWT, &provider{
		name: ProviderName,
		keys: keys,
		now:  time.Now,
	})
}

type provider struct {
	name string
	keys []string
	now  func() time.Time
}

func (p *provider) Identifier() string {
	if p == nil || p.name == "" {
		return ProviderName
	}
	return p.name
}

func (p *provider) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if p == nil || len(p.keys) == 0 {
		return nil, sdkaccess.NewNotHandledError()
	}
	token := bearerToken(r)
	if token == "" {
		return nil, sdkaccess.NewNotHandledError()
	}
	validated, err := codexplugin.ValidateAccessToken(token, p.keys, p.now())
	if err == nil {
		return &sdkaccess.Result{
			Provider:  p.Identifier(),
			Principal: validated.APIKey,
			Metadata: map[string]string{
				"source":       "codex-plugin-jwt",
				"email":        validated.Email,
				"plan_type":    validated.PlanType,
				"account_id":   validated.AccountID,
				"plugin_token": "true",
			},
		}, nil
	}
	if errors.Is(err, codexplugin.ErrNotCodexPluginToken) {
		return nil, sdkaccess.NewNotHandledError()
	}
	return nil, sdkaccess.NewInvalidCredentialError()
}

func bearerToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if authHeader == "" {
		return ""
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func normalizeKeys(keys []string) []string {
	normalized := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	return normalized
}
