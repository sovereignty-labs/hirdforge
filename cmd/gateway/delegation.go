package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	workspacepkg "github.com/kitporath/project_valhalla/pkg/workspace"
)

var thinkTagRE = regexp.MustCompile(`(?s)<think>.*?</think>`)

var controlTokenRE = regexp.MustCompile(`</?\|[^>]*>|<\w+\|>|</?(?:thought|channel|start_of_turn|end_of_turn|tool_call|tool_response)\b[^>]*>?`)

var (
	taskPRURLRE   = regexp.MustCompile(`/pulls/(\d+)\b`)
	taskPRRefRE   = regexp.MustCompile(`\bPR\s*#(\d+)\b`)
	taskPullRefRE = regexp.MustCompile(`\bpull request\s*#?(\d+)\b`)
)

func (g *gateway) addEvent(eventType, agent, summary string) {
	e := Event{Time: time.Now().Format(time.RFC3339), Type: eventType, Agent: agent, Summary: summary}
	g.eventMu.Lock()
	if len(g.events) == g.eventCap {
		copy(g.events, g.events[1:])
		g.events[len(g.events)-1] = e
	} else {
		g.events = append(g.events, e)
	}
	g.eventMu.Unlock()
	g.broadcastEvent(e)
	if structured, ok := parseTaskSocketEvent(e); ok {
		g.broadcastPayload(structured)
		go g.notifyDelegatingAgent(structured)
	}
}

func (g *gateway) addNotification(n Notification) {
	g.notifMu.Lock()
	if len(g.notifications) == g.notifCap {
		copy(g.notifications, g.notifications[1:])
		g.notifications[len(g.notifications)-1] = n
	} else {
		g.notifications = append(g.notifications, n)
	}
	g.notifMu.Unlock()
	g.broadcastPayload(n)
}

func (g *gateway) notificationsNewest() []Notification {
	g.notifMu.Lock()
	defer g.notifMu.Unlock()
	out := make([]Notification, len(g.notifications))
	for i := range g.notifications {
		out[i] = g.notifications[len(g.notifications)-1-i]
	}
	return out
}

func (g *gateway) addDelegationTimelineEvent(sessionID string, evt delegationTimelineEvent) {
	g.dtlMu.Lock()
	defer g.dtlMu.Unlock()
	g.delegationTimelines[sessionID] = append(g.delegationTimelines[sessionID], evt)
}

func (g *gateway) delegationTimeline(sessionID string) []delegationTimelineEvent {
	g.dtlMu.RLock()
	defer g.dtlMu.RUnlock()
	events, ok := g.delegationTimelines[sessionID]
	if !ok {
		return []delegationTimelineEvent{}
	}
	out := make([]delegationTimelineEvent, len(events))
	copy(out, events)
	return out
}

func registerDelegationRoutes(mux *http.ServeMux, gw *gateway, proxyClient, streamClient *http.Client) {
	mux.HandleFunc("/api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, gw.eventsNewest())
		case http.MethodPost:
			var in struct {
				Type     string                 `json:"type"`
				Agent    string                 `json:"agent"`
				Metadata map[string]interface{} `json:"metadata"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
				return
			}
			if in.Type == "" {
				in.Type = "task"
			}
			log.Printf("event received: type=%s agent=%s", in.Type, in.Agent)
			if in.Type == "workspace_update" && strings.TrimSpace(in.Agent) != "" {
				meta := asMap(in.Metadata["workspace"])
				var pr *workspacepkg.PRRef
				if prMap := asMap(meta["current_pr"]); len(prMap) > 0 {
					owner := strings.TrimSpace(asString(prMap["owner"]))
					repo := strings.TrimSpace(asString(prMap["repo"]))
					index := int(asInt64(prMap["index"]))
					if owner != "" && repo != "" && index > 0 {
						pr = &workspacepkg.PRRef{Owner: owner, Repo: repo, Index: index}
					}
				}
				sessionID := asString(meta["current_session_id"])
				objective := asString(meta["current_objective"])
				if sessionID != "" || objective != "" {
					gw.projector.SetArchitectContext(in.Agent, sessionID, objective)
				}
				ws := gw.projector.SetReviewContext(in.Agent, pr, asString(meta["current_review_file"]))
				if sessionID != "" || objective != "" {
					ws = gw.refreshArchitectWorkspace(in.Agent)
				}
				now := time.Now().Format(time.RFC3339)
				gw.broadcastPayload(map[string]interface{}{
					"type":      "workspace_update",
					"agent":     in.Agent,
					"timestamp": now,
					"workspace": ws,
				})
				writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
				return
			}
			msg, _ := in.Metadata["message"].(string)
			gw.addEvent(in.Type, in.Agent, msg)
			if strings.TrimSpace(in.Agent) != "" {
				eventMap := map[string]interface{}{
					"type":  in.Type,
					"agent": in.Agent,
				}
				for k, v := range in.Metadata {
					if k == "type" || k == "agent" {
						continue
					}
					eventMap[k] = v
				}
				gw.applyWorkspaceEvent(in.Agent, eventMap)
			}
			if sessionID, ok := in.Metadata["session_id"].(string); ok && sessionID != "" {
				evt := delegationTimelineEvent{
					Type:      in.Type,
					Agent:     in.Agent,
					Metadata:  in.Metadata,
					Timestamp: time.Now().Format(time.RFC3339),
				}
				gw.addDelegationTimelineEvent(sessionID, evt)
				gw.broadcastPayload(evt)
				gw.refreshArchitectWorkspacesBySession(sessionID)
			}

			if in.Type == "delegation_started" {
				if target, ok := in.Metadata["target_agent"].(string); ok && target != "" {
					sid, _ := in.Metadata["session_id"].(string)
					if sid == "" {
						sid = fmt.Sprintf("delegation-%s-%d", target, time.Now().UnixNano())
					}
					gw.setActiveRequest(target, sid, nil)
				}
				if taskID, ok := in.Metadata["task_id"].(string); ok && strings.TrimSpace(taskID) != "" && gw.a2aStore != nil {
					if err := gw.a2aStore.UpdateTaskStatus(strings.TrimSpace(taskID), TaskStatus{
						State:     TaskStateWorking,
						Timestamp: time.Now().UTC().Format(time.RFC3339),
					}); err != nil {
						log.Printf("a2aStore.UpdateTaskStatus(%s, working): %v", taskID, err)
					}
				}
			}
			if in.Type == "delegation_ended" {
				if target, ok := in.Metadata["target_agent"].(string); ok && target != "" {
					gw.clearActiveRequest(target, nil)
				}
				if taskID, ok := in.Metadata["task_id"].(string); ok && strings.TrimSpace(taskID) != "" && gw.a2aStore != nil {
					stateStr, _ := in.Metadata["state"].(string)
					stateStr = strings.TrimSpace(stateStr)
					if stateStr == "" {
						stateStr = string(TaskStateCompleted)
					}
					if err := gw.a2aStore.UpdateTaskStatus(strings.TrimSpace(taskID), TaskStatus{
						State:     TaskState(stateStr),
						Timestamp: time.Now().UTC().Format(time.RFC3339),
					}); err != nil {
						log.Printf("a2aStore.UpdateTaskStatus(%s, %s): %v", taskID, stateStr, err)
					}
				}
			}

			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/api/v1/delegation-timeline", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
		if sessionID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session_id query parameter is required"})
			return
		}
		events := gw.delegationTimeline(sessionID)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"session_id": sessionID,
			"events":     events,
			"count":      len(events),
		})
	})

	mux.HandleFunc("/api/v1/notify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in Notification
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		in.From = strings.TrimSpace(in.From)
		in.TaskID = strings.TrimSpace(in.TaskID)
		in.Agent = strings.TrimSpace(in.Agent)
		in.State = strings.TrimSpace(in.State)
		in.Result = strings.TrimSpace(in.Result)
		if in.From == "" || in.TaskID == "" || in.Agent == "" || in.State == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from, task_id, agent, and state are required"})
			return
		}
		if strings.TrimSpace(in.Timestamp) == "" {
			in.Timestamp = time.Now().UTC().Format(time.RFC3339)
		}
		gw.addNotification(in)
		writeJSON(w, http.StatusOK, in)
	})

	mux.HandleFunc("/api/v1/notifications", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, gw.notificationsNewest())
	})

	mux.HandleFunc("/api/v1/dispatch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in []dispatchReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		out := make([]dispatchResp, len(in))
		var wg sync.WaitGroup
		for i := range in {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				reqItem := in[idx]
				out[idx].Agent = reqItem.Agent
				agent, ok := gw.getAgent(reqItem.Agent)
				if !ok {
					out[idx].Error = "unknown agent"
					return
				}
				body, _ := json.Marshal(taskSendRequest{Content: reqItem.Content, From: reqItem.From})
				req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(agent.URL, "/")+"/tasks/send", bytes.NewReader(body))
				if err != nil {
					out[idx].Error = "failed to create upstream request"
					return
				}
				req.Header.Set("Content-Type", "application/json")
				resp, err := proxyClient.Do(req)
				if err != nil {
					out[idx].Error = "upstream request failed"
					return
				}
				defer resp.Body.Close()
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
					out[idx].Error = strings.TrimSpace(string(b))
					return
				}
				var tr struct {
					ID string `json:"id"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
					out[idx].Error = "invalid upstream response"
					return
				}
				out[idx].TaskID = tr.ID
			}(i)
		}
		wg.Wait()
		writeJSON(w, http.StatusOK, out)
	})

	mux.HandleFunc("/api/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		from := strings.TrimSpace(r.URL.Query().Get("from"))
		agentFilter := strings.TrimSpace(r.URL.Query().Get("agent"))
		writeJSON(w, http.StatusOK, gw.fetchTasks(r.Context(), status, from, agentFilter))
	})

	mux.HandleFunc("/api/v1/tasks/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		taskID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/"), "/")
		if taskID == "" || strings.Contains(taskID, "/") {
			http.NotFound(w, r)
			return
		}
		for _, agent := range gw.snapshotAgents() {
			req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, strings.TrimRight(agent.URL, "/")+"/tasks/"+url.PathEscape(taskID), nil)
			if err != nil {
				continue
			}
			resp, err := proxyClient.Do(req)
			if err != nil {
				continue
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				continue
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(body)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
	})

	mux.HandleFunc("/api/v1/message", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if r.URL.Query().Get("async") == "true" {
			var in messageReq
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
				return
			}
			agent, ok := gw.getAgent(in.Agent)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
				return
			}
			sessionID := strings.TrimSpace(in.SessionID)
			if sessionID == "" {
				sessionID = fmt.Sprintf("hirdforge-%s-%d", in.Agent, time.Now().UnixNano())
			}
			gw.lastSessionMu.Lock()
			gw.lastSession[in.Agent] = sessionID
			gw.lastSessionMu.Unlock()
			gw.addEvent("message", in.Agent, fmt.Sprintf("Message sent to %s", in.Agent))

			go func(agentName string, content string, rawSessionID string, explicitSource string) {
				sessionID := strings.TrimSpace(rawSessionID)
				if sessionID == "" {
					sessionID = fmt.Sprintf("mcp-%s-%d", agentName, time.Now().UnixNano())
				}
				sess := gw.sessionStore.ensureSession(sessionID, agentName)
				if sess.TaskSummary == "" {
					sess.TaskSummary = extractTaskSummary(content)
				}
				if sess.TaskRef == "" {
					sess.TaskRef = extractTaskRef(content)
				}
				if explicitSource != "" {
					sess.Source = explicitSource
				}
				agentCtx, agentCancel := context.WithTimeout(context.Background(), streamTimeout)
				defer agentCancel()
				gw.setActiveRequest(agentName, sessionID, agentCancel)
				defer gw.clearActiveRequest(agentName, agentCancel)

				body, _ := json.Marshal(agentMessageRequest{Content: content, SessionID: sessionID})
				uReq, err := http.NewRequestWithContext(agentCtx, http.MethodPost, strings.TrimRight(agent.URL, "/")+"/message", bytes.NewReader(body))
				if err != nil {
					log.Printf("async message: failed to create request for %s: %v", agentName, err)
					return
				}
				uReq.Header.Set("Content-Type", "application/json")
				uResp, err := streamClient.Do(uReq)
				if err != nil {
					log.Printf("async message: upstream request failed for %s: %v", agentName, err)
					return
				}
				defer uResp.Body.Close()
				var contentBuf strings.Builder
				scanner := bufio.NewScanner(uResp.Body)
				scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
				for scanner.Scan() {
					line := strings.TrimSpace(scanner.Text())
					if strings.HasPrefix(line, "data:") {
						payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
						var evt map[string]interface{}
						if json.Unmarshal([]byte(payload), &evt) == nil {
							typ, _ := evt["type"].(string)
							gw.applyWorkspaceEvent(agentName, evt)
							if typ == "content" {
								if c, ok := evt["content"].(string); ok {
									cleaned := controlTokenRE.ReplaceAllString(c, "")
									if cleaned != "" {
										contentBuf.WriteString(cleaned)
									}
								}
							}
							if typ == "done" {
								cleaned := thinkTagRE.ReplaceAllString(contentBuf.String(), "")
								gw.delegateResults.Store(sessionID, &delegateResult{
									Agent:     agentName,
									SessionID: sessionID,
									Content:   cleaned,
									Done:      true,
									CreatedAt: time.Now(),
								})
							}
						}
					}
				}
				if err := scanner.Err(); err != nil {
					cleaned := thinkTagRE.ReplaceAllString(contentBuf.String(), "")
					gw.delegateResults.Store(sessionID, &delegateResult{
						Agent:     agentName,
						SessionID: sessionID,
						Content:   cleaned,
						Done:      true,
						Error:     err.Error(),
						CreatedAt: time.Now(),
					})
				}
				if _, loaded := gw.delegateResults.Load(sessionID); !loaded {
					cleaned := thinkTagRE.ReplaceAllString(contentBuf.String(), "")
					gw.delegateResults.Store(sessionID, &delegateResult{
						Agent:     agentName,
						SessionID: sessionID,
						Content:   cleaned,
						Done:      true,
						Error:     "stream terminated before completion",
						CreatedAt: time.Now(),
					})
				}
			}(in.Agent, in.Content, sessionID, in.Source)

			writeJSON(w, http.StatusOK, map[string]string{"status": "queued", "session_id": sessionID, "agent": in.Agent})
			return
		}

		var in messageReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		agent, ok := gw.getAgent(in.Agent)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
			return
		}
		sessionID := strings.TrimSpace(in.SessionID)
		gw.injectionMu.Lock()
		paused := gw.pausedAgents[in.Agent]
		gw.injectionMu.Unlock()
		if detectSessionSource(sessionID) == "cronjob" && paused {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"content\":\"Agent %s is paused by Sovereign. Skipping task.\"}\n\n", in.Agent)
			fmt.Fprintf(w, "data: {\"done\":true}\n\n")
			gw.addEvent("agent_paused_skip", in.Agent, fmt.Sprintf("CronJob poll skipped — %s is paused", in.Agent))
			return
		}
		if !agent.Healthy {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent is unhealthy"})
			return
		}
		if sessionID == "" {
			sessionID = fmt.Sprintf("hirdforge-%s-%d", in.Agent, time.Now().UnixNano())
		}
		sess := gw.sessionStore.ensureSession(sessionID, in.Agent)
		if sess.TaskSummary == "" {
			sess.TaskSummary = extractTaskSummary(in.Content)
		}
		if sess.TaskRef == "" {
			sess.TaskRef = extractTaskRef(in.Content)
		}
		gw.lastSessionMu.Lock()
		gw.lastSession[in.Agent] = sessionID
		gw.lastSessionMu.Unlock()
		gw.addEvent("message", in.Agent, fmt.Sprintf("Message sent to %s", in.Agent))

		gw.proxyMessageSSE(w, streamClient, agent, in.Agent, in.Content, sessionID)
	})

	mux.HandleFunc("/api/v1/message/regenerate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			Agent string `json:"agent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		in.Agent = strings.TrimSpace(in.Agent)
		if in.Agent == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent is required"})
			return
		}
		agent, ok := gw.getAgent(in.Agent)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
			return
		}
		if !agent.Healthy {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent is unhealthy"})
			return
		}
		gw.lastSessionMu.RLock()
		prevSessionID := gw.lastSession[in.Agent]
		gw.lastSessionMu.RUnlock()
		if strings.TrimSpace(prevSessionID) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no previous session found for agent"})
			return
		}
		sess, ok := gw.sessionStore.get(prevSessionID)
		if !ok || len(sess.Messages) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no messages in session to regenerate"})
			return
		}
		var lastUser string
		for i := len(sess.Messages) - 1; i >= 0; i-- {
			if sess.Messages[i].Role == "user" {
				lastUser = sess.Messages[i].Content
				break
			}
		}
		if strings.TrimSpace(lastUser) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no user message found to regenerate"})
			return
		}
		gw.stopAgent(in.Agent)
		newSessionID := fmt.Sprintf("hirdforge-%s-%d", in.Agent, time.Now().UnixNano())
		gw.sessionStore.ensureSession(newSessionID, in.Agent)
		gw.lastSessionMu.Lock()
		gw.lastSession[in.Agent] = newSessionID
		gw.lastSessionMu.Unlock()
		gw.addEvent("message_regenerated", in.Agent, fmt.Sprintf("Regenerating last message for %s", in.Agent))
		gw.proxyMessageSSE(w, streamClient, agent, in.Agent, lastUser, newSessionID)
	})
}

func (gw *gateway) proxyMessageSSE(w http.ResponseWriter, streamClient *http.Client, agent *Agent, agentName, content, sessionID string) {
	agentCtx, agentCancel := context.WithTimeout(context.Background(), streamTimeout)
	defer agentCancel()
	gw.setActiveRequest(agentName, sessionID, agentCancel)
	defer gw.clearActiveRequest(agentName, agentCancel)

	body, _ := json.Marshal(agentMessageRequest{Content: content, SessionID: sessionID})
	uReq, err := http.NewRequestWithContext(agentCtx, http.MethodPost, strings.TrimRight(agent.URL, "/")+"/message", bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to create upstream request"})
		return
	}
	uReq.Header.Set("Content-Type", "application/json")
	uResp, err := streamClient.Do(uReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream request failed"})
		return
	}
	if uResp.StatusCode == 429 {
		b, _ := io.ReadAll(io.LimitReader(uResp.Body, 4096))
		uResp.Body.Close()
		log.Printf("rate limit: upstream agent returned 429 for %s", agentName)
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate_limited", "detail": strings.TrimSpace(string(b))})
		return
	}
	if uResp.StatusCode < 200 || uResp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(uResp.Body, 4096))
		uResp.Body.Close()
		writeJSON(w, uResp.StatusCode, map[string]string{"error": strings.TrimSpace(string(b))})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		uResp.Body.Close()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	keepaliveDone := make(chan struct{})
	defer close(keepaliveDone)
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				fmt.Fprintf(w, ": keepalive\n\n")
				flusher.Flush()
			case <-keepaliveDone:
				return
			case <-agentCtx.Done():
				return
			}
		}
	}()

	reader := bufio.NewReader(uResp.Body)
	var contentBuf strings.Builder
	var doneEvt map[string]interface{}
	var saveOnce sync.Once
	processInjectionQueue := func(baseSessionID string) {
		gw.injectionMu.Lock()
		pending := gw.injections[agentName]
		if len(pending) == 0 {
			gw.injectionMu.Unlock()
			return
		}
		next := pending[0]
		gw.injections[agentName] = pending[1:]
		gw.injectionMu.Unlock()
		injSessionID := strings.TrimSpace(next.SessionID)
		if injSessionID == "" {
			injSessionID = baseSessionID
		}
		go func() {
			injBody, _ := json.Marshal(map[string]string{
				"content":    next.Content,
				"session_id": injSessionID,
			})
			injReq, err := http.NewRequest(http.MethodPost, strings.TrimRight(agent.URL, "/")+"/message", bytes.NewReader(injBody))
			if err != nil {
				log.Printf("injection failed for %s: %v", agentName, err)
				return
			}
			injReq.Header.Set("Content-Type", "application/json")
			gw.addEvent("injection_sent", agentName, fmt.Sprintf("Sovereign injection delivered to %s", agentName))
			resp, err := http.DefaultClient.Do(injReq)
			if err != nil {
				log.Printf("injection request failed for %s: %v", agentName, err)
				return
			}
			defer resp.Body.Close()

			var fullResp strings.Builder
			scanner := bufio.NewScanner(resp.Body)
			scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "data: ") {
					chunk := strings.TrimPrefix(line, "data: ")
					var obj map[string]interface{}
					if json.Unmarshal([]byte(chunk), &obj) == nil {
						if c, ok := obj["content"].(string); ok {
							fullResp.WriteString(c)
						}
					}
				}
			}
			cleaned := thinkTagRE.ReplaceAllString(fullResp.String(), "")
			gw.sessionStore.appendConversation(injSessionID, agentName, next.Content, cleaned)
			gw.addEvent("injection_complete", agentName, fmt.Sprintf("Sovereign injection response from %s", agentName))
		}()
	}
	saveConversation := func() {
		saveOnce.Do(func() {
			raw := contentBuf.String()
			cleaned := thinkTagRE.ReplaceAllString(raw, "")
			gw.sessionStore.appendConversation(sessionID, agentName, content, cleaned)
		})
	}
	drainAgentResponse := func() {
		go func() {
			defer uResp.Body.Close()
			defer agentCancel()
			for {
				line, err := reader.ReadBytes('\n')
				if err != nil {
					break
				}
				_ = line
			}
			saveConversation()
		}()
	}
	forward := func(evt map[string]interface{}) bool {
		b, err := json.Marshal(evt)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	forwardReplaceIfNeeded := func() bool {
		raw := contentBuf.String()
		cleaned := thinkTagRE.ReplaceAllString(raw, "")
		if cleaned != raw && cleaned != "" {
			if !forward(map[string]interface{}{"type": "replace", "content": cleaned, "done": false}) {
				return false
			}
		}
		return true
	}
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			trim := strings.TrimSpace(string(line))
			if strings.HasPrefix(trim, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(trim, "data:"))
				var evt map[string]interface{}
				if json.Unmarshal([]byte(payload), &evt) == nil {
					typ, _ := evt["type"].(string)
					gw.applyWorkspaceEvent(agentName, evt)
					if typ == "content" {
						if c, _ := evt["content"].(string); c != "" {
							cleaned := controlTokenRE.ReplaceAllString(c, "")
							if cleaned == "" {
								goto lineDone
							}
							evt["content"] = cleaned
							contentBuf.WriteString(cleaned)
						}
						if !forward(evt) {
							drainAgentResponse()
							return
						}
						goto lineDone
					}
					if typ, _ := evt["type"].(string); typ == "tool_call" {
						gw.addEvent("tool_call", agentName, "Tool call observed")
						evtBytes, _ := json.Marshal(evt)
						evtBlob := strings.ToLower(string(evtBytes))
						toolName := strings.ToLower(strings.TrimSpace(fmt.Sprint(evt["name"])))
						if toolName == "" {
							if tc, ok := evt["tool_call"].(map[string]interface{}); ok {
								toolName = strings.ToLower(strings.TrimSpace(fmt.Sprint(tc["name"])))
								if toolName == "" {
									if fn, ok := tc["function"].(map[string]interface{}); ok {
										toolName = strings.ToLower(strings.TrimSpace(fmt.Sprint(fn["name"])))
									}
								}
							}
						}
						switch {
						case toolName == "exec" && strings.Contains(evtBlob, "cat /tmp/valhalla-personas"):
							gw.addEvent("skill_loaded", agentName, "Loaded skill file")
						case toolName == "git-clone":
							summary := "Cloning repository"
							if tc, ok := evt["tool_call"].(map[string]interface{}); ok {
								var args map[string]interface{}
								if a, ok := tc["arguments"].(map[string]interface{}); ok {
									args = a
								} else if aStr, ok := tc["arguments"].(string); ok && aStr != "" {
									_ = json.Unmarshal([]byte(aStr), &args)
								}
								if repo, ok := args["repo"].(string); ok && repo != "" {
									parts := strings.Split(repo, "/")
									if len(parts) >= 2 {
										summary = "Cloning " + parts[len(parts)-2] + "/" + parts[len(parts)-1]
									} else {
										summary = "Cloning " + repo
									}
								}
							}
							gw.addEvent("recon_started", agentName, summary)
						case toolName == "gitea" && strings.Contains(evtBlob, "create-pr"):
							summary := "Pull request created"
							if tc, ok := evt["tool_call"].(map[string]interface{}); ok {
								var args map[string]interface{}
								if a, ok := tc["arguments"].(map[string]interface{}); ok {
									args = a
								} else if aStr, ok := tc["arguments"].(string); ok && aStr != "" {
									_ = json.Unmarshal([]byte(aStr), &args)
								}
								if title, ok := args["title"].(string); ok && title != "" {
									summary = "PR: " + title
								}
							}
							gw.addEvent("pr_created", agentName, summary)
						case toolName == "write":
							summary := "Writing file"
							if tc, ok := evt["tool_call"].(map[string]interface{}); ok {
								var args map[string]interface{}
								if a, ok := tc["arguments"].(map[string]interface{}); ok {
									args = a
								} else if aStr, ok := tc["arguments"].(string); ok && aStr != "" {
									_ = json.Unmarshal([]byte(aStr), &args)
								}
								if path, ok := args["path"].(string); ok && path != "" {
									summary = "Writing " + path
								}
							}
							gw.addEvent("tool_call", agentName, summary)
						case toolName == "edit":
							summary := "Editing file"
							if tc, ok := evt["tool_call"].(map[string]interface{}); ok {
								var args map[string]interface{}
								if a, ok := tc["arguments"].(map[string]interface{}); ok {
									args = a
								} else if aStr, ok := tc["arguments"].(string); ok && aStr != "" {
									_ = json.Unmarshal([]byte(aStr), &args)
								}
								if path, ok := args["path"].(string); ok && path != "" {
									summary = "Editing " + path
								}
							}
							gw.addEvent("tool_call", agentName, summary)
						case toolName == "delegate":
							summary := "Delegating task"
							if tc, ok := evt["tool_call"].(map[string]interface{}); ok {
								var args map[string]interface{}
								if a, ok := tc["arguments"].(map[string]interface{}); ok {
									args = a
								} else if aStr, ok := tc["arguments"].(string); ok && aStr != "" {
									_ = json.Unmarshal([]byte(aStr), &args)
								}
								if target, ok := args["agent"].(string); ok && target != "" {
									summary = "Delegating to " + target
								}
							}
							gw.addEvent("tool_call", agentName, summary)
						case toolName == "read" && strings.Contains(strings.ToUpper(string(evtBytes)), "ARCHITECTURE"):
							gw.addEvent("recon_reading", agentName, "Reading ARCHITECTURE.md")
						}
					}
					if typ == "done" {
						doneEvt = evt
						if _, ok := doneEvt["session_id"]; !ok {
							doneEvt["session_id"] = sessionID
						}
						if !forwardReplaceIfNeeded() {
							drainAgentResponse()
							return
						}
						saveConversation()
						processInjectionQueue(sessionID)
						if !forward(doneEvt) {
							drainAgentResponse()
							return
						}
						uResp.Body.Close()
						return
					}
					if !forward(evt) {
						drainAgentResponse()
						return
					}
				}
			}
		lineDone:
		}
		if err == io.EOF {
			if !forwardReplaceIfNeeded() {
				uResp.Body.Close()
				return
			}
			saveConversation()
			processInjectionQueue(sessionID)
			if doneEvt != nil {
				_ = forward(doneEvt)
			} else {
				_ = forward(map[string]interface{}{"type": "done", "done": true, "session_id": sessionID})
			}
			uResp.Body.Close()
			return
		}
		if err != nil {
			uResp.Body.Close()
			return
		}
	}
}
