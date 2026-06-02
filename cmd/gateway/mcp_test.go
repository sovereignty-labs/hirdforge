package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPProtectedResourceMetadata(t *testing.T) {
	mux := http.NewServeMux()
	registerGatewayMCPOAuthRoutes(mux, newTestMCPOAuthServer())

	req := httptest.NewRequest(http.MethodGet, mcpProtectedResourcePath, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"scopes_supported":[]`) {
		t.Fatalf("raw body missing explicit empty scopes_supported: %s", rec.Body.String())
	}

	var payload struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if payload.Resource != mcpProtectedResourceURL {
		t.Fatalf("resource = %q", payload.Resource)
	}
	if len(payload.AuthorizationServers) != 1 || payload.AuthorizationServers[0] != mcpProtectedResourceAuthorizationServerURL {
		t.Fatalf("authorization_servers = %#v", payload.AuthorizationServers)
	}
	if payload.ScopesSupported == nil || len(payload.ScopesSupported) != 0 {
		t.Fatalf("scopes_supported = %#v", payload.ScopesSupported)
	}
}

func TestMCPAcceptsStaticBearer(t *testing.T) {
	server := newTestMCPServer()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Authorization", "Bearer "+server.token)
	rec := httptest.NewRecorder()

	server.handle(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Mcp-Session-Id"); got == "" {
		t.Fatal("missing Mcp-Session-Id")
	}
}

func TestMCPAcceptsCloudflareAccessHeaders(t *testing.T) {
	server := newTestMCPServer()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Cf-Access-Authenticated-User-Email", "user@example.com")
	req.Header.Set("Cf-Access-Jwt-Assertion", "jwt-assertion")
	rec := httptest.NewRecorder()

	server.handle(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Mcp-Session-Id"); got == "" {
		t.Fatal("missing Mcp-Session-Id")
	}
}

func TestMCPRejectsUnauthorizedRequests(t *testing.T) {
	server := newTestMCPServer()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	rec := httptest.NewRecorder()

	server.handle(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != mcpProtectedResourceWWWAuthenticate {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
}

func newTestMCPServer() *gatewayMCPServer {
	return &gatewayMCPServer{
		token:    "sovereign-token",
		sessions: newMCPSessionStore(),
		tools:    map[string]mcpToolHandler{},
	}
}
