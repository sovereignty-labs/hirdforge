// Package workbench implements the Hirdforge Workbench local runtime: its
// in-memory event log, project/provider state, builder planning and change
// proposals, Lockbox approvals, and the HTTP API that ties them together. The
// cmd/hirdforge-workbench command is a thin entrypoint around this package.
package workbench

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	modeName    = "workbench"
	startupType = "workbench.started"
	startupMsg  = "Hirdforge Workbench started"
)

const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Hirdforge Workbench</title>
<style>
  body { font-family: -apple-system, system-ui, sans-serif; margin: 2rem; color: #222; }
  h1 { font-size: 1.5rem; margin-bottom: 0.25rem; }
  h2 { font-size: 1.1rem; margin-top: 1.5rem; margin-bottom: 0.5rem; }
  p  { color: #555; }
  code { background: #f4f4f4; padding: 0.1rem 0.35rem; border-radius: 3px; }
  ul { list-style: none; padding: 0; }
  li { margin: 0.25rem 0; }
</style>
</head>
<body>
  <h1>Hirdforge Workbench</h1>
  <p>Local-first workbench skeleton. The event stream and builder loop are not wired yet.</p>
  <h2>Endpoints</h2>
  <ul>
    <li><code>GET /api/workbench/events</code></li>
    <li><code>GET /api/workbench/project</code></li>
    <li><code>GET /api/workbench/project/inspect</code></li>
    <li><code>GET /api/workbench/project/inspection</code></li>
    <li><code>GET /api/workbench/provider</code></li>
    <li><code>GET /api/workbench/build/session</code></li>
    <li><code>GET /api/workbench/build/sessions</code></li>
    <li><code>GET /api/workbench/build/prompt</code></li>
    <li><code>GET /api/workbench/build/proposal</code></li>
    <li><code>GET /api/workbench/build/proposals</code></li>
    <li><code>GET /api/workbench/lockbox/request</code></li>
    <li><code>GET /api/workbench/lockbox/requests</code></li>
    <li><code>GET /api/workbench/validation</code></li>
    <li><code>GET /api/workbench/diff</code></li>
  </ul>
</body>
</html>
`

// WorkbenchEvent is a single entry in the in-memory Cortex-lite event log.
type WorkbenchEvent struct {
	ID      string          `json:"id"`
	TS      time.Time       `json:"ts"`
	Type    string          `json:"type"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// eventStore is a small thread-safe append-only log used by the workbench.
type eventStore struct {
	mu     sync.Mutex
	nextID int64
	events []WorkbenchEvent
}

func newEventStore() *eventStore {
	return &eventStore{}
}

// Append records a new event and returns the stored entry, including the
// assigned id and timestamp.
func (s *eventStore) Append(evType, message string, data json.RawMessage) WorkbenchEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	ev := WorkbenchEvent{
		ID:      strconv.FormatInt(s.nextID, 10),
		TS:      time.Now().UTC(),
		Type:    evType,
		Message: message,
		Data:    data,
	}
	s.events = append(s.events, ev)
	return ev
}

// List returns a snapshot copy of the current event log.
func (s *eventStore) List() []WorkbenchEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]WorkbenchEvent, len(s.events))
	copy(out, s.events)
	return out
}

// ProjectState describes the project that is currently open in the workbench.
type ProjectState struct {
	Path          string `json:"path"`
	Name          string `json:"name"`
	Git           bool   `json:"git"`
	CurrentBranch string `json:"current_branch"`
}

// projectState holds the currently open project, if any. A nil current value
// means no project is open.
type projectState struct {
	mu      sync.Mutex
	current *ProjectState
}

func newProjectState() *projectState {
	return &projectState{}
}

func (p *projectState) Get() *ProjectState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current
}

func (p *projectState) Set(state ProjectState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.current = &state
}

// ProjectInspection is a read-only snapshot of the open project's shape: its
// working-tree status, a capped file list, inferred languages, recognized
// config files, derived test hints, and a README excerpt. It never contains
// any provider secret.
type ProjectInspection struct {
	Project       ProjectState `json:"project"`
	GitStatus     string       `json:"git_status"`
	Files         []string     `json:"files"`
	Languages     []string     `json:"languages"`
	ReadmeExcerpt string       `json:"readme_excerpt"`
	ConfigFiles   []string     `json:"config_files"`
	TestHints     []string     `json:"test_hints"`
}

// inspectionState holds the most recent project inspection, if any. A nil
// latest value means no inspection has been captured yet.
type inspectionState struct {
	mu     sync.Mutex
	latest *ProjectInspection
}

func newInspectionState() *inspectionState {
	return &inspectionState{}
}

func (s *inspectionState) Get() *ProjectInspection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest
}

func (s *inspectionState) Set(in ProjectInspection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latest = &in
}

// Server is the Workbench runtime: it ties the event store and project,
// provider, session, inspection, proposal, and Lockbox state to a small HTTP
// mux. It is the public entry type for the package.
type Server struct {
	store      *eventStore
	project    *projectState
	provider   *providerState
	sessions   *sessionStore
	inspection *inspectionState
	proposals  *proposalStore
	lockbox    *lockboxStore
	mux        *http.ServeMux
}

// New constructs a ready-to-serve Workbench Server with empty in-memory state
// and a single startup event already recorded.
func New() *Server {
	s := newEventStore()
	s.Append(startupType, startupMsg, nil)
	wb := &Server{
		store:      s,
		project:    newProjectState(),
		provider:   newProviderState(),
		sessions:   newSessionStore(),
		inspection: newInspectionState(),
		proposals:  newProposalStore(),
		lockbox:    newLockboxStore(),
	}
	wb.mux = wb.registerRoutes()
	return wb
}

// Handler returns the HTTP handler that serves the Workbench API.
func (wb *Server) Handler() http.Handler {
	return wb.mux
}

// isGitRepo reports whether path contains a .git directory or file (the
// latter covers submodules and worktrees).
func isGitRepo(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

// detectBranch returns the current branch name for the repo at path, or an
// error if the branch cannot be determined. Callers should treat any error
// as "unknown" and leave the branch field empty.
func detectBranch(path string) (string, error) {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ProviderState is the sanitized view of the configured model provider. The
// raw API key is never exposed through this type.
type ProviderState struct {
	BaseURL   string `json:"base_url"`
	APIKeySet bool   `json:"api_key_set"`
	Model     string `json:"model"`
}

// providerConfig is the internal record stored for the configured provider.
// The raw API key lives here and must never be returned in any response.
type providerConfig struct {
	baseURL string
	apiKey  string
	model   string
}

// providerState holds the currently configured provider, if any. A nil cfg
// means no provider is configured.
type providerState struct {
	mu  sync.Mutex
	cfg *providerConfig
}

func newProviderState() *providerState {
	return &providerState{}
}

// Get returns a sanitized snapshot of the provider state, or nil if no
// provider is configured.
func (p *providerState) Get() *ProviderState {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cfg == nil {
		return nil
	}
	return &ProviderState{
		BaseURL:   p.cfg.baseURL,
		APIKeySet: p.cfg.apiKey != "",
		Model:     p.cfg.model,
	}
}

// Config returns a copy of the internal configuration including the raw API
// key. It is intended for the test endpoint only and the result must not be
// exposed to API clients.
func (p *providerState) Config() *providerConfig {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cfg == nil {
		return nil
	}
	return &providerConfig{
		baseURL: p.cfg.baseURL,
		apiKey:  p.cfg.apiKey,
		model:   p.cfg.model,
	}
}

func (p *providerState) Set(cfg providerConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg = &cfg
}

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

// LockboxApprovalRequest is a human approval boundary around a Builder change
// proposal. It is a Lockbox-lite record only: approving or rejecting a request
// records the decision but never applies the proposed files. It never carries
// the provider API key.
type LockboxApprovalRequest struct {
	ID             string                `json:"id"`
	TS             time.Time             `json:"ts"`
	ProposalID     string                `json:"proposal_id"`
	Goal           string                `json:"goal"`
	Status         string                `json:"status"`
	Summary        string                `json:"summary"`
	Files          []BuilderProposedFile `json:"files"`
	DecisionTS     *time.Time            `json:"decision_ts"`
	DecisionReason string                `json:"decision_reason"`
}

const (
	lockboxStatusPending  = "pending"
	lockboxStatusApproved = "approved"
	lockboxStatusRejected = "rejected"
)

var (
	errLockboxNotFound   = errors.New("approval request not found")
	errLockboxNotPending = errors.New("approval request is not pending")
)

// cloneLockboxRequest deep-copies the mutable parts of a request (the files
// slice and the decision timestamp pointer) so callers can never mutate the
// store's internal state through a returned value.
func cloneLockboxRequest(in LockboxApprovalRequest) LockboxApprovalRequest {
	out := in
	if in.Files != nil {
		out.Files = make([]BuilderProposedFile, len(in.Files))
		copy(out.Files, in.Files)
	}
	if in.DecisionTS != nil {
		ts := *in.DecisionTS
		out.DecisionTS = &ts
	}
	return out
}

// lockboxStore is the in-memory record of Lockbox approval requests. Every
// accessor returns a deep copy, never a pointer into the stored slice.
type lockboxStore struct {
	mu       sync.Mutex
	nextID   int64
	requests []LockboxApprovalRequest
}

func newLockboxStore() *lockboxStore {
	return &lockboxStore{}
}

// Append assigns an id and timestamp to in, stores it, and returns a copy of
// the stored value.
func (s *lockboxStore) Append(in LockboxApprovalRequest) LockboxApprovalRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	in.ID = strconv.FormatInt(s.nextID, 10)
	in.TS = time.Now().UTC()
	s.requests = append(s.requests, in)
	return cloneLockboxRequest(in)
}

// List returns deep copies of all requests in insertion order (oldest first,
// newest last).
func (s *lockboxStore) List() []LockboxApprovalRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LockboxApprovalRequest, len(s.requests))
	for i := range s.requests {
		out[i] = cloneLockboxRequest(s.requests[i])
	}
	return out
}

// Current returns a deep copy of the most recent request, or nil if there are
// none.
func (s *lockboxStore) Current() *LockboxApprovalRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return nil
	}
	c := cloneLockboxRequest(s.requests[len(s.requests)-1])
	return &c
}

// Approve transitions the pending request with the given id to approved.
func (s *lockboxStore) Approve(id, reason string) (*LockboxApprovalRequest, error) {
	return s.decide(id, lockboxStatusApproved, reason)
}

// Reject transitions the pending request with the given id to rejected.
func (s *lockboxStore) Reject(id, reason string) (*LockboxApprovalRequest, error) {
	return s.decide(id, lockboxStatusRejected, reason)
}

// decide records a decision on a pending request, returning a deep copy of the
// updated request. It returns errLockboxNotFound or errLockboxNotPending when
// the request is missing or already decided.
func (s *lockboxStore) decide(id, status, reason string) (*LockboxApprovalRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.requests {
		if s.requests[i].ID != id {
			continue
		}
		if s.requests[i].Status != lockboxStatusPending {
			return nil, errLockboxNotPending
		}
		now := time.Now().UTC()
		s.requests[i].Status = status
		s.requests[i].DecisionTS = &now
		s.requests[i].DecisionReason = reason
		c := cloneLockboxRequest(s.requests[i])
		return &c, nil
	}
	return nil, errLockboxNotFound
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (wb *Server) registerRoutes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "ok",
			"mode":   modeName,
		})
	})

	mux.HandleFunc("/api/workbench/events", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, wb.store.List())
		case http.MethodPost:
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if err != nil {
				http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
				return
			}
			var in struct {
				Type    string          `json:"type"`
				Message string          `json:"message"`
				Data    json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(body, &in); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if in.Type == "" {
				http.Error(w, "type is required", http.StatusBadRequest)
				return
			}
			ev := wb.store.Append(in.Type, in.Message, in.Data)
			writeJSON(w, http.StatusCreated, ev)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/api/workbench/project/open", wb.handleProjectOpen)
	mux.HandleFunc("/api/workbench/project/inspect", wb.handleProjectInspect)
	mux.HandleFunc("/api/workbench/project/inspection", wb.handleProjectInspection)
	mux.HandleFunc("/api/workbench/project", wb.handleProjectGet)

	mux.HandleFunc("/api/workbench/provider/test", wb.handleProviderTest)
	mux.HandleFunc("/api/workbench/provider", wb.handleProviderRoot)

	mux.HandleFunc("/api/workbench/build/start", wb.handleBuildStart)
	mux.HandleFunc("/api/workbench/build/session", wb.handleBuildSession)
	mux.HandleFunc("/api/workbench/build/sessions", wb.handleBuildSessions)
	mux.HandleFunc("/api/workbench/build/prompt", wb.handleBuildPrompt)
	mux.HandleFunc("/api/workbench/build/propose", wb.handleBuildPropose)
	mux.HandleFunc("/api/workbench/build/proposal", wb.handleBuildProposal)
	mux.HandleFunc("/api/workbench/build/proposals", wb.handleBuildProposals)

	mux.HandleFunc("/api/workbench/lockbox/request", wb.handleLockboxRequest)
	mux.HandleFunc("/api/workbench/lockbox/requests", wb.handleLockboxRequests)
	mux.HandleFunc("/api/workbench/lockbox/approve", wb.handleLockboxApprove)
	mux.HandleFunc("/api/workbench/lockbox/reject", wb.handleLockboxReject)

	mux.HandleFunc("/api/workbench/validation", wb.handleValidation)
	mux.HandleFunc("/api/workbench/diff", wb.handleDiff)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(indexHTML))
	})

	return mux
}

func (wb *Server) handleProjectOpen(w http.ResponseWriter, r *http.Request) {
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
		Path string `json:"path"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if in.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if !filepath.IsAbs(in.Path) {
		http.Error(w, "path must be absolute", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(in.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "path does not exist", http.StatusBadRequest)
			return
		}
		http.Error(w, "stat: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !info.IsDir() {
		http.Error(w, "path must be a directory", http.StatusBadRequest)
		return
	}

	state := ProjectState{
		Path: in.Path,
		Name: filepath.Base(in.Path),
		Git:  isGitRepo(in.Path),
	}
	if state.Git {
		if branch, err := detectBranch(in.Path); err == nil {
			state.CurrentBranch = branch
		}
	}

	wb.project.Set(state)

	data, err := json.Marshal(state)
	if err != nil {
		http.Error(w, "marshal state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	wb.store.Append("project.opened", "Opened project "+state.Name, data)
	writeJSON(w, http.StatusCreated, state)
}

func (wb *Server) handleProjectGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state := wb.project.Get()
	if state == nil {
		http.Error(w, "no project open", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, *state)
}

const (
	// maxInspectFiles caps the recursive file listing produced by inspection.
	maxInspectFiles = 200
	// maxReadmeExcerpt caps the README excerpt stored in an inspection.
	maxReadmeExcerpt = 4000
)

// inspectSkipDirs are directory names skipped during the recursive file walk.
var inspectSkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	".venv":        true,
	"__pycache__":  true,
}

// inspectConfigFiles is the ordered set of recognized project config files.
var inspectConfigFiles = []string{
	"go.mod",
	"package.json",
	"pyproject.toml",
	"requirements.txt",
	"Cargo.toml",
	"Makefile",
	"Taskfile.yml",
	"docker-compose.yml",
	"Dockerfile",
	".gitignore",
}

// extLanguages maps a (lowercased) file extension to a language name.
var extLanguages = map[string]string{
	".go":   "Go",
	".py":   "Python",
	".js":   "JavaScript",
	".ts":   "TypeScript",
	".tsx":  "TypeScript",
	".jsx":  "JavaScript",
	".rs":   "Rust",
	".java": "Java",
	".c":    "C",
	".cpp":  "C++",
	".h":    "C",
	".md":   "Markdown",
	".yaml": "YAML",
	".yml":  "YAML",
	".json": "JSON",
	".toml": "TOML",
	".sh":   "Shell",
}

// errFileCap is the sentinel used to stop the file walk once the cap is hit.
var errFileCap = errors.New("inspect: file cap reached")

// truncateRunes returns s limited to at most max runes, avoiding splitting a
// multi-byte rune the way a byte slice would.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// walkProjectFiles returns up to maxInspectFiles paths (relative to root, slash
// separated) found by a recursive walk that skips inspectSkipDirs. The walk is
// best-effort: unreadable entries are skipped rather than failing the listing.
func walkProjectFiles(root string) []string {
	files := make([]string, 0, maxInspectFiles)
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if inspectSkipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		files = append(files, filepath.ToSlash(rel))
		if len(files) >= maxInspectFiles {
			return errFileCap
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errFileCap) {
		// Best-effort: return whatever was collected before the error.
		return files
	}
	return files
}

// detectLanguages returns the sorted, deduplicated set of languages implied by
// the extensions of the given files.
func detectLanguages(files []string) []string {
	set := map[string]bool{}
	for _, f := range files {
		if lang, ok := extLanguages[strings.ToLower(filepath.Ext(f))]; ok {
			set[lang] = true
		}
	}
	langs := make([]string, 0, len(set))
	for l := range set {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	return langs
}

// detectConfigFiles returns the recognized config files present at the project
// root, in inspectConfigFiles order.
func detectConfigFiles(root string) []string {
	found := make([]string, 0, len(inspectConfigFiles))
	for _, name := range inspectConfigFiles {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil && !info.IsDir() {
			found = append(found, name)
		}
	}
	return found
}

// deriveTestHints maps the present config files to test command hints. pytest
// is emitted once even if both Python config files are present.
func deriveTestHints(configFiles []string) []string {
	present := make(map[string]bool, len(configFiles))
	for _, c := range configFiles {
		present[c] = true
	}
	hints := []string{}
	if present["go.mod"] {
		hints = append(hints, "go test ./...")
	}
	if present["package.json"] {
		hints = append(hints, "npm test")
	}
	if present["pyproject.toml"] || present["requirements.txt"] {
		hints = append(hints, "pytest")
	}
	if present["Cargo.toml"] {
		hints = append(hints, "cargo test")
	}
	if present["Makefile"] {
		hints = append(hints, "make test")
	}
	return hints
}

// readReadmeExcerpt returns the first recognized README file's content at root,
// truncated to max runes, or "" if none is present or readable.
func readReadmeExcerpt(root string, max int) string {
	for _, name := range []string{"README.md", "README", "readme.md"} {
		p := filepath.Join(root, name)
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		return truncateRunes(string(data), max)
	}
	return ""
}

// inspectProject builds a read-only ProjectInspection. It never mutates the
// project and only shells out to "git -C <path> status --short" for git repos.
func inspectProject(project ProjectState) ProjectInspection {
	insp := ProjectInspection{
		Project:     project,
		Files:       walkProjectFiles(project.Path),
		ConfigFiles: detectConfigFiles(project.Path),
	}
	insp.Languages = detectLanguages(insp.Files)
	insp.TestHints = deriveTestHints(insp.ConfigFiles)
	insp.ReadmeExcerpt = readReadmeExcerpt(project.Path, maxReadmeExcerpt)
	if project.Git {
		if out, err := exec.Command("git", "-C", project.Path, "status", "--short").Output(); err == nil {
			insp.GitStatus = string(out)
		}
	}
	return insp
}

// runInspection inspects the project, stores it as the latest inspection, and
// records a project.inspected event. It returns the produced inspection.
func (wb *Server) runInspection(project ProjectState) ProjectInspection {
	insp := inspectProject(project)
	wb.inspection.Set(insp)
	if data, err := json.Marshal(insp); err == nil {
		wb.store.Append("project.inspected", "Inspected project "+project.Name, data)
	} else {
		wb.store.Append("project.inspected", "Inspected project "+project.Name, nil)
	}
	return insp
}

func (wb *Server) handleProjectInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	project := wb.project.Get()
	if project == nil {
		http.Error(w, "no project open", http.StatusConflict)
		return
	}
	insp := wb.runInspection(*project)
	writeJSON(w, http.StatusOK, insp)
}

func (wb *Server) handleProjectInspection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	insp := wb.inspection.Get()
	if insp == nil {
		http.Error(w, "no inspection", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, *insp)
}

func (wb *Server) handleProviderRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state := wb.provider.Get()
		if state == nil {
			http.Error(w, "no provider configured", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, *state)
	case http.MethodPost:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var in struct {
			BaseURL string `json:"base_url"`
			APIKey  string `json:"api_key"`
			Model   string `json:"model"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if in.BaseURL == "" {
			http.Error(w, "base_url is required", http.StatusBadRequest)
			return
		}
		if in.APIKey == "" {
			http.Error(w, "api_key is required", http.StatusBadRequest)
			return
		}
		if in.Model == "" {
			http.Error(w, "model is required", http.StatusBadRequest)
			return
		}
		u, err := url.Parse(in.BaseURL)
		if err != nil {
			http.Error(w, "invalid base_url: "+err.Error(), http.StatusBadRequest)
			return
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			http.Error(w, "base_url must be http or https", http.StatusBadRequest)
			return
		}
		if u.Host == "" {
			http.Error(w, "base_url must have a host", http.StatusBadRequest)
			return
		}

		wb.provider.Set(providerConfig{
			baseURL: in.BaseURL,
			apiKey:  in.APIKey,
			model:   in.Model,
		})

		state := ProviderState{
			BaseURL:   in.BaseURL,
			APIKeySet: true,
			Model:     in.Model,
		}
		data, err := json.Marshal(state)
		if err != nil {
			http.Error(w, "marshal state: "+err.Error(), http.StatusInternalServerError)
			return
		}
		wb.store.Append("provider.configured", "Configured provider model "+in.Model, data)
		writeJSON(w, http.StatusCreated, state)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

const providerTestTimeout = 20 * time.Second

func (wb *Server) handleProviderTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := wb.provider.Config()
	if cfg == nil {
		http.Error(w, "no provider configured", http.StatusNotFound)
		return
	}

	endpoint := strings.TrimRight(cfg.baseURL, "/") + "/chat/completions"
	reqBody, err := json.Marshal(map[string]any{
		"model": cfg.model,
		"messages": []map[string]string{
			{"role": "user", "content": "Reply with exactly: hirdforge provider online"},
		},
		"temperature": 0,
	})
	if err != nil {
		http.Error(w, "marshal request: "+err.Error(), http.StatusInternalServerError)
		return
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(reqBody)))
	if err != nil {
		wb.store.Append("provider.test_failed", "Provider test failed", nil)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":    false,
			"error": "build request: " + err.Error(),
		})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.apiKey)

	client := &http.Client{Timeout: providerTestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		wb.store.Append("provider.test_failed", "Provider test failed", nil)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		wb.store.Append("provider.test_failed", "Provider test failed", nil)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":    false,
			"error": "read response: " + err.Error(),
		})
		return
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		wb.store.Append("provider.tested", "Provider test succeeded", nil)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"status": resp.StatusCode,
			"model":  cfg.model,
		})
		return
	}

	errMsg := string(respBody)
	if len(errMsg) > 1000 {
		errMsg = errMsg[:1000]
	}
	wb.store.Append("provider.test_failed", "Provider test failed", nil)
	writeJSON(w, http.StatusBadGateway, map[string]any{
		"ok":     false,
		"status": resp.StatusCode,
		"error":  errMsg,
	})
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

// handleLockboxRequest serves GET (current approval request) and POST (create a
// new approval request from a proposed change proposal).
func (wb *Server) handleLockboxRequest(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		request := wb.lockbox.Current()
		if request == nil {
			http.Error(w, "no approval request", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, *request)
	case http.MethodPost:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var in struct {
			ProposalID string `json:"proposal_id"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}

		var proposal *BuilderChangeProposal
		if in.ProposalID == "" {
			proposal = wb.proposals.Current()
			if proposal == nil {
				http.Error(w, "no proposal to request approval for", http.StatusConflict)
				return
			}
		} else {
			proposal = wb.proposals.Find(in.ProposalID)
			if proposal == nil {
				http.Error(w, "proposal not found", http.StatusNotFound)
				return
			}
		}
		if proposal.Status != builderProposalStatusProposed {
			http.Error(w, "proposal is not in proposed status", http.StatusConflict)
			return
		}

		// Copy the proposed files so the request owns an independent slice.
		files := make([]BuilderProposedFile, len(proposal.Files))
		copy(files, proposal.Files)

		request := wb.lockbox.Append(LockboxApprovalRequest{
			ProposalID: proposal.ID,
			Goal:       proposal.Goal,
			Status:     lockboxStatusPending,
			Summary:    proposal.Summary,
			Files:      files,
		})
		data, err := json.Marshal(request)
		if err != nil {
			http.Error(w, "marshal request: "+err.Error(), http.StatusInternalServerError)
			return
		}
		wb.store.Append("lockbox.request.created", "Created Lockbox approval request", data)
		writeJSON(w, http.StatusCreated, request)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (wb *Server) handleLockboxRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, wb.lockbox.List())
}

func (wb *Server) handleLockboxApprove(w http.ResponseWriter, r *http.Request) {
	wb.handleLockboxDecision(w, r, lockboxStatusApproved)
}

func (wb *Server) handleLockboxReject(w http.ResponseWriter, r *http.Request) {
	wb.handleLockboxDecision(w, r, lockboxStatusRejected)
}

// handleLockboxDecision approves or rejects a pending request by id. It records
// the decision only; no proposed files are ever applied.
func (wb *Server) handleLockboxDecision(w http.ResponseWriter, r *http.Request, decision string) {
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
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if in.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	var (
		request   *LockboxApprovalRequest
		decideErr error
		eventType string
		eventMsg  string
	)
	if decision == lockboxStatusApproved {
		request, decideErr = wb.lockbox.Approve(in.ID, in.Reason)
		eventType, eventMsg = "lockbox.request.approved", "Approved Lockbox request"
	} else {
		request, decideErr = wb.lockbox.Reject(in.ID, in.Reason)
		eventType, eventMsg = "lockbox.request.rejected", "Rejected Lockbox request"
	}
	if decideErr != nil {
		switch {
		case errors.Is(decideErr, errLockboxNotFound):
			http.Error(w, "approval request not found", http.StatusNotFound)
		case errors.Is(decideErr, errLockboxNotPending):
			http.Error(w, "approval request is not pending", http.StatusConflict)
		default:
			http.Error(w, decideErr.Error(), http.StatusInternalServerError)
		}
		return
	}

	data, err := json.Marshal(request)
	if err != nil {
		http.Error(w, "marshal request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	wb.store.Append(eventType, eventMsg, data)
	writeJSON(w, http.StatusOK, *request)
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

func (wb *Server) handleValidation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectOpen := wb.project.Get() != nil
	providerConfigured := wb.provider.Get() != nil
	currentSession := wb.sessions.Current() != nil

	status := "blocked"
	if projectOpen && providerConfigured {
		status = "ready"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"project_open":        projectOpen,
		"provider_configured": providerConfigured,
		"current_session":     currentSession,
		"status":              status,
	})
}

func (wb *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	project := wb.project.Get()
	if project == nil {
		http.Error(w, "no project open", http.StatusConflict)
		return
	}
	if !project.Git {
		http.Error(w, "project is not a git repo", http.StatusConflict)
		return
	}

	wb.store.Append("diff.requested", "Requested project diff", nil)

	statOut, statErr := exec.Command("git", "-C", project.Path, "diff", "--stat").Output()
	if statErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":    false,
			"error": truncateString("git diff --stat failed: "+statErr.Error(), 1000),
		})
		return
	}
	diffOut, diffErr := exec.Command("git", "-C", project.Path, "diff", "--no-ext-diff").Output()
	if diffErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":    false,
			"error": truncateString("git diff --no-ext-diff failed: "+diffErr.Error(), 1000),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"stat": string(statOut),
		"diff": string(diffOut),
	})
}

func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
