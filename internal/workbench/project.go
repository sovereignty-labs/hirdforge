package workbench

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

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
		http.Error(w, `path must be an absolute path on this machine (e.g. /home/you/project or C:\Users\You\project)`, http.StatusBadRequest)
		return
	}
	info, err := os.Stat(in.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "path does not exist: "+in.Path, http.StatusBadRequest)
			return
		}
		http.Error(w, "cannot read path "+in.Path+": "+err.Error(), http.StatusBadRequest)
		return
	}
	if !info.IsDir() {
		http.Error(w, "path is not a directory: "+in.Path, http.StatusBadRequest)
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
