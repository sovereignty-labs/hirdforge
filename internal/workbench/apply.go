package workbench

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CortexAppliedFile is the per-file outcome of applying an approved aggregate.
type CortexAppliedFile struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

// CortexApplyResult records the outcome of applying an approved Cortex
// aggregate's files to the opened project. It never carries the provider API
// key. This is the only slice that writes files, and it only does so behind the
// Lockbox approval gate.
type CortexApplyResult struct {
	ID               string              `json:"id"`
	TS               time.Time           `json:"ts"`
	AggregateID      string              `json:"aggregate_id"`
	LockboxRequestID string              `json:"lockbox_request_id"`
	Status           string              `json:"status"`
	Files            []CortexAppliedFile `json:"files"`
	Error            string              `json:"error"`
}

const (
	cortexApplyStatusApplied = "applied"
	cortexApplyStatusFailed  = "failed"
)

// cloneCortexApplyResult deep-copies the files slice so callers can never mutate
// the store's internal state through a returned value.
func cloneCortexApplyResult(in CortexApplyResult) CortexApplyResult {
	out := in
	if in.Files != nil {
		out.Files = make([]CortexAppliedFile, len(in.Files))
		copy(out.Files, in.Files)
	}
	return out
}

// cortexApplyStore is the in-memory record of Cortex apply results. Every
// accessor returns a deep copy, never a pointer into the stored slice.
type cortexApplyStore struct {
	mu      sync.Mutex
	nextID  int64
	results []CortexApplyResult
}

func newCortexApplyStore() *cortexApplyStore {
	return &cortexApplyStore{}
}

// Append assigns an id and timestamp to in, stores it, and returns a copy.
func (s *cortexApplyStore) Append(in CortexApplyResult) CortexApplyResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	in.ID = strconv.FormatInt(s.nextID, 10)
	in.TS = time.Now().UTC()
	s.results = append(s.results, in)
	return cloneCortexApplyResult(in)
}

// Current returns a deep copy of the most recent result, or nil if none.
func (s *cortexApplyStore) Current() *CortexApplyResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.results) == 0 {
		return nil
	}
	c := cloneCortexApplyResult(s.results[len(s.results)-1])
	return &c
}

// Find returns a deep copy of the result with the given id, or nil.
func (s *cortexApplyStore) Find(id string) *CortexApplyResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.results {
		if s.results[i].ID == id {
			c := cloneCortexApplyResult(s.results[i])
			return &c
		}
	}
	return nil
}

// List returns deep copies of all results in insertion order.
func (s *cortexApplyStore) List() []CortexApplyResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CortexApplyResult, len(s.results))
	for i := range s.results {
		out[i] = cloneCortexApplyResult(s.results[i])
	}
	return out
}

// ListByAggregate returns deep copies of results for the given aggregate id.
func (s *cortexApplyStore) ListByAggregate(aggregateID string) []CortexApplyResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []CortexApplyResult{}
	for i := range s.results {
		if s.results[i].AggregateID == aggregateID {
			out = append(out, cloneCortexApplyResult(s.results[i]))
		}
	}
	return out
}

// resolveProjectPath resolves a project-relative path against root and returns
// the cleaned absolute target. It rejects empty/absolute/traversal paths, paths
// whose cleaned form escapes root, and symlink escapes detected by resolving the
// deepest existing path on the chain. It never creates anything.
func resolveProjectPath(root, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("path is empty")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative to the project root", rel)
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}

	target := filepath.Clean(filepath.Join(rootAbs, rel))
	relToRoot, err := filepath.Rel(rootAbs, target)
	if err != nil {
		return "", fmt.Errorf("path %q is not within the project root", rel)
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the project root", rel)
	}
	if target == rootAbs {
		return "", fmt.Errorf("path %q resolves to the project root itself", rel)
	}

	// Symlink-escape check: resolve symlinks on the deepest existing path of the
	// target's chain and verify it still lives under the resolved project root.
	rootEval, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		rootEval = rootAbs
	}
	check := target
	for {
		if _, statErr := os.Lstat(check); statErr == nil {
			break
		}
		parent := filepath.Dir(check)
		if parent == check {
			break
		}
		check = parent
	}
	if evalCheck, evalErr := filepath.EvalSymlinks(check); evalErr == nil {
		r, relErr := filepath.Rel(rootEval, evalCheck)
		if relErr != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q escapes the project root via a symlink", rel)
		}
	}

	return target, nil
}

// atomicWriteFile writes content to path atomically: it creates parent
// directories, writes a temp file in the same directory, then renames it over
// the target, removing the temp file on error. New files get mode 0644; when
// overwriting, the existing file mode is preserved where practical.
func atomicWriteFile(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		mode = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(dir, ".hirdforge-apply-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		cleanup()
		return fmt.Errorf("set temp file mode: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

// applyOneFile applies a single proposed change inside projectPath using the
// safe path resolver and atomic write. It never follows symlinks for the
// target.
func applyOneFile(projectPath string, f BuilderProposedFile) error {
	target, err := resolveProjectPath(projectPath, f.Path)
	if err != nil {
		return err
	}
	switch f.Action {
	case proposalActionCreate:
		if info, statErr := os.Lstat(target); statErr == nil {
			if !info.Mode().IsRegular() {
				return errors.New("create target exists and is not a regular file")
			}
			existing, readErr := os.ReadFile(target)
			if readErr != nil {
				return fmt.Errorf("create target exists and could not be read: %w", readErr)
			}
			if string(existing) != f.Content {
				return errors.New("create target already exists with different content")
			}
			return nil // identical content: no-op
		}
		return atomicWriteFile(target, []byte(f.Content))
	case proposalActionModify:
		info, statErr := os.Lstat(target)
		if statErr != nil {
			return errors.New("modify target does not exist")
		}
		if !info.Mode().IsRegular() {
			return errors.New("modify target is not a regular file")
		}
		return atomicWriteFile(target, []byte(f.Content))
	case proposalActionDelete:
		info, statErr := os.Lstat(target)
		if statErr != nil {
			return errors.New("delete target does not exist")
		}
		if !info.Mode().IsRegular() {
			return errors.New("delete target is not a regular file")
		}
		return os.Remove(target)
	default:
		return fmt.Errorf("unsupported action %q", f.Action)
	}
}

// applyAggregateFiles applies files to projectPath in order, returning a status
// for every file attempted. It stops on the first failure and does NOT roll
// back already-applied files in this slice.
func applyAggregateFiles(projectPath string, files []BuilderProposedFile) ([]CortexAppliedFile, error) {
	applied := []CortexAppliedFile{}
	for _, f := range files {
		entry := CortexAppliedFile{Path: f.Path, Action: f.Action, Status: cortexApplyStatusApplied}
		if err := applyOneFile(projectPath, f); err != nil {
			entry.Status = cortexApplyStatusFailed
			entry.Error = truncateString(err.Error(), 1000)
			applied = append(applied, entry)
			return applied, fmt.Errorf("file %q: %w", f.Path, err)
		}
		applied = append(applied, entry)
	}
	return applied, nil
}

// handleCortexApply serves GET (current apply result) and POST (apply an
// approved aggregate's files behind the Lockbox gate).
func (wb *Server) handleCortexApply(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		result := wb.cortexApplies.Current()
		if result == nil {
			http.Error(w, "no cortex apply result", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, *result)
	case http.MethodPost:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var in struct {
			LockboxRequestID string `json:"lockbox_request_id"`
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &in); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
		}

		var request *LockboxApprovalRequest
		if in.LockboxRequestID == "" {
			request = wb.lockbox.Current()
		} else {
			for _, lr := range wb.lockbox.List() {
				if lr.ID == in.LockboxRequestID {
					found := lr
					request = &found
					break
				}
			}
		}
		if request == nil {
			http.Error(w, "lockbox request not found", http.StatusNotFound)
			return
		}
		if request.Status != lockboxStatusApproved {
			http.Error(w, "lockbox request is not approved", http.StatusConflict)
			return
		}
		if !strings.HasPrefix(request.ProposalID, "aggregate:") {
			http.Error(w, "lockbox request is not a Cortex aggregate request", http.StatusConflict)
			return
		}

		aggregateID := strings.TrimPrefix(request.ProposalID, "aggregate:")
		agg := wb.cortexAggregates.Find(aggregateID)
		if agg == nil {
			http.Error(w, "cortex aggregate not found", http.StatusNotFound)
			return
		}
		if agg.Status != cortexAggregateStatusAggregated {
			http.Error(w, "cortex aggregate is not in aggregated status", http.StatusConflict)
			return
		}

		project := wb.project.Get()
		if project == nil {
			http.Error(w, "project must be open", http.StatusConflict)
			return
		}

		applied, applyErr := applyAggregateFiles(project.Path, agg.Files)
		result := CortexApplyResult{
			AggregateID:      agg.ID,
			LockboxRequestID: request.ID,
			Files:            applied,
		}
		if applyErr != nil {
			result.Status = cortexApplyStatusFailed
			result.Error = truncateString(applyErr.Error()+" (no rollback performed)", 1000)
			stored := wb.cortexApplies.Append(result)
			wb.appendCortexEvent("cortex.apply.failed", "Failed to apply Cortex aggregate", stored)
			writeJSON(w, http.StatusBadGateway, stored)
			return
		}

		result.Status = cortexApplyStatusApplied
		stored := wb.cortexApplies.Append(result)
		wb.appendCortexEvent("cortex.apply.completed", "Applied Cortex aggregate", stored)
		writeJSON(w, http.StatusOK, stored)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (wb *Server) handleCortexApplies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	aggregateID := r.URL.Query().Get("aggregate_id")
	var out []CortexApplyResult
	if aggregateID != "" {
		out = wb.cortexApplies.ListByAggregate(aggregateID)
	} else {
		out = wb.cortexApplies.List()
	}
	writeJSON(w, http.StatusOK, out)
}
