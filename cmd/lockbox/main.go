package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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
	queueSeq uint64
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
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "rejected", "queue_id": q.QueueID})
		return
	}

	svc, ok := s.services[q.Service]
	if !ok || !actionSupported(svc, q.Action) {
		q.Status = "error"
		q.Error = "service/action unavailable"
		s.addAudit(AuditEntry{
			Timestamp: now,
			HuntID:    req.HuntID,
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
	params := cloneParams(q.Params)
	service := q.Service
	action := q.Action
	queueID := q.QueueID
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
			HuntID:    req.HuntID,
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
		HuntID:    req.HuntID,
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

func main() {
	port := flag.Int("port", 8083, "HTTP port")
	flag.Parse()

	state := &lockboxState{
		hunts:    map[string]*HuntScope{},
		audits:   map[string][]AuditEntry{},
		queues:   map[string]*WriteQueue{},
		services: map[string]ServiceHandler{},
	}
	state.services["mock"] = &MockService{}

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

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("valhalla-lockbox listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
