package openai

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func makeIDToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".c2ln"
}

func TestParseIDTokenClaims(t *testing.T) {
	t.Run("extracts email and chatgpt account id", func(t *testing.T) {
		token := makeIDToken(t, map[string]any{"email": "dev@example.com", "chatgpt_account_id": "acc-123"})
		claims, err := parseIDTokenClaims(token)
		if err != nil {
			t.Fatalf("parseIDTokenClaims: %v", err)
		}
		if claims.Email != "dev@example.com" || claims.ChatGPTAccountID != "acc-123" {
			t.Fatalf("claims = %+v, want email dev@example.com + acc-123", claims)
		}
	})

	t.Run("empty token is not an error", func(t *testing.T) {
		claims, err := parseIDTokenClaims("")
		if err != nil {
			t.Fatalf("parseIDTokenClaims(\"\"): %v", err)
		}
		if claims.Email != "" || claims.ChatGPTAccountID != "" {
			t.Fatalf("claims = %+v, want zero values", claims)
		}
	})

	tests := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{"garbage string", "not-a-jwt", true},
		{"two segments", "a.b", true},
		{"bad payload encoding", "a.!!!b.c", true},
		{"payload is not json", "a." + base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".c", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseIDTokenClaims(tt.token); (err != nil) != tt.wantErr {
				t.Fatalf("parseIDTokenClaims(%q) error = %v, wantErr %v", tt.token, err, tt.wantErr)
			}
		})
	}
}

func TestParseTokenResponse(t *testing.T) {
	t.Run("full response with id_token claims", func(t *testing.T) {
		raw := []byte(`{"access_token":"at","refresh_token":"rt","expires_in":3600,"id_token":"` +
			makeIDToken(t, map[string]any{"email": "dev@example.com", "chatgpt_account_id": "acc-123"}) + `"}`)
		got, err := parseTokenResponse(raw, "", "", "")
		if err != nil {
			t.Fatalf("parseTokenResponse: %v", err)
		}
		if got.Type != "oauth" || got.Access != "at" || got.Refresh != "rt" {
			t.Fatalf("payload = %+v", got)
		}
		if got.Email != "dev@example.com" || got.AccountID != "acc-123" {
			t.Fatalf("claims = %q/%q, want dev@example.com/acc-123", got.Email, got.AccountID)
		}
		want := time.Now().UnixMilli() + 3600*1000 - expiresSkewMs
		if diff := got.Expires - want; diff > 5000 || diff < -5000 {
			t.Fatalf("Expires = %d, want ~%d (skew %d)", got.Expires, want, expiresSkewMs)
		}
	})

	t.Run("falls back to previous credential claims", func(t *testing.T) {
		raw := []byte(`{"access_token":"at2","expires_in":60}`) // no refresh_token, no id_token
		got, err := parseTokenResponse(raw, "rt-old", "old@example.com", "acc-old")
		if err != nil {
			t.Fatalf("parseTokenResponse: %v", err)
		}
		if got.Refresh != "rt-old" || got.Email != "old@example.com" || got.AccountID != "acc-old" {
			t.Fatalf("payload = %+v, want fallback rt-old/old@example.com/acc-old", got)
		}
	})

	t.Run("new id_token overrides fallback claims", func(t *testing.T) {
		raw := []byte(`{"access_token":"at3","refresh_token":"rt3","expires_in":60,"id_token":"` +
			makeIDToken(t, map[string]any{"email": "new@example.com"}) + `"}`)
		got, err := parseTokenResponse(raw, "rt-old", "old@example.com", "acc-old")
		if err != nil {
			t.Fatalf("parseTokenResponse: %v", err)
		}
		if got.Email != "new@example.com" {
			t.Fatalf("Email = %q, want new@example.com", got.Email)
		}
		if got.AccountID != "acc-old" {
			t.Fatalf("AccountID = %q, want fallback acc-old", got.AccountID)
		}
	})

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"missing access token", `{"refresh_token":"rt","expires_in":60}`, "missing fields"},
		{"missing expires_in", `{"access_token":"at"}`, "missing fields"},
		{"missing refresh without fallback", `{"access_token":"at","expires_in":60}`, "missing refresh_token"},
		{"invalid json", `nonsense`, "invalid character"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseTokenResponse([]byte(tt.raw), "", "", "")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseTokenResponse(%s) error = %v, want substring %q", tt.raw, err, tt.want)
			}
		})
	}
}

func TestValidateOpenAIEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{"auth.openai.com", "https://auth.openai.com/oauth/token", false},
		{"openai.com apex", "https://openai.com/oauth/authorize", false},
		{"other openai host", "https://api.openai.com/v1", false},
		{"http rejected", "http://auth.openai.com/oauth/token", true},
		{"wrong host", "https://evil.example.com/oauth/token", true},
		{"lookalike suffix", "https://notopenai.com/oauth/token", true},
		{"empty", "", true},
		{"garbage", "://", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOpenAIEndpoint(tt.rawURL, "token_endpoint")
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateOpenAIEndpoint(%q) error = %v, wantErr %v", tt.rawURL, err, tt.wantErr)
			}
		})
	}
}
