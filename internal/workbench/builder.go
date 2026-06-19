package workbench

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BuilderPrompt is the fully rendered prompt sent to the provider for a
// Builder planning call. It is intentionally free of the raw API key so it can
// be persisted and surfaced without leaking the provider secret.
type BuilderPrompt struct {
	Goal          string       `json:"goal"`
	Project       ProjectState `json:"project"`
	ProviderModel string       `json:"provider_model"`
	SystemPrompt  string       `json:"system_prompt"`
	UserPrompt    string       `json:"user_prompt"`
}

// PromptPreview is the sanitized view of the prompt that produced a session's
// plan. It is surfaced through the session and the build/prompt endpoint and
// never contains the provider API key.
type PromptPreview struct {
	SystemPrompt string `json:"system_prompt"`
	UserPrompt   string `json:"user_prompt"`
}

// BuilderSession represents a single Builder session. It is created by
// /api/workbench/build/start and stores a snapshot of the project and
// provider at the time the goal was submitted.
type BuilderSession struct {
	ID            string         `json:"id"`
	TS            time.Time      `json:"ts"`
	Goal          string         `json:"goal"`
	Status        string         `json:"status"`
	Project       ProjectState   `json:"project"`
	ProviderModel string         `json:"provider_model"`
	Plan          []string       `json:"plan"`
	PromptPreview *PromptPreview `json:"prompt_preview,omitempty"`
}

const (
	builderStatusPlanned = "planned"
	builderStatusFailed  = "failed"

	builderPlanPlaceholder = "Await execution wiring"

	builderSystemPrompt = "You are Hirdforge Builder. Produce a concise implementation plan only. Do not claim files were edited."

	buildPlanTimeout = 30 * time.Second
)

// sessionStore is the in-memory record of Builder sessions.
type sessionStore struct {
	mu       sync.Mutex
	nextID   int64
	sessions []BuilderSession
}

func newSessionStore() *sessionStore {
	return &sessionStore{}
}

// Append assigns an id and timestamp to in, stores it, and returns the
// stored value.
func (s *sessionStore) Append(in BuilderSession) BuilderSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	in.ID = strconv.FormatInt(s.nextID, 10)
	in.TS = time.Now().UTC()
	s.sessions = append(s.sessions, in)
	return in
}

// List returns a snapshot copy of all sessions in insertion order (oldest
// first, newest last).
func (s *sessionStore) List() []BuilderSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]BuilderSession, len(s.sessions))
	copy(out, s.sessions)
	return out
}

// Current returns a copy of the most recent session, or nil if there are
// none.
func (s *sessionStore) Current() *BuilderSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) == 0 {
		return nil
	}
	last := s.sessions[len(s.sessions)-1]
	return &last
}

// fallbackPlan returns the deterministic plan used when the provider is
// unreachable or returns content the workbench cannot parse into a plan. Each
// call returns a fresh slice so callers may store or mutate it independently.
func fallbackPlan() []string {
	return []string{
		"Inspect project context",
		"Prepare Builder prompt",
		builderPlanPlaceholder,
	}
}

const (
	// builderPromptMaxFiles caps how many inspected files are listed in the
	// Builder user prompt.
	builderPromptMaxFiles = 80
	// builderPromptMaxReadme caps how much of the README excerpt is included in
	// the Builder user prompt.
	builderPromptMaxReadme = 2000
)

// builderProjectContext renders the goal, project, and read-only inspection
// context shared by the Builder plan and proposal prompts. The returned text
// ends with a trailing blank line so a JSON-shape instruction can follow.
func builderProjectContext(goal string, project ProjectState, insp ProjectInspection) string {
	var b strings.Builder
	b.WriteString("Goal: " + goal + "\n")
	b.WriteString("Project name: " + project.Name + "\n")
	b.WriteString("Project path: " + project.Path + "\n")
	b.WriteString("Git: " + strconv.FormatBool(project.Git) + "\n")
	b.WriteString("Current branch: " + project.CurrentBranch + "\n\n")

	b.WriteString("Git status:\n")
	if strings.TrimSpace(insp.GitStatus) == "" {
		b.WriteString("(clean or not a git repo)\n")
	} else {
		b.WriteString(insp.GitStatus)
		if !strings.HasSuffix(insp.GitStatus, "\n") {
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")

	b.WriteString("Config files: " + strings.Join(insp.ConfigFiles, ", ") + "\n")
	b.WriteString("Languages: " + strings.Join(insp.Languages, ", ") + "\n")
	b.WriteString("Test hints: " + strings.Join(insp.TestHints, ", ") + "\n\n")

	files := insp.Files
	if len(files) > builderPromptMaxFiles {
		files = files[:builderPromptMaxFiles]
	}
	b.WriteString("Files (" + strconv.Itoa(len(files)) + " shown):\n")
	for _, f := range files {
		b.WriteString("- " + f + "\n")
	}
	b.WriteString("\n")

	if readme := truncateRunes(insp.ReadmeExcerpt, builderPromptMaxReadme); readme != "" {
		b.WriteString("README excerpt:\n")
		b.WriteString(readme)
		b.WriteString("\n\n")
	}

	return b.String()
}

// builderUserPrompt renders the user message for a Builder planning call. It
// describes the goal, the open project, and the read-only inspection context,
// then instructs the provider to reply with a JSON-only plan.
func builderUserPrompt(goal string, project ProjectState, insp ProjectInspection) string {
	return builderProjectContext(goal, project, insp) +
		"Return JSON only, with no surrounding prose, in exactly this shape:\n" +
		`{"plan":["step 1","step 2","step 3"]}`
}

// requestProviderPlan calls the configured provider for a Builder plan.
//
// The return values encode the three outcomes the caller must distinguish:
//   - (plan, nil): the provider returned 2xx and the message content parsed
//     into a non-empty plan array; the caller uses this plan.
//   - (nil, nil): the provider returned 2xx but the content could not be parsed
//     into a non-empty plan; the caller falls back to fallbackPlan and still
//     treats the session as planned.
//   - (nil, err): the provider returned a non-2xx status or the request failed
//     at the transport layer; the caller marks the session failed.
func requestProviderPlan(cfg *providerConfig, prompt BuilderPrompt) ([]string, error) {
	endpoint := strings.TrimRight(cfg.baseURL, "/") + "/chat/completions"
	reqBody, err := json.Marshal(map[string]any{
		"model": cfg.model,
		"messages": []map[string]string{
			{"role": "system", "content": prompt.SystemPrompt},
			{"role": "user", "content": prompt.UserPrompt},
		},
		"temperature": 0,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.apiKey)

	client := &http.Client{Timeout: buildPlanTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBody))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("provider returned status %d: %s", resp.StatusCode, msg)
	}

	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return nil, nil
	}
	if len(envelope.Choices) == 0 {
		return nil, nil
	}

	var parsed struct {
		Plan []string `json:"plan"`
	}
	if err := json.Unmarshal([]byte(envelope.Choices[0].Message.Content), &parsed); err != nil {
		return nil, nil
	}
	if len(parsed.Plan) == 0 {
		return nil, nil
	}
	return parsed.Plan, nil
}

func (wb *Server) handleBuildStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var in struct {
		Goal string `json:"goal"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if in.Goal == "" {
		http.Error(w, "goal is required", http.StatusBadRequest)
		return
	}

	project := wb.project.Get()
	if project == nil {
		http.Error(w, "project must be open", http.StatusConflict)
		return
	}

	cfg := wb.provider.Config()
	if cfg == nil {
		http.Error(w, "provider must be configured", http.StatusConflict)
		return
	}

	// Use the latest inspection for prompt context, running one now (and
	// storing it) if none has been captured yet.
	inspection := wb.inspection.Get()
	if inspection == nil {
		produced := wb.runInspection(*project)
		inspection = &produced
	}

	prompt := BuilderPrompt{
		Goal:          in.Goal,
		Project:       *project,
		ProviderModel: cfg.model,
		SystemPrompt:  builderSystemPrompt,
		UserPrompt:    builderUserPrompt(in.Goal, *project, *inspection),
	}
	preview := &PromptPreview{
		SystemPrompt: prompt.SystemPrompt,
		UserPrompt:   prompt.UserPrompt,
	}

	plan, planErr := requestProviderPlan(cfg, prompt)
	if planErr != nil {
		// Provider returned non-2xx or the request failed: record a failed
		// session that still carries the prompt preview and a fallback plan.
		session := wb.sessions.Append(BuilderSession{
			Goal:          in.Goal,
			Status:        builderStatusFailed,
			Project:       *project,
			ProviderModel: cfg.model,
			Plan:          fallbackPlan(),
			PromptPreview: preview,
		})
		if data, err := json.Marshal(session); err == nil {
			wb.store.Append("builder.plan.failed", "Builder plan failed", data)
		} else {
			wb.store.Append("builder.plan.failed", "Builder plan failed", nil)
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"session": session,
			"error":   truncateString(planErr.Error(), 1000),
		})
		return
	}

	// 2xx response: use the provider plan when present, otherwise fall back to
	// the deterministic plan. Either way the session is planned.
	if len(plan) == 0 {
		plan = fallbackPlan()
	}

	session := wb.sessions.Append(BuilderSession{
		Goal:          in.Goal,
		Status:        builderStatusPlanned,
		Project:       *project,
		ProviderModel: cfg.model,
		Plan:          plan,
		PromptPreview: preview,
	})

	sessionData, err := json.Marshal(session)
	if err != nil {
		http.Error(w, "marshal session: "+err.Error(), http.StatusInternalServerError)
		return
	}
	promptData, err := json.Marshal(prompt)
	if err != nil {
		http.Error(w, "marshal prompt: "+err.Error(), http.StatusInternalServerError)
		return
	}

	wb.store.Append("goal.created", "Created goal: "+in.Goal, sessionData)
	wb.store.Append("builder.prompt.created", "Created Builder prompt", promptData)
	wb.store.Append("builder.plan.created", "Created Builder plan", sessionData)

	writeJSON(w, http.StatusCreated, session)
}

func (wb *Server) handleBuildPrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	session := wb.sessions.Current()
	if session == nil || session.PromptPreview == nil {
		http.Error(w, "no prompt", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, *session.PromptPreview)
}

func (wb *Server) handleBuildSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	session := wb.sessions.Current()
	if session == nil {
		http.Error(w, "no session", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, *session)
}

func (wb *Server) handleBuildSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, wb.sessions.List())
}
