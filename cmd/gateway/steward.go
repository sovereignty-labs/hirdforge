package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"git.hirdforge.com/kit/hirdforge/internal/steward"
)

// The interlocutor surface (§7, Phase 4 P4.6). The gateway hosts the steward
// Engine — the session store, the plan-mode parse/validate, and the blessing —
// and PROXIES the model call to a persistent interlocutor agent over A2A. Where
// the model runs (which lane) is deployment configuration; this surface is not.
//
// Doctrine: the interlocutor proposes; only the blessing (/handoff) writes, and it
// writes through the same createLabeledIssueAndRoute path operator dispatch uses,
// from which Cortex routes deterministically. The engine has no other write path.

// stewardConfig is the deployment-configurable wiring. Model/endpoint are per-role
// config and collapse to one endpoint for a single-model install (D-INTERLOCUTOR):
// point AgentURL's agent and InferenceURL at the same lane everything else uses.
type stewardConfig struct {
	AgentURL       string // A2A base URL of the interlocutor agent (empty = surface disabled)
	InferenceURL   string // OpenAI-compatible base URL for the fold summarizer
	InferenceKey   string
	InferenceModel string
	DefaultRepo    string // where a blessing files issues
	DefaultLabel   string // the routing label a code step is filed under
}

// firstNonEmpty returns the first argument that is non-empty after trimming — used
// so the interlocutor's summarizer config defaults to the single-lane cortex
// values, making a single-model install work with no extra flags.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// a2aTurnRunner runs one interlocutor turn by sending it to the agent over A2A and
// polling for the completed result. It implements steward.TurnRunner.
type a2aTurnRunner struct {
	url    string
	client *http.Client
}

func (r a2aTurnRunner) RunTurn(ctx context.Context, sessionID, message string, sc steward.SessionContext) (string, error) {
	if strings.TrimSpace(r.url) == "" {
		return "", fmt.Errorf("steward: no interlocutor agent configured (--steward-url)")
	}
	body, _ := json.Marshal(taskSendRequest{Content: formatTurnContent(message, sc), From: "gateway-steward", SessionID: sessionID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.url, "/")+"/tasks/send", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("steward agent send %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var sent struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sent); err != nil || strings.TrimSpace(sent.ID) == "" {
		return "", fmt.Errorf("steward agent: no task id in response")
	}
	return r.pollResult(ctx, sent.ID)
}

// agentTask is the shape the agent's /tasks/{id} returns: a string status and the
// output in `result` (NOT the gateway's richer A2A Task type — the persistent
// agent server uses this simpler envelope).
type agentTask struct {
	Status string `json:"status"`
	Result string `json:"result"`
}

// pollResult waits for the agent's task to complete and returns its output text.
func (r a2aTurnRunner) pollResult(ctx context.Context, taskID string) (string, error) {
	url := strings.TrimRight(r.url, "/") + "/tasks/" + taskID
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		resp, err := r.client.Do(req)
		if err != nil {
			return "", err
		}
		var task agentTask
		derr := json.NewDecoder(resp.Body).Decode(&task)
		resp.Body.Close()
		if derr != nil {
			return "", derr
		}
		switch st := strings.ToLower(strings.TrimSpace(task.Status)); {
		case st == "completed":
			if strings.TrimSpace(task.Result) != "" {
				return task.Result, nil
			}
			return "", fmt.Errorf("steward agent: completed with empty result")
		case strings.Contains(st, "fail"), strings.Contains(st, "error"), st == "canceled", st == "cancelled":
			return "", fmt.Errorf("steward agent task %s: %s", taskID, task.Status)
		}
		// submitted / working / input-needed → keep polling until ctx deadline.
	}
}

// formatTurnContent threads the session context the engine decided to keep — the
// running summary, the pinned plan, and the recent turns — into the content the
// agent receives for this turn. The agent's steward procedure (its system prompt)
// tells it to reply in prose and append a fenced plan only when proposing work.
func formatTurnContent(message string, sc steward.SessionContext) string {
	var b strings.Builder
	if strings.TrimSpace(sc.Summary) != "" {
		b.WriteString("Summary of earlier conversation (older turns, condensed):\n")
		b.WriteString(sc.Summary)
		b.WriteString("\n\n")
	}
	if sc.ActivePlan != nil {
		pj, _ := json.Marshal(sc.ActivePlan)
		b.WriteString("The active plan under discussion (pinned; may be revised):\n")
		b.Write(pj)
		b.WriteString("\n\n")
	}
	if len(sc.Recent) > 0 {
		b.WriteString("Recent conversation:\n")
		for _, t := range sc.Recent {
			fmt.Fprintf(&b, "Operator: %s\nYou: %s\n", t.Message, t.Reply)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Operator: %s", message)
	return b.String()
}

// inferenceSummarizer folds older turns into a running summary via an
// OpenAI-compatible endpoint. It implements steward.Summarizer. The endpoint/model
// are configuration and may point at a faster lane than the interlocutor's.
type inferenceSummarizer struct {
	url, key, model string
	client          *http.Client
}

func (s inferenceSummarizer) Fold(ctx context.Context, prior string, older []steward.Turn) (string, error) {
	if strings.TrimSpace(s.url) == "" {
		return "", fmt.Errorf("steward: no summarizer inference configured")
	}
	var u strings.Builder
	if strings.TrimSpace(prior) != "" {
		u.WriteString("Existing summary so far:\n" + prior + "\n\n")
	}
	u.WriteString("Fold these additional earlier turns into the summary, preserving concrete facts (names, hosts, numbers, decisions). Return ONLY the updated summary prose:\n")
	for _, t := range older {
		fmt.Fprintf(&u, "- Operator: %s\n  You: %s\n", t.Message, t.Reply)
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model":       s.model,
		"temperature": 0,
		"max_tokens":  2000, // headroom: a reasoning model spends tokens thinking first
		"messages": []map[string]string{
			{"role": "system", "content": "You compress conversation history without losing concrete facts. Answer directly with the summary; keep thinking brief."},
			{"role": "user", "content": u.String()},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.url, "/")+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(s.key) != "" {
		req.Header.Set("Authorization", "Bearer "+s.key)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("summarizer %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Choices) == 0 {
		return "", fmt.Errorf("summarizer: undecodable response")
	}
	return out.Choices[0].Message.Content, nil // empty is handled as a no-fold by the store
}

// newStewardFiler builds the StepFiler that turns a blessed step into routed work
// through the ONE dispatch path (createLabeledIssueAndRoute), attributed to the
// interlocutor. Code and operational steps both go through here; the gate the step
// declared travels in the issue body for the operator's record.
func (g *gateway) newStewardFiler(cfg stewardConfig) steward.StepFiler {
	return func(ctx context.Context, planID string, s steward.Step) (steward.Created, error) {
		repo := strings.TrimSpace(s.Repo)
		if repo == "" {
			repo = cfg.DefaultRepo
		}
		label := strings.TrimSpace(s.Label)
		if label == "" {
			label = cfg.DefaultLabel
		}
		body := s.Detail
		if strings.TrimSpace(body) == "" {
			body = s.Title
		}
		body += fmt.Sprintf("\n\n---\nFrom interlocutor plan %q, step %q (gate: %s). Filed by the operator's blessing.", planID, s.ID, s.Gate)
		num, err := g.createLabeledIssueAndRoute(ctx, repo, s.Title, body, label, "steward")
		if err != nil {
			return steward.Created{}, err
		}
		return steward.Created{
			StepID: s.ID, Repo: repo, Number: num, Label: label,
			URL: fmt.Sprintf("%s/%s/issues/%d", strings.TrimRight(g.giteaURL, "/"), repo, num),
		}, nil
	}
}

// registerStewardRoutes wires the §7 surface. All four endpoints are inert except
// /handoff, which is the blessing. If no interlocutor agent is configured the chat
// endpoints answer 503 (a valid deployment may not run one), but the surface still
// loads so the UI contract is stable.
func registerStewardRoutes(mux *http.ServeMux, g *gateway) {
	if g.stewardEngine == nil {
		return
	}

	mux.HandleFunc("/api/v1/steward/chat", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			SessionID string `json:"session_id"`
			Message   string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Message) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session_id and message required"})
			return
		}
		out, err := g.stewardEngine.Chat(r.Context(), strings.TrimSpace(in.SessionID), in.Message)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"session_id": in.SessionID, "reply": out.Reply, "plan": out.Plan})
	})

	mux.HandleFunc("/api/v1/steward/revise", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			SessionID   string `json:"session_id"`
			PlanID      string `json:"plan_id"`
			Instruction string `json:"instruction"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Instruction) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "instruction required"})
			return
		}
		// Revise is another inert turn: ask the interlocutor to amend the named plan.
		msg := fmt.Sprintf("Revise the plan %q: %s", strings.TrimSpace(in.PlanID), in.Instruction)
		out, err := g.stewardEngine.Chat(r.Context(), strings.TrimSpace(in.SessionID), msg)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"session_id": in.SessionID, "reply": out.Reply, "plan": out.Plan})
	})

	mux.HandleFunc("/api/v1/steward/handoff", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			SessionID string   `json:"session_id"`
			PlanID    string   `json:"plan_id"`
			StepIDs   []string `json:"step_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.PlanID) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session_id and plan_id required"})
			return
		}
		created, err := g.stewardEngine.Handoff(r.Context(), strings.TrimSpace(in.SessionID), strings.TrimSpace(in.PlanID), in.StepIDs, g.stewardFiler)
		if err != nil {
			// A refused blessing (held/unknown step, already blessed, nothing to do)
			// is a client-visible 409, not a server error — it is a loud, expected no-op.
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "created_issues": created})
			return
		}
		log.Printf("steward: blessing filed %d issue(s) from plan %q (session %q)", len(created), in.PlanID, in.SessionID)
		writeJSON(w, http.StatusOK, map[string]any{"created_issues": created})
	})

	mux.HandleFunc("/api/v1/steward/sessions/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/steward/sessions/"), "/")
		if id == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session id required"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"session_id":     id,
			"turns":          g.stewardEngine.History(id),
			"created_issues": g.stewardEngine.Created(id),
		})
	})
}
