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

// BuilderProposedFile is a single proposed change in a BuilderChangeProposal.
// It describes a change only; the workbench never applies it. Action is one of
// create, modify, or delete.
type BuilderProposedFile struct {
	Path      string `json:"path"`
	Action    string `json:"action"`
	Content   string `json:"content"`
	Rationale string `json:"rationale"`
}

// BuilderChangeProposal is a provider-produced, in-memory-only set of proposed
// file changes for a goal. It is review material: nothing here is ever written
// to disk, and it never carries the provider API key.
type BuilderChangeProposal struct {
	ID        string                `json:"id"`
	TS        time.Time             `json:"ts"`
	SessionID string                `json:"session_id"`
	Goal      string                `json:"goal"`
	Status    string                `json:"status"`
	Summary   string                `json:"summary"`
	Files     []BuilderProposedFile `json:"files"`
}

const (
	proposalActionCreate = "create"
	proposalActionModify = "modify"
	proposalActionDelete = "delete"

	builderProposalStatusProposed = "proposed"
	builderProposalStatusFailed   = "failed"

	builderProposalFallbackSummary = "Proposal unavailable: the provider did not return a valid change set."

	builderProposalSystemPrompt = "You are Hirdforge Builder. Propose a concrete set of file changes as a structured change set. Do not edit any files yourself; only describe the proposed changes. Respond with JSON only and do not claim files were edited."
)

// proposalStore is the in-memory record of Builder change proposals.
type proposalStore struct {
	mu        sync.Mutex
	nextID    int64
	proposals []BuilderChangeProposal
}

func newProposalStore() *proposalStore {
	return &proposalStore{}
}

// Append assigns an id and timestamp to in, stores it, and returns the stored
// value.
func (s *proposalStore) Append(in BuilderChangeProposal) BuilderChangeProposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	in.ID = strconv.FormatInt(s.nextID, 10)
	in.TS = time.Now().UTC()
	s.proposals = append(s.proposals, in)
	return in
}

// List returns a snapshot copy of all proposals in insertion order (oldest
// first, newest last).
func (s *proposalStore) List() []BuilderChangeProposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]BuilderChangeProposal, len(s.proposals))
	copy(out, s.proposals)
	return out
}

// Current returns a copy of the most recent proposal, or nil if there are
// none.
func (s *proposalStore) Current() *BuilderChangeProposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.proposals) == 0 {
		return nil
	}
	last := s.proposals[len(s.proposals)-1]
	return &last
}

// Find returns a copy of the proposal with the given id, or nil if there is no
// such proposal.
func (s *proposalStore) Find(id string) *BuilderChangeProposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.proposals {
		if s.proposals[i].ID == id {
			c := s.proposals[i]
			return &c
		}
	}
	return nil
}

// builderProposalUserPrompt renders the user message for a Builder change
// proposal. It reuses the shared project context and asks for a JSON-only
// change set.
func builderProposalUserPrompt(goal string, project ProjectState, insp ProjectInspection) string {
	var b strings.Builder
	b.WriteString(builderProjectContext(goal, project, insp))
	b.WriteString("Propose the file changes that achieve the goal. ")
	b.WriteString("Return JSON only, with no surrounding prose, in exactly this shape:\n")
	b.WriteString(`{"summary":"...","files":[{"path":"relative/path","action":"create|modify|delete","content":"...","rationale":"..."}]}`)
	b.WriteString("\n")
	b.WriteString("Paths must be relative to the project root. Use action \"delete\" with empty content to remove a file. The files array may be empty if no changes are needed.")
	return b.String()
}

// parseProposalContent decodes and validates the provider's proposal JSON. It
// returns the validated summary and files (always a non-nil slice) on success,
// or an error describing the first validation failure. Unlike a plan, an
// unparseable or invalid proposal is an error, not a fallback.
func parseProposalContent(content string) (string, []BuilderProposedFile, error) {
	var parsed struct {
		Summary string                `json:"summary"`
		Files   []BuilderProposedFile `json:"files"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return "", nil, fmt.Errorf("provider content is not valid proposal JSON: %w", err)
	}
	if strings.TrimSpace(parsed.Summary) == "" {
		return "", nil, errors.New("proposal summary is required")
	}
	files := parsed.Files
	if files == nil {
		files = []BuilderProposedFile{}
	}
	for i, f := range files {
		if strings.TrimSpace(f.Path) == "" {
			return "", nil, fmt.Errorf("file %d: path is required", i)
		}
		switch f.Action {
		case proposalActionCreate, proposalActionModify, proposalActionDelete:
		default:
			return "", nil, fmt.Errorf("file %d (%s): action must be create, modify, or delete", i, f.Path)
		}
		if f.Content == "" && f.Action != proposalActionDelete {
			return "", nil, fmt.Errorf("file %d (%s): content is required for action %q", i, f.Path, f.Action)
		}
	}
	return parsed.Summary, files, nil
}

// requestProviderProposal calls the configured provider for a change proposal.
// It returns the validated summary and files, or an error covering any failure
// mode (non-2xx status, transport error, or an invalid/unparseable proposal).
func requestProviderProposal(cfg *providerConfig, systemPrompt, userPrompt string) (string, []BuilderProposedFile, error) {
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
		return "", nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(reqBody)))
	if err != nil {
		return "", nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.apiKey)

	client := &http.Client{Timeout: buildPlanTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBody))
		if msg == "" {
			msg = resp.Status
		}
		return "", nil, fmt.Errorf("provider returned status %d: %s", resp.StatusCode, msg)
	}

	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return "", nil, fmt.Errorf("decode provider response: %w", err)
	}
	if len(envelope.Choices) == 0 {
		return "", nil, errors.New("provider response contained no choices")
	}

	return parseProposalContent(envelope.Choices[0].Message.Content)
}

func (wb *Server) handleBuildPropose(w http.ResponseWriter, r *http.Request) {
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

	// Associate the proposal with the most recent Builder session, if any.
	sessionID := ""
	if cur := wb.sessions.Current(); cur != nil {
		sessionID = cur.ID
	}

	userPrompt := builderProposalUserPrompt(in.Goal, *project, *inspection)
	summary, files, proposeErr := requestProviderProposal(cfg, builderProposalSystemPrompt, userPrompt)
	if proposeErr != nil {
		// Provider failed or returned an invalid change set: record a failed,
		// proposal-only result. No files are ever written.
		proposal := wb.proposals.Append(BuilderChangeProposal{
			SessionID: sessionID,
			Goal:      in.Goal,
			Status:    builderProposalStatusFailed,
			Summary:   builderProposalFallbackSummary,
			Files:     []BuilderProposedFile{},
		})
		if data, err := json.Marshal(proposal); err == nil {
			wb.store.Append("builder.proposal.failed", "Builder change proposal failed", data)
		} else {
			wb.store.Append("builder.proposal.failed", "Builder change proposal failed", nil)
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"proposal": proposal,
			"error":    truncateString(proposeErr.Error(), 1000),
		})
		return
	}

	proposal := wb.proposals.Append(BuilderChangeProposal{
		SessionID: sessionID,
		Goal:      in.Goal,
		Status:    builderProposalStatusProposed,
		Summary:   summary,
		Files:     files,
	})
	data, err := json.Marshal(proposal)
	if err != nil {
		http.Error(w, "marshal proposal: "+err.Error(), http.StatusInternalServerError)
		return
	}
	wb.store.Append("builder.proposal.created", "Created Builder change proposal", data)
	writeJSON(w, http.StatusCreated, proposal)
}

func (wb *Server) handleBuildProposal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	proposal := wb.proposals.Current()
	if proposal == nil {
		http.Error(w, "no proposal", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, *proposal)
}

func (wb *Server) handleBuildProposals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, wb.proposals.List())
}
