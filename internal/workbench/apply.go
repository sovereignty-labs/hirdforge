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

		request, agg, project, gateStatus, gateMsg := wb.cortexApplyGate(in.LockboxRequestID)
		if gateStatus != 0 {
			http.Error(w, gateMsg, gateStatus)
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

// cortexApplyGate resolves and validates the approved aggregate Lockbox request
// shared by apply and apply-preview. On failure it returns a non-zero HTTP
// status and a message; on success the status is 0 and the resolved request,
// aggregate, and open project are returned.
func (wb *Server) cortexApplyGate(lockboxRequestID string) (*LockboxApprovalRequest, *CortexAggregateProposal, *ProjectState, int, string) {
	var request *LockboxApprovalRequest
	if lockboxRequestID == "" {
		request = wb.lockbox.Current()
	} else {
		for _, lr := range wb.lockbox.List() {
			if lr.ID == lockboxRequestID {
				found := lr
				request = &found
				break
			}
		}
	}
	if request == nil {
		return nil, nil, nil, http.StatusNotFound, "lockbox request not found"
	}
	if request.Status != lockboxStatusApproved {
		return nil, nil, nil, http.StatusConflict, "lockbox request is not approved"
	}
	if !strings.HasPrefix(request.ProposalID, "aggregate:") {
		return nil, nil, nil, http.StatusConflict, "lockbox request is not a Cortex aggregate request"
	}

	agg := wb.cortexAggregates.Find(strings.TrimPrefix(request.ProposalID, "aggregate:"))
	if agg == nil {
		return nil, nil, nil, http.StatusNotFound, "cortex aggregate not found"
	}
	if agg.Status != cortexAggregateStatusAggregated {
		return nil, nil, nil, http.StatusConflict, "cortex aggregate is not in aggregated status"
	}

	project := wb.project.Get()
	if project == nil {
		return nil, nil, nil, http.StatusConflict, "project must be open"
	}
	return request, agg, project, 0, ""
}

// CortexApplyPreviewFile is the per-file planned effect of an apply, computed
// without touching the filesystem beyond reading existing files.
type CortexApplyPreviewFile struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Status string `json:"status"`
	Exists bool   `json:"exists"`
	Diff   string `json:"diff"`
	Error  string `json:"error"`
}

// CortexApplyPreview is a non-writing preview of applying an approved aggregate:
// it reports per-file planned effects, current file state, and a diff-like
// preview. It never carries the provider API key and never writes files.
type CortexApplyPreview struct {
	ID               string                   `json:"id"`
	TS               time.Time                `json:"ts"`
	AggregateID      string                   `json:"aggregate_id"`
	LockboxRequestID string                   `json:"lockbox_request_id"`
	Status           string                   `json:"status"`
	Files            []CortexApplyPreviewFile `json:"files"`
	Error            string                   `json:"error"`
}

const (
	cortexApplyPreviewStatusReady   = "ready"
	cortexApplyPreviewStatusBlocked = "blocked"
	cortexApplyPreviewStatusFailed  = "failed"
)

// cloneCortexApplyPreview deep-copies the files slice so callers can never
// mutate the store's internal state through a returned value.
func cloneCortexApplyPreview(in CortexApplyPreview) CortexApplyPreview {
	out := in
	if in.Files != nil {
		out.Files = make([]CortexApplyPreviewFile, len(in.Files))
		copy(out.Files, in.Files)
	}
	return out
}

// cortexApplyPreviewStore is the in-memory record of Cortex apply previews.
// Every accessor returns a deep copy, never a pointer into the stored slice.
type cortexApplyPreviewStore struct {
	mu       sync.Mutex
	nextID   int64
	previews []CortexApplyPreview
}

func newCortexApplyPreviewStore() *cortexApplyPreviewStore {
	return &cortexApplyPreviewStore{}
}

// Append assigns an id and timestamp to in, stores it, and returns a copy.
func (s *cortexApplyPreviewStore) Append(in CortexApplyPreview) CortexApplyPreview {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	in.ID = strconv.FormatInt(s.nextID, 10)
	in.TS = time.Now().UTC()
	s.previews = append(s.previews, in)
	return cloneCortexApplyPreview(in)
}

// Current returns a deep copy of the most recent preview, or nil if none.
func (s *cortexApplyPreviewStore) Current() *CortexApplyPreview {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.previews) == 0 {
		return nil
	}
	c := cloneCortexApplyPreview(s.previews[len(s.previews)-1])
	return &c
}

// Find returns a deep copy of the preview with the given id, or nil.
func (s *cortexApplyPreviewStore) Find(id string) *CortexApplyPreview {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.previews {
		if s.previews[i].ID == id {
			c := cloneCortexApplyPreview(s.previews[i])
			return &c
		}
	}
	return nil
}

// List returns deep copies of all previews in insertion order.
func (s *cortexApplyPreviewStore) List() []CortexApplyPreview {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CortexApplyPreview, len(s.previews))
	for i := range s.previews {
		out[i] = cloneCortexApplyPreview(s.previews[i])
	}
	return out
}

// ListByAggregate returns deep copies of previews for the given aggregate id.
func (s *cortexApplyPreviewStore) ListByAggregate(aggregateID string) []CortexApplyPreview {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []CortexApplyPreview{}
	for i := range s.previews {
		if s.previews[i].AggregateID == aggregateID {
			out = append(out, cloneCortexApplyPreview(s.previews[i]))
		}
	}
	return out
}

// cortexDiffLines splits content into lines, dropping the trailing empty element
// produced by a final newline so a "no diff" change set is empty.
func cortexDiffLines(s string) []string {
	if s == "" {
		return []string{}
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// cortexUnifiedDiff renders a simple whole-file, line-based unified-style diff:
// every old line is removed and every new line is added. It is intentionally not
// a minimal (Myers) diff.
func cortexUnifiedDiff(path, oldContent, newContent string) string {
	var b strings.Builder
	b.WriteString("--- a/" + path + "\n")
	b.WriteString("+++ b/" + path + "\n")
	for _, line := range cortexDiffLines(oldContent) {
		b.WriteString("-" + line + "\n")
	}
	for _, line := range cortexDiffLines(newContent) {
		b.WriteString("+" + line + "\n")
	}
	return b.String()
}

// previewOneFile computes the non-writing planned effect for one proposed file.
func previewOneFile(projectPath string, f BuilderProposedFile) CortexApplyPreviewFile {
	pf := CortexApplyPreviewFile{Path: f.Path, Action: f.Action}
	target, err := resolveProjectPath(projectPath, f.Path)
	if err != nil {
		pf.Status = cortexApplyPreviewStatusBlocked
		pf.Error = truncateString(err.Error(), 1000)
		return pf
	}

	info, statErr := os.Lstat(target)
	pf.Exists = statErr == nil
	isRegular := pf.Exists && info.Mode().IsRegular()

	switch f.Action {
	case proposalActionCreate:
		if !pf.Exists {
			pf.Status = cortexApplyPreviewStatusReady
			pf.Diff = cortexUnifiedDiff(f.Path, "", f.Content)
			return pf
		}
		if !isRegular {
			pf.Status = cortexApplyPreviewStatusBlocked
			pf.Error = "create target exists and is not a regular file"
			return pf
		}
		existing, readErr := os.ReadFile(target)
		if readErr != nil {
			pf.Status = cortexApplyPreviewStatusBlocked
			pf.Error = truncateString("create target exists and could not be read: "+readErr.Error(), 1000)
			return pf
		}
		if string(existing) == f.Content {
			pf.Status = cortexApplyPreviewStatusReady
			pf.Diff = "no-op: target already exists with identical content"
			return pf
		}
		pf.Status = cortexApplyPreviewStatusBlocked
		pf.Error = "create target already exists with different content"
		pf.Diff = cortexUnifiedDiff(f.Path, string(existing), f.Content)
		return pf
	case proposalActionModify:
		if !pf.Exists {
			pf.Status = cortexApplyPreviewStatusBlocked
			pf.Error = "modify target does not exist"
			return pf
		}
		if !isRegular {
			pf.Status = cortexApplyPreviewStatusBlocked
			pf.Error = "modify target is not a regular file"
			return pf
		}
		existing, readErr := os.ReadFile(target)
		if readErr != nil {
			pf.Status = cortexApplyPreviewStatusBlocked
			pf.Error = truncateString("modify target could not be read: "+readErr.Error(), 1000)
			return pf
		}
		pf.Status = cortexApplyPreviewStatusReady
		pf.Diff = cortexUnifiedDiff(f.Path, string(existing), f.Content)
		return pf
	case proposalActionDelete:
		if !pf.Exists {
			pf.Status = cortexApplyPreviewStatusBlocked
			pf.Error = "delete target does not exist"
			return pf
		}
		if !isRegular {
			pf.Status = cortexApplyPreviewStatusBlocked
			pf.Error = "delete target is not a regular file"
			return pf
		}
		existing, readErr := os.ReadFile(target)
		if readErr != nil {
			pf.Status = cortexApplyPreviewStatusBlocked
			pf.Error = truncateString("delete target could not be read: "+readErr.Error(), 1000)
			return pf
		}
		pf.Status = cortexApplyPreviewStatusReady
		pf.Diff = cortexUnifiedDiff(f.Path, string(existing), "")
		return pf
	default:
		pf.Status = cortexApplyPreviewStatusBlocked
		pf.Error = truncateString("unsupported action "+f.Action, 1000)
		return pf
	}
}

// previewAggregateFiles evaluates every file (it does not stop on the first
// blocked file) and returns the per-file planned effects. It never writes.
func previewAggregateFiles(projectPath string, files []BuilderProposedFile) []CortexApplyPreviewFile {
	out := make([]CortexApplyPreviewFile, 0, len(files))
	for _, f := range files {
		out = append(out, previewOneFile(projectPath, f))
	}
	return out
}

// handleCortexApplyPreview serves GET (current preview) and POST (build a
// non-writing preview of applying an approved aggregate).
func (wb *Server) handleCortexApplyPreview(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		preview := wb.cortexApplyPreviews.Current()
		if preview == nil {
			http.Error(w, "no cortex apply preview", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, *preview)
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

		request, agg, project, gateStatus, gateMsg := wb.cortexApplyGate(in.LockboxRequestID)
		if gateStatus != 0 {
			http.Error(w, gateMsg, gateStatus)
			return
		}

		previewFiles := previewAggregateFiles(project.Path, agg.Files)
		status := cortexApplyPreviewStatusReady
		for _, pf := range previewFiles {
			switch pf.Status {
			case cortexApplyPreviewStatusFailed:
				status = cortexApplyPreviewStatusFailed
			case cortexApplyPreviewStatusBlocked:
				if status != cortexApplyPreviewStatusFailed {
					status = cortexApplyPreviewStatusBlocked
				}
			}
		}

		preview := CortexApplyPreview{
			AggregateID:      agg.ID,
			LockboxRequestID: request.ID,
			Status:           status,
			Files:            previewFiles,
		}
		if status == cortexApplyPreviewStatusFailed {
			preview.Error = "apply preview failed due to an internal error"
		}
		stored := wb.cortexApplyPreviews.Append(preview)
		wb.appendCortexEvent("cortex.apply.preview.created", "Created Cortex apply preview", stored)

		switch status {
		case cortexApplyPreviewStatusReady:
			writeJSON(w, http.StatusOK, stored)
		case cortexApplyPreviewStatusBlocked:
			writeJSON(w, http.StatusConflict, stored)
		default:
			writeJSON(w, http.StatusBadGateway, stored)
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (wb *Server) handleCortexApplyPreviews(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	aggregateID := r.URL.Query().Get("aggregate_id")
	var out []CortexApplyPreview
	if aggregateID != "" {
		out = wb.cortexApplyPreviews.ListByAggregate(aggregateID)
	} else {
		out = wb.cortexApplyPreviews.List()
	}
	writeJSON(w, http.StatusOK, out)
}
