package workbench

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ArchitectMessage is one turn in an Architect planning conversation. The
// architect refines a vague goal into a buildable spec; these messages are the
// transcript and never carry the provider API key.
type ArchitectMessage struct {
	ID      string    `json:"id"`
	TS      time.Time `json:"ts"`
	Role    string    `json:"role"`
	Content string    `json:"content"`
}

// ArchitectSpecDraft is the structured spec the Architect refines across a
// conversation. It is planning material only: it feeds Cortex task creation and
// is never applied to the project.
type ArchitectSpecDraft struct {
	Goal               string   `json:"goal"`
	Constraints        []string `json:"constraints"`
	AffectedAreas      []string `json:"affected_areas"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Risks              []string `json:"risks"`
	OpenQuestions      []string `json:"open_questions"`
	SuggestedLanes     []string `json:"suggested_lanes"`
}

// ArchitectSession is a single Architect planning/spec conversation: a project
// snapshot, the message transcript, and the current spec draft.
type ArchitectSession struct {
	ID       string             `json:"id"`
	TS       time.Time          `json:"ts"`
	Project  ProjectState       `json:"project"`
	Status   string             `json:"status"`
	Messages []ArchitectMessage `json:"messages"`
	Spec     ArchitectSpecDraft `json:"spec"`
}

const (
	architectRoleUser      = "user"
	architectRoleArchitect = "architect"
	architectRoleSystem    = "system"

	architectStatusActive   = "active"
	architectStatusAccepted = "accepted"

	architectSystemPrompt = "You are the Hirdforge Architect. Through conversation, refine the user's goal into a concise, buildable spec. Do not write files, run commands, or claim any change was made. Respond with JSON only."
)

// cloneStringSlice returns a copy of in, preserving nil so a returned spec never
// shares backing arrays with the store.
func cloneStringSlice(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// cloneArchitectSpecDraft deep-copies every slice in the draft.
func cloneArchitectSpecDraft(in ArchitectSpecDraft) ArchitectSpecDraft {
	out := in
	out.Constraints = cloneStringSlice(in.Constraints)
	out.AffectedAreas = cloneStringSlice(in.AffectedAreas)
	out.AcceptanceCriteria = cloneStringSlice(in.AcceptanceCriteria)
	out.Risks = cloneStringSlice(in.Risks)
	out.OpenQuestions = cloneStringSlice(in.OpenQuestions)
	out.SuggestedLanes = cloneStringSlice(in.SuggestedLanes)
	return out
}

// cloneArchitectSession deep-copies the message slice and the spec so callers
// can never mutate the store's internal state through a returned value.
func cloneArchitectSession(in ArchitectSession) ArchitectSession {
	out := in
	if in.Messages != nil {
		out.Messages = make([]ArchitectMessage, len(in.Messages))
		copy(out.Messages, in.Messages)
	}
	out.Spec = cloneArchitectSpecDraft(in.Spec)
	return out
}

// normalizeSpecSlices replaces any nil slice in the draft with an empty slice so
// the spec always serializes its fields as [] rather than null.
func normalizeSpecSlices(spec *ArchitectSpecDraft) {
	if spec.Constraints == nil {
		spec.Constraints = []string{}
	}
	if spec.AffectedAreas == nil {
		spec.AffectedAreas = []string{}
	}
	if spec.AcceptanceCriteria == nil {
		spec.AcceptanceCriteria = []string{}
	}
	if spec.Risks == nil {
		spec.Risks = []string{}
	}
	if spec.OpenQuestions == nil {
		spec.OpenQuestions = []string{}
	}
	if spec.SuggestedLanes == nil {
		spec.SuggestedLanes = []string{}
	}
}

// architectSessionStore is the in-memory record of Architect sessions. Every
// accessor returns a deep copy, never a pointer into the stored slice.
type architectSessionStore struct {
	mu       sync.Mutex
	nextID   int64
	sessions []ArchitectSession
}

func newArchitectSessionStore() *architectSessionStore {
	return &architectSessionStore{}
}

// Append assigns an id and timestamp to in, stores it, and returns a copy.
func (s *architectSessionStore) Append(in ArchitectSession) ArchitectSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	in.ID = strconv.FormatInt(s.nextID, 10)
	in.TS = time.Now().UTC()
	stored := cloneArchitectSession(in)
	s.sessions = append(s.sessions, stored)
	return cloneArchitectSession(stored)
}

// Current returns a deep copy of the most recent session, or nil if none.
func (s *architectSessionStore) Current() *ArchitectSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) == 0 {
		return nil
	}
	c := cloneArchitectSession(s.sessions[len(s.sessions)-1])
	return &c
}

// Find returns a deep copy of the session with the given id, or nil.
func (s *architectSessionStore) Find(id string) *ArchitectSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.sessions {
		if s.sessions[i].ID == id {
			c := cloneArchitectSession(s.sessions[i])
			return &c
		}
	}
	return nil
}

// List returns deep copies of all sessions in insertion order.
func (s *architectSessionStore) List() []ArchitectSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ArchitectSession, len(s.sessions))
	for i := range s.sessions {
		out[i] = cloneArchitectSession(s.sessions[i])
	}
	return out
}

// Update replaces the stored session that shares in's id and returns a deep
// copy of the stored value, or nil if there is no such session.
func (s *architectSessionStore) Update(in ArchitectSession) *ArchitectSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.sessions {
		if s.sessions[i].ID == in.ID {
			s.sessions[i] = cloneArchitectSession(in)
			c := cloneArchitectSession(s.sessions[i])
			return &c
		}
	}
	return nil
}

// appendArchitectMessage appends a message to session, assigning it a stable
// per-session id and a timestamp.
func appendArchitectMessage(session *ArchitectSession, role, content string) {
	session.Messages = append(session.Messages, ArchitectMessage{
		ID:      strconv.Itoa(len(session.Messages) + 1),
		TS:      time.Now().UTC(),
		Role:    role,
		Content: content,
	})
}

// appendArchitectEvent marshals payload as the event data, falling back to nil
// data when marshaling fails.
func (wb *Server) appendArchitectEvent(evType, message string, payload any) {
	if data, err := json.Marshal(payload); err == nil {
		wb.store.Append(evType, message, data)
	} else {
		wb.store.Append(evType, message, nil)
	}
}

// architectContent is the provider's parsed Architect response.
type architectContent struct {
	Message string             `json:"message"`
	Spec    ArchitectSpecDraft `json:"spec"`
}

// parseArchitectContent decodes and validates a provider Architect response: it
// requires a message and a spec goal, and normalizes nil spec slices to [].
func parseArchitectContent(content string) (architectContent, error) {
	var parsed architectContent
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return architectContent{}, fmt.Errorf("provider content is not valid architect JSON: %w", err)
	}
	if strings.TrimSpace(parsed.Message) == "" {
		return architectContent{}, errors.New("architect message is required")
	}
	if strings.TrimSpace(parsed.Spec.Goal) == "" {
		return architectContent{}, errors.New("architect spec goal is required")
	}
	normalizeSpecSlices(&parsed.Spec)
	return parsed, nil
}

// requestProviderArchitect calls the configured provider for an Architect turn.
// It returns the validated content, or an error covering any failure mode
// (non-2xx status, transport error, or invalid/unparseable content).
func requestProviderArchitect(cfg *providerConfig, systemPrompt, userPrompt string) (architectContent, error) {
	endpoint := strings.TrimRight(cfg.baseURL, "/") + "/chat/completions"
	reqBody, err := json.Marshal(map[string]any{
		"model": cfg.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": 0,
	})
	if err != nil {
		return architectContent{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(reqBody)))
	if err != nil {
		return architectContent{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.apiKey)

	client := &http.Client{Timeout: buildPlanTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return architectContent{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return architectContent{}, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBody))
		if msg == "" {
			msg = resp.Status
		}
		return architectContent{}, fmt.Errorf("provider returned status %d: %s", resp.StatusCode, msg)
	}

	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return architectContent{}, fmt.Errorf("decode provider response: %w", err)
	}
	if len(envelope.Choices) == 0 {
		return architectContent{}, errors.New("provider response contained no choices")
	}
	return parseArchitectContent(envelope.Choices[0].Message.Content)
}

// architectSpecText renders the current spec draft for the provider prompt.
func architectSpecText(spec ArchitectSpecDraft) string {
	var b strings.Builder
	b.WriteString("Goal: " + spec.Goal + "\n")
	b.WriteString("Constraints: " + strings.Join(spec.Constraints, "; ") + "\n")
	b.WriteString("Affected areas: " + strings.Join(spec.AffectedAreas, "; ") + "\n")
	b.WriteString("Acceptance criteria: " + strings.Join(spec.AcceptanceCriteria, "; ") + "\n")
	b.WriteString("Risks: " + strings.Join(spec.Risks, "; ") + "\n")
	b.WriteString("Open questions: " + strings.Join(spec.OpenQuestions, "; ") + "\n")
	b.WriteString("Suggested lanes: " + strings.Join(spec.SuggestedLanes, "; ") + "\n")
	return b.String()
}

// architectUserPrompt renders the provider user message for an Architect turn:
// the project and read-only inspection context, the prior conversation, the
// current spec draft, and the new user message.
func architectUserPrompt(project ProjectState, insp ProjectInspection, prior []ArchitectMessage, spec ArchitectSpecDraft, newMessage string) string {
	var b strings.Builder
	b.WriteString("Hirdforge Architect planning conversation.\n\n")
	b.WriteString(builderProjectContext(spec.Goal, project, insp))

	b.WriteString("Current spec draft:\n")
	b.WriteString(architectSpecText(spec))
	b.WriteString("\n")

	b.WriteString("Conversation so far:\n")
	if len(prior) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, m := range prior {
			b.WriteString("- " + m.Role + ": " + m.Content + "\n")
		}
	}
	b.WriteString("\n")

	b.WriteString("New user message: " + newMessage + "\n\n")

	b.WriteString("Refine the spec and reply. Return JSON only, with no surrounding prose, in exactly this shape:\n")
	b.WriteString(`{"message":"...","spec":{"goal":"...","constraints":["..."],"affected_areas":["..."],"acceptance_criteria":["..."],"risks":["..."],"open_questions":["..."],"suggested_lanes":["..."]}}`)
	return b.String()
}

// handleArchitectSession serves GET (current session) and POST (create an active
// session from a goal). The POST never calls the provider.
func (wb *Server) handleArchitectSession(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		session := wb.architect.Current()
		if session == nil {
			http.Error(w, "no architect session", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, *session)
	case http.MethodPost:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var in struct {
			Goal string `json:"goal"`
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &in); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
		}

		project := wb.project.Get()
		if project == nil {
			http.Error(w, "project must be open", http.StatusConflict)
			return
		}
		if in.Goal == "" {
			http.Error(w, "goal is required", http.StatusBadRequest)
			return
		}

		spec := ArchitectSpecDraft{Goal: in.Goal}
		normalizeSpecSlices(&spec)
		session := ArchitectSession{
			Project: *project,
			Status:  architectStatusActive,
			Spec:    spec,
		}
		appendArchitectMessage(&session, architectRoleUser, in.Goal)

		stored := wb.architect.Append(session)
		wb.appendArchitectEvent("architect.session.created", "Created Architect session", stored)
		writeJSON(w, http.StatusCreated, stored)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (wb *Server) handleArchitectSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, wb.architect.List())
}

// handleArchitectMessage appends a user turn, asks the provider to refine the
// spec, and records the architect reply. It never writes files and never
// exposes the provider API key.
func (wb *Server) handleArchitectMessage(w http.ResponseWriter, r *http.Request) {
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
		SessionID string `json:"session_id"`
		Message   string `json:"message"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	var session *ArchitectSession
	if in.SessionID == "" {
		session = wb.architect.Current()
	} else {
		session = wb.architect.Find(in.SessionID)
	}
	if session == nil {
		http.Error(w, "architect session not found", http.StatusNotFound)
		return
	}
	if session.Status == architectStatusAccepted {
		http.Error(w, "session is already accepted", http.StatusConflict)
		return
	}

	cfg := wb.provider.Config()
	if cfg == nil {
		http.Error(w, "provider must be configured", http.StatusConflict)
		return
	}
	if in.Message == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	// Prior turns drive the prompt; the new user message is appended now and
	// shown to the provider separately.
	prior := make([]ArchitectMessage, len(session.Messages))
	copy(prior, session.Messages)
	appendArchitectMessage(session, architectRoleUser, in.Message)

	insp := wb.inspection.Get()
	var inspValue ProjectInspection
	if insp != nil {
		inspValue = *insp
	}

	userPrompt := architectUserPrompt(session.Project, inspValue, prior, session.Spec, in.Message)
	content, archErr := requestProviderArchitect(cfg, architectSystemPrompt, userPrompt)
	if archErr != nil {
		appendArchitectMessage(session, architectRoleSystem, truncateString("Architect request failed: "+archErr.Error(), 1000))
		stored := wb.architect.Update(*session)
		wb.appendArchitectEvent("architect.message.failed", "Failed Architect message", stored)
		writeJSON(w, http.StatusBadGateway, *stored)
		return
	}

	appendArchitectMessage(session, architectRoleArchitect, content.Message)
	session.Spec = content.Spec
	stored := wb.architect.Update(*session)
	wb.appendArchitectEvent("architect.message.created", "Created Architect message", stored)
	wb.appendArchitectEvent("architect.spec.updated", "Updated Architect spec", stored.Spec)
	writeJSON(w, http.StatusOK, *stored)
}

// handleArchitectAccept marks a session's refined spec accepted. It does not
// create a Cortex task.
func (wb *Server) handleArchitectAccept(w http.ResponseWriter, r *http.Request) {
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
		SessionID string `json:"session_id"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	var session *ArchitectSession
	if in.SessionID == "" {
		session = wb.architect.Current()
	} else {
		session = wb.architect.Find(in.SessionID)
	}
	if session == nil {
		http.Error(w, "architect session not found", http.StatusNotFound)
		return
	}
	if session.Status == architectStatusAccepted {
		http.Error(w, "session is already accepted", http.StatusConflict)
		return
	}
	if strings.TrimSpace(session.Spec.Goal) == "" {
		http.Error(w, "spec goal is required before accepting", http.StatusConflict)
		return
	}

	session.Status = architectStatusAccepted
	stored := wb.architect.Update(*session)
	wb.appendArchitectEvent("architect.spec.accepted", "Accepted Architect spec", stored)
	writeJSON(w, http.StatusOK, *stored)
}

// handleArchitectCortexTask converts an accepted Architect spec into a Cortex
// task. It only plans the task: no Builder proposals, worktrees, or file writes.
func (wb *Server) handleArchitectCortexTask(w http.ResponseWriter, r *http.Request) {
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
		SessionID string            `json:"session_id"`
		Mode      string            `json:"mode"`
		Roles     *CortexRoleCounts `json:"roles"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	var session *ArchitectSession
	if in.SessionID == "" {
		session = wb.architect.Current()
	} else {
		session = wb.architect.Find(in.SessionID)
	}
	if session == nil {
		http.Error(w, "architect session not found", http.StatusNotFound)
		return
	}
	if session.Status != architectStatusAccepted {
		http.Error(w, "session must be accepted", http.StatusConflict)
		return
	}

	mode := in.Mode
	if mode == "" {
		mode = cortexModeMulti
	}
	counts, errMsg := resolveCortexRoleCounts(mode, in.Roles)
	if errMsg != "" {
		http.Error(w, errMsg, http.StatusBadRequest)
		return
	}

	goal := session.Spec.Goal
	task := wb.cortex.Create(CortexTask{
		Goal:   goal,
		Status: cortexTaskStatusPlanned,
		Lanes:  buildCortexLanes(counts, goal),
	})
	wb.appendArchitectEvent("architect.cortex_task.created", "Created Cortex task from Architect spec", map[string]any{
		"architect_session_id": session.ID,
		"task":                 task,
	})
	writeJSON(w, http.StatusCreated, task)
}
