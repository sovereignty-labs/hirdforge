package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	gatewayMCPGiteaBaseURL = "http://gitea-http.gitea.svc.cluster.local:3000"
	gatewayMCPSeidrURL     = "http://seidr.valhalla.svc:8082/query"
)

type jsonRPCRequest struct {
	JSONRPC string                 `json:"jsonrpc"`
	ID      interface{}            `json:"id,omitempty"`
	Method  string                 `json:"method"`
	Params  map[string]interface{} `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      interface{}   `json:"id,omitempty"`
	Result  interface{}   `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpToolHandler struct {
	Name        string
	Description string
	InputSchema map[string]interface{}
	WriteTier   string
	Handler     func(context.Context, map[string]interface{}) (interface{}, error)
}

type mcpSession struct {
	ID         string
	CreatedAt  time.Time
	LastSeenAt time.Time
}

type mcpSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*mcpSession
	seq      uint64
}

type gatewayMCPServer struct {
	token      string
	giteaToken string
	httpClient *http.Client
	gateway    *gateway
	mux        *http.ServeMux
	tools      map[string]mcpToolHandler
	sessions   *mcpSessionStore
}

func newMCPSessionStore() *mcpSessionStore {
	return &mcpSessionStore{sessions: map[string]*mcpSession{}}
}

func (s *mcpSessionStore) newID() string {
	return fmt.Sprintf("mcp-%d-%d", time.Now().Unix(), atomic.AddUint64(&s.seq, 1))
}

func (s *mcpSessionStore) create() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.newID()
	now := time.Now().UTC()
	s.sessions[id] = &mcpSession{
		ID:         id,
		CreatedAt:  now,
		LastSeenAt: now,
	}
	return id
}

func (s *mcpSessionStore) touch(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[strings.TrimSpace(id)]
	if !ok {
		return false
	}
	session.LastSeenAt = time.Now().UTC()
	return true
}

func registerGatewayMCP(mux *http.ServeMux, gw *gateway) {
	server := &gatewayMCPServer{
		token:      strings.TrimSpace(os.Getenv("MCP_SOVEREIGN_TOKEN")),
		giteaToken: strings.TrimSpace(os.Getenv("GITEA_TOKEN")),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		gateway:    gw,
		mux:        mux,
		tools:      map[string]mcpToolHandler{},
		sessions:   newMCPSessionStore(),
	}
	server.registerTools()
	registerGatewayMCPOAuth(mux)
	mux.HandleFunc("/mcp", server.handle)
}

func (s *gatewayMCPServer) registerTools() {
	s.tools["close_issue"] = mcpToolHandler{
		Name:        "close_issue",
		Description: "Close a Gitea issue.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"owner": map[string]interface{}{"type": "string"},
				"repo":  map[string]interface{}{"type": "string"},
				"index": map[string]interface{}{"type": "integer"},
			},
			"required": []string{"owner", "repo", "index"},
		},
		WriteTier: "safe_write",
		Handler: func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			owner, err := strParam(params, "owner", true)
			if err != nil {
				return nil, err
			}
			repo, err := strParam(params, "repo", true)
			if err != nil {
				return nil, err
			}
			index, err := intParam(params, "index", true)
			if err != nil {
				return nil, err
			}
			payload := map[string]string{"state": "closed"}
			path := fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d", url.PathEscape(owner), url.PathEscape(repo), index)
			return s.callGiteaJSON(ctx, http.MethodPatch, path, payload)
		},
	}
	s.tools["comment_on_issue"] = mcpToolHandler{
		Name:        "comment_on_issue",
		Description: "Post a comment on a Gitea issue.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"owner": map[string]interface{}{"type": "string"},
				"repo":  map[string]interface{}{"type": "string"},
				"index": map[string]interface{}{"type": "integer"},
				"body":  map[string]interface{}{"type": "string"},
			},
			"required": []string{"owner", "repo", "index", "body"},
		},
		WriteTier: "safe_write",
		Handler: func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			owner, err := strParam(params, "owner", true)
			if err != nil {
				return nil, err
			}
			repo, err := strParam(params, "repo", true)
			if err != nil {
				return nil, err
			}
			index, err := intParam(params, "index", true)
			if err != nil {
				return nil, err
			}
			body, err := strParam(params, "body", true)
			if err != nil {
				return nil, err
			}
			payload := map[string]string{"body": body}
			path := fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d/comments", url.PathEscape(owner), url.PathEscape(repo), index)
			return s.callGiteaJSON(ctx, http.MethodPost, path, payload)
		},
	}
	s.tools["fleet_state"] = mcpToolHandler{
		Name:        "fleet_state",
		Description: "Return current gateway fleet state for all agents.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		WriteTier: "read",
		Handler: func(ctx context.Context, _ map[string]interface{}) (interface{}, error) {
			return s.invokeLocalJSON(ctx, http.MethodGet, "/api/v1/fleet/state", nil)
		},
	}
	s.tools["list_issues"] = mcpToolHandler{
		Name:        "list_issues",
		Description: "List Gitea issues for a repository.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"owner": map[string]interface{}{"type": "string"},
				"repo":  map[string]interface{}{"type": "string"},
				"state": map[string]interface{}{"type": "string"},
				"labels": map[string]interface{}{
					"type":  "array",
					"items": map[string]interface{}{"type": "string"},
				},
			},
			"required": []string{"owner", "repo"},
		},
		WriteTier: "read",
		Handler: func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			owner, err := strParam(params, "owner", true)
			if err != nil {
				return nil, err
			}
			repo, err := strParam(params, "repo", true)
			if err != nil {
				return nil, err
			}
			query := url.Values{}
			if state, err := strParam(params, "state", false); err != nil {
				return nil, err
			} else if state != "" {
				query.Set("state", state)
			}
			if labels, err := stringSliceParam(params, "labels"); err != nil {
				return nil, err
			} else if len(labels) > 0 {
				query.Set("labels", strings.Join(labels, ","))
			}
			path := fmt.Sprintf("/api/v1/repos/%s/%s/issues", url.PathEscape(owner), url.PathEscape(repo))
			if encoded := query.Encode(); encoded != "" {
				path += "?" + encoded
			}
			return s.callGiteaJSON(ctx, http.MethodGet, path, nil)
		},
	}
	s.tools["get_issue"] = mcpToolHandler{
		Name:        "get_issue",
		Description: "Get a single Gitea issue including its body.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"owner": map[string]interface{}{"type": "string"},
				"repo":  map[string]interface{}{"type": "string"},
				"index": map[string]interface{}{"type": "integer"},
			},
			"required": []string{"owner", "repo", "index"},
		},
		WriteTier: "read",
		Handler: func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			owner, err := strParam(params, "owner", true)
			if err != nil {
				return nil, err
			}
			repo, err := strParam(params, "repo", true)
			if err != nil {
				return nil, err
			}
			index, err := intParam(params, "index", true)
			if err != nil {
				return nil, err
			}
			path := fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d", url.PathEscape(owner), url.PathEscape(repo), index)
			return s.callGiteaJSON(ctx, http.MethodGet, path, nil)
		},
	}
	s.tools["create_issue"] = mcpToolHandler{
		Name:        "create_issue",
		Description: "Create a Gitea issue in a repository.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"owner": map[string]interface{}{"type": "string"},
				"repo":  map[string]interface{}{"type": "string"},
				"title": map[string]interface{}{"type": "string"},
				"body":  map[string]interface{}{"type": "string"},
				"labels": map[string]interface{}{
					"type":  "array",
					"items": map[string]interface{}{"type": "string"},
				},
			},
			"required": []string{"owner", "repo", "title", "body"},
		},
		WriteTier: "safe_write",
		Handler: func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			owner, err := strParam(params, "owner", true)
			if err != nil {
				return nil, err
			}
			repo, err := strParam(params, "repo", true)
			if err != nil {
				return nil, err
			}
			title, err := strParam(params, "title", true)
			if err != nil {
				return nil, err
			}
			body, err := strParam(params, "body", true)
			if err != nil {
				return nil, err
			}
			payload := map[string]interface{}{
				"title": title,
				"body":  body,
			}
			if labels, err := stringSliceParam(params, "labels"); err != nil {
				return nil, err
			} else if len(labels) > 0 {
				labelIDs, err := s.resolveGiteaLabelIDs(ctx, owner, repo, labels)
				if err != nil {
					return nil, err
				}
				if len(labelIDs) > 0 {
					payload["labels"] = labelIDs
				}
			}
			path := fmt.Sprintf("/api/v1/repos/%s/%s/issues", url.PathEscape(owner), url.PathEscape(repo))
			return s.callGiteaJSON(ctx, http.MethodPost, path, payload)
		},
	}
	s.tools["list_prs"] = mcpToolHandler{
		Name:        "list_prs",
		Description: "Return the gateway's current pull request overview.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		WriteTier: "read",
		Handler: func(ctx context.Context, _ map[string]interface{}) (interface{}, error) {
			if s.gateway != nil {
				s.gateway.reposMu.RLock()
				repos := append([]string(nil), s.gateway.repos...)
				s.gateway.reposMu.RUnlock()
				if len(repos) == 0 {
					s.gateway.refreshRepos()
				}
			}
			return s.invokeLocalJSON(ctx, http.MethodGet, "/api/v1/gitea/prs", nil)
		},
	}
	s.tools["cluster_pods"] = mcpToolHandler{
		Name:        "cluster_pods",
		Description: "Return current cluster pod status from the gateway.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		WriteTier: "read",
		Handler: func(ctx context.Context, _ map[string]interface{}) (interface{}, error) {
			return s.invokeLocalJSON(ctx, http.MethodGet, "/api/v1/cluster/pods", nil)
		},
	}
	s.tools["delegate"] = mcpToolHandler{
		Name:        "delegate",
		Description: "Send a message to an agent through the gateway and return the full streamed response.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"agent":      map[string]interface{}{"type": "string"},
				"message":    map[string]interface{}{"type": "string"},
				"session_id": map[string]interface{}{"type": "string"},
			},
			"required": []string{"agent", "message"},
		},
		WriteTier: "safe_write",
		Handler: func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			agent, err := strParam(params, "agent", true)
			if err != nil {
				return nil, err
			}
			message, err := strParam(params, "message", true)
			if err != nil {
				return nil, err
			}
			sessionID, err := strParam(params, "session_id", false)
			if err != nil {
				return nil, err
			}
			ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
			defer cancel()
			return s.delegateViaGateway(ctx, agent, message, sessionID)
		},
	}
	s.tools["recall"] = mcpToolHandler{
		Name:        "recall",
		Description: "Query Seidr memory recall for an agent.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"agent_name": map[string]interface{}{"type": "string"},
				"query":      map[string]interface{}{"type": "string"},
				"n_results":  map[string]interface{}{"type": "integer"},
			},
			"required": []string{"agent_name", "query"},
		},
		WriteTier: "read",
		Handler: func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			agentName, err := strParam(params, "agent_name", true)
			if err != nil {
				return nil, err
			}
			query, err := strParam(params, "query", true)
			if err != nil {
				return nil, err
			}
			nResults, err := intParam(params, "n_results", false)
			if err != nil {
				return nil, err
			}
			if nResults == 0 {
				nResults = 5
			}
			payload := map[string]interface{}{
				"agent_name": agentName,
				"query":      query,
				"n_results":  nResults,
			}
			return s.postJSON(ctx, gatewayMCPSeidrURL, payload, nil)
		},
	}
	s.tools["gitea_file_read"] = mcpToolHandler{
		Name:        "gitea_file_read",
		Description: "Read a file from Gitea and return the decoded text content.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"owner":    map[string]interface{}{"type": "string"},
				"repo":     map[string]interface{}{"type": "string"},
				"filepath": map[string]interface{}{"type": "string"},
			},
			"required": []string{"owner", "repo", "filepath"},
		},
		WriteTier: "read",
		Handler: func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			owner, err := strParam(params, "owner", true)
			if err != nil {
				return nil, err
			}
			repo, err := strParam(params, "repo", true)
			if err != nil {
				return nil, err
			}
			filepath, err := strParam(params, "filepath", true)
			if err != nil {
				return nil, err
			}
			path := fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(filepath))
			var resp giteaContentResponse
			if err := s.callGiteaInto(ctx, http.MethodGet, path, nil, &resp); err != nil {
				return nil, err
			}
			if resp.Encoding != "base64" {
				return nil, fmt.Errorf("unsupported content encoding: %s", resp.Encoding)
			}
			decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(resp.Content, "\n", ""))
			if err != nil {
				return nil, fmt.Errorf("decode content: %w", err)
			}
			return map[string]interface{}{
				"path":    resp.Path,
				"sha":     resp.SHA,
				"content": string(decoded),
			}, nil
		},
	}
	s.tools["update_issue"] = mcpToolHandler{
		Name:        "update_issue",
		Description: "Update labels on a Gitea issue. Replaces all existing labels with the provided list.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"owner": map[string]interface{}{"type": "string"},
				"repo":  map[string]interface{}{"type": "string"},
				"index": map[string]interface{}{"type": "integer"},
				"labels": map[string]interface{}{
					"type":  "array",
					"items": map[string]interface{}{"type": "string"},
				},
			},
			"required": []string{"owner", "repo", "index", "labels"},
		},
		WriteTier: "safe_write",
		Handler: func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			owner, err := strParam(params, "owner", true)
			if err != nil {
				return nil, err
			}
			repo, err := strParam(params, "repo", true)
			if err != nil {
				return nil, err
			}
			index, err := intParam(params, "index", true)
			if err != nil {
				return nil, err
			}
			labels, err := stringSliceParam(params, "labels")
			if err != nil {
				return nil, err
			}
			labelIDs, err := s.resolveGiteaLabelIDs(ctx, owner, repo, labels)
			if err != nil {
				return nil, err
			}
			payload := map[string]interface{}{"labels": labelIDs}
			path := fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d/labels", url.PathEscape(owner), url.PathEscape(repo), index)
			return s.callGiteaJSON(ctx, http.MethodPut, path, payload)
		},
	}
	s.tools["get_delegate_result"] = mcpToolHandler{
		Name:        "get_delegate_result",
		Description: "Retrieve the response from a previously delegated agent task by session_id.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"session_id": map[string]interface{}{"type": "string"},
			},
			"required": []string{"session_id"},
		},
		WriteTier: "read",
		Handler: func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			sessionID, err := strParam(params, "session_id", true)
			if err != nil {
				return nil, err
			}
			if raw, ok := s.gateway.delegateResults.Load(sessionID); ok {
				return raw, nil
			}
			return map[string]interface{}{
				"session_id": sessionID,
				"done":       false,
				"content":    "",
			}, nil
		},
	}
}

func (s *gatewayMCPServer) handle(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(s.token) == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !validBearerToken(r.Header.Get("Authorization"), s.token) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req jsonRPCRequest
	if err := decodeJSONStrict(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      nil,
			Error:   &jsonRPCError{Code: -32700, Message: "parse error: " + err.Error()},
		})
		return
	}
	if strings.TrimSpace(req.JSONRPC) == "" {
		req.JSONRPC = "2.0"
	}

	sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
	if req.Method == "initialize" {
		sessionID = s.sessions.create()
		w.Header().Set("Mcp-Session-Id", sessionID)
		writeJSON(w, http.StatusOK, jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"protocolVersion": "2025-03-26",
				"serverInfo": map[string]interface{}{
					"name":    "valhalla-gateway",
					"version": "0.1.0",
				},
				"capabilities": map[string]interface{}{
					"tools": map[string]interface{}{},
				},
			},
		})
		return
	}
	if !s.sessions.touch(sessionID) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Mcp-Session-Id", sessionID)
	if req.Method == "notifications/initialized" || req.ID == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	resp := jsonRPCResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "tools/list":
		names := make([]string, 0, len(s.tools))
		for name := range s.tools {
			names = append(names, name)
		}
		sort.Strings(names)
		tools := make([]map[string]interface{}, 0, len(names))
		for _, name := range names {
			tool := s.tools[name]
			tools = append(tools, map[string]interface{}{
				"name":        tool.Name,
				"description": tool.Description,
				"inputSchema": tool.InputSchema,
			})
		}
		resp.Result = map[string]interface{}{"tools": tools}
	case "tools/call":
		toolName, _ := req.Params["name"].(string)
		toolName = strings.TrimSpace(toolName)
		if toolName == "" {
			resp.Error = &jsonRPCError{Code: -32602, Message: "missing tool name"}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		args, _ := req.Params["arguments"].(map[string]interface{})
		if args == nil {
			args = map[string]interface{}{}
		}
		tool, ok := s.tools[toolName]
		if !ok {
			resp.Error = &jsonRPCError{Code: -32601, Message: "unknown tool: " + toolName}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		logJSON("info", "mcp tool call", map[string]interface{}{
			"tool":           toolName,
			"caller_session": sessionID,
			"params":         redactToolParams(toolName, args),
		})
		out, err := tool.Handler(r.Context(), cloneParams(args))
		if err != nil {
			logJSON("error", "mcp tool error", map[string]interface{}{
				"tool":           toolName,
				"caller_session": sessionID,
				"params":         redactToolParams(toolName, args),
				"error":          err.Error(),
			})
			resp.Result = map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": err.Error()},
				},
				"isError": true,
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		resp.Result = map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": toJSONString(out)},
			},
			"isError": false,
		}
	default:
		resp.Error = &jsonRPCError{Code: -32601, Message: "method not found: " + req.Method}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *gatewayMCPServer) invokeLocalJSON(ctx context.Context, method, path string, payload interface{}) (interface{}, error) {
	status, body, contentType, err := s.invokeLocal(ctx, method, path, payload)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("local handler %s returned %d: %s", path, status, strings.TrimSpace(string(body)))
	}
	if !strings.Contains(strings.ToLower(contentType), "json") {
		return nil, fmt.Errorf("local handler %s returned non-JSON content type %q", path, contentType)
	}
	var out interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", path, err)
	}
	return out, nil
}

func (s *gatewayMCPServer) invokeLocal(ctx context.Context, method, path string, payload interface{}) (int, []byte, string, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, "", err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://gateway.local"+path, body)
	if err != nil {
		return 0, nil, "", err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	s.mux.ServeHTTP(recorder, req)
	return recorder.Code, recorder.Body.Bytes(), recorder.Header().Get("Content-Type"), nil
}

func (s *gatewayMCPServer) delegateViaGateway(ctx context.Context, agent, message, sessionID string) (interface{}, error) {
	out, err := s.invokeLocalJSON(ctx, http.MethodPost, "/api/v1/message?async=true", map[string]string{
		"agent":      agent,
		"content":    message,
		"session_id": sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("delegate failed: %w", err)
	}
	return out, nil
}

func (s *gatewayMCPServer) callGiteaJSON(ctx context.Context, method, path string, payload interface{}) (interface{}, error) {
	var out interface{}
	if err := s.callGiteaInto(ctx, method, path, payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *gatewayMCPServer) resolveGiteaLabelIDs(ctx context.Context, owner, repo string, labelNames []string) ([]int, error) {
	if len(labelNames) == 0 {
		return nil, nil
	}
	path := fmt.Sprintf("/api/v1/repos/%s/%s/labels", url.PathEscape(owner), url.PathEscape(repo))
	var labels []map[string]interface{}
	if err := s.callGiteaInto(ctx, http.MethodGet, path, nil, &labels); err != nil {
		return nil, fmt.Errorf("fetch labels: %w", err)
	}
	nameToID := map[string]int{}
	for _, l := range labels {
		name, _ := l["name"].(string)
		if name == "" {
			continue
		}
		idf, _ := l["id"].(float64)
		nameToID[name] = int(idf)
	}
	var ids []int
	var missing []string
	for _, name := range labelNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		id, ok := nameToID[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		ids = append(ids, id)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("labels not found: %s", strings.Join(missing, ", "))
	}
	return ids, nil
}

func (s *gatewayMCPServer) callGiteaInto(ctx context.Context, method, path string, payload interface{}, out interface{}) error {
	if strings.TrimSpace(s.giteaToken) == "" {
		return fmt.Errorf("GITEA_TOKEN is not configured")
	}
	fullURL := strings.TrimRight(gatewayMCPGiteaBaseURL, "/") + path
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+s.giteaToken)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gitea status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode gitea response: %w", err)
	}
	return nil
}

func (s *gatewayMCPServer) postJSON(ctx context.Context, endpoint string, payload interface{}, out interface{}) (interface{}, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		var decoded interface{}
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		return decoded, nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

func validBearerToken(headerValue, expected string) bool {
	headerValue = strings.TrimSpace(headerValue)
	if headerValue == "" || !strings.HasPrefix(strings.ToLower(headerValue), "bearer ") {
		return false
	}
	return strings.TrimSpace(headerValue[len("Bearer "):]) == expected
}

func decodeJSONStrict(r *http.Request, dst interface{}) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("unexpected trailing data")
	}
	return nil
}

func cloneParams(in map[string]interface{}) map[string]interface{} {
	if in == nil {
		return map[string]interface{}{}
	}
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func toJSONString(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func strParam(params map[string]interface{}, key string, required bool) (string, error) {
	raw, ok := params[key]
	if !ok {
		if required {
			return "", fmt.Errorf("missing %s", key)
		}
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("%s cannot be empty", key)
	}
	return value, nil
}

func intParam(params map[string]interface{}, key string, required bool) (int, error) {
	raw, ok := params[key]
	if !ok {
		if required {
			return 0, fmt.Errorf("missing %s", key)
		}
		return 0, nil
	}
	switch v := raw.(type) {
	case float64:
		return int(v), nil
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
}

func stringSliceParam(params map[string]interface{}, key string) ([]string, error) {
	raw, ok := params[key]
	if !ok || raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s must contain only strings", key)
			}
			str = strings.TrimSpace(str)
			if str != "" {
				out = append(out, str)
			}
		}
		return out, nil
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		return out, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, nil
		}
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
}

func redactToolParams(toolName string, params map[string]interface{}) map[string]interface{} {
	redacted := cloneParams(params)
	for key := range redacted {
		lower := strings.ToLower(strings.TrimSpace(key))
		switch lower {
		case "message", "body", "query", "content", "token", "authorization":
			redacted[key] = "[redacted]"
		}
	}
	if strings.EqualFold(toolName, "delegate") {
		if _, ok := redacted["message"]; ok {
			redacted["message"] = "[redacted]"
		}
	}
	return redacted
}

func logJSON(level, msg string, fields map[string]interface{}) {
	entry := map[string]interface{}{
		"ts":    time.Now().UTC().Format(time.RFC3339),
		"level": level,
		"msg":   msg,
	}
	for k, v := range fields {
		entry[k] = v
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	b = append(b, '\n')
	_, _ = os.Stdout.Write(b)
}
