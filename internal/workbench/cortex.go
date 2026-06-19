package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CortexLane is a single local sub-agent lane within a CortexTask. A lane will
// later run in an isolated git worktree; this slice keeps the worktree fields
// empty and executes lanes deterministically.
type CortexLane struct {
	ID             string `json:"id"`
	Role           string `json:"role"`
	Index          int    `json:"index"`
	Task           string `json:"task"`
	Status         string `json:"status"`
	WorkspacePath  string `json:"workspace_path"`
	BaseBranch     string `json:"base_branch"`
	WorktreeBranch string `json:"worktree_branch"`
	PromptPreview  string `json:"prompt_preview"`
	Result         string `json:"result"`
	Error          string `json:"error"`
}

// CortexTask is a local Cortex orchestration unit: a goal decomposed into one
// or more role lanes across the architect, builder, reviewer, and validator
// pools. It is review/orchestration material only — no files are written.
type CortexTask struct {
	ID     string       `json:"id"`
	TS     time.Time    `json:"ts"`
	Goal   string       `json:"goal"`
	Status string       `json:"status"`
	Lanes  []CortexLane `json:"lanes"`
}

// CortexRoleCounts is the number of lanes requested for each role.
type CortexRoleCounts struct {
	Architect int `json:"architect"`
	Builder   int `json:"builder"`
	Reviewer  int `json:"reviewer"`
	Validator int `json:"validator"`
}

const (
	cortexRoleArchitect = "architect"
	cortexRoleBuilder   = "builder"
	cortexRoleReviewer  = "reviewer"
	cortexRoleValidator = "validator"

	cortexModeSingle = "single"
	cortexModeMulti  = "multi"

	cortexTaskStatusPlanned   = "planned"
	cortexTaskStatusRunning   = "running"
	cortexTaskStatusCompleted = "completed"
	cortexTaskStatusFailed    = "failed"

	cortexLaneStatusPending   = "pending"
	cortexLaneStatusRunning   = "running"
	cortexLaneStatusCompleted = "completed"
	cortexLaneStatusFailed    = "failed"

	// cortexMaxLanes caps the total lanes a single task may request.
	cortexMaxLanes = 12
)

// cloneCortexTask deep-copies a task's lane slice so callers can never mutate
// the store's internal state through a returned value.
func cloneCortexTask(in CortexTask) CortexTask {
	out := in
	if in.Lanes != nil {
		out.Lanes = make([]CortexLane, len(in.Lanes))
		copy(out.Lanes, in.Lanes)
	}
	return out
}

// cortexStore is the in-memory record of Cortex tasks. Every accessor returns a
// deep copy, never a pointer into the stored slice.
type cortexStore struct {
	mu     sync.Mutex
	nextID int64
	tasks  []CortexTask
}

func newCortexStore() *cortexStore {
	return &cortexStore{}
}

// Create assigns an id and timestamp to in, stores it, and returns a copy of
// the stored value.
func (s *cortexStore) Create(in CortexTask) CortexTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	in.ID = strconv.FormatInt(s.nextID, 10)
	in.TS = time.Now().UTC()
	s.tasks = append(s.tasks, in)
	return cloneCortexTask(in)
}

// Current returns a deep copy of the most recent task, or nil if there are
// none.
func (s *cortexStore) Current() *CortexTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tasks) == 0 {
		return nil
	}
	c := cloneCortexTask(s.tasks[len(s.tasks)-1])
	return &c
}

// Find returns a deep copy of the task with the given id, or nil if there is no
// such task.
func (s *cortexStore) Find(id string) *CortexTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tasks {
		if s.tasks[i].ID == id {
			c := cloneCortexTask(s.tasks[i])
			return &c
		}
	}
	return nil
}

// List returns deep copies of all tasks in insertion order (oldest first,
// newest last).
func (s *cortexStore) List() []CortexTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CortexTask, len(s.tasks))
	for i := range s.tasks {
		out[i] = cloneCortexTask(s.tasks[i])
	}
	return out
}

// SetTaskStatus updates a task's status and returns a deep copy, or nil if the
// task is not found.
func (s *cortexStore) SetTaskStatus(id, status string) *CortexTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tasks {
		if s.tasks[i].ID == id {
			s.tasks[i].Status = status
			c := cloneCortexTask(s.tasks[i])
			return &c
		}
	}
	return nil
}

// UpdateLane updates the status/result/error of a lane within a task and
// returns a deep copy of the task, or nil if the task or lane is not found.
func (s *cortexStore) UpdateLane(taskID, laneID, status, result, errMsg string) *CortexTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tasks {
		if s.tasks[i].ID != taskID {
			continue
		}
		for j := range s.tasks[i].Lanes {
			if s.tasks[i].Lanes[j].ID == laneID {
				s.tasks[i].Lanes[j].Status = status
				s.tasks[i].Lanes[j].Result = result
				s.tasks[i].Lanes[j].Error = errMsg
				c := cloneCortexTask(s.tasks[i])
				return &c
			}
		}
	}
	return nil
}

// SetLaneWorktree fills the workspace/branch fields of a lane within a task and
// returns a deep copy of the task, or nil if the task or lane is not found.
func (s *cortexStore) SetLaneWorktree(taskID, laneID, workspacePath, baseBranch, worktreeBranch string) *CortexTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tasks {
		if s.tasks[i].ID != taskID {
			continue
		}
		for j := range s.tasks[i].Lanes {
			if s.tasks[i].Lanes[j].ID == laneID {
				s.tasks[i].Lanes[j].WorkspacePath = workspacePath
				s.tasks[i].Lanes[j].BaseBranch = baseBranch
				s.tasks[i].Lanes[j].WorktreeBranch = worktreeBranch
				c := cloneCortexTask(s.tasks[i])
				return &c
			}
		}
	}
	return nil
}

// cortexRoleLabel returns the human-facing label for a role.
func cortexRoleLabel(role string) string {
	switch role {
	case cortexRoleArchitect:
		return "Architect"
	case cortexRoleBuilder:
		return "Builder"
	case cortexRoleReviewer:
		return "Reviewer"
	case cortexRoleValidator:
		return "Validator"
	default:
		return role
	}
}

// cortexLaneTask renders a lane's task description, e.g. "Builder lane 2: ship".
func cortexLaneTask(role string, index int, goal string) string {
	return fmt.Sprintf("%s lane %d: %s", cortexRoleLabel(role), index, goal)
}

// cortexLaneResult renders the deterministic result recorded for a completed
// lane in this stubbed slice.
func cortexLaneResult(role string, index int) string {
	switch role {
	case cortexRoleArchitect:
		return fmt.Sprintf("Architect lane %d planned decomposition.", index)
	case cortexRoleBuilder:
		return fmt.Sprintf("Builder lane %d ready for isolated worktree proposal generation.", index)
	case cortexRoleReviewer:
		return fmt.Sprintf("Reviewer lane %d ready to review Builder output.", index)
	case cortexRoleValidator:
		return fmt.Sprintf("Validator lane %d ready to recommend validation checks.", index)
	default:
		return ""
	}
}

// buildCortexLanes creates lanes in deterministic role order (architect,
// builder, reviewer, validator) with stable sequential ids and 1-based,
// per-role indexes. Worktree/workspace fields are intentionally left empty.
func buildCortexLanes(counts CortexRoleCounts, goal string) []CortexLane {
	order := []struct {
		role  string
		count int
	}{
		{cortexRoleArchitect, counts.Architect},
		{cortexRoleBuilder, counts.Builder},
		{cortexRoleReviewer, counts.Reviewer},
		{cortexRoleValidator, counts.Validator},
	}
	total := counts.Architect + counts.Builder + counts.Reviewer + counts.Validator
	lanes := make([]CortexLane, 0, total)
	id := 0
	for _, o := range order {
		for i := 1; i <= o.count; i++ {
			id++
			lanes = append(lanes, CortexLane{
				ID:     strconv.Itoa(id),
				Role:   o.role,
				Index:  i,
				Task:   cortexLaneTask(o.role, i, goal),
				Status: cortexLaneStatusPending,
			})
		}
	}
	return lanes
}

// cortexLaneByID returns the lane with the given id from task, or a zero lane.
func cortexLaneByID(task *CortexTask, laneID string) CortexLane {
	if task != nil {
		for _, l := range task.Lanes {
			if l.ID == laneID {
				return l
			}
		}
	}
	return CortexLane{}
}

// appendCortexEvent marshals payload as the event data, falling back to nil
// data when marshaling fails.
func (wb *Server) appendCortexEvent(evType, message string, payload any) {
	if data, err := json.Marshal(payload); err == nil {
		wb.store.Append(evType, message, data)
	} else {
		wb.store.Append(evType, message, nil)
	}
}

// handleCortexTask serves GET (current task) and POST (create a task with one
// or many lanes per role).
func (wb *Server) handleCortexTask(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		task := wb.cortex.Current()
		if task == nil {
			http.Error(w, "no cortex task", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, *task)
	case http.MethodPost:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var in struct {
			Goal  string            `json:"goal"`
			Mode  string            `json:"mode"`
			Roles *CortexRoleCounts `json:"roles"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if in.Goal == "" {
			http.Error(w, "goal is required", http.StatusBadRequest)
			return
		}
		mode := in.Mode
		if mode == "" {
			mode = cortexModeSingle
		}
		if mode != cortexModeSingle && mode != cortexModeMulti {
			http.Error(w, "mode must be single or multi", http.StatusBadRequest)
			return
		}

		var counts CortexRoleCounts
		switch {
		case in.Roles != nil:
			counts = *in.Roles
		case mode == cortexModeMulti:
			counts = CortexRoleCounts{Architect: 1, Builder: 2, Reviewer: 1, Validator: 1}
		default:
			counts = CortexRoleCounts{Architect: 0, Builder: 1, Reviewer: 0, Validator: 0}
		}

		if counts.Architect < 0 || counts.Builder < 0 || counts.Reviewer < 0 || counts.Validator < 0 {
			http.Error(w, "role counts must not be negative", http.StatusBadRequest)
			return
		}
		total := counts.Architect + counts.Builder + counts.Reviewer + counts.Validator
		if total == 0 {
			http.Error(w, "at least one lane is required", http.StatusBadRequest)
			return
		}
		if total > cortexMaxLanes {
			http.Error(w, "total lane count exceeds the maximum of 12", http.StatusBadRequest)
			return
		}

		task := wb.cortex.Create(CortexTask{
			Goal:   in.Goal,
			Status: cortexTaskStatusPlanned,
			Lanes:  buildCortexLanes(counts, in.Goal),
		})
		wb.appendCortexEvent("cortex.task.created", "Created Cortex task", task)
		writeJSON(w, http.StatusCreated, task)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (wb *Server) handleCortexTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, wb.cortex.List())
}

func (wb *Server) handleCortexLanes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	task := wb.cortex.Current()
	if task == nil {
		http.Error(w, "no cortex task", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, task.Lanes)
}

// handleCortexRun deterministically "runs" a task: it marks the task and each
// lane running, then completed, recording a stubbed result per lane. No
// provider call, file write, or git worktree is involved in this slice.
func (wb *Server) handleCortexRun(w http.ResponseWriter, r *http.Request) {
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
		ID string `json:"id"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	var task *CortexTask
	if in.ID == "" {
		task = wb.cortex.Current()
	} else {
		task = wb.cortex.Find(in.ID)
	}
	if task == nil {
		http.Error(w, "cortex task not found", http.StatusNotFound)
		return
	}
	id := task.ID

	running := wb.cortex.SetTaskStatus(id, cortexTaskStatusRunning)
	wb.appendCortexEvent("cortex.task.started", "Started Cortex task", running)

	for _, lane := range task.Lanes {
		updated := wb.cortex.UpdateLane(id, lane.ID, cortexLaneStatusRunning, "", "")
		wb.appendCortexEvent("cortex.lane.started", "Started Cortex lane", cortexLaneByID(updated, lane.ID))
	}

	for _, lane := range task.Lanes {
		result := cortexLaneResult(lane.Role, lane.Index)
		updated := wb.cortex.UpdateLane(id, lane.ID, cortexLaneStatusCompleted, result, "")
		wb.appendCortexEvent("cortex.lane.completed", "Completed Cortex lane", cortexLaneByID(updated, lane.ID))
	}

	completed := wb.cortex.SetTaskStatus(id, cortexTaskStatusCompleted)
	wb.appendCortexEvent("cortex.task.completed", "Completed Cortex task", completed)

	writeJSON(w, http.StatusOK, *completed)
}

// cortexWorktreeTimeout bounds each "git worktree add" invocation.
const cortexWorktreeTimeout = 30 * time.Second

// cortexBuilderLanes returns the task's Builder lanes (a non-nil slice).
func cortexBuilderLanes(task *CortexTask) []CortexLane {
	out := []CortexLane{}
	if task != nil {
		for _, l := range task.Lanes {
			if l.Role == cortexRoleBuilder {
				out = append(out, l)
			}
		}
	}
	return out
}

// gitWorktreeAdd runs a bounded "git -C <repo> worktree add -B <branch> <path>
// <base>" and returns its combined output (capped) and any error.
func gitWorktreeAdd(repoPath, branch, worktreePath, baseBranch string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cortexWorktreeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "add", "-B", branch, worktreePath, baseBranch)
	out, err := cmd.CombinedOutput()
	return truncateString(strings.TrimSpace(string(out)), 1000), err
}

// handleCortexWorktrees serves GET (Builder lanes with their worktree fields)
// and POST (allocate git worktrees for the task's Builder lanes only). It never
// writes project files or runs provider/lane execution.
func (wb *Server) handleCortexWorktrees(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		task := wb.cortex.Current()
		if task == nil {
			http.Error(w, "no cortex task", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, cortexBuilderLanes(task))
	case http.MethodPost:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var in struct {
			ID string `json:"id"`
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &in); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
		}

		var task *CortexTask
		if in.ID == "" {
			task = wb.cortex.Current()
		} else {
			task = wb.cortex.Find(in.ID)
		}
		if task == nil {
			http.Error(w, "cortex task not found", http.StatusNotFound)
			return
		}
		taskID := task.ID

		project := wb.project.Get()
		if project == nil {
			http.Error(w, "project must be open", http.StatusConflict)
			return
		}
		if !project.Git {
			http.Error(w, "project is not a git repo", http.StatusConflict)
			return
		}

		baseBranch := project.CurrentBranch
		if baseBranch == "" {
			detected, derr := detectBranch(project.Path)
			if derr != nil {
				writeJSON(w, http.StatusBadGateway, map[string]any{
					"error": truncateString("detect base branch failed: "+derr.Error(), 1000),
				})
				return
			}
			baseBranch = detected
		}

		// Worktree root lives outside the project, alongside it.
		root := filepath.Join(filepath.Dir(project.Path), ".hirdforge-worktrees", filepath.Base(project.Path), "task-"+taskID)

		for _, lane := range task.Lanes {
			if lane.Role != cortexRoleBuilder {
				continue // only Builder lanes receive worktrees
			}
			if lane.WorkspacePath != "" || lane.WorktreeBranch != "" {
				continue // idempotent: already allocated
			}
			if err := os.MkdirAll(root, 0o755); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]any{
					"lane":  lane.ID,
					"error": truncateString("create worktree root failed: "+err.Error(), 1000),
				})
				return
			}
			worktreePath := filepath.Join(root, "builder-"+strconv.Itoa(lane.Index))
			branch := "hirdforge/task-" + taskID + "/builder-" + strconv.Itoa(lane.Index)
			if out, gerr := gitWorktreeAdd(project.Path, branch, worktreePath, baseBranch); gerr != nil {
				msg := gerr.Error()
				if out != "" {
					msg += ": " + out
				}
				writeJSON(w, http.StatusBadGateway, map[string]any{
					"lane":  lane.ID,
					"error": truncateString("git worktree add failed: "+msg, 1000),
				})
				return
			}
			wb.cortex.SetLaneWorktree(taskID, lane.ID, worktreePath, baseBranch, branch)
		}

		updated := wb.cortex.Find(taskID)
		if updated == nil {
			http.Error(w, "cortex task not found", http.StatusInternalServerError)
			return
		}
		wb.appendCortexEvent("cortex.worktrees.allocated", "Allocated Cortex Builder worktrees", updated)
		writeJSON(w, http.StatusOK, *updated)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// CortexLaneProposal is a provider-produced, in-memory-only change proposal for
// a single Cortex Builder lane. Like a global Builder proposal it is review
// material: nothing here is ever written to disk, and it never carries the
// provider API key. Files reuse []BuilderProposedFile.
type CortexLaneProposal struct {
	ID            string                `json:"id"`
	TS            time.Time             `json:"ts"`
	TaskID        string                `json:"task_id"`
	LaneID        string                `json:"lane_id"`
	LaneIndex     int                   `json:"lane_index"`
	WorkspacePath string                `json:"workspace_path"`
	Status        string                `json:"status"`
	Summary       string                `json:"summary"`
	Files         []BuilderProposedFile `json:"files"`
	Error         string                `json:"error"`
}

// cloneCortexLaneProposal deep-copies the files slice so callers can never
// mutate the store's internal state through a returned value.
func cloneCortexLaneProposal(in CortexLaneProposal) CortexLaneProposal {
	out := in
	if in.Files != nil {
		out.Files = make([]BuilderProposedFile, len(in.Files))
		copy(out.Files, in.Files)
	}
	return out
}

// cortexLaneProposalStore is the in-memory record of Cortex Builder lane
// proposals. Every accessor returns a deep copy, never a pointer into the
// stored slice.
type cortexLaneProposalStore struct {
	mu        sync.Mutex
	nextID    int64
	proposals []CortexLaneProposal
}

func newCortexLaneProposalStore() *cortexLaneProposalStore {
	return &cortexLaneProposalStore{}
}

// Append assigns an id and timestamp to in, stores it, and returns a copy.
func (s *cortexLaneProposalStore) Append(in CortexLaneProposal) CortexLaneProposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	in.ID = strconv.FormatInt(s.nextID, 10)
	in.TS = time.Now().UTC()
	s.proposals = append(s.proposals, in)
	return cloneCortexLaneProposal(in)
}

// Current returns a deep copy of the most recent proposal, or nil if none.
func (s *cortexLaneProposalStore) Current() *CortexLaneProposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.proposals) == 0 {
		return nil
	}
	c := cloneCortexLaneProposal(s.proposals[len(s.proposals)-1])
	return &c
}

// Find returns a deep copy of the proposal with the given id, or nil.
func (s *cortexLaneProposalStore) Find(id string) *CortexLaneProposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.proposals {
		if s.proposals[i].ID == id {
			c := cloneCortexLaneProposal(s.proposals[i])
			return &c
		}
	}
	return nil
}

// List returns deep copies of all proposals in insertion order.
func (s *cortexLaneProposalStore) List() []CortexLaneProposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CortexLaneProposal, len(s.proposals))
	for i := range s.proposals {
		out[i] = cloneCortexLaneProposal(s.proposals[i])
	}
	return out
}

// ListByTask returns deep copies of the proposals for the given task id.
func (s *cortexLaneProposalStore) ListByTask(taskID string) []CortexLaneProposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []CortexLaneProposal{}
	for i := range s.proposals {
		if s.proposals[i].TaskID == taskID {
			out = append(out, cloneCortexLaneProposal(s.proposals[i]))
		}
	}
	return out
}

// ListByLane returns deep copies of the proposals for the given lane id.
func (s *cortexLaneProposalStore) ListByLane(laneID string) []CortexLaneProposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []CortexLaneProposal{}
	for i := range s.proposals {
		if s.proposals[i].LaneID == laneID {
			out = append(out, cloneCortexLaneProposal(s.proposals[i]))
		}
	}
	return out
}

// cortexLaneProposalUserPrompt renders the provider user message for a Builder
// lane proposal: it scopes the request to the task goal, the lane, the lane's
// worktree/workspace, and the read-only inspection context.
func cortexLaneProposalUserPrompt(task CortexTask, lane CortexLane, contextPath string, contextProject ProjectState, insp ProjectInspection, warning string) string {
	var b strings.Builder
	b.WriteString("Cortex Builder lane change proposal.\n")
	b.WriteString("Task goal: " + task.Goal + "\n")
	b.WriteString("Lane id: " + lane.ID + "\n")
	b.WriteString("Lane index: " + strconv.Itoa(lane.Index) + "\n")
	b.WriteString("Lane task: " + lane.Task + "\n")
	b.WriteString("Workspace path: " + contextPath + "\n")
	b.WriteString("Base branch: " + lane.BaseBranch + "\n")
	b.WriteString("Worktree branch: " + lane.WorktreeBranch + "\n")
	if warning != "" {
		b.WriteString("Warning: " + warning + "\n")
	}
	b.WriteString("\n")
	b.WriteString(builderProjectContext(task.Goal, contextProject, insp))
	b.WriteString("Propose the file changes for this Builder lane only. ")
	b.WriteString("Return JSON only, with no surrounding prose, in exactly this shape:\n")
	b.WriteString(`{"summary":"...","files":[{"path":"relative/path","action":"create|modify|delete","content":"...","rationale":"..."}]}`)
	b.WriteString("\n")
	b.WriteString("Paths must be relative to the workspace root. Use action \"delete\" with empty content to remove a file. The files array may be empty if no changes are needed.")
	return b.String()
}

// handleCortexLanePropose asks the provider for a change proposal scoped to a
// single Builder lane and stores it per lane. It inspects the lane's workspace
// (or the project as a fallback) read-only and never writes proposed files.
func (wb *Server) handleCortexLanePropose(w http.ResponseWriter, r *http.Request) {
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
		TaskID string `json:"task_id"`
		LaneID string `json:"lane_id"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	var task *CortexTask
	if in.TaskID == "" {
		task = wb.cortex.Current()
	} else {
		task = wb.cortex.Find(in.TaskID)
	}
	if task == nil {
		http.Error(w, "cortex task not found", http.StatusNotFound)
		return
	}
	if in.LaneID == "" {
		http.Error(w, "lane_id is required", http.StatusBadRequest)
		return
	}

	var lane *CortexLane
	for i := range task.Lanes {
		if task.Lanes[i].ID == in.LaneID {
			l := task.Lanes[i]
			lane = &l
			break
		}
	}
	if lane == nil {
		http.Error(w, "lane not found", http.StatusNotFound)
		return
	}
	if lane.Role != cortexRoleBuilder {
		http.Error(w, "lane is not a builder lane", http.StatusConflict)
		return
	}

	cfg := wb.provider.Config()
	if cfg == nil {
		http.Error(w, "provider must be configured", http.StatusConflict)
		return
	}
	project := wb.project.Get()
	if project == nil {
		http.Error(w, "project must be open", http.StatusConflict)
		return
	}

	// Choose the inspection context path: the lane's worktree when present,
	// otherwise the project path with a warning surfaced in the prompt.
	contextPath := lane.WorkspacePath
	warning := ""
	if contextPath == "" {
		contextPath = project.Path
		warning = "lane workspace_path is empty; using project path " + project.Path + " as proposal context"
	}

	contextProject := ProjectState{
		Path: contextPath,
		Name: filepath.Base(contextPath),
		Git:  isGitRepo(contextPath),
	}
	if contextProject.Git {
		if branch, derr := detectBranch(contextPath); derr == nil {
			contextProject.CurrentBranch = branch
		}
	}
	insp := inspectProject(contextProject)

	userPrompt := cortexLaneProposalUserPrompt(*task, *lane, contextPath, contextProject, insp, warning)
	summary, files, proposeErr := requestProviderProposal(cfg, builderProposalSystemPrompt, userPrompt)

	base := CortexLaneProposal{
		TaskID:        task.ID,
		LaneID:        lane.ID,
		LaneIndex:     lane.Index,
		WorkspacePath: contextPath,
	}
	if proposeErr != nil {
		base.Status = builderProposalStatusFailed
		base.Files = []BuilderProposedFile{}
		base.Error = truncateString(proposeErr.Error(), 1000)
		stored := wb.cortexLaneProposals.Append(base)
		wb.appendCortexEvent("cortex.lane.proposal.failed", "Failed Cortex Builder lane proposal", stored)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"proposal": stored,
			"error":    truncateString(proposeErr.Error(), 1000),
		})
		return
	}

	base.Status = builderProposalStatusProposed
	base.Summary = summary
	base.Files = files
	stored := wb.cortexLaneProposals.Append(base)
	wb.appendCortexEvent("cortex.lane.proposal.created", "Created Cortex Builder lane proposal", stored)
	writeJSON(w, http.StatusCreated, stored)
}

func (wb *Server) handleCortexLaneProposal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	proposal := wb.cortexLaneProposals.Current()
	if proposal == nil {
		http.Error(w, "no lane proposal", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, *proposal)
}

func (wb *Server) handleCortexLaneProposals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := r.URL.Query().Get("task_id")
	laneID := r.URL.Query().Get("lane_id")

	var out []CortexLaneProposal
	switch {
	case taskID != "" && laneID != "":
		out = []CortexLaneProposal{}
		for _, p := range wb.cortexLaneProposals.List() {
			if p.TaskID == taskID && p.LaneID == laneID {
				out = append(out, p)
			}
		}
	case taskID != "":
		out = wb.cortexLaneProposals.ListByTask(taskID)
	case laneID != "":
		out = wb.cortexLaneProposals.ListByLane(laneID)
	default:
		out = wb.cortexLaneProposals.List()
	}
	writeJSON(w, http.StatusOK, out)
}

// CortexAggregateConflict describes a single file path that more than one
// Builder lane proposal changes in incompatible ways.
type CortexAggregateConflict struct {
	Path        string   `json:"path"`
	ProposalIDs []string `json:"proposal_ids"`
	Reason      string   `json:"reason"`
}

// CortexAggregateProposal merges a task's proposed Builder lane proposals into a
// single reviewable candidate. It is review material only: nothing here is ever
// written to disk, and it never carries the provider API key. Files reuse
// []BuilderProposedFile.
type CortexAggregateProposal struct {
	ID                string                    `json:"id"`
	TS                time.Time                 `json:"ts"`
	TaskID            string                    `json:"task_id"`
	Status            string                    `json:"status"`
	Summary           string                    `json:"summary"`
	SourceProposalIDs []string                  `json:"source_proposal_ids"`
	Files             []BuilderProposedFile     `json:"files"`
	Conflicts         []CortexAggregateConflict `json:"conflicts"`
	Notes             string                    `json:"notes"`
}

const (
	cortexAggregateStatusAggregated = "aggregated"
	cortexAggregateStatusConflicted = "conflicted"
	cortexAggregateStatusFailed     = "failed"
)

// cloneCortexAggregateProposal deep-copies the slices so callers can never
// mutate the store's internal state through a returned value.
func cloneCortexAggregateProposal(in CortexAggregateProposal) CortexAggregateProposal {
	out := in
	if in.SourceProposalIDs != nil {
		out.SourceProposalIDs = make([]string, len(in.SourceProposalIDs))
		copy(out.SourceProposalIDs, in.SourceProposalIDs)
	}
	if in.Files != nil {
		out.Files = make([]BuilderProposedFile, len(in.Files))
		copy(out.Files, in.Files)
	}
	if in.Conflicts != nil {
		out.Conflicts = make([]CortexAggregateConflict, len(in.Conflicts))
		for i := range in.Conflicts {
			c := in.Conflicts[i]
			if in.Conflicts[i].ProposalIDs != nil {
				c.ProposalIDs = make([]string, len(in.Conflicts[i].ProposalIDs))
				copy(c.ProposalIDs, in.Conflicts[i].ProposalIDs)
			}
			out.Conflicts[i] = c
		}
	}
	return out
}

// cortexAggregateStore is the in-memory record of Cortex aggregate proposals.
// Every accessor returns a deep copy, never a pointer into the stored slice.
type cortexAggregateStore struct {
	mu         sync.Mutex
	nextID     int64
	aggregates []CortexAggregateProposal
}

func newCortexAggregateStore() *cortexAggregateStore {
	return &cortexAggregateStore{}
}

// Append assigns an id and timestamp to in, stores it, and returns a copy.
func (s *cortexAggregateStore) Append(in CortexAggregateProposal) CortexAggregateProposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	in.ID = strconv.FormatInt(s.nextID, 10)
	in.TS = time.Now().UTC()
	s.aggregates = append(s.aggregates, in)
	return cloneCortexAggregateProposal(in)
}

// Current returns a deep copy of the most recent aggregate, or nil if none.
func (s *cortexAggregateStore) Current() *CortexAggregateProposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.aggregates) == 0 {
		return nil
	}
	c := cloneCortexAggregateProposal(s.aggregates[len(s.aggregates)-1])
	return &c
}

// Find returns a deep copy of the aggregate with the given id, or nil.
func (s *cortexAggregateStore) Find(id string) *CortexAggregateProposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.aggregates {
		if s.aggregates[i].ID == id {
			c := cloneCortexAggregateProposal(s.aggregates[i])
			return &c
		}
	}
	return nil
}

// List returns deep copies of all aggregates in insertion order.
func (s *cortexAggregateStore) List() []CortexAggregateProposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CortexAggregateProposal, len(s.aggregates))
	for i := range s.aggregates {
		out[i] = cloneCortexAggregateProposal(s.aggregates[i])
	}
	return out
}

// ListByTask returns deep copies of the aggregates for the given task id.
func (s *cortexAggregateStore) ListByTask(taskID string) []CortexAggregateProposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []CortexAggregateProposal{}
	for i := range s.aggregates {
		if s.aggregates[i].TaskID == taskID {
			out = append(out, cloneCortexAggregateProposal(s.aggregates[i]))
		}
	}
	return out
}

// aggregateLaneProposals merges proposed file changes across lane proposals (in
// insertion order). A path that more than one proposal changes with a differing
// action or content is a conflict: it is excluded from files and recorded in
// conflicts. Identical same-path changes are deduplicated, not conflicted.
func aggregateLaneProposals(proposals []CortexLaneProposal) ([]BuilderProposedFile, []CortexAggregateConflict) {
	type entry struct {
		proposalID string
		file       BuilderProposedFile
	}
	byPath := map[string][]entry{}
	pathOrder := []string{}
	for _, p := range proposals {
		for _, f := range p.Files {
			if _, seen := byPath[f.Path]; !seen {
				pathOrder = append(pathOrder, f.Path)
			}
			byPath[f.Path] = append(byPath[f.Path], entry{proposalID: p.ID, file: f})
		}
	}

	files := []BuilderProposedFile{}
	conflicts := []CortexAggregateConflict{}
	for _, path := range pathOrder {
		entries := byPath[path]

		distinct := []string{}
		seen := map[string]bool{}
		for _, e := range entries {
			if !seen[e.proposalID] {
				seen[e.proposalID] = true
				distinct = append(distinct, e.proposalID)
			}
		}

		if len(distinct) > 1 {
			identical := true
			first := entries[0].file
			for _, e := range entries[1:] {
				if e.file.Action != first.Action || e.file.Content != first.Content {
					identical = false
					break
				}
			}
			if !identical {
				conflicts = append(conflicts, CortexAggregateConflict{
					Path:        path,
					ProposalIDs: distinct,
					Reason:      "path changed by multiple lane proposals with differing action or content",
				})
				continue
			}
		}
		files = append(files, entries[0].file)
	}
	return files, conflicts
}

// handleCortexAggregate serves GET (current aggregate) and POST (aggregate a
// task's proposed Builder lane proposals into one reviewable candidate).
func (wb *Server) handleCortexAggregate(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		agg := wb.cortexAggregates.Current()
		if agg == nil {
			http.Error(w, "no cortex aggregate", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, *agg)
	case http.MethodPost:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var in struct {
			TaskID string `json:"task_id"`
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &in); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
		}

		var task *CortexTask
		if in.TaskID == "" {
			task = wb.cortex.Current()
		} else {
			task = wb.cortex.Find(in.TaskID)
		}
		if task == nil {
			http.Error(w, "cortex task not found", http.StatusNotFound)
			return
		}

		proposed := []CortexLaneProposal{}
		for _, p := range wb.cortexLaneProposals.ListByTask(task.ID) {
			if p.Status == builderProposalStatusProposed {
				proposed = append(proposed, p)
			}
		}
		if len(proposed) == 0 {
			http.Error(w, "no proposed Builder lane proposals to aggregate", http.StatusConflict)
			return
		}

		sourceIDs := make([]string, 0, len(proposed))
		for _, p := range proposed {
			sourceIDs = append(sourceIDs, p.ID)
		}
		files, conflicts := aggregateLaneProposals(proposed)

		agg := CortexAggregateProposal{
			TaskID:            task.ID,
			SourceProposalIDs: sourceIDs,
			Files:             files,
			Conflicts:         conflicts,
		}
		if len(conflicts) > 0 {
			agg.Status = cortexAggregateStatusConflicted
			agg.Summary = fmt.Sprintf("Aggregated %d Builder lane proposal(s); %d path(s) conflict and require review. %d non-conflicting file(s) ready.", len(proposed), len(conflicts), len(files))
			agg.Notes = "Conflicting files were excluded; resolve conflicts before requesting Lockbox approval."
		} else {
			agg.Status = cortexAggregateStatusAggregated
			agg.Summary = fmt.Sprintf("Aggregated %d Builder lane proposal(s) into %d file(s).", len(proposed), len(files))
		}

		stored := wb.cortexAggregates.Append(agg)
		wb.appendCortexEvent("cortex.aggregate.created", "Created Cortex aggregate proposal", stored)
		writeJSON(w, http.StatusCreated, stored)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (wb *Server) handleCortexAggregates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := r.URL.Query().Get("task_id")
	var out []CortexAggregateProposal
	if taskID != "" {
		out = wb.cortexAggregates.ListByTask(taskID)
	} else {
		out = wb.cortexAggregates.List()
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCortexAggregateLockbox creates a Lockbox approval request from an
// aggregated (non-conflicted) Cortex aggregate proposal. It records the request
// for review only; no proposed files are ever applied.
func (wb *Server) handleCortexAggregateLockbox(w http.ResponseWriter, r *http.Request) {
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
		ID string `json:"id"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	var agg *CortexAggregateProposal
	if in.ID == "" {
		agg = wb.cortexAggregates.Current()
	} else {
		agg = wb.cortexAggregates.Find(in.ID)
	}
	if agg == nil {
		http.Error(w, "cortex aggregate not found", http.StatusNotFound)
		return
	}
	if agg.Status != cortexAggregateStatusAggregated {
		http.Error(w, "aggregate is not in aggregated status", http.StatusConflict)
		return
	}

	goal := ""
	if task := wb.cortex.Find(agg.TaskID); task != nil {
		goal = task.Goal
	}

	files := make([]BuilderProposedFile, len(agg.Files))
	copy(files, agg.Files)
	request := wb.lockbox.Append(LockboxApprovalRequest{
		ProposalID: "aggregate:" + agg.ID,
		Goal:       goal,
		Status:     lockboxStatusPending,
		Summary:    agg.Summary,
		Files:      files,
	})
	wb.appendCortexEvent("cortex.aggregate.lockbox_requested", "Created Lockbox request from Cortex aggregate", request)
	writeJSON(w, http.StatusCreated, request)
}

// CortexAggregateReview is a Reviewer lane's provider-backed verdict on a Cortex
// aggregate proposal. It is review material only: nothing here is ever written
// to disk, and it never carries the provider API key.
type CortexAggregateReview struct {
	ID              string    `json:"id"`
	TS              time.Time `json:"ts"`
	AggregateID     string    `json:"aggregate_id"`
	TaskID          string    `json:"task_id"`
	LaneID          string    `json:"lane_id"`
	LaneIndex       int       `json:"lane_index"`
	Status          string    `json:"status"`
	Verdict         string    `json:"verdict"`
	Summary         string    `json:"summary"`
	Risks           []string  `json:"risks"`
	Recommendations []string  `json:"recommendations"`
	Error           string    `json:"error"`
}

const (
	cortexReviewStatusReviewed = "reviewed"
	cortexReviewStatusFailed   = "failed"

	cortexReviewVerdictApprove = "approve"
	cortexReviewVerdictRevise  = "revise"
	cortexReviewVerdictReject  = "reject"

	cortexReviewSystemPrompt = "You are a Hirdforge Cortex Reviewer lane. Review the aggregated change proposal for correctness, conflicts, and risk. Do not edit any files. Respond with JSON only."
)

// cloneCortexAggregateReview deep-copies the slices so callers can never mutate
// the store's internal state through a returned value.
func cloneCortexAggregateReview(in CortexAggregateReview) CortexAggregateReview {
	out := in
	if in.Risks != nil {
		out.Risks = make([]string, len(in.Risks))
		copy(out.Risks, in.Risks)
	}
	if in.Recommendations != nil {
		out.Recommendations = make([]string, len(in.Recommendations))
		copy(out.Recommendations, in.Recommendations)
	}
	return out
}

// cortexReviewStore is the in-memory record of Cortex aggregate reviews. Every
// accessor returns a deep copy, never a pointer into the stored slice.
type cortexReviewStore struct {
	mu      sync.Mutex
	nextID  int64
	reviews []CortexAggregateReview
}

func newCortexReviewStore() *cortexReviewStore {
	return &cortexReviewStore{}
}

// Append assigns an id and timestamp to in, stores it, and returns a copy.
func (s *cortexReviewStore) Append(in CortexAggregateReview) CortexAggregateReview {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	in.ID = strconv.FormatInt(s.nextID, 10)
	in.TS = time.Now().UTC()
	s.reviews = append(s.reviews, in)
	return cloneCortexAggregateReview(in)
}

// Current returns a deep copy of the most recent review, or nil if none.
func (s *cortexReviewStore) Current() *CortexAggregateReview {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reviews) == 0 {
		return nil
	}
	c := cloneCortexAggregateReview(s.reviews[len(s.reviews)-1])
	return &c
}

// Find returns a deep copy of the review with the given id, or nil.
func (s *cortexReviewStore) Find(id string) *CortexAggregateReview {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.reviews {
		if s.reviews[i].ID == id {
			c := cloneCortexAggregateReview(s.reviews[i])
			return &c
		}
	}
	return nil
}

// List returns deep copies of all reviews in insertion order.
func (s *cortexReviewStore) List() []CortexAggregateReview {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CortexAggregateReview, len(s.reviews))
	for i := range s.reviews {
		out[i] = cloneCortexAggregateReview(s.reviews[i])
	}
	return out
}

// ListByAggregate returns deep copies of reviews for the given aggregate id.
func (s *cortexReviewStore) ListByAggregate(aggregateID string) []CortexAggregateReview {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []CortexAggregateReview{}
	for i := range s.reviews {
		if s.reviews[i].AggregateID == aggregateID {
			out = append(out, cloneCortexAggregateReview(s.reviews[i]))
		}
	}
	return out
}

// ListByTask returns deep copies of reviews for the given task id.
func (s *cortexReviewStore) ListByTask(taskID string) []CortexAggregateReview {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []CortexAggregateReview{}
	for i := range s.reviews {
		if s.reviews[i].TaskID == taskID {
			out = append(out, cloneCortexAggregateReview(s.reviews[i]))
		}
	}
	return out
}

// ListByLane returns deep copies of reviews for the given lane id.
func (s *cortexReviewStore) ListByLane(laneID string) []CortexAggregateReview {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []CortexAggregateReview{}
	for i := range s.reviews {
		if s.reviews[i].LaneID == laneID {
			out = append(out, cloneCortexAggregateReview(s.reviews[i]))
		}
	}
	return out
}

// cortexReviewContent is the provider's parsed review JSON.
type cortexReviewContent struct {
	Verdict         string   `json:"verdict"`
	Summary         string   `json:"summary"`
	Risks           []string `json:"risks"`
	Recommendations []string `json:"recommendations"`
}

// parseCortexReviewContent decodes and validates a provider review response.
func parseCortexReviewContent(content string) (cortexReviewContent, error) {
	var parsed cortexReviewContent
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return cortexReviewContent{}, fmt.Errorf("provider content is not valid review JSON: %w", err)
	}
	switch parsed.Verdict {
	case cortexReviewVerdictApprove, cortexReviewVerdictRevise, cortexReviewVerdictReject:
	default:
		return cortexReviewContent{}, fmt.Errorf("verdict must be approve, revise, or reject (got %q)", parsed.Verdict)
	}
	if strings.TrimSpace(parsed.Summary) == "" {
		return cortexReviewContent{}, errors.New("review summary is required")
	}
	if parsed.Risks == nil {
		parsed.Risks = []string{}
	}
	if parsed.Recommendations == nil {
		parsed.Recommendations = []string{}
	}
	return parsed, nil
}

// requestProviderReview calls the configured provider for an aggregate review.
// It returns the validated review content, or an error covering any failure
// mode (non-2xx status, transport error, or an invalid/unparseable review).
func requestProviderReview(cfg *providerConfig, systemPrompt, userPrompt string) (cortexReviewContent, error) {
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
		return cortexReviewContent{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(reqBody)))
	if err != nil {
		return cortexReviewContent{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.apiKey)

	client := &http.Client{Timeout: buildPlanTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return cortexReviewContent{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return cortexReviewContent{}, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBody))
		if msg == "" {
			msg = resp.Status
		}
		return cortexReviewContent{}, fmt.Errorf("provider returned status %d: %s", resp.StatusCode, msg)
	}

	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return cortexReviewContent{}, fmt.Errorf("decode provider response: %w", err)
	}
	if len(envelope.Choices) == 0 {
		return cortexReviewContent{}, errors.New("provider response contained no choices")
	}
	return parseCortexReviewContent(envelope.Choices[0].Message.Content)
}

// cortexAggregateReviewUserPrompt renders the provider user message for a
// Reviewer lane's review of an aggregate proposal.
func cortexAggregateReviewUserPrompt(agg CortexAggregateProposal, task CortexTask, lane CortexLane) string {
	var b strings.Builder
	b.WriteString("Cortex Reviewer lane review of an aggregate change proposal.\n")
	b.WriteString("Task goal: " + task.Goal + "\n")
	b.WriteString("Reviewer lane id: " + lane.ID + "\n")
	b.WriteString("Reviewer lane index: " + strconv.Itoa(lane.Index) + "\n")
	b.WriteString("Reviewer lane task: " + lane.Task + "\n\n")

	b.WriteString("Aggregate id: " + agg.ID + "\n")
	b.WriteString("Aggregate status: " + agg.Status + "\n")
	b.WriteString("Aggregate summary: " + agg.Summary + "\n")
	b.WriteString("Source proposal ids: " + strings.Join(agg.SourceProposalIDs, ", ") + "\n")
	if agg.Notes != "" {
		b.WriteString("Notes: " + agg.Notes + "\n")
	}
	b.WriteString("\n")

	b.WriteString("Aggregate files (" + strconv.Itoa(len(agg.Files)) + "):\n")
	for _, f := range agg.Files {
		b.WriteString("- " + f.Action + " " + f.Path + "\n")
	}
	b.WriteString("\n")

	b.WriteString("Conflicts (" + strconv.Itoa(len(agg.Conflicts)) + "):\n")
	for _, c := range agg.Conflicts {
		b.WriteString("- " + c.Path + " across proposals " + strings.Join(c.ProposalIDs, ", ") + ": " + c.Reason + "\n")
	}
	b.WriteString("\n")

	b.WriteString("Review the aggregate. Return JSON only, with no surrounding prose, in exactly this shape:\n")
	b.WriteString(`{"verdict":"approve|revise|reject","summary":"...","risks":["..."],"recommendations":["..."]}`)
	return b.String()
}

// handleCortexAggregateReview serves GET (current review) and POST (a Reviewer
// lane's provider-backed review of an aggregate). It never applies or writes
// proposed files.
func (wb *Server) handleCortexAggregateReview(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		review := wb.cortexReviews.Current()
		if review == nil {
			http.Error(w, "no cortex aggregate review", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, *review)
	case http.MethodPost:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var in struct {
			AggregateID string `json:"aggregate_id"`
			LaneID      string `json:"lane_id"`
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &in); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
		}

		var agg *CortexAggregateProposal
		if in.AggregateID == "" {
			agg = wb.cortexAggregates.Current()
		} else {
			agg = wb.cortexAggregates.Find(in.AggregateID)
		}
		if agg == nil {
			http.Error(w, "cortex aggregate not found", http.StatusNotFound)
			return
		}
		if in.LaneID == "" {
			http.Error(w, "lane_id is required", http.StatusBadRequest)
			return
		}

		task := wb.cortex.Find(agg.TaskID)
		if task == nil {
			http.Error(w, "cortex task not found", http.StatusNotFound)
			return
		}

		var lane *CortexLane
		for i := range task.Lanes {
			if task.Lanes[i].ID == in.LaneID {
				l := task.Lanes[i]
				lane = &l
				break
			}
		}
		if lane == nil {
			http.Error(w, "lane not found", http.StatusNotFound)
			return
		}
		if lane.Role != cortexRoleReviewer {
			http.Error(w, "lane is not a reviewer lane", http.StatusConflict)
			return
		}

		cfg := wb.provider.Config()
		if cfg == nil {
			http.Error(w, "provider must be configured", http.StatusConflict)
			return
		}

		userPrompt := cortexAggregateReviewUserPrompt(*agg, *task, *lane)
		content, reviewErr := requestProviderReview(cfg, cortexReviewSystemPrompt, userPrompt)

		base := CortexAggregateReview{
			AggregateID: agg.ID,
			TaskID:      task.ID,
			LaneID:      lane.ID,
			LaneIndex:   lane.Index,
		}
		if reviewErr != nil {
			base.Status = cortexReviewStatusFailed
			base.Risks = []string{}
			base.Recommendations = []string{}
			base.Error = truncateString(reviewErr.Error(), 1000)
			stored := wb.cortexReviews.Append(base)
			wb.appendCortexEvent("cortex.aggregate.review.failed", "Failed Cortex aggregate review", stored)
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"review": stored,
				"error":  truncateString(reviewErr.Error(), 1000),
			})
			return
		}

		base.Status = cortexReviewStatusReviewed
		base.Verdict = content.Verdict
		base.Summary = content.Summary
		base.Risks = content.Risks
		base.Recommendations = content.Recommendations
		stored := wb.cortexReviews.Append(base)
		wb.appendCortexEvent("cortex.aggregate.review.created", "Created Cortex aggregate review", stored)
		writeJSON(w, http.StatusCreated, stored)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (wb *Server) handleCortexAggregateReviews(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	aggregateID := q.Get("aggregate_id")
	taskID := q.Get("task_id")
	laneID := q.Get("lane_id")

	var out []CortexAggregateReview
	switch {
	case aggregateID != "":
		out = wb.cortexReviews.ListByAggregate(aggregateID)
	case taskID != "":
		out = wb.cortexReviews.ListByTask(taskID)
	case laneID != "":
		out = wb.cortexReviews.ListByLane(laneID)
	default:
		out = wb.cortexReviews.List()
	}
	writeJSON(w, http.StatusOK, out)
}
