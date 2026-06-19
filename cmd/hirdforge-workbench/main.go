package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultHost = "127.0.0.1"
	defaultPort = "7777"
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

// workbench ties the event store and project state to a small HTTP mux.
type workbench struct {
	store      *eventStore
	project    *projectState
	provider   *providerState
	sessions   *sessionStore
	inspection *inspectionState
	mux        *http.ServeMux
}

func newWorkbench() *workbench {
	s := newEventStore()
	s.Append(startupType, startupMsg, nil)
	wb := &workbench{
		store:      s,
		project:    newProjectState(),
		provider:   newProviderState(),
		sessions:   newSessionStore(),
		inspection: newInspectionState(),
	}
	wb.mux = wb.registerRoutes()
	return wb
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

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (wb *workbench) registerRoutes() *http.ServeMux {
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

func (wb *workbench) handleProjectOpen(w http.ResponseWriter, r *http.Request) {
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

func (wb *workbench) handleProjectGet(w http.ResponseWriter, r *http.Request) {
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
func (wb *workbench) runInspection(project ProjectState) ProjectInspection {
	insp := inspectProject(project)
	wb.inspection.Set(insp)
	if data, err := json.Marshal(insp); err == nil {
		wb.store.Append("project.inspected", "Inspected project "+project.Name, data)
	} else {
		wb.store.Append("project.inspected", "Inspected project "+project.Name, nil)
	}
	return insp
}

func (wb *workbench) handleProjectInspect(w http.ResponseWriter, r *http.Request) {
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

func (wb *workbench) handleProjectInspection(w http.ResponseWriter, r *http.Request) {
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

func (wb *workbench) handleProviderRoot(w http.ResponseWriter, r *http.Request) {
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

func (wb *workbench) handleProviderTest(w http.ResponseWriter, r *http.Request) {
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

// builderUserPrompt renders the user message for a Builder planning call. It
// describes the goal, the open project, and the read-only inspection context,
// then instructs the provider to reply with a JSON-only plan.
func builderUserPrompt(goal string, project ProjectState, insp ProjectInspection) string {
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

	b.WriteString("Return JSON only, with no surrounding prose, in exactly this shape:\n")
	b.WriteString(`{"plan":["step 1","step 2","step 3"]}`)
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

func (wb *workbench) handleBuildStart(w http.ResponseWriter, r *http.Request) {
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

func (wb *workbench) handleBuildPrompt(w http.ResponseWriter, r *http.Request) {
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

func (wb *workbench) handleBuildSession(w http.ResponseWriter, r *http.Request) {
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

func (wb *workbench) handleBuildSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, wb.sessions.List())
}

func (wb *workbench) handleValidation(w http.ResponseWriter, r *http.Request) {
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

func (wb *workbench) handleDiff(w http.ResponseWriter, r *http.Request) {
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

func resolveAddr() string {
	host := os.Getenv("HIRDFORGE_WORKBENCH_HOST")
	if host == "" {
		host = defaultHost
	}
	port := os.Getenv("HIRDFORGE_WORKBENCH_PORT")
	if port == "" {
		port = defaultPort
	}
	return net.JoinHostPort(host, port)
}

func run(ctx context.Context, addr string) error {
	wb := newWorkbench()
	server := &http.Server{
		Addr:              addr,
		Handler:           wb.mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("hirdforge-workbench: listening on http://%s (mode=%s)", addr, modeName)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func main() {
	addrFlag := flag.String("addr", "", "listen address (host:port); overrides HIRDFORGE_WORKBENCH_HOST/PORT")
	flag.Parse()

	addr := *addrFlag
	if addr == "" {
		addr = resolveAddr()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, addr); err != nil {
		fmt.Fprintf(os.Stderr, "hirdforge-workbench: %v\n", err)
		os.Exit(1)
	}
}
