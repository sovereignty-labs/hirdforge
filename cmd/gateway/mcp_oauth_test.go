package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestMCPOAuthMetadata(t *testing.T) {
	t.Setenv("MCP_PUBLIC_URL", "https://mcp.hirdforge.com")
	t.Setenv("MCP_OAUTH_CLIENT_ID", "claude-ai")
	t.Setenv("MCP_OAUTH_CLIENT_SECRET", "topsecret")
	t.Setenv("MCP_SOVEREIGN_TOKEN", "sovereign-token")

	mux := http.NewServeMux()
	registerGatewayMCPOAuth(mux)

	req := httptest.NewRequest(http.MethodGet, mcpOAuthWellKnownPath, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if payload["issuer"] != "https://mcp.hirdforge.com" {
		t.Fatalf("issuer = %v", payload["issuer"])
	}
	if payload["authorization_endpoint"] != "https://mcp.hirdforge.com"+mcpOAuthAuthorizePath {
		t.Fatalf("authorization_endpoint = %v", payload["authorization_endpoint"])
	}
}

func TestMCPOAuthAuthorizeAndTokenFormFlow(t *testing.T) {
	server := newTestMCPOAuthServer()
	mux := http.NewServeMux()
	registerGatewayMCPOAuthRoutes(mux, server)

	challenge := pkceChallenge("verifier-123")
	redirectURI := "https://claude.ai/api/mcp/auth_callback"

	getReq := httptest.NewRequest(http.MethodGet, mcpOAuthAuthorizePath+"?client_id=claude-ai&redirect_uri="+url.QueryEscape(redirectURI)+"&response_type=code&state=opaque-state&code_challenge="+url.QueryEscape(challenge)+"&code_challenge_method=S256", nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("authorize GET status = %d, body = %s", getRec.Code, getRec.Body.String())
	}
	if !strings.Contains(getRec.Body.String(), "Hirdforge MCP Access") {
		t.Fatalf("authorize page missing title")
	}

	form := url.Values{
		"client_id":             {"claude-ai"},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"state":                 {"opaque-state"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	postReq := httptest.NewRequest(http.MethodPost, mcpOAuthAuthorizePath, strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postRec := httptest.NewRecorder()
	mux.ServeHTTP(postRec, postReq)

	if postRec.Code != http.StatusFound {
		t.Fatalf("authorize POST status = %d, body = %s", postRec.Code, postRec.Body.String())
	}
	location := postRec.Header().Get("Location")
	u, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse redirect location: %v", err)
	}
	code := u.Query().Get("code")
	if len(code) != 32 {
		t.Fatalf("authorization code length = %d", len(code))
	}
	if u.Query().Get("state") != "opaque-state" {
		t.Fatalf("state = %q", u.Query().Get("state"))
	}

	tokenForm := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {"claude-ai"},
		"client_secret": {"topsecret"},
		"code_verifier": {"verifier-123"},
	}
	tokenReq := httptest.NewRequest(http.MethodPost, mcpOAuthTokenPath, strings.NewReader(tokenForm.Encode()))
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenRec := httptest.NewRecorder()
	mux.ServeHTTP(tokenRec, tokenReq)

	if tokenRec.Code != http.StatusOK {
		t.Fatalf("token status = %d, body = %s", tokenRec.Code, tokenRec.Body.String())
	}
	var tokenPayload map[string]interface{}
	if err := json.Unmarshal(tokenRec.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatalf("unmarshal token response: %v", err)
	}
	if tokenPayload["access_token"] != "sovereign-token" {
		t.Fatalf("access_token = %v", tokenPayload["access_token"])
	}
	if tokenPayload["token_type"] != "bearer" {
		t.Fatalf("token_type = %v", tokenPayload["token_type"])
	}
}

func TestMCPOAuthTokenJSONBodyAndSingleUse(t *testing.T) {
	server := newTestMCPOAuthServer()
	code := "0123456789abcdef0123456789abcdef"
	server.store.put(code, mcpAuthorizationCode{
		ClientID:      "claude-ai",
		RedirectURI:   "https://claude.ai/api/mcp/auth_callback",
		CodeChallenge: pkceChallenge("json-verifier"),
		CreatedAt:     server.now(),
		ExpiresAt:     server.now().Add(5 * time.Minute),
	})

	body := `{"grant_type":"authorization_code","code":"` + code + `","redirect_uri":"https://claude.ai/api/mcp/auth_callback","client_id":"claude-ai","client_secret":"topsecret","code_verifier":"json-verifier"}`
	req := httptest.NewRequest(http.MethodPost, mcpOAuthTokenPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleToken(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("token JSON status = %d, body = %s", rec.Code, rec.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, mcpOAuthTokenPath, strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	server.handleToken(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("second token status = %d, body = %s", rec2.Code, rec2.Body.String())
	}
}

func TestMCPOAuthTokenRejectsExpiredCodeAndBadVerifier(t *testing.T) {
	server := newTestMCPOAuthServer()
	server.now = func() time.Time {
		return time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	}

	expiredCode := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	server.store.put(expiredCode, mcpAuthorizationCode{
		ClientID:      "claude-ai",
		RedirectURI:   "https://claude.ai/api/mcp/auth_callback",
		CodeChallenge: pkceChallenge("expired"),
		CreatedAt:     server.now().Add(-10 * time.Minute),
		ExpiresAt:     server.now().Add(-1 * time.Second),
	})
	expiredReq := httptest.NewRequest(http.MethodPost, mcpOAuthTokenPath, strings.NewReader(url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {expiredCode},
		"redirect_uri":  {"https://claude.ai/api/mcp/auth_callback"},
		"client_id":     {"claude-ai"},
		"client_secret": {"topsecret"},
		"code_verifier": {"expired"},
	}.Encode()))
	expiredReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	expiredRec := httptest.NewRecorder()
	server.handleToken(expiredRec, expiredReq)
	if expiredRec.Code != http.StatusBadRequest {
		t.Fatalf("expired token status = %d, body = %s", expiredRec.Code, expiredRec.Body.String())
	}

	badVerifierCode := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	server.store.put(badVerifierCode, mcpAuthorizationCode{
		ClientID:      "claude-ai",
		RedirectURI:   "https://claude.ai/api/mcp/auth_callback",
		CodeChallenge: pkceChallenge("correct-verifier"),
		CreatedAt:     server.now(),
		ExpiresAt:     server.now().Add(5 * time.Minute),
	})
	badReq := httptest.NewRequest(http.MethodPost, mcpOAuthTokenPath, strings.NewReader(url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {badVerifierCode},
		"redirect_uri":  {"https://claude.ai/api/mcp/auth_callback"},
		"client_id":     {"claude-ai"},
		"client_secret": {"topsecret"},
		"code_verifier": {"wrong-verifier"},
	}.Encode()))
	badReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	badRec := httptest.NewRecorder()
	server.handleToken(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("bad verifier status = %d, body = %s", badRec.Code, badRec.Body.String())
	}
}

func newTestMCPOAuthServer() *gatewayMCPOAuthServer {
	return &gatewayMCPOAuthServer{
		publicURL:    "https://mcp.hirdforge.com",
		clientID:     "claude-ai",
		clientSecret: "topsecret",
		accessToken:  "sovereign-token",
		codeTTL:      5 * time.Minute,
		store:        newMCPAuthorizationCodeStore(),
		now: func() time.Time {
			return time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
		},
	}
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
