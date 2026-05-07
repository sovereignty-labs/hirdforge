package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type jsonRPCRequest struct {
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	JSONRPC string          `json:"jsonrpc"`
}

type jsonRPCResponse struct {
	ID      interface{}   `json:"id,omitempty"`
	Result  interface{}   `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
	JSONRPC string        `json:"jsonrpc"`
}

type jsonRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type a2aTaskState string

const (
	a2aTaskStateSubmitted   a2aTaskState = "submitted"
	a2aTaskStateWorking     a2aTaskState = "working"
	a2aTaskStateCompleted   a2aTaskState = "completed"
	a2aTaskStateFailed      a2aTaskState = "failed"
	a2aTaskStateCanceled    a2aTaskState = "canceled"
	a2aTaskStateInputNeeded a2aTaskState = "input-needed"
)

type a2aTask struct {
	ID        string                 `json:"id"`
	ContextID string                 `json:"contextId"`
	Status    a2aTaskStatus          `json:"status"`
	Artifacts []a2aArtifact          `json:"artifacts,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

type a2aTaskStatus struct {
	State     a2aTaskState `json:"state"`
	Timestamp string       `json:"timestamp,omitempty"`
	Message   *a2aMessage  `json:"message,omitempty"`
}

type a2aMessage struct {
	Role      string    `json:"role,omitempty"`
	Parts     []a2aPart `json:"parts,omitempty"`
	MessageID string    `json:"messageId,omitempty"`
}

type a2aPart struct {
	Text     string                 `json:"text,omitempty"`
	Data     interface{}            `json:"data,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

type a2aArtifact struct {
	ArtifactID string    `json:"artifactId,omitempty"`
	Parts      []a2aPart `json:"parts,omitempty"`
}

type a2aPushNotificationConfig struct {
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
}

type a2aTaskStatusUpdateEvent struct {
	TaskID string        `json:"taskId"`
	Status a2aTaskStatus `json:"status"`
	Final  bool          `json:"final"`
}

type a2aTaskArtifactUpdateEvent struct {
	TaskID   string      `json:"taskId"`
	Artifact a2aArtifact `json:"artifact"`
}

type a2aSendMessageRequest struct {
	Message          a2aMessage                 `json:"message"`
	PushNotification *a2aPushNotificationConfig `json:"pushNotification,omitempty"`
}

type a2aGetTaskRequest struct {
	ID string `json:"id"`
}

type a2aCancelTaskRequest struct {
	ID string `json:"id"`
}

type a2aPushPayload struct {
	TaskID    string         `json:"taskId"`
	Status    a2aTaskStatus  `json:"status"`
	Artifacts []a2aArtifact  `json:"artifacts,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type a2aRuntimeEvent struct {
	Status   *a2aTaskStatusUpdateEvent
	Artifact *a2aTaskArtifactUpdateEvent
}

type a2aRuntime struct {
	agentName           string
	gatewayURL          string
	processConversation conversationProcessor
	pushClient          *http.Client
	eventClient         *http.Client

	mu           sync.RWMutex
	tasks        map[string]*a2aTask
	cancels      map[string]context.CancelFunc
	activeTaskID string
}

var (
	errA2ABusy     = errors.New("agent is busy")
	errA2ANotFound = errors.New("task not found")
)

func newA2ARuntime(agentName, gatewayURL string, processConversation conversationProcessor) *a2aRuntime {
	return &a2aRuntime{
		agentName:           strings.TrimSpace(agentName),
		gatewayURL:          strings.TrimSpace(gatewayURL),
		processConversation: processConversation,
		pushClient:          &http.Client{Timeout: 10 * time.Second},
		eventClient:         &http.Client{Timeout: 5 * time.Second},
		tasks:               map[string]*a2aTask{},
		cancels:             map[string]context.CancelFunc{},
	}
}

func (rt *a2aRuntime) postGatewayEvent(eventType, agentName string, metadata map[string]interface{}) {
	log.Printf("a2a: postGatewayEvent called type=%s agent=%s gatewayURL=%q", eventType, agentName, rt.gatewayURL)
	if strings.TrimSpace(rt.gatewayURL) == "" {
		return
	}
	go func() {
		body, err := json.Marshal(map[string]interface{}{
			"type":     eventType,
			"agent":    agentName,
			"metadata": metadata,
		})
		if err != nil {
			log.Printf("a2a: postGatewayEvent marshal error: %v", err)
			return
		}
		url := strings.TrimRight(rt.gatewayURL, "/") + "/api/v1/events"
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			log.Printf("a2a: postGatewayEvent request error: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := rt.eventClient.Do(req)
		if err != nil {
			log.Printf("a2a: postGatewayEvent send error: %v", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			log.Printf("a2a: postGatewayEvent gateway returned %d", resp.StatusCode)
		}
		log.Printf("a2a: postGatewayEvent sent type=%s status=%d", eventType, resp.StatusCode)
	}()
}

func (rt *a2aRuntime) handleJSONRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rt.writeJSONRPC(w, req.ID, nil, &jsonRPCError{Code: -32700, Message: "parse error", Data: err.Error()})
		return
	}
	if strings.TrimSpace(req.JSONRPC) != "2.0" {
		rt.writeJSONRPC(w, req.ID, nil, &jsonRPCError{Code: -32600, Message: "invalid request"})
		return
	}
	switch strings.TrimSpace(req.Method) {
	case "message/send":
		var params a2aSendMessageRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			rt.writeJSONRPC(w, req.ID, nil, &jsonRPCError{Code: -32602, Message: "invalid params", Data: err.Error()})
			return
		}
		task, _, err := rt.submit(params, nil)
		if err != nil {
			rt.writeJSONRPC(w, req.ID, nil, jsonRPCErrorFor(err))
			return
		}
		rt.writeJSONRPC(w, req.ID, task, nil)
	case "tasks/get":
		var params a2aGetTaskRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			rt.writeJSONRPC(w, req.ID, nil, &jsonRPCError{Code: -32602, Message: "invalid params", Data: err.Error()})
			return
		}
		task, err := rt.getTask(strings.TrimSpace(params.ID))
		if err != nil {
			rt.writeJSONRPC(w, req.ID, nil, jsonRPCErrorFor(err))
			return
		}
		rt.writeJSONRPC(w, req.ID, task, nil)
	case "tasks/cancel":
		var params a2aCancelTaskRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			rt.writeJSONRPC(w, req.ID, nil, &jsonRPCError{Code: -32602, Message: "invalid params", Data: err.Error()})
			return
		}
		task, err := rt.cancelTask(strings.TrimSpace(params.ID))
		if err != nil {
			rt.writeJSONRPC(w, req.ID, nil, jsonRPCErrorFor(err))
			return
		}
		rt.writeJSONRPC(w, req.ID, task, nil)
	default:
		rt.writeJSONRPC(w, req.ID, nil, &jsonRPCError{Code: -32601, Message: "method not found"})
	}
}

func (rt *a2aRuntime) writeJSONRPC(w http.ResponseWriter, id interface{}, result interface{}, rpcErr *jsonRPCError) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jsonRPCResponse{
		ID:      id,
		Result:  result,
		Error:   rpcErr,
		JSONRPC: "2.0",
	})
}

func jsonRPCErrorFor(err error) *jsonRPCError {
	switch {
	case errors.Is(err, errA2ABusy):
		return &jsonRPCError{Code: -32000, Message: err.Error()}
	case errors.Is(err, errA2ANotFound):
		return &jsonRPCError{Code: -32004, Message: err.Error()}
	default:
		return &jsonRPCError{Code: -32001, Message: err.Error()}
	}
}

func (rt *a2aRuntime) submit(req a2aSendMessageRequest, sink func(a2aRuntimeEvent) bool) (*a2aTask, <-chan struct{}, error) {
	content := flattenA2AMessage(req.Message)
	if content == "" {
		return nil, nil, fmt.Errorf("message content is required")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	taskID := uuid.NewString()
	contextID := strings.TrimSpace(req.Message.MessageID)
	if contextID == "" {
		contextID = taskID
	}
	task := &a2aTask{
		ID:        taskID,
		ContextID: contextID,
		Status: a2aTaskStatus{
			State:     a2aTaskStateSubmitted,
			Timestamp: now,
			Message:   cloneA2AMessagePtr(&req.Message),
		},
		Metadata: map[string]interface{}{
			"agent":     rt.agentName,
			"transport": "a2a",
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if strings.TrimSpace(rt.activeTaskID) != "" {
		cancel()
		return nil, nil, errA2ABusy
	}
	rt.tasks[task.ID] = cloneA2ATask(task)
	rt.cancels[task.ID] = cancel
	rt.activeTaskID = task.ID

	go rt.runTask(ctx, cloneA2ATask(task), content, req.PushNotification, sink, done)
	return cloneA2ATask(task), done, nil
}

func (rt *a2aRuntime) getTask(id string) (*a2aTask, error) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	task, ok := rt.tasks[strings.TrimSpace(id)]
	if !ok {
		return nil, errA2ANotFound
	}
	return cloneA2ATask(task), nil
}

func (rt *a2aRuntime) cancelTask(id string) (*a2aTask, error) {
	taskID := strings.TrimSpace(id)
	if taskID == "" {
		return nil, fmt.Errorf("task id is required")
	}
	rt.mu.Lock()
	task, ok := rt.tasks[taskID]
	if !ok {
		rt.mu.Unlock()
		return nil, errA2ANotFound
	}
	task.Status = a2aTaskStatus{
		State:     a2aTaskStateCanceled,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Message: &a2aMessage{
			Role: "system",
			Parts: []a2aPart{{
				Text: "task canceled",
			}},
		},
	}
	cancel := rt.cancels[taskID]
	taskCopy := cloneA2ATask(task)
	rt.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return taskCopy, nil
}

func (rt *a2aRuntime) runTask(ctx context.Context, task *a2aTask, content string, push *a2aPushNotificationConfig, sink func(a2aRuntimeEvent) bool, done chan struct{}) {
	defer close(done)
	defer func() {
		rt.mu.Lock()
		delete(rt.cancels, task.ID)
		if rt.activeTaskID == task.ID {
			rt.activeTaskID = ""
		}
		rt.mu.Unlock()
	}()

	workingStatus := a2aTaskStatus{
		State:     a2aTaskStateWorking,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	rt.updateTask(task.ID, func(current *a2aTask) {
		current.Status = workingStatus
	})
	if sink != nil {
		_ = sink(a2aRuntimeEvent{
			Status: &a2aTaskStatusUpdateEvent{
				TaskID: task.ID,
				Status: workingStatus,
				Final:  false,
			},
		})
	}

	rt.postGatewayEvent("delegation_started", rt.agentName, map[string]interface{}{
		"target_agent": rt.agentName,
		"session_id":   task.ContextID,
		"task_id":      task.ID,
	})

	baseEmit := rt.eventEmitter(task.ID, sink)
	emit := func(chunk interface{}) bool {
		if c, ok := chunk.(sseChunk); ok && c.Type == "tool_call" {
			buf, _ := json.Marshal(c)
			meta := map[string]interface{}{}
			_ = json.Unmarshal(buf, &meta)
			meta["session_id"] = task.ContextID
			meta["task_id"] = task.ID
			rt.postGatewayEvent(c.Type, rt.agentName, meta)
		}
		return baseEmit(chunk)
	}

	result, err := rt.processConversation(ctx, task.ID, task.ID, content, emit, nil)
	switch {
	case ctx.Err() == context.Canceled:
		finalStatus := a2aTaskStatus{
			State:     a2aTaskStateCanceled,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Message: &a2aMessage{
				Role:  "system",
				Parts: []a2aPart{{Text: "task canceled"}},
			},
		}
		finalTask := rt.updateTask(task.ID, func(current *a2aTask) {
			current.Status = finalStatus
		})
		rt.emitFinalStatus(task.ID, finalStatus, sink)
		rt.postGatewayEvent("delegation_ended", rt.agentName, map[string]interface{}{
			"target_agent": rt.agentName,
			"session_id":   task.ContextID,
			"task_id":      task.ID,
			"state":        string(a2aTaskStateCanceled),
		})
		rt.postCompletion(push, finalTask)
	case err != nil:
		finalStatus := a2aTaskStatus{
			State:     a2aTaskStateFailed,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Message: &a2aMessage{
				Role:  "assistant",
				Parts: []a2aPart{{Text: err.Error()}},
			},
		}
		finalTask := rt.updateTask(task.ID, func(current *a2aTask) {
			current.Status = finalStatus
		})
		rt.emitFinalStatus(task.ID, finalStatus, sink)
		rt.postGatewayEvent("delegation_ended", rt.agentName, map[string]interface{}{
			"target_agent": rt.agentName,
			"session_id":   task.ContextID,
			"task_id":      task.ID,
			"state":        string(a2aTaskStateFailed),
		})
		rt.postCompletion(push, finalTask)
	default:
		artifact := a2aArtifact{
			ArtifactID: "artifact-" + task.ID,
			Parts: []a2aPart{{
				Text: result,
			}},
		}
		finalStatus := a2aTaskStatus{
			State:     a2aTaskStateCompleted,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}
		finalTask := rt.updateTask(task.ID, func(current *a2aTask) {
			current.Artifacts = append(current.Artifacts, artifact)
			current.Status = finalStatus
		})
		if sink != nil {
			_ = sink(a2aRuntimeEvent{
				Artifact: &a2aTaskArtifactUpdateEvent{
					TaskID:   task.ID,
					Artifact: artifact,
				},
			})
		}
		rt.emitFinalStatus(task.ID, finalStatus, sink)
		rt.postGatewayEvent("delegation_ended", rt.agentName, map[string]interface{}{
			"target_agent": rt.agentName,
			"session_id":   task.ContextID,
			"task_id":      task.ID,
			"state":        string(a2aTaskStateCompleted),
		})
		rt.postCompletion(push, finalTask)
	}
}

func (rt *a2aRuntime) eventEmitter(taskID string, sink func(a2aRuntimeEvent) bool) func(interface{}) bool {
	return func(chunk interface{}) bool {
		if sink == nil {
			return true
		}
		update := a2aStatusUpdateForChunk(taskID, chunk)
		if update == nil {
			return true
		}
		return sink(a2aRuntimeEvent{Status: update})
	}
}

func (rt *a2aRuntime) emitFinalStatus(taskID string, status a2aTaskStatus, sink func(a2aRuntimeEvent) bool) {
	if sink == nil {
		return
	}
	_ = sink(a2aRuntimeEvent{
		Status: &a2aTaskStatusUpdateEvent{
			TaskID: taskID,
			Status: status,
			Final:  true,
		},
	})
}

func (rt *a2aRuntime) updateTask(taskID string, mutate func(*a2aTask)) *a2aTask {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	current, ok := rt.tasks[taskID]
	if !ok {
		return nil
	}
	mutate(current)
	return cloneA2ATask(current)
}

func (rt *a2aRuntime) postCompletion(cfg *a2aPushNotificationConfig, task *a2aTask) {
	if cfg == nil || strings.TrimSpace(cfg.URL) == "" || task == nil {
		return
	}
	payload := a2aPushPayload{
		TaskID:    task.ID,
		Status:    task.Status,
		Artifacts: task.Artifacts,
		Metadata:  cloneAnyMap(task.Metadata),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("a2a: marshal push payload failed: %v", err)
		return
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimSpace(cfg.URL), bytes.NewReader(body))
	if err != nil {
		log.Printf("a2a: build push request failed: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(cfg.Token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := rt.pushClient.Do(req)
	if err != nil {
		log.Printf("a2a: push notification failed for task %s: %v", task.ID, err)
		return
	}
	_ = resp.Body.Close()
}

func flattenA2AMessage(msg a2aMessage) string {
	parts := make([]string, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		if text := strings.TrimSpace(part.Text); text != "" {
			parts = append(parts, text)
			continue
		}
		if part.Data != nil {
			body, err := json.Marshal(part.Data)
			if err == nil && strings.TrimSpace(string(body)) != "" {
				parts = append(parts, string(body))
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func a2aStatusUpdateForChunk(taskID string, chunk interface{}) *a2aTaskStatusUpdateEvent {
	payload, ok := chunk.(sseChunk)
	if !ok {
		return nil
	}
	switch payload.Type {
	case "content", "replace":
	default:
		return nil
	}
	if strings.TrimSpace(payload.Content) == "" {
		return nil
	}
	return &a2aTaskStatusUpdateEvent{
		TaskID: taskID,
		Status: a2aTaskStatus{
			State:     a2aTaskStateWorking,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Message: &a2aMessage{
				Role: "assistant",
				Parts: []a2aPart{{
					Text: payload.Content,
					Metadata: map[string]interface{}{
						"chunkType": payload.Type,
					},
				}},
			},
		},
		Final: false,
	}
}

func emitA2AStatusSSE(base func(interface{}) bool, taskID string, chunk interface{}) bool {
	update := a2aStatusUpdateForChunk(taskID, chunk)
	if update == nil {
		return true
	}
	return base(map[string]interface{}{
		"type":   "a2a_status",
		"update": update,
	})
}

func emitA2AArtifactSSE(base func(interface{}) bool, taskID, result string) bool {
	if strings.TrimSpace(result) == "" {
		return true
	}
	return base(map[string]interface{}{
		"type": "a2a_artifact",
		"update": a2aTaskArtifactUpdateEvent{
			TaskID: taskID,
			Artifact: a2aArtifact{
				ArtifactID: "artifact-" + taskID,
				Parts: []a2aPart{{
					Text: result,
				}},
			},
		},
	})
}

func agentBaseURL(r *http.Request) string {
	return "http://" + agentHostWithPort(r, portValue)
}

func agentGRPCURL(r *http.Request) string {
	return "grpc://" + agentHostWithPort(r, grpcPortValue)
}

func agentHostWithPort(r *http.Request, port string) string {
	host := strings.TrimSpace(r.Host)
	if host == "" {
		host, _ = os.Hostname()
	}
	if host == "" {
		host = "localhost"
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	return net.JoinHostPort(host, strings.TrimSpace(port))
}

func agentDescriptionFromSoul(soul string) string {
	for _, line := range strings.Split(soul, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if line != "" {
			return line
		}
	}
	return "Valhalla AI agent"
}

func cloneA2ATask(task *a2aTask) *a2aTask {
	if task == nil {
		return nil
	}
	body, err := json.Marshal(task)
	if err != nil {
		copy := *task
		return &copy
	}
	var cloned a2aTask
	if err := json.Unmarshal(body, &cloned); err != nil {
		copy := *task
		return &copy
	}
	return &cloned
}

func cloneA2AMessagePtr(msg *a2aMessage) *a2aMessage {
	if msg == nil {
		return nil
	}
	body, err := json.Marshal(msg)
	if err != nil {
		copy := *msg
		return &copy
	}
	var cloned a2aMessage
	if err := json.Unmarshal(body, &cloned); err != nil {
		copy := *msg
		return &copy
	}
	return &cloned
}

func cloneAnyMap(in map[string]interface{}) map[string]interface{} {
	if len(in) == 0 {
		return nil
	}
	body, err := json.Marshal(in)
	if err != nil {
		out := map[string]interface{}{}
		for k, v := range in {
			out[k] = v
		}
		return out
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		out = map[string]interface{}{}
		for k, v := range in {
			out[k] = v
		}
	}
	return out
}
