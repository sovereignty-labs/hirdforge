package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

type routeDeps struct {
	proxyClient  *http.Client
	streamClient *http.Client
	giteaClient  *http.Client
	lockboxURL   string
	giteaRepo    string
}

func registerRoutes(mux *http.ServeMux, gw *gateway, deps routeDeps) {
	mux.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/ui/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, uiHTML)
	})

	gw.registerWebsocketRoutes(mux)
	registerAgentRoutes(mux, gw, deps.proxyClient)
	registerSessionRoutes(mux, gw)
	registerApprovalRoutes(mux, gw, deps.lockboxURL)
	registerSettingsRoutes(mux, gw)
	registerClusterRoutes(mux, gw)
	registerGiteaRoutes(mux, gw, deps.giteaClient, deps.giteaRepo)
	registerDelegationRoutes(mux, gw, deps.proxyClient, deps.streamClient)
	registerSeidrRoutes(mux, gw)

	mux.HandleFunc("/api/v1/whoami", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		user := strings.TrimSpace(r.Header.Get("X-Forwarded-User"))
		email := strings.TrimSpace(r.Header.Get("X-Forwarded-Email"))
		if user == "" {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"user":          "Sovereign",
				"role":          "sovereign",
				"authenticated": false,
			})
			return
		}
		resp := map[string]interface{}{
			"user":          user,
			"role":          "sovereign",
			"authenticated": true,
		}
		if email != "" {
			resp["email"] = email
		}
		writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("/api/v1/ping", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "agent": "gateway", "timestamp": time.Now().Unix()})
	})

	mux.HandleFunc("/api/v1/metrics/fleet", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		agents := gw.snapshotAgents()
		type agentMetrics struct {
			Name         string `json:"name"`
			Reachable    bool   `json:"reachable"`
			MetricsBytes int    `json:"metrics_bytes"`
		}
		metricsList := make([]agentMetrics, 0, len(agents))
		reachable := 0
		for _, a := range agents {
			m := agentMetrics{Name: a.Name, Reachable: false, MetricsBytes: 0}
			url := strings.TrimRight(a.URL, "/") + "/metrics"
			req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
			if err == nil {
				client := &http.Client{Timeout: 2 * time.Second}
				resp, err := client.Do(req)
				if err == nil {
					if resp.StatusCode == 200 {
						body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
						m.Reachable = true
						m.MetricsBytes = len(body)
						reachable++
					}
					_ = resp.Body.Close()
				}
			}
			metricsList = append(metricsList, m)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"agents":       metricsList,
			"timestamp":    time.Now().UTC().Format(time.RFC3339),
			"total_agents": len(agents),
			"reachable":    reachable,
		})
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		agents := gw.snapshotAgents()
		healthy := 0
		for _, a := range agents {
			if a.Healthy {
				healthy++
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ready", "agents": len(agents), "healthy": healthy})
	})
}

func registerSessionRoutes(mux *http.ServeMux, gw *gateway) {
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		agent := strings.TrimSpace(r.URL.Query().Get("agent"))
		source := strings.TrimSpace(r.URL.Query().Get("source"))
		writeJSON(w, http.StatusOK, gw.sessionStore.list(agent, source))
	})

	mux.HandleFunc("/api/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
		if path == "" || strings.Contains(path, "//") {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(path, "/messages") {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			sessionID := strings.TrimSuffix(path, "/messages")
			sessionID = strings.Trim(sessionID, "/")
			if sessionID == "" || strings.Contains(sessionID, "/") {
				http.NotFound(w, r)
				return
			}
			var in ChatMessage
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
				return
			}
			if strings.TrimSpace(in.Role) == "" || strings.TrimSpace(in.Agent) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "role and agent are required"})
				return
			}
			id := gw.sessionStore.appendMessage(sessionID, in.Agent, in)
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "id": id})
			return
		}
		sessionID := strings.Trim(path, "/")
		if sessionID == "" || strings.Contains(sessionID, "/") {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			sess, ok := gw.sessionStore.get(sessionID)
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
				return
			}
			writeJSON(w, http.StatusOK, sess)
		case http.MethodDelete:
			if !gw.sessionStore.delete(sessionID) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": sessionID})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func registerApprovalRoutes(mux *http.ServeMux, gw *gateway, lockboxURL string) {
	proxyLockbox := func(w http.ResponseWriter, r *http.Request, method, path string, body []byte) {
		base := strings.TrimSpace(lockboxURL)
		if base == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "lockbox not configured"})
			return
		}
		var bodyReader io.Reader
		if body != nil {
			bodyReader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(r.Context(), method, strings.TrimRight(base, "/")+path, bodyReader)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		if contentType := strings.TrimSpace(resp.Header.Get("Content-Type")); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 4<<20))
	}

	mux.HandleFunc("/api/v1/approvals", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		proxyLockbox(w, r, http.MethodGet, "/queues", nil)
	})

	mux.HandleFunc("/api/v1/approvals/approve", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			QueueID string `json:"queue_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		in.QueueID = strings.TrimSpace(in.QueueID)
		if in.QueueID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "queue_id is required"})
			return
		}
		huntID, err := resolveApprovalHuntID(r.Context(), lockboxURL, in.QueueID)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "not found") {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "queue item not found"})
			} else {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			}
			return
		}
		payload, _ := json.Marshal(map[string]interface{}{
			"hunt_id":  huntID,
			"queue_id": in.QueueID,
			"approved": true,
		})
		proxyLockbox(w, r, http.MethodPost, "/approve-write", payload)
	})

	mux.HandleFunc("/api/v1/approvals/reject", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			QueueID string `json:"queue_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		in.QueueID = strings.TrimSpace(in.QueueID)
		if in.QueueID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "queue_id is required"})
			return
		}
		huntID, err := resolveApprovalHuntID(r.Context(), lockboxURL, in.QueueID)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "not found") {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "queue item not found"})
			} else {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			}
			return
		}
		payload, _ := json.Marshal(map[string]interface{}{
			"hunt_id":  huntID,
			"queue_id": in.QueueID,
			"approved": false,
		})
		proxyLockbox(w, r, http.MethodPost, "/approve-write", payload)
	})

	mux.HandleFunc("/api/v1/approvals/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/approvals/"), "/")
		parts := strings.Split(path, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] != "revise" {
			http.NotFound(w, r)
			return
		}
		queueID := strings.TrimSpace(parts[0])
		var in struct {
			Note string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		in.Note = strings.TrimSpace(in.Note)
		if in.Note == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "note is required"})
			return
		}
		huntID, err := resolveApprovalHuntID(r.Context(), lockboxURL, queueID)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "not found") {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "queue item not found"})
			} else {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			}
			return
		}
		payload, _ := json.Marshal(map[string]interface{}{
			"hunt_id":  huntID,
			"queue_id": queueID,
			"note":     in.Note,
		})
		proxyLockbox(w, r, http.MethodPost, "/revise", payload)
	})
}

func registerSettingsRoutes(mux *http.ServeMux, gw *gateway) {
	mux.HandleFunc("/api/v1/settings/agents/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/settings/agents/"), "/")
		parts := strings.Split(path, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] != "model" {
			http.NotFound(w, r)
			return
		}
		var in struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Model) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model is required"})
			return
		}
		if err := updateAgentModel(gw.k8s, parts[0], strings.TrimSpace(in.Model)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"agent":  parts[0],
			"model":  strings.TrimSpace(in.Model),
			"status": "updated",
		})
	})

	mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, gw.settings.All())
		case http.MethodPut:
			var in map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
				return
			}
			gw.settings.SetBulk(in)
			writeJSON(w, http.StatusOK, gw.settings.All())
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/api/v1/settings/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		key := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/settings/"), "/")
		if key == "" || strings.Contains(key, "/") {
			http.NotFound(w, r)
			return
		}
		var raw interface{}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		if wrapped, ok := raw.(map[string]interface{}); ok {
			if value, exists := wrapped["value"]; exists && len(wrapped) == 1 {
				raw = value
			}
		}
		gw.settings.Set(key, raw)
		writeJSON(w, http.StatusOK, gw.settings.All())
	})
}

func registerSeidrRoutes(mux *http.ServeMux, gw *gateway) {
	mux.HandleFunc("/api/v1/seidr/memories", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(gw.seidrURL) == "" {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Seidr unavailable"})
			return
		}
		agent := strings.TrimSpace(r.URL.Query().Get("agent"))
		query := strings.TrimSpace(r.URL.Query().Get("query"))
		limit := clampInt(asInt(r.URL.Query().Get("limit")), 20, 100)
		payload, _ := json.Marshal(map[string]interface{}{
			"query_text": query,
			"agent_name": agent,
			"n_results":  limit,
		})
		client := &http.Client{Timeout: 10 * time.Second}
		req, err := http.NewRequest(http.MethodPost, strings.TrimRight(gw.seidrURL, "/")+"/query", bytes.NewReader(payload))
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		for k, vv := range resp.Header {
			if strings.EqualFold(k, "Content-Type") && len(vv) > 0 {
				w.Header().Set("Content-Type", vv[0])
				break
			}
		}
		w.WriteHeader(resp.StatusCode)
		if len(respBody) > 0 {
			_, _ = w.Write(respBody)
		}
	})

	mux.HandleFunc("/api/v1/seidr/memories/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(gw.seidrURL) == "" {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Seidr unavailable"})
			return
		}
		id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/seidr/memories/"), "/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		agent := strings.TrimSpace(r.URL.Query().Get("agent"))
		client := &http.Client{Timeout: 10 * time.Second}
		u := strings.TrimRight(gw.seidrURL, "/") + "/memories/" + url.PathEscape(id)
		if agent != "" {
			u += "?agent_name=" + url.QueryEscape(agent)
		}
		req, err := http.NewRequest(http.MethodDelete, u, nil)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		for k, vv := range resp.Header {
			if strings.EqualFold(k, "Content-Type") && len(vv) > 0 {
				w.Header().Set("Content-Type", vv[0])
				break
			}
		}
		w.WriteHeader(resp.StatusCode)
		if len(respBody) > 0 {
			_, _ = w.Write(respBody)
		}
	})
}

func registerGiteaRoutes(mux *http.ServeMux, gw *gateway, giteaClient *http.Client, giteaRepo string) {
	mux.HandleFunc("/api/v1/gitea/commits", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(gw.giteaURL) == "" {
			writeJSON(w, http.StatusOK, []giteaCommit{})
			return
		}
		var resp []map[string]interface{}
		err := fetchGiteaJSON(giteaClient, gw.giteaURL, gw.giteaToken, "/api/v1/repos/"+giteaRepo+"/commits?limit=10", &resp)
		if err != nil {
			writeJSON(w, http.StatusOK, []giteaCommit{})
			return
		}
		out := make([]giteaCommit, 0, len(resp))
		for _, c := range resp {
			commit := asMap(c["commit"])
			author := asMap(commit["author"])
			sha := asString(c["sha"])
			msg := asString(commit["message"])
			if i := strings.Index(msg, "\n"); i >= 0 {
				msg = msg[:i]
			}
			out = append(out, giteaCommit{
				SHA:     sha,
				Message: msg,
				Author:  asString(author["name"]),
				Date:    asString(author["date"]),
			})
		}
		if len(out) > 10 {
			out = out[:10]
		}
		writeJSON(w, http.StatusOK, out)
	})

	mux.HandleFunc("/api/v1/history/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit := clampInt(asInt(r.URL.Query().Get("limit")), 50, 100)
		agent := strings.TrimSpace(r.URL.Query().Get("agent"))
		outcome := strings.TrimSpace(r.URL.Query().Get("outcome"))
		writeJSON(w, http.StatusOK, fetchTaskHistory(giteaClient, gw.giteaURL, gw.giteaToken, "kit/hirdforge-tasks", agent, outcome, limit))
	})

	mux.HandleFunc("/api/v1/history/deployments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit := clampInt(asInt(r.URL.Query().Get("limit")), 20, 100)
		writeJSON(w, http.StatusOK, fetchDeploymentHistory(giteaClient, gw.giteaURL, gw.giteaToken, limit))
	})

	mux.HandleFunc("/api/v1/gitea/prs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(gw.giteaURL) == "" {
			writeJSON(w, http.StatusOK, []giteaPRInfo{})
			return
		}
		gw.reposMu.RLock()
		repos := append([]string(nil), gw.repos...)
		gw.reposMu.RUnlock()
		if len(repos) == 0 {
			writeJSON(w, http.StatusOK, []giteaPRInfo{})
			return
		}
		type query struct {
			repo  string
			state string
			limit int
		}
		queries := make([]query, 0, len(repos)*2)
		for _, repo := range repos {
			queries = append(queries,
				query{repo: repo, state: "open", limit: 20},
				query{repo: repo, state: "closed", limit: 10},
			)
		}
		out := make([]giteaPRInfo, 0, 60)
		for _, q := range queries {
			path := fmt.Sprintf("/api/v1/repos/%s/pulls?state=%s&sort=newest&limit=%d", q.repo, q.state, q.limit)
			var prs []map[string]interface{}
			status, body, err := giteaGetJSONWithStatus(giteaClient, gw.giteaURL, gw.giteaToken, path, &prs)
			if err != nil {
				log.Printf("gitea prs: skipping %s: %v", q.repo, err)
				continue
			}
			if status < 200 || status >= 300 {
				log.Printf("gitea prs: skipping %s (%s): status %d: %s", q.repo, q.state, status, strings.TrimSpace(string(body)))
				continue
			}
			for _, pr := range prs {
				number := asInt64(pr["number"])
				labelsRaw := asSlice(pr["labels"])
				labels := make([]string, 0, len(labelsRaw))
				for _, l := range labelsRaw {
					name := asString(asMap(l)["name"])
					if name != "" {
						labels = append(labels, name)
					}
				}
				approvals := int64(0)
				if q.state == "open" && number > 0 {
					var reviews []map[string]interface{}
					reviewPath := fmt.Sprintf("/api/v1/repos/%s/pulls/%d/reviews", q.repo, number)
					if reviewStatus, _, err := giteaGetJSONWithStatus(giteaClient, gw.giteaURL, gw.giteaToken, reviewPath, &reviews); err == nil && reviewStatus >= 200 && reviewStatus < 300 {
						latestByReviewer := map[string]string{}
						for _, review := range reviews {
							user := asString(asMap(review["user"])["login"])
							state := strings.ToUpper(asString(review["state"]))
							if user == "" || state == "" {
								continue
							}
							latestByReviewer[user] = state
						}
						for _, state := range latestByReviewer {
							if state == "APPROVED" {
								approvals++
							}
						}
					}
				}
				out = append(out, giteaPRInfo{
					Number:    number,
					Title:     asString(pr["title"]),
					State:     asString(pr["state"]),
					User:      asString(asMap(pr["user"])["login"]),
					Repo:      q.repo,
					Base:      asString(asMap(pr["base"])["ref"]),
					Head:      asString(asMap(pr["head"])["ref"]),
					Body:      truncateRunes(asString(pr["body"]), 200),
					CreatedAt: asString(pr["created_at"]),
					UpdatedAt: asString(pr["updated_at"]),
					HTMLURL:   asString(pr["html_url"]),
					Labels:    labels,
					Approvals: approvals,
					Mergeable: asBool(pr["mergeable"]),
				})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
		writeJSON(w, http.StatusOK, out)
	})

	registerGiteaPRRoutes(mux, gw, giteaClient)

	mux.HandleFunc("/api/v1/gitea/repos", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(gw.giteaURL) == "" {
			writeJSON(w, http.StatusOK, []giteaRepoListItem{})
			return
		}
		all := make([]map[string]interface{}, 0, 40)
		{
			var repos []map[string]interface{}
			status, body, err := giteaGetJSONWithStatus(giteaClient, gw.giteaURL, gw.giteaToken, "/api/v1/user/repos?limit=20", &repos)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Gitea unreachable"})
				return
			}
			if status < 200 || status >= 300 {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(body))})
				return
			}
			all = append(all, repos...)
		}
		{
			var repos []map[string]interface{}
			status, _, err := giteaGetJSONWithStatus(giteaClient, gw.giteaURL, gw.giteaToken, "/api/v1/users/kit/repos?limit=20", &repos)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Gitea unreachable"})
				return
			}
			if status >= 200 && status < 300 {
				all = append(all, repos...)
			}
		}
		{
			var repos []map[string]interface{}
			status, _, err := giteaGetJSONWithStatus(giteaClient, gw.giteaURL, gw.giteaToken, "/api/v1/orgs/warband/repos?limit=20", &repos)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Gitea unreachable"})
				return
			}
			if status >= 200 && status < 300 {
				all = append(all, repos...)
			}
		}
		seen := map[string]giteaRepoListItem{}
		for _, repo := range all {
			full := asString(repo["full_name"])
			if full == "" {
				continue
			}
			seen[full] = giteaRepoListItem{
				Name:        asString(repo["name"]),
				Owner:       asString(asMap(repo["owner"])["login"]),
				FullName:    full,
				Description: asString(repo["description"]),
				Language:    asString(repo["language"]),
				OpenPRs:     asInt64(repo["open_pr_counter"]),
				Stars:       asInt64(repo["stars_count"]),
				HTMLURL:     asString(repo["html_url"]),
			}
		}
		out := make([]giteaRepoListItem, 0, len(seen))
		for _, r := range seen {
			out = append(out, r)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].FullName < out[j].FullName })
		writeJSON(w, http.StatusOK, out)
	})

	mux.HandleFunc("/api/v1/gitea/repo", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(gw.giteaURL) == "" {
			writeJSON(w, http.StatusOK, map[string]interface{}{})
			return
		}
		var repo map[string]interface{}
		err := fetchGiteaJSON(giteaClient, gw.giteaURL, gw.giteaToken, "/api/v1/repos/"+giteaRepo, &repo)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{})
			return
		}
		out := giteaRepoInfo{
			Name:          asString(repo["name"]),
			Stars:         asInt64(repo["stars_count"]),
			Forks:         asInt64(repo["forks_count"]),
			OpenIssues:    asInt64(repo["open_issues_count"]),
			Size:          asInt64(repo["size"]),
			DefaultBranch: asString(repo["default_branch"]),
		}
		writeJSON(w, http.StatusOK, out)
	})
}
