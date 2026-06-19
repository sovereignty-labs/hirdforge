package workbench

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
