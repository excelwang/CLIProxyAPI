package codexplugin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	Issuer          = "cli-proxy-api/codex-plugin"
	DefaultPlanType = "team"
	DefaultTTL      = 365 * 24 * time.Hour
)

var (
	ErrNotCodexPluginToken = errors.New("not a codex plugin token")
	ErrInvalidToken        = errors.New("invalid codex plugin token")
)

type IssueOptions struct {
	Email    string
	PlanType string
	TTL      time.Duration
	Now      time.Time
}

type tokenHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type authClaims struct {
	AccountID string `json:"chatgpt_account_id,omitempty"`
	PlanType  string `json:"chatgpt_plan_type,omitempty"`
	UserID    string `json:"chatgpt_user_id,omitempty"`
}

type profileClaims struct {
	Email string `json:"email,omitempty"`
}

type tokenClaims struct {
	Iss     string        `json:"iss"`
	Sub     string        `json:"sub"`
	Iat     int64         `json:"iat"`
	Nbf     int64         `json:"nbf"`
	Exp     int64         `json:"exp"`
	Email   string        `json:"email,omitempty"`
	Profile profileClaims `json:"https://api.openai.com/profile,omitempty"`
	Auth    authClaims    `json:"https://api.openai.com/auth,omitempty"`
}

type authJSON struct {
	AuthMode     string    `json:"auth_mode"`
	OpenAIAPIKey *string   `json:"OPENAI_API_KEY,omitempty"`
	Tokens       tokenData `json:"tokens"`
	LastRefresh  string    `json:"last_refresh"`
}

type tokenData struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

type ValidatedToken struct {
	APIKey    string
	Email     string
	PlanType  string
	AccountID string
	UserID    string
	ExpiresAt time.Time
}

func BuildAuthJSON(apiKey string, opts IssueOptions) ([]byte, error) {
	token, claims, err := IssueAccessToken(apiKey, opts)
	if err != nil {
		return nil, err
	}
	doc := authJSON{
		AuthMode: "chatgptAuthTokens",
		Tokens: tokenData{
			IDToken:      token,
			AccessToken:  token,
			RefreshToken: "",
			AccountID:    claims.Auth.AccountID,
		},
		LastRefresh: claimsTime(opts.Now).Format(time.RFC3339Nano),
	}
	return json.MarshalIndent(doc, "", "  ")
}

func IssueAccessToken(apiKey string, opts IssueOptions) (string, tokenClaims, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", tokenClaims{}, fmt.Errorf("empty api key")
	}
	now := claimsTime(opts.Now)
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	email := strings.TrimSpace(opts.Email)
	if email == "" {
		email = defaultEmail(apiKey)
	}
	planType := strings.TrimSpace(opts.PlanType)
	if planType == "" {
		planType = DefaultPlanType
	}
	accountID := derivedUUID("account", apiKey)
	userID := derivedUUID("user", apiKey)
	subject := derivedUUID("subject", apiKey)
	claims := tokenClaims{
		Iss:   Issuer,
		Sub:   subject,
		Iat:   now.Unix(),
		Nbf:   now.Unix(),
		Exp:   now.Add(ttl).Unix(),
		Email: email,
		Profile: profileClaims{
			Email: email,
		},
		Auth: authClaims{
			AccountID: accountID,
			PlanType:  planType,
			UserID:    userID,
		},
	}
	header := tokenHeader{
		Alg: "HS256",
		Typ: "JWT",
	}
	headerRaw, err := json.Marshal(header)
	if err != nil {
		return "", tokenClaims{}, fmt.Errorf("marshal token header: %w", err)
	}
	payloadRaw, err := json.Marshal(claims)
	if err != nil {
		return "", tokenClaims{}, fmt.Errorf("marshal token claims: %w", err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(headerRaw) + "." + base64.RawURLEncoding.EncodeToString(payloadRaw)
	mac := hmac.New(sha256.New, []byte(apiKey))
	_, _ = mac.Write([]byte(unsigned))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return unsigned + "." + signature, claims, nil
}

func ValidateAccessToken(token string, apiKeys []string, now time.Time) (*ValidatedToken, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrNotCodexPluginToken
	}

	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrNotCodexPluginToken
	}
	var claims tokenClaims
	if err := json.Unmarshal(payloadRaw, &claims); err != nil {
		return nil, ErrNotCodexPluginToken
	}
	if claims.Iss != Issuer {
		return nil, ErrNotCodexPluginToken
	}

	now = claimsTime(now)
	if claims.Nbf > 0 && now.Unix() < claims.Nbf {
		return nil, fmt.Errorf("%w: token not active yet", ErrInvalidToken)
	}
	if claims.Exp > 0 && now.Unix() >= claims.Exp {
		return nil, fmt.Errorf("%w: token expired", ErrInvalidToken)
	}

	unsigned := parts[0] + "." + parts[1]
	for _, apiKey := range apiKeys {
		apiKey = strings.TrimSpace(apiKey)
		if apiKey == "" {
			continue
		}
		expected := sign(unsigned, apiKey)
		if !hmac.Equal([]byte(expected), []byte(parts[2])) {
			continue
		}
		return &ValidatedToken{
			APIKey:    apiKey,
			Email:     claims.Email,
			PlanType:  claims.Auth.PlanType,
			AccountID: claims.Auth.AccountID,
			UserID:    claims.Auth.UserID,
			ExpiresAt: time.Unix(claims.Exp, 0).UTC(),
		}, nil
	}
	return nil, fmt.Errorf("%w: signature mismatch", ErrInvalidToken)
}

func sign(unsigned, apiKey string) string {
	mac := hmac.New(sha256.New, []byte(apiKey))
	_, _ = mac.Write([]byte(unsigned))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func claimsTime(now time.Time) time.Time {
	if now.IsZero() {
		now = time.Now()
	}
	return now.UTC()
}

func defaultEmail(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return fmt.Sprintf("codex-%x@cliproxyapi.local", sum[:6])
}

func derivedUUID(kind, apiKey string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex-plugin:"+kind+":"+apiKey)).String()
}
