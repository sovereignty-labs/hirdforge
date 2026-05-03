package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

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
	req.AgentName = strings.TrimSpace(req.AgentName)
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
			QueueID:   qid,
			HuntID:    req.HuntID,
			Service:   req.Service,
			Action:    req.Action,
			AgentName: req.AgentName,
			Params:    cloneParams(req.Params),
			Status:    "pending",
			Trigger:   cloneTrigger(req.Trigger),
			QueuedAt:  now,
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
		tool, nativeTool := s.tools[action]
		_, upstreamTool := s.upstreamTools[action]
		if !nativeTool && !upstreamTool {
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

		var (
			data interface{}
			err  error
		)
		if nativeTool {
			data, err = tool.Handler(params)
		} else {
			data, err = s.proxyUpstreamToolCall(action, params)
		}
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

func (s *lockboxState) reviseWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req reviseWriteReq
	if err := decodeJSONStrict(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"status": "error", "error": "invalid JSON: " + err.Error()})
		return
	}
	req.HuntID = strings.TrimSpace(req.HuntID)
	req.QueueID = strings.TrimSpace(req.QueueID)
	req.Note = strings.TrimSpace(req.Note)
	if req.HuntID == "" || req.QueueID == "" || req.Note == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"status": "error", "error": "hunt_id, queue_id, and note are required"})
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

	q.Status = "revising"
	q.Note = req.Note
	s.addAudit(AuditEntry{
		Timestamp: now,
		HuntID:    req.HuntID,
		Service:   q.Service,
		Action:    q.Action,
		Params:    cloneParams(q.Params),
		Status:    "revised",
		Note:      req.Note,
	}, q.QueueID)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "revising",
		"queue_id": q.QueueID,
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

func (s *lockboxState) listQueues(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	queues := make([]map[string]interface{}, 0, len(s.queues))
	for _, q := range s.queues {
		if q.Status != "pending" {
			continue
		}
		item := map[string]interface{}{
			"queue_id":  q.QueueID,
			"hunt_id":   q.HuntID,
			"service":   q.Service,
			"action":    q.Action,
			"params":    cloneParams(q.Params),
			"status":    q.Status,
			"note":      q.Note,
			"trigger":   q.Trigger,
			"queued_at": q.QueuedAt,
		}
		if strings.TrimSpace(q.AgentName) != "" {
			item["agent_name"] = q.AgentName
		}
		queues = append(queues, item)
	}
	s.mu.RUnlock()
	sort.Slice(queues, func(i, j int) bool {
		iq, _ := queues[i]["queued_at"].(time.Time)
		jq, _ := queues[j]["queued_at"].(time.Time)
		return iq.Before(jq)
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "ok",
		"queues": queues,
	})
}
