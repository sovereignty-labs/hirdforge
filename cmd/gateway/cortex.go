package main

// v2 Cortex wiring (P1.1). The Cortex module lives in internal/cortex; this
// file is the gateway glue: the --cortex-config flag, the issues-webhook
// ingest path, and the read endpoints. With no --cortex-config the module is
// entirely disabled and v1 behavior is untouched.

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"git.hirdforge.com/kit/hirdforge/internal/cortex"
)

// giteaIssuesPayload is the subset of Gitea's "issues" webhook payload Cortex
// consumes. The fired label rides in Label; the issue's full label set rides
// in Issue.Labels.
type giteaIssuesPayload struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Issue struct {
		Number int64  `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	} `json:"issue"`
	Label struct {
		Name string `json:"name"`
	} `json:"label"`
}

// handleCortexIssuesEvent parses a Gitea "issues" webhook body and routes the
// label event through Cortex. Called from handleGiteaWebhook AFTER HMAC
// validation. Actions other than label application are ignored (that is a
// logged no-op, not a silent one — Cortex records a decision either way when
// the event reaches it).
func (g *gateway) handleCortexIssuesEvent(body []byte) {
	if g.cortex == nil {
		return
	}
	var p giteaIssuesPayload
	if err := json.Unmarshal(body, &p); err != nil {
		log.Printf("cortex: issues webhook: bad payload: %v", err)
		return
	}
	// Gitea fires action "label_updated" (and some versions "labeled") when a
	// label lands on an issue.
	if p.Action != "label_updated" && p.Action != "labeled" {
		return
	}
	labels := make([]string, 0, len(p.Issue.Labels))
	for _, l := range p.Issue.Labels {
		labels = append(labels, l.Name)
	}
	fired := strings.TrimSpace(p.Label.Name)
	firedSet := []string{fired}
	if fired == "" {
		// Some Gitea versions omit the fired label on label_updated; fall
		// back to evaluating every label currently on the issue. Route
		// matching stays exact-equality either way.
		firedSet = labels
	}
	for _, label := range firedSet {
		if label == "" {
			continue
		}
		ev := cortex.Event{
			Type:        cortex.EventIssueLabeled,
			Repo:        p.Repository.FullName,
			Label:       label,
			IssueNumber: p.Issue.Number,
			IssueTitle:  p.Issue.Title,
			IssueBody:   p.Issue.Body,
			IssueLabels: labels,
		}
		if _, err := g.cortex.HandleEvent(ev); err != nil {
			log.Printf("cortex: ERROR handling %s: %v", ev.Type, err)
		}
	}
}

// registerCortexRoutes wires the v2 read surface (observability contract §2;
// the full P1.8 surface widens this).
func registerCortexRoutes(mux *http.ServeMux, g *gateway) {
	mux.HandleFunc("/api/v1/cortex/routes", func(w http.ResponseWriter, r *http.Request) {
		if g.cortex == nil {
			http.Error(w, "cortex disabled", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, g.cortex.Config())
	})
	mux.HandleFunc("/api/v1/cortex/log", func(w http.ResponseWriter, r *http.Request) {
		if g.cortex == nil {
			http.Error(w, "cortex disabled", http.StatusNotFound)
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		writeJSON(w, http.StatusOK, g.cortex.RecentDecisions(limit))
	})
	mux.HandleFunc("/api/v1/cortex/tasks", func(w http.ResponseWriter, r *http.Request) {
		if g.cortex == nil {
			http.Error(w, "cortex disabled", http.StatusNotFound)
			return
		}
		f := cortex.TaskFilter{
			Status: r.URL.Query().Get("status"),
			Agent:  r.URL.Query().Get("agent"),
			Repo:   r.URL.Query().Get("repo"),
		}
		f.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
		tasks, err := g.cortex.Store().ListTasks(f)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if tasks == nil {
			tasks = []cortex.TaskRecord{}
		}
		writeJSON(w, http.StatusOK, tasks)
	})
	mux.HandleFunc("/api/v1/cortex/tasks/", func(w http.ResponseWriter, r *http.Request) {
		if g.cortex == nil {
			http.Error(w, "cortex disabled", http.StatusNotFound)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/cortex/tasks/")
		if id == "" {
			http.Error(w, "missing task id", http.StatusBadRequest)
			return
		}
		task, history, err := g.cortex.Store().GetTask(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"task": task, "history": history})
	})
}
