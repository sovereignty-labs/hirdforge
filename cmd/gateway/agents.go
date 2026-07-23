package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (g *gateway) snapshotAgents() []Agent {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Agent, 0, len(g.order))
	for _, n := range g.order {
		if a := g.agents[n]; a != nil {
			out = append(out, *a)
		}
	}
	return out
}

func (g *gateway) getAgent(name string) (*Agent, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	a, ok := g.agents[name]
	if !ok {
		return nil, false
	}
	cp := *a
	return &cp, true
}

func (g *gateway) agentFleet(name string) string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if agent, ok := g.agents[strings.TrimSpace(name)]; ok && agent != nil {
		if fleet := strings.TrimSpace(agent.Fleet); fleet != "" {
			return fleet
		}
	}
	return normalizeFleetName(g.defaultFleet)
}

func (g *gateway) updateAgent(updated Agent) {
	g.mu.Lock()
	old := g.agents[updated.Name]
	if old == nil {
		g.mu.Unlock()
		return
	}
	changed := old.Healthy != updated.Healthy
	*old = updated
	g.mu.Unlock()
	if changed {
		state := "unhealthy"
		if updated.Healthy {
			state = "healthy"
		}
		msg := fmt.Sprintf("%s became %s", updated.Name, state)
		log.Printf("agent health changed: %s", msg)
		g.addEvent("health_change", updated.Name, msg)
	}
}

func (g *gateway) refreshAgentHealth() {
	client := &http.Client{Timeout: 3 * time.Second}
	g.mu.RLock()
	names := append([]string(nil), g.order...)
	urls := make(map[string]string, len(g.order))
	roles := make(map[string]string, len(g.order))
	warbands := make(map[string]string, len(g.order))
	for _, n := range g.order {
		urls[n] = g.agents[n].URL
		roles[n] = g.agents[n].Role
		warbands[n] = g.agents[n].Fleet
	}
	g.mu.RUnlock()

	for _, name := range names {
		updated := Agent{Name: name, URL: urls[name], Tools: []string{}}
		updated.Role = roles[name]
		updated.Fleet = warbands[name]
		h, err := queryAgentHealth(client, urls[name])
		if err == nil {
			updated.Healthy = true
			updated.Model = h.Model
			updated.Tools = h.Tools
			updated.UptimeSeconds = h.UptimeSeconds
			updated.RequestsServed = h.RequestsServed
			updated.ToolCallsMade = h.ToolCallsMade
		} else {
			updated.Healthy = false
			updated.Model = ""
			updated.Tools = []string{}
			updated.UptimeSeconds = 0
			updated.RequestsServed = 0
			updated.ToolCallsMade = 0
		}
		g.updateAgent(updated)
	}
}

func queryAgentHealth(client *http.Client, baseURL string) (agentHealthResponse, error) {
	var h agentHealthResponse
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/health")
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return h, fmt.Errorf("status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return h, err
	}
	return h, nil
}

func registerAgentRoutes(mux *http.ServeMux, gw *gateway, proxyClient *http.Client) {
	mux.HandleFunc("/api/v1/agents/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/agents/")
		switch {
		case strings.HasSuffix(path, "/objective"):
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/objective")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			if _, ok := gw.getAgent(name); !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown agent"})
				return
			}
			sessionID := gw.activeSessionID(name)
			if sessionID == "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			sess, ok := gw.sessionStore.get(sessionID)
			if !ok {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			objective := objectiveFromSession(sess)
			if objective == "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"objective": objective})
		case strings.HasSuffix(path, "/workspace"):
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/workspace")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			if _, ok := gw.getAgent(name); !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown agent"})
				return
			}
			writeJSON(w, http.StatusOK, gw.projector.Get(name))
		case strings.HasSuffix(path, "/inject"):
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/inject")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			if _, ok := gw.getAgent(name); !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
				return
			}
			var req struct {
				Content   string `json:"content"`
				SessionID string `json:"session_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Content) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content is required"})
				return
			}
			gw.injectionMu.Lock()
			gw.injections[name] = append(gw.injections[name], InjectionMessage{
				Agent:     name,
				Content:   req.Content,
				SessionID: req.SessionID,
				QueuedAt:  time.Now().Unix(),
			})
			gw.injectionMu.Unlock()
			gw.addEvent("injection_queued", name, fmt.Sprintf("Sovereign queued injection for %s", name))
			writeJSON(w, http.StatusOK, map[string]string{"status": "queued", "agent": name})
		case strings.HasSuffix(path, "/session/new"):
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/session/new")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			if _, ok := gw.getAgent(name); !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown agent"})
				return
			}
			gw.stopAgent(name)
			newSessionID := fmt.Sprintf("hirdforge-%s-%d", name, time.Now().UnixNano())
			gw.lastSessionMu.Lock()
			gw.lastSession[name] = newSessionID
			gw.lastSessionMu.Unlock()
			gw.addEvent("session_reset", name, fmt.Sprintf("Session reset for %s", name))
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "agent": name, "session_id": newSessionID})
		case strings.HasSuffix(path, "/pause"):
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/pause")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			if _, ok := gw.getAgent(name); !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
				return
			}
			gw.injectionMu.Lock()
			gw.pausedAgents[name] = true
			gw.injectionMu.Unlock()
			gw.addEvent("agent_paused", name, fmt.Sprintf("%s paused by Sovereign", name))
			writeJSON(w, http.StatusOK, map[string]string{"status": "paused", "agent": name})
		case strings.HasSuffix(path, "/resume"):
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/resume")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			if _, ok := gw.getAgent(name); !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
				return
			}
			gw.injectionMu.Lock()
			delete(gw.pausedAgents, name)
			gw.injectionMu.Unlock()
			gw.addEvent("agent_resumed", name, fmt.Sprintf("%s resumed by Sovereign", name))
			writeJSON(w, http.StatusOK, map[string]string{"status": "resumed", "agent": name})
		case strings.HasSuffix(path, "/state"):
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/state")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			if _, ok := gw.getAgent(name); !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
				return
			}
			state := AgentState{Name: name}
			gw.injectionMu.Lock()
			state.Paused = gw.pausedAgents[name]
			gw.injectionMu.Unlock()
			gw.arMu.RLock()
			if ar, ok := gw.activeRequests[name]; ok {
				state.Active = true
				state.SessionID = ar.SessionID
				state.Source = detectSessionSource(ar.SessionID)
				state.Since = ar.StartedAt.Unix()
				if sess, ok := gw.sessionStore.get(ar.SessionID); ok {
					state.TaskRef = sess.TaskRef
					if sess.Source != "" {
						state.Source = sess.Source
					}
				}
			}
			gw.arMu.RUnlock()
			writeJSON(w, http.StatusOK, state)
		case strings.HasSuffix(path, "/configure"):
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/configure")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			if _, ok := gw.getAgent(name); !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
				return
			}
			var in agentConfigureRequest
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
				return
			}
			if strings.TrimSpace(gw.giteaURL) == "" {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "missing Gitea URL"})
				return
			}
			if gw.giteaToken == "" {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "missing Gitea token"})
				return
			}
			prURL, err := createGitOpsAgentConfigPR(proxyClient, gw.giteaURL, gw.giteaToken, gw.k8s.podNS, name, in)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "pr_url": prURL})
		case strings.HasSuffix(path, "/files"):
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/files")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			agent, ok := gw.getAgent(name)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
				return
			}
			uReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, strings.TrimRight(agent.URL, "/")+"/api/v1/files", nil)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to create upstream request"})
				return
			}
			uResp, err := proxyClient.Do(uReq)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream request failed"})
				return
			}
			defer uResp.Body.Close()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(uResp.StatusCode)
			_, _ = io.Copy(w, io.LimitReader(uResp.Body, 4<<20))
		case strings.HasSuffix(path, "/stop"):
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/stop")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			stopped := gw.stopAgent(name)
			writeJSON(w, http.StatusOK, map[string]interface{}{"status": "stopped", "agent": name, "active": stopped})
		case strings.HasSuffix(path, "/restart"):
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/restart")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			if _, ok := gw.getAgent(name); !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
				return
			}
			if gw.k8s == nil || !gw.k8s.enabled {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "kubernetes integration disabled"})
				return
			}
			labelSelector := fmt.Sprintf("asgard.io/agent=%s", name)
			podListPath := fmt.Sprintf("/api/v1/namespaces/%s/pods?labelSelector=%s", url.PathEscape(gw.k8s.podNS), url.QueryEscape(labelSelector))
			_, podBody, _, err := gw.k8s.getJSONWithStatus(podListPath)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to query pods"})
				return
			}
			items := asSlice(podBody["items"])
			if len(items) == 0 {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "no pod found for agent"})
				return
			}
			var deletedPods []string
			for _, item := range items {
				podName := asString(asMap(asMap(item)["metadata"])["name"])
				deletePath := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s", url.PathEscape(gw.k8s.podNS), url.PathEscape(podName))
				resp, err := gw.k8s.do(http.MethodDelete, deletePath, nil)
				if err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "kubernetes request failed"})
					return
				}
				resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					deletedPods = append(deletedPods, podName)
				}
			}
			log.Printf("pod restart: agent=%s pods=%v triggered_by=%s", name, deletedPods, r.RemoteAddr)
			writeJSON(w, http.StatusOK, map[string]interface{}{"restarted": true, "agent": name, "pods": deletedPods})
		default:
			http.NotFound(w, r)
		}
	})

	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, gw.snapshotAgents())
	})

	mux.HandleFunc("/api/v1/agents/stop-all", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		n := gw.stopAllAgents()
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "stopped", "count": n})
	})

	mux.HandleFunc("/api/v1/fleet/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, gw.fleetState(r.URL.Query().Get("warband")))
	})
}
