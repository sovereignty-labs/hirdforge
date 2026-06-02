package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	mcpOAuthCodeTTL          = 5 * time.Minute
	mcpOAuthCleanupInterval  = time.Minute
	mcpOAuthTokenExpiry      = 86400
	mcpOAuthWellKnownPath    = "/.well-known/oauth-authorization-server"
	mcpOAuthAuthorizePath    = "/oauth/authorize"
	mcpOAuthTokenPath        = "/oauth/token"
	mcpOAuthClientSecretPost = "client_secret_post"
)

type mcpAuthorizationCode struct {
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

type mcpAuthorizationCodeStore struct {
	mu    sync.Mutex
	codes map[string]mcpAuthorizationCode
}

type gatewayMCPOAuthServer struct {
	publicURL    string
	clientID     string
	clientSecret string
	accessToken  string
	codeTTL      time.Duration
	store        *mcpAuthorizationCodeStore
	now          func() time.Time
}

type mcpTokenRequest struct {
	GrantType    string `json:"grant_type"`
	Code         string `json:"code"`
	RedirectURI  string `json:"redirect_uri"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	CodeVerifier string `json:"code_verifier"`
}

type mcpAuthorizePageData struct {
	ClientID            string
	AuthorizePath       string
	DenyURL             string
	ResponseType        string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	RedirectURI         string
}

var mcpAuthorizeTemplate = template.Must(template.New("mcp-authorize").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Hirdforge MCP Access</title>
  <style>
    :root {
      color-scheme: dark;
      --bg: #0b1020;
      --panel: rgba(14, 21, 38, 0.94);
      --panel-border: rgba(148, 163, 184, 0.18);
      --text: #e5eefc;
      --muted: #98a8c5;
      --accent: #d97706;
      --accent-strong: #f59e0b;
      --danger: #334155;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      display: grid;
      place-items: center;
      padding: 24px;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background:
        radial-gradient(circle at top, rgba(217, 119, 6, 0.18), transparent 35%),
        linear-gradient(180deg, #10182d 0%, var(--bg) 100%);
      color: var(--text);
    }
    .card {
      width: min(100%, 460px);
      background: var(--panel);
      border: 1px solid var(--panel-border);
      border-radius: 20px;
      padding: 32px;
      box-shadow: 0 24px 80px rgba(0, 0, 0, 0.35);
    }
    .brand {
      font-size: 13px;
      font-weight: 700;
      letter-spacing: 0.12em;
      text-transform: uppercase;
      color: var(--accent-strong);
      margin-bottom: 14px;
    }
    h1 {
      margin: 0 0 12px;
      font-size: 28px;
      line-height: 1.15;
    }
    p {
      margin: 0 0 16px;
      color: var(--muted);
      line-height: 1.6;
    }
    .client {
      margin: 20px 0 28px;
      padding: 14px 16px;
      border-radius: 14px;
      background: rgba(148, 163, 184, 0.08);
      border: 1px solid rgba(148, 163, 184, 0.14);
      word-break: break-word;
    }
    .client strong {
      display: block;
      margin-bottom: 6px;
      color: var(--muted);
      font-size: 12px;
      text-transform: uppercase;
      letter-spacing: 0.08em;
    }
    .actions {
      display: flex;
      gap: 12px;
      flex-wrap: wrap;
    }
    button, a {
      appearance: none;
      border: 0;
      border-radius: 12px;
      padding: 12px 18px;
      font: inherit;
      font-weight: 600;
      text-decoration: none;
      cursor: pointer;
      transition: transform 120ms ease, opacity 120ms ease, background-color 120ms ease;
    }
    button:hover, a:hover { transform: translateY(-1px); }
    .approve {
      background: linear-gradient(135deg, var(--accent) 0%, var(--accent-strong) 100%);
      color: #1b1306;
      flex: 1 1 180px;
    }
    .deny {
      background: var(--danger);
      color: var(--text);
      flex: 1 1 140px;
      text-align: center;
    }
  </style>
</head>
<body>
  <main class="card">
    <div class="brand">Hirdforge</div>
    <h1>Hirdforge MCP Access</h1>
    <p>This connector is requesting access to the Hirdforge MCP gateway.</p>
    <div class="client">
      <strong>Client ID</strong>
      <span>{{.ClientID}}</span>
    </div>
    <form method="post" action="{{.AuthorizePath}}">
      <input type="hidden" name="client_id" value="{{.ClientID}}">
      <input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
      <input type="hidden" name="response_type" value="{{.ResponseType}}">
      <input type="hidden" name="state" value="{{.State}}">
      <input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
      <input type="hidden" name="code_challenge_method" value="{{.CodeChallengeMethod}}">
      <div class="actions">
        <button class="approve" type="submit">Authorize</button>
        <a class="deny" href="{{.DenyURL}}">Deny</a>
      </div>
    </form>
  </main>
</body>
</html>`))

func registerGatewayMCPOAuth(mux *http.ServeMux) {
	server := newGatewayMCPOAuthServerFromEnv()
	registerGatewayMCPOAuthRoutes(mux, server)
}

func registerGatewayMCPOAuthRoutes(mux *http.ServeMux, server *gatewayMCPOAuthServer) {
	mux.HandleFunc(mcpOAuthWellKnownPath, server.handleAuthorizationServerMetadata)
	mux.HandleFunc(mcpOAuthAuthorizePath, server.handleAuthorize)
	mux.HandleFunc(mcpOAuthTokenPath, server.handleToken)
	mux.HandleFunc("/authorize", server.handleAuthorize)
	mux.HandleFunc("/token", server.handleToken)
	mux.HandleFunc(mcpProtectedResourcePath, handleMCPProtectedResourceMetadata)
}

func newGatewayMCPOAuthServerFromEnv() *gatewayMCPOAuthServer {
	store := newMCPAuthorizationCodeStore()
	go store.startCleanupLoop(mcpOAuthCleanupInterval)
	return &gatewayMCPOAuthServer{
		publicURL:    strings.TrimRight(strings.TrimSpace(os.Getenv("MCP_PUBLIC_URL")), "/"),
		clientID:     strings.TrimSpace(os.Getenv("MCP_OAUTH_CLIENT_ID")),
		clientSecret: strings.TrimSpace(os.Getenv("MCP_OAUTH_CLIENT_SECRET")),
		accessToken:  strings.TrimSpace(os.Getenv("MCP_SOVEREIGN_TOKEN")),
		codeTTL:      mcpOAuthCodeTTL,
		store:        store,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func newMCPAuthorizationCodeStore() *mcpAuthorizationCodeStore {
	return &mcpAuthorizationCodeStore{codes: map[string]mcpAuthorizationCode{}}
}

func (s *mcpAuthorizationCodeStore) put(code string, value mcpAuthorizationCode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[code] = value
}

func (s *mcpAuthorizationCodeStore) consume(code string) (mcpAuthorizationCode, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.codes[code]
	if ok {
		delete(s.codes, code)
	}
	return value, ok
}

func (s *mcpAuthorizationCodeStore) deleteExpired(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for code, value := range s.codes {
		if !value.ExpiresAt.After(now) {
			delete(s.codes, code)
		}
	}
}

func (s *mcpAuthorizationCodeStore) startCleanupLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for now := range ticker.C {
		s.deleteExpired(now.UTC())
	}
}

func (s *gatewayMCPOAuthServer) handleAuthorizationServerMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.publicURL == "" {
		http.Error(w, "MCP_PUBLIC_URL is not configured", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"issuer":                                s.publicURL,
		"authorization_endpoint":                s.publicURL + mcpOAuthAuthorizePath,
		"token_endpoint":                        s.publicURL + mcpOAuthTokenPath,
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{mcpOAuthClientSecretPost},
	})
}

func (s *gatewayMCPOAuthServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.serveAuthorizePage(w, r)
	case http.MethodPost:
		s.processAuthorizeApproval(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *gatewayMCPOAuthServer) serveAuthorizePage(w http.ResponseWriter, r *http.Request) {
	params, err := s.validateAuthorizeParams(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := mcpAuthorizeTemplate.Execute(w, mcpAuthorizePageData{
		ClientID:            params.Get("client_id"),
		AuthorizePath:       mcpOAuthAuthorizePath,
		DenyURL:             buildRedirectWithParams(params.Get("redirect_uri"), map[string]string{"error": "access_denied", "state": params.Get("state")}),
		ResponseType:        params.Get("response_type"),
		State:               params.Get("state"),
		CodeChallenge:       params.Get("code_challenge"),
		CodeChallengeMethod: params.Get("code_challenge_method"),
		RedirectURI:         params.Get("redirect_uri"),
	}); err != nil {
		http.Error(w, "render authorize page", http.StatusInternalServerError)
	}
}

func (s *gatewayMCPOAuthServer) processAuthorizeApproval(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form body", http.StatusBadRequest)
		return
	}
	params, err := s.validateAuthorizeParams(r.PostForm)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	code, err := generateMCPAuthorizationCode()
	if err != nil {
		http.Error(w, "failed to generate authorization code", http.StatusInternalServerError)
		return
	}
	now := s.now()
	s.store.put(code, mcpAuthorizationCode{
		ClientID:      params.Get("client_id"),
		RedirectURI:   params.Get("redirect_uri"),
		CodeChallenge: params.Get("code_challenge"),
		CreatedAt:     now,
		ExpiresAt:     now.Add(s.codeTTL),
	})

	http.Redirect(w, r, buildRedirectWithParams(params.Get("redirect_uri"), map[string]string{
		"code":  code,
		"state": params.Get("state"),
	}), http.StatusFound)
}

func (s *gatewayMCPOAuthServer) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, err := decodeMCPTokenRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.GrantType != "authorization_code" {
		http.Error(w, "grant_type must be authorization_code", http.StatusBadRequest)
		return
	}
	if s.clientID == "" || req.ClientID != s.clientID {
		http.Error(w, "invalid client_id", http.StatusUnauthorized)
		return
	}
	log.Printf("mcp oauth: token exchange client_id=%s has_secret=%v has_verifier=%v", req.ClientID, req.ClientSecret != "", req.CodeVerifier != "")
	if req.CodeVerifier == "" {
		if s.clientSecret == "" || req.ClientSecret != s.clientSecret {
			http.Error(w, "invalid client_secret", http.StatusUnauthorized)
			return
		}
	}
	code, ok := s.store.consume(strings.TrimSpace(req.Code))
	if !ok {
		http.Error(w, "invalid authorization code", http.StatusBadRequest)
		return
	}
	now := s.now()
	if !code.ExpiresAt.After(now) {
		http.Error(w, "authorization code expired", http.StatusBadRequest)
		return
	}
	if req.RedirectURI != code.RedirectURI {
		http.Error(w, "redirect_uri mismatch", http.StatusBadRequest)
		return
	}
	if req.ClientID != code.ClientID {
		http.Error(w, "client_id mismatch", http.StatusBadRequest)
		return
	}
	if !verifyMCPPKCES256(strings.TrimSpace(req.CodeVerifier), code.CodeChallenge) {
		http.Error(w, "invalid code_verifier", http.StatusBadRequest)
		return
	}
	if s.accessToken == "" {
		http.Error(w, "MCP_SOVEREIGN_TOKEN is not configured", http.StatusServiceUnavailable)
		return
	}
	log.Printf("mcp oauth: token exchange SUCCEEDED for client_id=%s", req.ClientID)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"access_token": s.accessToken,
		"token_type":   "bearer",
		"expires_in":   mcpOAuthTokenExpiry,
	})
}

func (s *gatewayMCPOAuthServer) validateAuthorizeParams(values url.Values) (url.Values, error) {
	clientID := strings.TrimSpace(values.Get("client_id"))
	if clientID == "" {
		return nil, fmt.Errorf("missing client_id")
	}
	if s.clientID == "" || clientID != s.clientID {
		return nil, fmt.Errorf("invalid client_id")
	}
	redirectURI := strings.TrimSpace(values.Get("redirect_uri"))
	if redirectURI == "" {
		return nil, fmt.Errorf("missing redirect_uri")
	}
	if _, err := url.ParseRequestURI(redirectURI); err != nil {
		return nil, fmt.Errorf("invalid redirect_uri")
	}
	if strings.TrimSpace(values.Get("response_type")) != "code" {
		return nil, fmt.Errorf("response_type must be code")
	}
	if strings.TrimSpace(values.Get("state")) == "" {
		return nil, fmt.Errorf("missing state")
	}
	if strings.TrimSpace(values.Get("code_challenge")) == "" {
		return nil, fmt.Errorf("missing code_challenge")
	}
	if strings.TrimSpace(values.Get("code_challenge_method")) != "S256" {
		return nil, fmt.Errorf("code_challenge_method must be S256")
	}
	return values, nil
}

func decodeMCPTokenRequest(r *http.Request) (mcpTokenRequest, error) {
	var req mcpTokenRequest
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	switch contentType {
	case "", "application/x-www-form-urlencoded", "multipart/form-data":
		if err := r.ParseForm(); err != nil {
			return req, fmt.Errorf("invalid form body")
		}
		req = mcpTokenRequest{
			GrantType:    strings.TrimSpace(r.Form.Get("grant_type")),
			Code:         strings.TrimSpace(r.Form.Get("code")),
			RedirectURI:  strings.TrimSpace(r.Form.Get("redirect_uri")),
			ClientID:     strings.TrimSpace(r.Form.Get("client_id")),
			ClientSecret: strings.TrimSpace(r.Form.Get("client_secret")),
			CodeVerifier: strings.TrimSpace(r.Form.Get("code_verifier")),
		}
	case "application/json":
		if err := decodeJSONStrict(r, &req); err != nil {
			return req, fmt.Errorf("invalid JSON body: %w", err)
		}
		req.GrantType = strings.TrimSpace(req.GrantType)
		req.Code = strings.TrimSpace(req.Code)
		req.RedirectURI = strings.TrimSpace(req.RedirectURI)
		req.ClientID = strings.TrimSpace(req.ClientID)
		req.ClientSecret = strings.TrimSpace(req.ClientSecret)
		req.CodeVerifier = strings.TrimSpace(req.CodeVerifier)
	default:
		return req, fmt.Errorf("unsupported content type")
	}
	return req, nil
}

func generateMCPAuthorizationCode() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func verifyMCPPKCES256(verifier, expectedChallenge string) bool {
	if verifier == "" || expectedChallenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	actual := base64.RawURLEncoding.EncodeToString(sum[:])
	return actual == expectedChallenge
}

func buildRedirectWithParams(rawURL string, params map[string]string) string {
	redirectURI, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	query := redirectURI.Query()
	for key, value := range params {
		query.Set(key, value)
	}
	redirectURI.RawQuery = query.Encode()
	return redirectURI.String()
}
