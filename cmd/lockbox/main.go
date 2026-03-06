package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	calendarv3 "google.golang.org/api/calendar/v3"
	gmailv1 "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

type HuntScope struct {
	HuntID       string
	Permissions  map[string]map[string]struct{} // service -> allowed actions
	MaxRequests  int
	UsedRequests int
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

type AuditEntry struct {
	Timestamp time.Time              `json:"timestamp"`
	HuntID    string                 `json:"hunt_id"`
	Service   string                 `json:"service"`
	Action    string                 `json:"action"`
	Params    map[string]interface{} `json:"params,omitempty"`
	Status    string                 `json:"status"`
	Error     string                 `json:"error,omitempty"`
}

type WriteQueue struct {
	QueueID  string                 `json:"queue_id"`
	HuntID   string                 `json:"hunt_id"`
	Service  string                 `json:"service"`
	Action   string                 `json:"action"`
	Params   map[string]interface{} `json:"params"`
	Status   string                 `json:"status"`
	Error    string                 `json:"error,omitempty"`
	QueuedAt time.Time              `json:"queued_at"`
}

type ServiceCredential struct {
	Type  string            `json:"type"`
	Token string            `json:"token"`
	Extra map[string]string `json:"extra"`
}

type ServiceHandler interface {
	Name() string
	SupportedActions() []string
	Execute(action string, params map[string]interface{}, cred ServiceCredential) (interface{}, error)
	IsWriteAction(action string) bool
}

type MockService struct{}

func (m *MockService) Name() string { return "mock" }
func (m *MockService) SupportedActions() []string {
	return []string{"read_data", "write_data"}
}
func (m *MockService) Execute(action string, params map[string]interface{}, cred ServiceCredential) (interface{}, error) {
	return map[string]interface{}{
		"action":    action,
		"params":    params,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"mock":      true,
	}, nil
}
func (m *MockService) IsWriteAction(action string) bool {
	return action == "write_data"
}

type lockboxState struct {
	mu       sync.RWMutex
	hunts    map[string]*HuntScope
	audits   map[string][]AuditEntry
	queues   map[string]*WriteQueue
	services map[string]ServiceHandler
	tools    map[string]ToolHandler
	queueSeq uint64
}

type ToolHandler struct {
	Name        string
	Description string
	InputSchema map[string]interface{}
	WriteTier   string
	Handler     func(params map[string]interface{}) (interface{}, error)
}

type oauthTokenFile struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	Expiry       string `json:"expiry,omitempty"`
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

type googleClients struct {
	gmail    *gmailv1.Service
	calendar *calendarv3.Service
}

type permissionSpec struct {
	Service string   `json:"service"`
	Actions []string `json:"actions"`
}

type registerReq struct {
	HuntID      string           `json:"hunt_id"`
	Permissions []permissionSpec `json:"permissions"`
	MaxRequests int              `json:"max_requests"`
	TTLSeconds  int              `json:"ttl_seconds"`
}

type actionReq struct {
	HuntID  string                 `json:"hunt_id"`
	Service string                 `json:"service"`
	Action  string                 `json:"action"`
	Params  map[string]interface{} `json:"params"`
}

type approveWriteReq struct {
	HuntID   string `json:"hunt_id"`
	QueueID  string `json:"queue_id"`
	Approved *bool  `json:"approved"`
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
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

func actionSupported(svc ServiceHandler, action string) bool {
	for _, a := range svc.SupportedActions() {
		if a == action {
			return true
		}
	}
	return false
}

func (s *lockboxState) addAudit(entry AuditEntry, queueID string) {
	s.audits[entry.HuntID] = append(s.audits[entry.HuntID], entry)
	logFields := map[string]interface{}{
		"ts":      entry.Timestamp.UTC().Format(time.RFC3339),
		"hunt_id": entry.HuntID,
		"service": entry.Service,
		"action":  entry.Action,
		"status":  entry.Status,
	}
	if entry.Error != "" {
		logFields["error"] = entry.Error
	}
	if queueID != "" {
		logFields["queue_id"] = queueID
	}
	b, err := json.Marshal(logFields)
	if err == nil {
		b = append(b, '\n')
		_, _ = os.Stdout.Write(b)
	}
}

func permissionsToMap(specs []permissionSpec) (map[string]map[string]struct{}, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("permissions must not be empty")
	}
	out := make(map[string]map[string]struct{}, len(specs))
	for _, p := range specs {
		service := strings.TrimSpace(p.Service)
		if service == "" {
			return nil, fmt.Errorf("permission service must not be empty")
		}
		if len(p.Actions) == 0 {
			return nil, fmt.Errorf("permission actions must not be empty for service %q", service)
		}
		if _, ok := out[service]; !ok {
			out[service] = map[string]struct{}{}
		}
		for _, a := range p.Actions {
			action := strings.TrimSpace(a)
			if action == "" {
				return nil, fmt.Errorf("permission action must not be empty for service %q", service)
			}
			out[service][action] = struct{}{}
		}
	}
	return out, nil
}

func (s *lockboxState) pruneExpired() {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for huntID, scope := range s.hunts {
		if now.After(scope.ExpiresAt) {
			delete(s.hunts, huntID)
			delete(s.audits, huntID)
			for qid, q := range s.queues {
				if q.HuntID == huntID {
					delete(s.queues, qid)
				}
			}
		}
	}
}

func (s *lockboxState) registerHunt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req registerReq
	if err := decodeJSONStrict(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"status": "error", "error": "invalid JSON: " + err.Error()})
		return
	}
	req.HuntID = strings.TrimSpace(req.HuntID)
	if req.HuntID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"status": "error", "error": "hunt_id is required"})
		return
	}
	perms, err := permissionsToMap(req.Permissions)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"status": "error", "error": err.Error()})
		return
	}
	if req.MaxRequests <= 0 {
		req.MaxRequests = 20
	}
	if req.TTLSeconds <= 0 {
		req.TTLSeconds = 300
	}
	now := time.Now().UTC()
	scope := &HuntScope{
		HuntID:       req.HuntID,
		Permissions:  perms,
		MaxRequests:  req.MaxRequests,
		UsedRequests: 0,
		ExpiresAt:    now.Add(time.Duration(req.TTLSeconds) * time.Second),
		CreatedAt:    now,
	}
	s.mu.Lock()
	s.hunts[req.HuntID] = scope
	delete(s.audits, req.HuntID)
	for qid, q := range s.queues {
		if q.HuntID == req.HuntID {
			delete(s.queues, qid)
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":     "registered",
		"hunt_id":    req.HuntID,
		"expires_at": scope.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func (s *lockboxState) action(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req actionReq
	if err := decodeJSONStrict(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"status": "error", "error": "invalid JSON: " + err.Error()})
		return
	}
	req.HuntID = strings.TrimSpace(req.HuntID)
	req.Service = strings.TrimSpace(req.Service)
	req.Action = strings.TrimSpace(req.Action)
	if req.HuntID == "" || req.Service == "" || req.Action == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"status": "error", "error": "hunt_id, service, and action are required"})
		return
	}
	req.Params = cloneParams(req.Params)

	now := time.Now().UTC()

	s.mu.Lock()
	scope, ok := s.hunts[req.HuntID]
	if !ok {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"status": "error", "error": "hunt scope not found"})
		return
	}
	if now.After(scope.ExpiresAt) {
		delete(s.hunts, req.HuntID)
		delete(s.audits, req.HuntID)
		for qid, q := range s.queues {
			if q.HuntID == req.HuntID {
				delete(s.queues, qid)
			}
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusGone, map[string]interface{}{"status": "expired", "error": req.HuntID + " scope has expired"})
		return
	}
	if scope.UsedRequests >= scope.MaxRequests {
		s.mu.Unlock()
		writeJSON(w, http.StatusTooManyRequests, map[string]interface{}{"status": "denied", "error": "request cap exceeded"})
		return
	}
	servicePerms, ok := scope.Permissions[req.Service]
	if !ok {
		s.mu.Unlock()
		writeJSON(w, http.StatusForbidden, map[string]interface{}{"status": "denied", "error": fmt.Sprintf("service %q not permitted for %s", req.Service, req.HuntID)})
		return
	}
	if _, ok := servicePerms[req.Action]; !ok {
		s.mu.Unlock()
		writeJSON(w, http.StatusForbidden, map[string]interface{}{"status": "denied", "error": fmt.Sprintf("action %q not permitted for %s", req.Action, req.HuntID)})
		return
	}
	svc, ok := s.services[req.Service]
	if !ok || !actionSupported(svc, req.Action) {
		s.mu.Unlock()
		writeJSON(w, http.StatusForbidden, map[string]interface{}{"status": "denied", "error": fmt.Sprintf("action %q not permitted for %s", req.Action, req.HuntID)})
		return
	}

	if svc.IsWriteAction(req.Action) {
		scope.UsedRequests++
		remaining := scope.MaxRequests - scope.UsedRequests
		qid := fmt.Sprintf("q-%06d", atomic.AddUint64(&s.queueSeq, 1))
		queue := &WriteQueue{
			QueueID:  qid,
			HuntID:   req.HuntID,
			Service:  req.Service,
			Action:   req.Action,
			Params:   cloneParams(req.Params),
			Status:   "pending",
			QueuedAt: now,
		}
		s.queues[qid] = queue
		s.addAudit(AuditEntry{
			Timestamp: now,
			HuntID:    req.HuntID,
			Service:   req.Service,
			Action:    req.Action,
			Params:    cloneParams(req.Params),
			Status:    "queued",
		}, qid)
		s.mu.Unlock()
		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"status":             "queued",
			"queue_id":           qid,
			"requests_remaining": remaining,
		})
		return
	}

	scope.UsedRequests++
	remaining := scope.MaxRequests - scope.UsedRequests
	s.mu.Unlock()

	data, err := svc.Execute(req.Action, req.Params, ServiceCredential{})
	if err != nil {
		s.mu.Lock()
		s.addAudit(AuditEntry{
			Timestamp: now,
			HuntID:    req.HuntID,
			Service:   req.Service,
			Action:    req.Action,
			Params:    cloneParams(req.Params),
			Status:    "error",
			Error:     err.Error(),
		}, "")
		s.mu.Unlock()
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"status": "error", "error": err.Error()})
		return
	}

	s.mu.Lock()
	s.addAudit(AuditEntry{
		Timestamp: now,
		HuntID:    req.HuntID,
		Service:   req.Service,
		Action:    req.Action,
		Params:    cloneParams(req.Params),
		Status:    "ok",
	}, "")
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":             "ok",
		"data":               data,
		"requests_remaining": remaining,
	})
}

func (s *lockboxState) approveWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req approveWriteReq
	if err := decodeJSONStrict(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"status": "error", "error": "invalid JSON: " + err.Error()})
		return
	}
	req.HuntID = strings.TrimSpace(req.HuntID)
	req.QueueID = strings.TrimSpace(req.QueueID)
	if req.HuntID == "" || req.QueueID == "" || req.Approved == nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"status": "error", "error": "hunt_id, queue_id, and approved are required"})
		return
	}

	now := time.Now().UTC()

	s.mu.Lock()
	q, ok := s.queues[req.QueueID]
	if !ok || q.HuntID != req.HuntID {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"status": "error", "error": "queue item not found"})
		return
	}
	if q.Status != "pending" {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]interface{}{"status": "error", "error": "queue item is not pending"})
		return
	}
	if q.Service != "mcp" {
		scope, ok := s.hunts[req.HuntID]
		if !ok {
			s.mu.Unlock()
			writeJSON(w, http.StatusNotFound, map[string]interface{}{"status": "error", "error": "hunt scope not found"})
			return
		}
		if now.After(scope.ExpiresAt) {
			delete(s.hunts, req.HuntID)
			delete(s.audits, req.HuntID)
			for qid, queued := range s.queues {
				if queued.HuntID == req.HuntID {
					delete(s.queues, qid)
				}
			}
			s.mu.Unlock()
			writeJSON(w, http.StatusGone, map[string]interface{}{"status": "expired", "error": req.HuntID + " scope has expired"})
			return
		}
	}

	if !*req.Approved {
		q.Status = "rejected"
		s.addAudit(AuditEntry{
			Timestamp: now,
			HuntID:    req.HuntID,
			Service:   q.Service,
			Action:    q.Action,
			Params:    cloneParams(q.Params),
			Status:    "denied",
			Error:     "write action rejected",
		}, q.QueueID)
		delete(s.queues, q.QueueID)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "rejected", "queue_id": q.QueueID})
		return
	}

	params := cloneParams(q.Params)
	service := q.Service
	action := q.Action
	queueID := q.QueueID
	huntID := q.HuntID

	if service == "mcp" {
		tool, ok := s.tools[action]
		if !ok {
			q.Status = "error"
			q.Error = "tool unavailable"
			s.addAudit(AuditEntry{
				Timestamp: now,
				HuntID:    huntID,
				Service:   service,
				Action:    action,
				Params:    cloneParams(params),
				Status:    "error",
				Error:     q.Error,
			}, queueID)
			s.mu.Unlock()
			writeJSON(w, http.StatusBadGateway, map[string]interface{}{"status": "error", "error": q.Error})
			return
		}
		s.mu.Unlock()

		data, err := tool.Handler(params)
		if err != nil {
			s.mu.Lock()
			if qq, ok := s.queues[queueID]; ok {
				qq.Status = "error"
				qq.Error = err.Error()
			}
			s.addAudit(AuditEntry{
				Timestamp: now,
				HuntID:    huntID,
				Service:   service,
				Action:    action,
				Params:    cloneParams(params),
				Status:    "error",
				Error:     err.Error(),
			}, queueID)
			s.mu.Unlock()
			writeJSON(w, http.StatusBadGateway, map[string]interface{}{"status": "error", "error": err.Error()})
			return
		}

		s.mu.Lock()
		if qq, ok := s.queues[queueID]; ok {
			qq.Status = "approved"
			qq.Error = ""
		}
		s.addAudit(AuditEntry{
			Timestamp: now,
			HuntID:    huntID,
			Service:   service,
			Action:    action,
			Params:    cloneParams(params),
			Status:    "ok",
		}, queueID)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":   "approved",
			"queue_id": queueID,
			"data":     data,
		})
		return
	}

	svc, ok := s.services[q.Service]
	if !ok || !actionSupported(svc, q.Action) {
		q.Status = "error"
		q.Error = "service/action unavailable"
		s.addAudit(AuditEntry{
			Timestamp: now,
			HuntID:    huntID,
			Service:   q.Service,
			Action:    q.Action,
			Params:    cloneParams(q.Params),
			Status:    "error",
			Error:     q.Error,
		}, q.QueueID)
		s.mu.Unlock()
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"status": "error", "error": q.Error})
		return
	}
	s.mu.Unlock()

	data, err := svc.Execute(action, params, ServiceCredential{})
	if err != nil {
		s.mu.Lock()
		if qq, ok := s.queues[queueID]; ok {
			qq.Status = "error"
			qq.Error = err.Error()
		}
		s.addAudit(AuditEntry{
			Timestamp: now,
			HuntID:    huntID,
			Service:   service,
			Action:    action,
			Params:    cloneParams(params),
			Status:    "error",
			Error:     err.Error(),
		}, queueID)
		s.mu.Unlock()
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"status": "error", "error": err.Error()})
		return
	}

	s.mu.Lock()
	if qq, ok := s.queues[queueID]; ok {
		qq.Status = "approved"
		qq.Error = ""
	}
	s.addAudit(AuditEntry{
		Timestamp: now,
		HuntID:    huntID,
		Service:   service,
		Action:    action,
		Params:    cloneParams(params),
		Status:    "ok",
	}, queueID)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "approved",
		"queue_id": queueID,
		"data":     data,
	})
}

func (s *lockboxState) audit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	const prefix = "/audit/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	huntID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, prefix))
	if huntID == "" || strings.Contains(huntID, "/") {
		http.NotFound(w, r)
		return
	}
	s.mu.RLock()
	_, ok := s.hunts[huntID]
	if !ok {
		s.mu.RUnlock()
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"status": "error", "error": "hunt scope not found"})
		return
	}
	entries := append([]AuditEntry(nil), s.audits[huntID]...)
	s.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"hunt_id": huntID,
		"entries": entries,
	})
}

func (s *lockboxState) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	hunts := len(s.hunts)
	queuesPending := 0
	for _, q := range s.queues {
		if q.Status == "pending" {
			queuesPending++
		}
	}
	serviceNames := make([]string, 0, len(s.services))
	for name := range s.services {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)
	s.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":         "ready",
		"service":        "lockbox",
		"hunts":          hunts,
		"queues_pending": queuesPending,
		"services":       serviceNames,
	})
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
		tools := make([]map[string]interface{}, 0, len(names))
		for _, name := range names {
			t := s.tools[name]
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
		argsRaw, _ := req.Params["arguments"]
		args, ok := argsRaw.(map[string]interface{})
		if !ok || args == nil {
			args = map[string]interface{}{}
		}
		s.mu.RLock()
		tool, ok := s.tools[toolName]
		s.mu.RUnlock()
		if !ok {
			resp.Error = &jsonRPCError{Code: -32601, Message: "unknown tool: " + toolName}
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
				QueueID:  qid,
				HuntID:   "mcp-session",
				Service:  "mcp",
				Action:   toolName,
				Params:   cloneParams(args),
				Status:   "pending",
				QueuedAt: now,
			}
			s.addAudit(AuditEntry{
				Timestamp: now,
				HuntID:    "mcp-session",
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
	val, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	val = strings.TrimSpace(val)
	if required && val == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return val, nil
}

func intParam(params map[string]interface{}, key string, fallback int) int {
	raw, ok := params[key]
	if !ok {
		return fallback
	}
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return fallback
	}
}

func saveOAuthToken(path string, token *oauth2.Token) error {
	payload := oauthTokenFile{
		AccessToken:  token.AccessToken,
		TokenType:    token.TokenType,
		RefreshToken: token.RefreshToken,
	}
	if !token.Expiry.IsZero() {
		payload.Expiry = token.Expiry.UTC().Format(time.RFC3339)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func loadOAuthToken(path string) (*oauth2.Token, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var payload oauthTokenFile
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	token := &oauth2.Token{
		AccessToken:  strings.TrimSpace(payload.AccessToken),
		TokenType:    strings.TrimSpace(payload.TokenType),
		RefreshToken: strings.TrimSpace(payload.RefreshToken),
	}
	if payload.Expiry != "" {
		if t, err := time.Parse(time.RFC3339, payload.Expiry); err == nil {
			token.Expiry = t
		}
	}
	return token, nil
}

func setupGoogleClients(ctx context.Context, clientID, clientSecret, tokenFile string) (*googleClients, error) {
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	tokenFile = strings.TrimSpace(tokenFile)
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("google OAuth credentials are required")
	}
	if tokenFile == "" {
		tokenFile = "/vault/secrets/google-token.json"
	}
	config := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		Scopes: []string{
			gmailv1.GmailReadonlyScope,
			gmailv1.GmailModifyScope,
			gmailv1.GmailSendScope,
			calendarv3.CalendarReadonlyScope,
			calendarv3.CalendarEventsScope,
		},
		RedirectURL: "urn:ietf:wg:oauth:2.0:oob",
	}

	token, err := loadOAuthToken(tokenFile)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read token file: %w", err)
		}
		authURL := config.AuthCodeURL("lockbox-offline", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
		fmt.Printf("Lockbox Google OAuth setup required.\nOpen this URL in your browser and authorize:\n%s\n\nPaste authorization code: ", authURL)
		var code string
		if _, scanErr := fmt.Scanln(&code); scanErr != nil {
			return nil, fmt.Errorf("read authorization code: %w", scanErr)
		}
		token, err = config.Exchange(ctx, strings.TrimSpace(code))
		if err != nil {
			return nil, fmt.Errorf("exchange auth code: %w", err)
		}
		if err := saveOAuthToken(tokenFile, token); err != nil {
			return nil, fmt.Errorf("save token file: %w", err)
		}
	}

	tokenSource := config.TokenSource(ctx, token)
	refreshed, err := tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = token.RefreshToken
	}
	if err := saveOAuthToken(tokenFile, refreshed); err != nil {
		return nil, fmt.Errorf("persist refreshed token: %w", err)
	}
	httpClient := oauth2.NewClient(ctx, oauth2.ReuseTokenSource(refreshed, tokenSource))
	gmailSvc, err := gmailv1.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("gmail client: %w", err)
	}
	calendarSvc, err := calendarv3.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("calendar client: %w", err)
	}
	return &googleClients{gmail: gmailSvc, calendar: calendarSvc}, nil
}

func decodeBodyPart(payload *gmailv1.MessagePart) string {
	if payload == nil {
		return ""
	}
	if payload.Body != nil && payload.Body.Data != "" {
		if decoded, err := base64.URLEncoding.DecodeString(payload.Body.Data); err == nil {
			return string(decoded)
		}
	}
	for _, part := range payload.Parts {
		if strings.HasPrefix(part.MimeType, "text/plain") || part.MimeType == "" {
			if body := decodeBodyPart(part); strings.TrimSpace(body) != "" {
				return body
			}
		}
	}
	return ""
}

func headerValue(payload *gmailv1.MessagePart, name string) string {
	if payload == nil {
		return ""
	}
	for _, h := range payload.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

func registerGoogleTools(state *lockboxState, clients *googleClients) {
	state.tools["gmail_list_inbox"] = ToolHandler{
		Name:        "gmail_list_inbox",
		Description: "List inbox emails",
		WriteTier:   "read",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"max_results": map[string]interface{}{"type": "integer"},
			},
		},
		Handler: func(params map[string]interface{}) (interface{}, error) {
			maxResults := intParam(params, "max_results", 10)
			if maxResults <= 0 {
				maxResults = 10
			}
			resp, err := clients.gmail.Users.Messages.List("me").LabelIds("INBOX").MaxResults(int64(maxResults)).Do()
			if err != nil {
				return nil, err
			}
			items := make([]map[string]interface{}, 0, len(resp.Messages))
			for _, m := range resp.Messages {
				msg, err := clients.gmail.Users.Messages.Get("me", m.Id).Format("metadata").MetadataHeaders("From", "Subject", "Date").Do()
				if err != nil {
					continue
				}
				items = append(items, map[string]interface{}{
					"id":      msg.Id,
					"from":    headerValue(msg.Payload, "From"),
					"subject": headerValue(msg.Payload, "Subject"),
					"snippet": msg.Snippet,
					"date":    headerValue(msg.Payload, "Date"),
				})
			}
			return items, nil
		},
	}

	state.tools["gmail_read_email"] = ToolHandler{
		Name:        "gmail_read_email",
		Description: "Read one email by ID",
		WriteTier:   "read",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{"type": "string"},
			},
			"required": []string{"id"},
		},
		Handler: func(params map[string]interface{}) (interface{}, error) {
			id, err := strParam(params, "id", true)
			if err != nil {
				return nil, err
			}
			msg, err := clients.gmail.Users.Messages.Get("me", id).Format("full").Do()
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{
				"from":    headerValue(msg.Payload, "From"),
				"to":      headerValue(msg.Payload, "To"),
				"subject": headerValue(msg.Payload, "Subject"),
				"body":    decodeBodyPart(msg.Payload),
				"date":    headerValue(msg.Payload, "Date"),
			}, nil
		},
	}

	state.tools["gmail_archive_email"] = ToolHandler{
		Name:        "gmail_archive_email",
		Description: "Archive one email by removing INBOX label",
		WriteTier:   "safe_write",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{"type": "string"},
			},
			"required": []string{"id"},
		},
		Handler: func(params map[string]interface{}) (interface{}, error) {
			id, err := strParam(params, "id", true)
			if err != nil {
				return nil, err
			}
			_, err = clients.gmail.Users.Messages.Modify("me", id, &gmailv1.ModifyMessageRequest{
				RemoveLabelIds: []string{"INBOX"},
			}).Do()
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"success": true}, nil
		},
	}

	state.tools["gmail_send_email"] = ToolHandler{
		Name:        "gmail_send_email",
		Description: "Send an email",
		WriteTier:   "destructive_write",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"to":      map[string]interface{}{"type": "string"},
				"subject": map[string]interface{}{"type": "string"},
				"body":    map[string]interface{}{"type": "string"},
			},
			"required": []string{"to", "subject", "body"},
		},
		Handler: func(params map[string]interface{}) (interface{}, error) {
			to, err := strParam(params, "to", true)
			if err != nil {
				return nil, err
			}
			subject, err := strParam(params, "subject", true)
			if err != nil {
				return nil, err
			}
			body, err := strParam(params, "body", true)
			if err != nil {
				return nil, err
			}
			msg := fmt.Sprintf("To: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", to, subject, body)
			encoded := base64.URLEncoding.EncodeToString([]byte(msg))
			resp, err := clients.gmail.Users.Messages.Send("me", &gmailv1.Message{Raw: encoded}).Do()
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"id": resp.Id, "success": true}, nil
		},
	}

	state.tools["calendar_list_events"] = ToolHandler{
		Name:        "calendar_list_events",
		Description: "List upcoming calendar events",
		WriteTier:   "read",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"days_ahead": map[string]interface{}{"type": "integer"},
			},
		},
		Handler: func(params map[string]interface{}) (interface{}, error) {
			daysAhead := intParam(params, "days_ahead", 7)
			if daysAhead <= 0 {
				daysAhead = 7
			}
			start := time.Now().UTC()
			end := start.Add(time.Duration(daysAhead) * 24 * time.Hour)
			resp, err := clients.calendar.Events.List("primary").
				ShowDeleted(false).
				SingleEvents(true).
				OrderBy("startTime").
				TimeMin(start.Format(time.RFC3339)).
				TimeMax(end.Format(time.RFC3339)).
				Do()
			if err != nil {
				return nil, err
			}
			items := make([]map[string]interface{}, 0, len(resp.Items))
			for _, e := range resp.Items {
				startVal := e.Start.DateTime
				if startVal == "" {
					startVal = e.Start.Date
				}
				endVal := e.End.DateTime
				if endVal == "" {
					endVal = e.End.Date
				}
				items = append(items, map[string]interface{}{
					"id":       e.Id,
					"summary":  e.Summary,
					"start":    startVal,
					"end":      endVal,
					"location": e.Location,
				})
			}
			return items, nil
		},
	}

	state.tools["calendar_create_event"] = ToolHandler{
		Name:        "calendar_create_event",
		Description: "Create a calendar event",
		WriteTier:   "safe_write",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"summary":     map[string]interface{}{"type": "string"},
				"start":       map[string]interface{}{"type": "string"},
				"end":         map[string]interface{}{"type": "string"},
				"location":    map[string]interface{}{"type": "string"},
				"description": map[string]interface{}{"type": "string"},
			},
			"required": []string{"summary", "start", "end"},
		},
		Handler: func(params map[string]interface{}) (interface{}, error) {
			summary, err := strParam(params, "summary", true)
			if err != nil {
				return nil, err
			}
			start, err := strParam(params, "start", true)
			if err != nil {
				return nil, err
			}
			end, err := strParam(params, "end", true)
			if err != nil {
				return nil, err
			}
			location, _ := strParam(params, "location", false)
			description, _ := strParam(params, "description", false)
			event := &calendarv3.Event{
				Summary:     summary,
				Location:    location,
				Description: description,
				Start:       &calendarv3.EventDateTime{DateTime: start},
				End:         &calendarv3.EventDateTime{DateTime: end},
			}
			created, err := clients.calendar.Events.Insert("primary", event).Do()
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{
				"id":   created.Id,
				"link": created.HtmlLink,
			}, nil
		},
	}
}

func main() {
	port := flag.Int("port", 8083, "HTTP port")
	googleClientID := flag.String("google-client-id", "", "Google OAuth client ID")
	googleClientSecret := flag.String("google-client-secret", "", "Google OAuth client secret")
	googleTokenFile := flag.String("google-token-file", "/vault/secrets/google-token.json", "Path to stored Google OAuth token")
	flag.Parse()

	state := &lockboxState{
		hunts:    map[string]*HuntScope{},
		audits:   map[string][]AuditEntry{},
		queues:   map[string]*WriteQueue{},
		services: map[string]ServiceHandler{},
		tools:    map[string]ToolHandler{},
	}
	state.services["mock"] = &MockService{}

	if strings.TrimSpace(*googleClientID) != "" || strings.TrimSpace(*googleClientSecret) != "" {
		clients, err := setupGoogleClients(context.Background(), *googleClientID, *googleClientSecret, *googleTokenFile)
		if err != nil {
			log.Fatalf("google setup failed: %v", err)
		}
		registerGoogleTools(state, clients)
	}

	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for range t.C {
			state.pruneExpired()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/register", state.registerHunt)
	mux.HandleFunc("/action", state.action)
	mux.HandleFunc("/approve-write", state.approveWrite)
	mux.HandleFunc("/audit/", state.audit)
	mux.HandleFunc("/health", state.health)
	mux.HandleFunc("/mcp", state.mcp)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("valhalla-lockbox listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
