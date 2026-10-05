// Package openai implements the OpenAI ChatGPT (Codex) subscription OAuth
// vendor: `cincai credential login openai-oauth`. Constants mirror the Codex
// CLI's published login flow (issuer auth.openai.com, loopback callback port
// 1455). The stored credential exposes the subscription's access/refresh
// tokens for providers that relay the ChatGPT backend (Responses API).
package openai

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/subosito/cincai/credential/oauth/flow"
	"github.com/subosito/cincai/credential/oauth/wire"
)

// Module implements OpenAI login and refresh.
type Module struct{}

const (
	// clientID is the Codex CLI's public OAuth client. The ChatGPT subscription
	// backend issues tokens to this client.
	clientID         = "app_EMoamEEZ73f0CkXaXp7hrann"
	discoveryURL     = "https://auth.openai.com/.well-known/openid-configuration"
	redirectHost     = "localhost"
	redirectPort     = 1455
	redirectPath     = "/auth/callback"
	scope            = "openid profile email offline_access"
	expiresSkewMs    = 5 * 60 * 1000
	requestTimeout   = 20 * time.Second
	discoveryTimeout = 15 * time.Second
)

// fixedRedirectURI is the Codex login allow-listed loopback redirect. The
// callback flow binds 127.0.0.1 and must advertise exactly this URI.
var fixedRedirectURI = fmt.Sprintf("http://%s:%d%s", redirectHost, redirectPort, redirectPath)

func (Module) Login(ctx context.Context, ctrl flow.Controller) (wire.OAuthPayload, error) {
	var verifier string
	var out wire.OAuthPayload
	cb := flow.CallbackFlow{
		PreferredPort:    redirectPort,
		CallbackPath:     redirectPath,
		Hostname:         redirectHost,
		FixedRedirectURI: fixedRedirectURI,
		Controller:       ctrl,
	}
	err := cb.Run(ctx,
		func(state, redirectURI string) (flow.AuthInfo, error) {
			var challenge string
			var err error
			verifier, challenge, err = flow.GeneratePKCE()
			if err != nil {
				return flow.AuthInfo{}, err
			}
			discovery, err := fetchDiscovery(ctx)
			if err != nil {
				return flow.AuthInfo{}, err
			}
			nonce, err := randomNonce()
			if err != nil {
				return flow.AuthInfo{}, err
			}
			params := url.Values{
				"response_type":         {"code"},
				"client_id":             {clientID},
				"redirect_uri":          {redirectURI},
				"scope":                 {scope},
				"code_challenge":        {challenge},
				"code_challenge_method": {"S256"},
				"state":                 {state},
				"nonce":                 {nonce},
			}
			return flow.AuthInfo{
				URL:          discovery.AuthorizationEndpoint + "?" + params.Encode(),
				Instructions: "Complete login in your browser for OpenAI (ChatGPT subscription).",
			}, nil
		},
		func(code, _, redirectURI string) error {
			discovery, err := fetchDiscovery(ctx)
			if err != nil {
				return err
			}
			token, err := exchangeCode(ctx, discovery.TokenEndpoint, code, redirectURI, verifier)
			if err != nil {
				return err
			}
			out = token
			return nil
		},
	)
	if err != nil {
		return wire.OAuthPayload{}, err
	}
	return out, nil
}

func (Module) Refresh(ctx context.Context, cred wire.OAuthPayload) (wire.OAuthPayload, error) {
	discovery, err := fetchDiscovery(ctx)
	if err != nil {
		return wire.OAuthPayload{}, err
	}
	body := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {cred.Refresh},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, discovery.TokenEndpoint, strings.NewReader(body.Encode()))
	if err != nil {
		return wire.OAuthPayload{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: requestTimeout}
	res, err := client.Do(req)
	if err != nil {
		return wire.OAuthPayload{}, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return wire.OAuthPayload{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return wire.OAuthPayload{}, fmt.Errorf("openai token refresh HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	// Keep the previous identity claims unless the new id_token carries them.
	return parseTokenResponse(raw, cred.Refresh, cred.Email, cred.AccountID)
}

type discoveryDoc struct {
	AuthorizationEndpoint string
	TokenEndpoint         string
}

func fetchDiscovery(ctx context.Context) (discoveryDoc, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return discoveryDoc{}, err
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: discoveryTimeout}
	res, err := client.Do(req)
	if err != nil {
		return discoveryDoc{}, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return discoveryDoc{}, err
	}
	if res.StatusCode != http.StatusOK {
		return discoveryDoc{}, fmt.Errorf("openai discovery HTTP %d", res.StatusCode)
	}
	var doc struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return discoveryDoc{}, err
	}
	if err := validateOpenAIEndpoint(doc.AuthorizationEndpoint, "authorization_endpoint"); err != nil {
		return discoveryDoc{}, err
	}
	if err := validateOpenAIEndpoint(doc.TokenEndpoint, "token_endpoint"); err != nil {
		return discoveryDoc{}, err
	}
	return discoveryDoc{
		AuthorizationEndpoint: doc.AuthorizationEndpoint,
		TokenEndpoint:         doc.TokenEndpoint,
	}, nil
}

func exchangeCode(ctx context.Context, tokenEndpoint, code, redirectURI, verifier string) (wire.OAuthPayload, error) {
	body := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(body.Encode()))
	if err != nil {
		return wire.OAuthPayload{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: requestTimeout}
	res, err := client.Do(req)
	if err != nil {
		return wire.OAuthPayload{}, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return wire.OAuthPayload{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return wire.OAuthPayload{}, fmt.Errorf("openai token exchange HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	return parseTokenResponse(raw, "", "", "")
}

func parseTokenResponse(raw []byte, fallbackRefresh, fallbackEmail, fallbackAccountID string) (wire.OAuthPayload, error) {
	var data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		IDToken      string `json:"id_token"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return wire.OAuthPayload{}, err
	}
	if data.AccessToken == "" || data.ExpiresIn == 0 {
		return wire.OAuthPayload{}, fmt.Errorf("openai token response missing fields")
	}
	refresh := data.RefreshToken
	if refresh == "" {
		refresh = fallbackRefresh
	}
	if refresh == "" {
		return wire.OAuthPayload{}, fmt.Errorf("openai token response missing refresh_token")
	}
	email, accountID := fallbackEmail, fallbackAccountID
	if claims, err := parseIDTokenClaims(data.IDToken); err == nil {
		if claims.Email != "" {
			email = claims.Email
		}
		if claims.ChatGPTAccountID != "" {
			accountID = claims.ChatGPTAccountID
		}
	}
	return wire.OAuthPayload{
		Type:      "oauth",
		Refresh:   refresh,
		Access:    data.AccessToken,
		Expires:   time.Now().UnixMilli() + data.ExpiresIn*1000 - expiresSkewMs,
		Email:     email,
		AccountID: accountID,
	}, nil
}

// idTokenClaims are the unverified claims cincai reads from the id_token JWT.
// Verification is the issuer's job over the token endpoint response; these
// claims only label the stored credential (email) and route the ChatGPT
// backend workspace header (chatgpt_account_id).
type idTokenClaims struct {
	Email            string `json:"email"`
	ChatGPTAccountID string `json:"chatgpt_account_id"`
}

// parseIDTokenClaims decodes the id_token JWT payload without verifying the
// signature. An empty token is not an error — callers fall back to the
// previous credential's claims.
func parseIDTokenClaims(idToken string) (idTokenClaims, error) {
	var claims idTokenClaims
	if strings.TrimSpace(idToken) == "" {
		return claims, nil
	}
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return claims, fmt.Errorf("openai id_token: not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, fmt.Errorf("openai id_token: decode payload: %w", err)
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, fmt.Errorf("openai id_token: parse claims: %w", err)
	}
	return claims, nil
}

func validateOpenAIEndpoint(rawURL, field string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return fmt.Errorf("invalid OpenAI %s: %s", field, rawURL)
	}
	host := strings.ToLower(u.Hostname())
	if host != "openai.com" && !strings.HasSuffix(host, ".openai.com") {
		return fmt.Errorf("invalid OpenAI %s: %s", field, rawURL)
	}
	return nil
}

func randomNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
