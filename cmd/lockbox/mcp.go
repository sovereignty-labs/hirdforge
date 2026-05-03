package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

type upstreamTool struct {
	Name        string
	Description string
	InputSchema map[string]interface{}
}

type upstreamCallError struct {
	statusCode int
	body       string
	rpcCode    int
	rpcMessage string
	err        error
}

func (e *upstreamCallError) Error() string {
	switch {
	case e.rpcCode != 0:
		return fmt.Sprintf("upstream MCP error %d: %s", e.rpcCode, e.rpcMessage)
	case e.statusCode != 0:
		return fmt.Sprintf("upstream status %d: %s", e.statusCode, strings.TrimSpace(e.body))
	case e.err != nil:
		return e.err.Error()
	default:
		return "upstream call failed"
	}
}

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

func classifyUpstreamWriteTier(toolName string) string {
	name := strings.ToLower(strings.TrimSpace(toolName))
	switch {
	case strings.HasPrefix(name, "search_"),
		strings.HasPrefix(name, "get_"),
		strings.HasPrefix(name, "list_"),
		strings.HasPrefix(name, "check_"):
		return "read"
	case strings.HasPrefix(name, "manage_"),
		strings.HasPrefix(name, "modify_"),
		strings.HasPrefix(name, "batch_modify_"),
		strings.HasPrefix(name, "copy_"),
		strings.HasPrefix(name, "import_"):
		return "safe_write"
	case strings.HasPrefix(name, "send_"),
		strings.HasPrefix(name, "create_"),
		strings.HasPrefix(name, "update_"),
		strings.HasPrefix(name, "delete_"),
		strings.HasPrefix(name, "set_"),
		strings.HasPrefix(name, "draft_"):
		return "destructive_write"
	default:
		return "destructive_write"
	}
}

func parseSSEJSONRPCResponse(body io.Reader) (jsonRPCResponse, error) {
	var out jsonRPCResponse
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var dataLines []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	if len(dataLines) == 0 {
		return out, fmt.Errorf("upstream response missing SSE data line")
	}
	payload := strings.Join(dataLines, "\n")
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		return out, fmt.Errorf("decode SSE JSON-RPC payload: %w", err)
	}
	return out, nil
}

func callUpstreamRPC(url, sessionID string, reqBody jsonRPCRequest, includeSession bool) (jsonRPCResponse, http.Header, error) {
	var out jsonRPCResponse
	body, err := json.Marshal(reqBody)
	if err != nil {
		return out, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimSpace(url), strings.NewReader(string(body)))
	if err != nil {
		return out, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if includeSession {
		if strings.TrimSpace(sessionID) == "" {
			return out, nil, fmt.Errorf("missing upstream MCP session id")
		}
		req.Header.Set("Mcp-Session-Id", strings.TrimSpace(sessionID))
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return out, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return out, resp.Header, &upstreamCallError{
			statusCode: resp.StatusCode,
			body:       string(b),
		}
	}
	buf, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return out, resp.Header, err
	}
	out, err = parseSSEJSONRPCResponse(bytes.NewReader(buf))
	if err != nil {
		return out, resp.Header, &upstreamCallError{err: fmt.Errorf("%w: %s", err, strings.TrimSpace(string(buf)))}
	}
	return out, resp.Header, nil
}

func hasStaleSessionText(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return false
	}
	if !strings.Contains(text, "session") {
		return false
	}
	return strings.Contains(text, "not found") || strings.Contains(text, "expired") || strings.Contains(text, "invalid")
}

func isLikelyStaleSessionError(err error) bool {
	if err == nil {
		return false
	}
	var ue *upstreamCallError
	if errors.As(err, &ue) {
		if ue.statusCode == http.StatusBadRequest || ue.statusCode == http.StatusNotFound || ue.statusCode == http.StatusGone {
			return true
		}
		if ue.rpcCode == -32000 {
			return true
		}
		if hasStaleSessionText(ue.body) || hasStaleSessionText(ue.rpcMessage) {
			return true
		}
	}
	return hasStaleSessionText(err.Error())
}

func (s *lockboxState) refreshUpstreamSessionIfNeeded(previousSession string) error {
	s.upstreamInitMu.Lock()
	defer s.upstreamInitMu.Unlock()

	s.mu.RLock()
	current := strings.TrimSpace(s.upstreamSessionID)
	url := strings.TrimSpace(s.upstreamMCPURL)
	s.mu.RUnlock()
	if url == "" {
		return fmt.Errorf("upstream MCP not configured")
	}
	if current != "" && current != strings.TrimSpace(previousSession) {
		return nil
	}
	log.Printf("upstream session stale, re-initializing")
	return s.discoverUpstreamTools()
}

func (s *lockboxState) discoverUpstreamTools() error {
	s.mu.RLock()
	url := strings.TrimSpace(s.upstreamMCPURL)
	s.mu.RUnlock()
	if url == "" {
		return nil
	}
	initReq := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]interface{}{
				"name":    "lockbox",
				"version": "1.0",
			},
		},
	}
	_, headers, err := callUpstreamRPC(url, "", initReq, false)
	if err != nil {
		return err
	}
	sessionID := strings.TrimSpace(headers.Get("Mcp-Session-Id"))
	if sessionID == "" {
		return fmt.Errorf("upstream initialize missing Mcp-Session-Id header")
	}
	listResp, _, err := callUpstreamRPC(url, sessionID, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
		Params:  map[string]interface{}{},
	}, true)
	if err != nil {
		return err
	}
	if listResp.Error != nil {
		return fmt.Errorf("upstream tools/list error %d: %s", listResp.Error.Code, listResp.Error.Message)
	}
	resultMap, ok := listResp.Result.(map[string]interface{})
	if !ok {
		return fmt.Errorf("upstream tools/list result format invalid")
	}
	rawTools, ok := resultMap["tools"].([]interface{})
	if !ok {
		return fmt.Errorf("upstream tools/list missing tools array")
	}
	discovered := map[string]upstreamTool{}
	for _, raw := range rawTools {
		tm, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		name := strings.TrimSpace(fmt.Sprint(tm["name"]))
		if name == "" {
			continue
		}
		description := strings.TrimSpace(fmt.Sprint(tm["description"]))
		var inputSchema map[string]interface{}
		if m, ok := tm["inputSchema"].(map[string]interface{}); ok {
			inputSchema = m
		}
		discovered[name] = upstreamTool{
			Name:        name,
			Description: description,
			InputSchema: inputSchema,
		}
	}
	s.mu.Lock()
	s.upstreamSessionID = sessionID
	s.upstreamTools = discovered
	s.mu.Unlock()
	return nil
}

func (s *lockboxState) proxyUpstreamToolCall(toolName string, args map[string]interface{}) (interface{}, error) {
	s.mu.RLock()
	url := strings.TrimSpace(s.upstreamMCPURL)
	sessionID := strings.TrimSpace(s.upstreamSessionID)
	googleUserEmail := strings.TrimSpace(s.googleUserEmail)
	s.mu.RUnlock()
	if url == "" {
		return nil, fmt.Errorf("upstream MCP not configured")
	}
	if sessionID == "" {
		return nil, fmt.Errorf("upstream MCP session is not initialized")
	}
	if googleUserEmail != "" {
		if _, exists := args["user_google_email"]; !exists {
			args["user_google_email"] = googleUserEmail
		}
	}
	callOnce := func(session string) (interface{}, error) {
		id := atomic.AddUint64(&s.upstreamRPCSeq, 1)
		resp, _, err := callUpstreamRPC(url, session, jsonRPCRequest{
			JSONRPC: "2.0",
			ID:      id,
			Method:  "tools/call",
			Params: map[string]interface{}{
				"name":      toolName,
				"arguments": cloneParams(args),
			},
		}, true)
		if err != nil {
			return nil, err
		}
		if resp.Error != nil {
			return nil, &upstreamCallError{
				statusCode: http.StatusOK,
				rpcCode:    resp.Error.Code,
				rpcMessage: resp.Error.Message,
			}
		}
		return resp.Result, nil
	}
	out, err := callOnce(sessionID)
	if err == nil {
		return out, nil
	}
	if !isLikelyStaleSessionError(err) {
		return nil, err
	}
	if refreshErr := s.refreshUpstreamSessionIfNeeded(sessionID); refreshErr != nil {
		return nil, refreshErr
	}
	s.mu.RLock()
	newSessionID := strings.TrimSpace(s.upstreamSessionID)
	s.mu.RUnlock()
	out, retryErr := callOnce(newSessionID)
	if retryErr != nil {
		return nil, retryErr
	}
	return out, nil
}

func (s *lockboxState) mcp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	if req.Method == "notifications/initialized" || req.ID == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"serverInfo": map[string]interface{}{
				"name":    "valhalla-lockbox",
				"version": "0.1.0",
			},
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
		}
	case "tools/list":
		s.mu.RLock()
		names := make([]string, 0, len(s.tools))
		for name := range s.tools {
			names = append(names, name)
		}
		sort.Strings(names)
		tools := make([]map[string]interface{}, 0, len(names)+len(s.upstreamTools))
		for _, name := range names {
			t := s.tools[name]
			tools = append(tools, map[string]interface{}{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": t.InputSchema,
			})
		}
		upstreamNames := make([]string, 0, len(s.upstreamTools))
		for name := range s.upstreamTools {
			upstreamNames = append(upstreamNames, name)
		}
		sort.Strings(upstreamNames)
		for _, name := range upstreamNames {
			if _, exists := s.tools[name]; exists {
				continue
			}
			t := s.upstreamTools[name]
			tools = append(tools, map[string]interface{}{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": t.InputSchema,
			})
		}
		s.mu.RUnlock()
		resp.Result = map[string]interface{}{"tools": tools}
	case "tools/call":
		toolName, _ := req.Params["name"].(string)
		toolName = strings.TrimSpace(toolName)
		if toolName == "" {
			resp.Error = &jsonRPCError{Code: -32602, Message: "missing tool name"}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		args, ok := req.Params["arguments"].(map[string]interface{})
		if !ok || args == nil {
			args = map[string]interface{}{}
		}
		s.mu.RLock()
		googleUserEmail := strings.TrimSpace(s.googleUserEmail)
		s.mu.RUnlock()
		if googleUserEmail != "" {
			if _, exists := args["user_google_email"]; !exists {
				args["user_google_email"] = googleUserEmail
			}
		}
		trigger := extractTrigger(args)
		agentName := extractAgentName(args)
		s.mu.RLock()
		tool, nativeTool := s.tools[toolName]
		_, upstreamTool := s.upstreamTools[toolName]
		s.mu.RUnlock()
		if !nativeTool && !upstreamTool {
			resp.Error = &jsonRPCError{Code: -32601, Message: "unknown tool: " + toolName}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		if upstreamTool && !nativeTool {
			writeTier := classifyUpstreamWriteTier(toolName)
			var (
				out interface{}
				err error
			)
			switch writeTier {
			case "read":
				out, err = s.proxyUpstreamToolCall(toolName, args)
			case "safe_write":
				out, err = s.proxyUpstreamToolCall(toolName, args)
				if err == nil {
					s.mu.Lock()
					s.addAudit(AuditEntry{
						Timestamp: time.Now().UTC(),
						HuntID:    "mcp-session",
						Service:   "mcp",
						Action:    toolName,
						Params:    cloneParams(args),
						Status:    "executed_safe_write",
					}, "")
					s.mu.Unlock()
				}
			case "destructive_write":
				now := time.Now().UTC()
				qid := fmt.Sprintf("q-%06d", atomic.AddUint64(&s.queueSeq, 1))
				s.mu.Lock()
				s.queues[qid] = &WriteQueue{
					QueueID:   qid,
					HuntID:    "mcp",
					Service:   "mcp",
					Action:    toolName,
					AgentName: agentName,
					Params:    cloneParams(args),
					Status:    "pending",
					Trigger:   cloneTrigger(trigger),
					QueuedAt:  now,
				}
				s.addAudit(AuditEntry{
					Timestamp: now,
					HuntID:    "mcp",
					Service:   "mcp",
					Action:    toolName,
					Params:    cloneParams(args),
					Status:    "queued",
				}, qid)
				s.mu.Unlock()
				resp.Result = map[string]interface{}{
					"content": []map[string]interface{}{
						{
							"type": "text",
							"text": toJSONString(map[string]interface{}{
								"status":   "queued_for_approval",
								"queue_id": qid,
								"message":  "Destructive action queued for Sovereign approval",
							}),
						},
					},
					"isError": false,
				}
				writeJSON(w, http.StatusOK, resp)
				return
			default:
				resp.Error = &jsonRPCError{Code: -32602, Message: "invalid write tier: " + writeTier}
				writeJSON(w, http.StatusOK, resp)
				return
			}
			if err != nil {
				resp.Error = &jsonRPCError{Code: -32000, Message: err.Error()}
				writeJSON(w, http.StatusOK, resp)
				return
			}
			resp.Result = out
			writeJSON(w, http.StatusOK, resp)
			return
		}
		writeTier := strings.TrimSpace(tool.WriteTier)
		if writeTier == "" {
			writeTier = "read"
		}
		var (
			out interface{}
			err error
		)
		switch writeTier {
		case "read":
			out, err = tool.Handler(args)
		case "safe_write":
			out, err = tool.Handler(args)
			if err == nil {
				s.mu.Lock()
				s.addAudit(AuditEntry{
					Timestamp: time.Now().UTC(),
					HuntID:    "mcp-session",
					Service:   "mcp",
					Action:    toolName,
					Params:    cloneParams(args),
					Status:    "executed_safe_write",
				}, "")
				s.mu.Unlock()
			}
		case "destructive_write":
			now := time.Now().UTC()
			qid := fmt.Sprintf("q-%06d", atomic.AddUint64(&s.queueSeq, 1))
			s.mu.Lock()
			s.queues[qid] = &WriteQueue{
				QueueID:   qid,
				HuntID:    "mcp",
				Service:   "mcp",
				Action:    toolName,
				AgentName: agentName,
				Params:    cloneParams(args),
				Status:    "pending",
				Trigger:   cloneTrigger(trigger),
				QueuedAt:  now,
			}
			s.addAudit(AuditEntry{
				Timestamp: now,
				HuntID:    "mcp",
				Service:   "mcp",
				Action:    toolName,
				Params:    cloneParams(args),
				Status:    "queued",
			}, qid)
			s.mu.Unlock()
			resp.Result = map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": toJSONString(map[string]interface{}{
							"status":   "queued_for_approval",
							"queue_id": qid,
							"message":  "Destructive action queued for Sovereign approval",
						}),
					},
				},
				"isError": false,
			}
			writeJSON(w, http.StatusOK, resp)
			return
		default:
			resp.Error = &jsonRPCError{Code: -32602, Message: "invalid write tier: " + writeTier}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		if err != nil {
			resp.Result = map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": err.Error()},
				},
				"isError": true,
			}
		} else {
			text := toJSONString(out)
			resp.Result = map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": text},
				},
				"isError": false,
			}
		}
	default:
		resp.Error = &jsonRPCError{Code: -32601, Message: "method not found: " + req.Method}
	}
	writeJSON(w, http.StatusOK, resp)
}
