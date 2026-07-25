package main

// Phase 4 — the Steward surface (OBSERVABILITY_CONTRACT §7).
//
// The Steward is a model, and models are forbidden in the coordination path. It
// is safe because it PROPOSES and this file DECIDES: the engine hands back a
// validated proposal, and the only side effect reachable from here is creating a
// labeled issue through the same call the operator's POST /cortex/dispatch uses.
// From there Cortex routes deterministically, exactly as for any other issue.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"git.hirdforge.com/kit/hirdforge/internal/cortex"
	"git.hirdforge.com/kit/hirdforge/internal/steward"
)

// stewardInference is a minimal OpenAI-compatible chat client. The Steward needs
// one structured completion per turn — no tool-calling, no streaming — so this
// stays deliberately small rather than reusing the agent's full loop.
type stewardInference struct {
	url    string
	model  string
	apiKey string
	client *http.Client
}

func (s *stewardInference) Complete(ctx context.Context, system string, history []steward.Turn, message string) (string, error) {
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	msgs := []msg{{Role: "system", Content: system}}
	for _, t := range history {
		msgs = append(msgs, msg{Role: "user", Content: t.Message})
		if strings.TrimSpace(t.Reply) != "" {
			msgs = append(msgs, msg{Role: "assistant", Content: t.Reply})
		}
	}
	msgs = append(msgs, msg{Role: "user", Content: message})

	body, _ := json.Marshal(map[string]any{
		"model": s.model, "messages": msgs, "stream": false, "temperature": 0.2,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(s.url, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(s.apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("inference status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("inference returned no choices")
	}
	return out.Choices[0].Message.Content, nil
}

// stewardIssues creates issues through the gateway's existing dispatch path.
type stewardIssues struct{ g *gateway }

func (s *stewardIssues) CreateLabeledIssue(ctx context.Context, repo, title, body, label string) (steward.Created, error) {
	// Attribution (§7): the issue itself carries the marker, so a
	// Steward-authored task is distinguishable from an operator-authored one
	// without changing the persistence schema — the trailer travels with the
	// issue and into the dispatch cause below.
	marked := strings.TrimRight(body, "\n") + "\n\n---\n_Filed by the Steward on the operator's behalf._"
	num, err := s.g.createLabeledIssueAndRoute(ctx, repo, title, marked, label, "steward")
	if err != nil {
		return steward.Created{}, err
	}
	return steward.Created{
		Repo: repo, Number: num, Label: label,
		URL: fmt.Sprintf("%s/%s/issues/%d", strings.TrimRight(s.g.giteaURL, "/"), repo, num),
	}, nil
}

// createLabeledIssueAndRoute is the ONE path from "someone wants work done" to a
// routed task: create the issue, label it, and fire the routing event. Gitea does
// not emit a webhook for label changes made through the API, so this owns its own
// trigger (same reasoning as the operator dispatch handler, which now shares it).
func (g *gateway) createLabeledIssueAndRoute(ctx context.Context, repo, title, body, label, createdBy string) (int64, error) {
	issueBody, _ := json.Marshal(map[string]any{"title": title, "body": body})
	resp, err := giteaRequest(http.DefaultClient, http.MethodPost, g.giteaURL, g.giteaToken,
		fmt.Sprintf("/api/v1/repos/%s/issues", repo), strings.NewReader(string(issueBody)))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var issue struct {
		Number int64 `json:"number"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&issue)
	if resp.StatusCode != http.StatusCreated || issue.Number == 0 {
		return 0, fmt.Errorf("gitea issue create: status %d", resp.StatusCode)
	}
	labelBody, _ := json.Marshal(map[string]any{"labels": []string{label}})
	if lresp, lerr := giteaRequest(http.DefaultClient, http.MethodPost, g.giteaURL, g.giteaToken,
		fmt.Sprintf("/api/v1/repos/%s/issues/%d/labels", repo, issue.Number), strings.NewReader(string(labelBody))); lerr == nil {
		_ = lresp.Body.Close()
	}
	log.Printf("cortex: %s dispatch -> issue %s#%d labeled %s", createdBy, repo, issue.Number, label)

	ev := cortex.Event{
		Type: cortex.EventIssueLabeled, Repo: repo, Label: label,
		IssueNumber: issue.Number, IssueTitle: title, IssueBody: body,
		IssueLabels: []string{label},
	}
	if _, herr := g.cortex.HandleEvent(ev); herr != nil {
		log.Printf("cortex: ERROR routing %s-created issue %s#%d: %v", createdBy, repo, issue.Number, herr)
	}
	return issue.Number, nil
}

// stewardStatus answers status questions from the task records — a projection of
// the observability surface, never the model's recollection (§7).
type stewardStatus struct{ g *gateway }

func (s *stewardStatus) Summarize(_ context.Context, query string) (string, error) {
	if s.g.cortex == nil {
		return "", fmt.Errorf("cortex is not enabled")
	}
	tasks, err := s.g.cortex.Store().ListTasks(cortex.TaskFilter{})
	if err != nil {
		return "", err
	}
	// If the operator named a task id, answer about exactly that one.
	if id := findTaskID(query, tasks); id != "" {
		t, history, gerr := s.g.cortex.Store().GetTask(id)
		if gerr != nil {
			return "", gerr
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Task %s (%s#%d) is **%s**", t.ID, t.IssueRepo, t.IssueNumber, t.Status)
		if t.PRNumber > 0 {
			fmt.Fprintf(&b, " — PR #%d", t.PRNumber)
		}
		if t.Attempt > 1 {
			fmt.Fprintf(&b, ", attempt %d", t.Attempt)
		}
		b.WriteString(".\n")
		if len(history) > 0 {
			last := history[len(history)-1]
			// The mechanical reason, verbatim — this is the whole point.
			fmt.Fprintf(&b, "Latest transition: %s — %s", last.ToStatus, last.Reason)
		}
		return b.String(), nil
	}

	// Otherwise summarise what is in flight.
	byStatus := map[string][]cortex.TaskRecord{}
	for _, t := range tasks {
		byStatus[t.Status] = append(byStatus[t.Status], t)
	}
	active := []string{cortex.StatusQueued, cortex.StatusDispatched, cortex.StatusBuilding, cortex.StatusReview, cortex.StatusApproved}
	var b strings.Builder
	total := 0
	for _, st := range active {
		list := byStatus[st]
		if len(list) == 0 {
			continue
		}
		total += len(list)
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		fmt.Fprintf(&b, "**%s** (%d): ", st, len(list))
		ids := make([]string, 0, len(list))
		for _, t := range list {
			ids = append(ids, fmt.Sprintf("%s (%s#%d)", t.ID, t.IssueRepo, t.IssueNumber))
		}
		b.WriteString(strings.Join(ids, ", "))
		b.WriteString("\n")
	}
	if total == 0 {
		return "Nothing is in flight right now.", nil
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// findTaskID returns a task id mentioned in the query, if any.
func findTaskID(query string, tasks []cortex.TaskRecord) string {
	q := strings.ToLower(query)
	for _, t := range tasks {
		if strings.Contains(q, strings.ToLower(t.ID)) {
			return t.ID
		}
	}
	return ""
}

// registerStewardRoutes wires the §7 surface.
func registerStewardRoutes(mux *http.ServeMux, g *gateway) {
	if g.steward == nil {
		return
	}
	mux.HandleFunc("/api/v1/steward/chat", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			SessionID string `json:"session_id"`
			Message   string `json:"message"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Message) == "" {
			http.Error(w, "message required", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.SessionID) == "" {
			req.SessionID = fmt.Sprintf("s-%d", time.Now().UnixNano())
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		res, err := g.steward.Run(ctx, req.SessionID, req.Message)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"session_id": req.SessionID, "reply": res.Reply,
			"intent": res.Intent, "created_issue": res.Created,
		})
	})

	mux.HandleFunc("/api/v1/steward/sessions/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/steward/sessions/")
		if id == "" {
			http.Error(w, "session id required", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"session_id": id, "turns": g.steward.Sessions.History(id),
		})
	})
}
