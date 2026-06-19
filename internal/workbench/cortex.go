package workbench

import (
	"context"
	"encoding/json"
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
