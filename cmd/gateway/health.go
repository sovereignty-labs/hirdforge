package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type fleetHealthAgent struct {
	Name          string `json:"name"`
	Healthy       bool   `json:"healthy"`
	Model         string `json:"model"`
	UptimeSeconds int    `json:"uptime_seconds"`
}

type fleetHealthResponse struct {
	Total     int                `json:"total"`
	Healthy   int                `json:"healthy"`
	Unhealthy int                `json:"unhealthy"`
	Agents    []fleetHealthAgent `json:"agents"`
}

type repoPRHealth struct {
	Repo         string `json:"repo"`
	Open         int    `json:"open"`
	Closed       int    `json:"closed"`
	RecentMerges int    `json:"recent_merges"`
}

type prsHealthResponse struct {
	Repos     []repoPRHealth `json:"repos"`
	TotalOpen int            `json:"total_open"`
}

type tasksHealthResponse struct {
	Total    int               `json:"total"`
	ByStatus map[string]int    `json:"by_status"`
	ByAgent  map[string]int    `json:"by_agent"`
}

type branchesHealthRepo struct {
	Repo           string `json:"repo"`
	Branches       int    `json:"branches"`
	StaleBranches  int    `json:"stale_branches"`
}

type branchesHealthResponse struct {
	Repos []branchesHealthRepo `json:"repos"`
}

func (gw *gateway) registerHealthEndpoints(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/health/fleet", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		agents := gw.snapshotAgents()
		resp := fleetHealthResponse{Agents: make([]fleetHealthAgent, 0, len(agents))}
		for _, a := range agents {
			fa := fleetHealthAgent{
				Name:          a.Name,
				Healthy:       a.Healthy,
				Model:         a.Model,
				UptimeSeconds: a.UptimeSeconds,
			}
			resp.Agents = append(resp.Agents, fa)
			resp.Total++
			if a.Healthy {
				resp.Healthy++
			} else {
				resp.Unhealthy++
			}
		}
		writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("/api/v1/health/prs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		base := strings.TrimSpace(gw.giteaURL)
		if base == "" {
			writeJSON(w, http.StatusOK, prsHealthResponse{Repos: []repoPRHealth{}})
			return
		}
		client := &http.Client{Timeout: 10 * time.Second}
		repos := []string{"gitea_admin/project_valhalla", "kit/valhalla-infra", "kit/hirdforge-personas", "kit/hirdforge-tasks"}
		out := prsHealthResponse{Repos: make([]repoPRHealth, 0, len(repos))}
		for _, repo := range repos {
			var openPRs []map[string]interface{}
			if _, _, err := giteaGetJSONWithStatus(client, base, gw.giteaToken, "/api/v1/repos/"+repo+"/pulls?state=open&limit=100", &openPRs); err != nil {
				continue
			}
			var closedPRs []map[string]interface{}
			if _, _, err := giteaGetJSONWithStatus(client, base, gw.giteaToken, "/api/v1/repos/"+repo+"/pulls?state=closed&limit=100", &closedPRs); err != nil {
				continue
			}
			recentMerges := 0
			cutoff := time.Now().Add(-7 * 24 * time.Hour)
			for _, pr := range closedPRs {
				if asString(pr["merged_at"]) == "" {
					continue
				}
				if t, err := time.Parse(time.RFC3339, asString(pr["merged_at"])); err == nil && t.After(cutoff) {
					recentMerges++
				}
			}
			rec := repoPRHealth{
				Repo:         repo,
				Open:         len(openPRs),
				Closed:       len(closedPRs),
				RecentMerges: recentMerges,
			}
			out.Repos = append(out.Repos, rec)
			out.TotalOpen += rec.Open
		}
		writeJSON(w, http.StatusOK, out)
	})

	mux.HandleFunc("/api/v1/health/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		base := strings.TrimSpace(gw.giteaURL)
		if base == "" || strings.TrimSpace(gw.taskRepo) == "" {
			writeJSON(w, http.StatusOK, tasksHealthResponse{ByStatus: map[string]int{}, ByAgent: map[string]int{}})
			return
		}
		client := &http.Client{Timeout: 10 * time.Second}
		path := "/api/v1/repos/" + gw.taskRepo + "/issues?state=open&type=issues&limit=1000"
		var issues []map[string]interface{}
		statusCode, body, err := giteaGetJSONWithStatus(client, base, gw.giteaToken, path, &issues)
		if err != nil || statusCode < 200 || statusCode >= 300 {
			writeJSON(w, http.StatusOK, tasksHealthResponse{ByStatus: map[string]int{}, ByAgent: map[string]int{}})
			return
		}
		_ = body
		resp := tasksHealthResponse{
			ByStatus: map[string]int{},
			ByAgent:  map[string]int{},
		}
		for _, iss := range issues {
			labels := asSlice(iss["labels"])
			statusLabel := ""
			agentLabel := ""
			for _, l := range labels {
				lm := asMap(l)
				name := asString(lm["name"])
				if strings.HasPrefix(name, "status/") && statusLabel == "" {
					statusLabel = strings.TrimPrefix(name, "status/")
				}
				if strings.HasPrefix(name, "agent/") && agentLabel == "" {
					agentLabel = strings.TrimPrefix(name, "agent/")
				}
			}
			resp.Total++
			if statusLabel != "" {
				resp.ByStatus[statusLabel]++
			}
			if agentLabel != "" {
				resp.ByAgent[agentLabel]++
			}
		}
		writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("/api/v1/health/memory", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		base := strings.TrimSpace(gw.seidrURL)
		if base == "" {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Seidr URL not configured"})
			return
		}
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(strings.TrimRight(base, "/") + "/health")
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Seidr unreachable"})
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})

	mux.HandleFunc("/api/v1/health/branches", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		base := strings.TrimSpace(gw.giteaURL)
		if base == "" {
			writeJSON(w, http.StatusOK, branchesHealthResponse{Repos: []branchesHealthRepo{}})
			return
		}
		client := &http.Client{Timeout: 10 * time.Second}
		repos := []string{"gitea_admin/project_valhalla", "kit/valhalla-infra", "kit/hirdforge-personas", "kit/hirdforge-tasks"}
		out := branchesHealthResponse{Repos: make([]branchesHealthRepo, 0, len(repos))}
		for _, repo := range repos {
			path := "/api/v1/repos/" + repo + "/branches?limit=250"
			var branches []map[string]interface{}
			statusCode, _, err := giteaGetJSONWithStatus(client, base, gw.giteaToken, path, &branches)
			if err != nil || statusCode < 200 || statusCode >= 300 {
				continue
			}
			bh := branchesHealthRepo{Repo: repo}
			for _, br := range branches {
				name := asString(br["name"])
				if name == "" {
					continue
				}
				bh.Branches++
				ln := strings.ToLower(name)
				if strings.HasPrefix(ln, "ci-") || strings.HasPrefix(ln, "ci/") || strings.HasPrefix(ln, "settings/") || strings.HasPrefix(ln, "auto-") {
					bh.StaleBranches++
				}
			}
			out.Repos = append(out.Repos, bh)
		}
		writeJSON(w, http.StatusOK, out)
	})
}
