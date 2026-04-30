package workspace

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type AgentWorkspace struct {
	AgentName          string               `json:"agent_name"`
	CurrentRepo        string               `json:"current_repo"`
	CurrentBranch      string               `json:"current_branch"`
	CurrentFile        string               `json:"current_file"`
	CurrentSessionID   string               `json:"current_session_id,omitempty"`
	CurrentObjective   string               `json:"current_objective,omitempty"`
	CurrentObjectiveAt time.Time            `json:"current_objective_at,omitempty"`
	CurrentPR          *PRRef               `json:"current_pr,omitempty"`
	CurrentReviewFile  string               `json:"current_review_file,omitempty"`
	SessionTasks       []TaskRef            `json:"session_tasks,omitempty"`
	SessionTimeline    []TimelineEvent      `json:"session_timeline,omitempty"`
	FilesTouched       map[string]FileState `json:"files_touched"`
	Plan               *Plan                `json:"plan,omitempty"`
	LastUpdated        time.Time            `json:"last_updated"`
}

type PRRef struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	Index int    `json:"index"`
}

type FileState struct {
	Path       string    `json:"path"`
	State      string    `json:"state"`
	LastAccess time.Time `json:"last_access"`
	LineCursor int       `json:"line_cursor"`
}

type Plan struct {
	Steps       []string  `json:"steps"`
	CurrentStep int       `json:"current_step"`
	StartedAt   time.Time `json:"started_at"`
}

type TaskRef struct {
	ID        string    `json:"id"`
	Agent     string    `json:"agent"`
	Status    string    `json:"status"`
	Content   string    `json:"content"`
	SessionID string    `json:"session_id,omitempty"`
	PRNumber  int       `json:"pr_number,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type TimelineEvent struct {
	Type      string                 `json:"type"`
	Agent     string                 `json:"agent"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Timestamp string                 `json:"timestamp"`
}

type Projector struct {
	mu         sync.RWMutex
	workspaces map[string]*AgentWorkspace
}

func NewProjector() *Projector {
	return &Projector{workspaces: map[string]*AgentWorkspace{}}
}

func IsTypedEvent(eventType string) bool {
	switch eventType {
	case "file_read", "file_write", "git_clone", "git_branch_create", "git_commit", "git_push", "pr_create", "exec", "http_request", "plan", "plan_step_complete", "delegate":
		return true
	default:
		return false
	}
}

func (p *Projector) Apply(agentName string, eventType string, payload map[string]interface{}) {
	if p == nil || agentName == "" || !IsTypedEvent(eventType) {
		return
	}
	now := time.Now()

	p.mu.Lock()
	defer p.mu.Unlock()

	ws := p.ensureLocked(agentName)
	ws.LastUpdated = now

	switch eventType {
	case "delegate":
		sessionID := stringPayload(payload, "session_id")
		objective := stringPayload(payload, "objective_summary")
		if sessionID != "" {
			ws.CurrentSessionID = sessionID
		}
		if objective != "" {
			ws.CurrentObjective = objective
		}
		ws.CurrentObjectiveAt = now
	case "plan":
		steps := stringSlicePayload(payload, "steps")
		if len(steps) == 0 {
			return
		}
		ws.Plan = &Plan{
			Steps:       steps,
			CurrentStep: 0,
			StartedAt:   now,
		}
	case "plan_step_complete":
		if ws.Plan == nil {
			return
		}
		step := intPayload(payload, "step")
		if step < 0 {
			return
		}
		next := step + 1
		if next > len(ws.Plan.Steps) {
			next = len(ws.Plan.Steps)
		}
		ws.Plan.CurrentStep = next
	case "file_read":
		path := stringPayload(payload, "path")
		if path == "" {
			return
		}
		p.applyRepoBranch(ws, payload)
		state := ws.FilesTouched[path]
		if state.State != "writing" {
			state.State = "read"
			state.LineCursor = 0
		}
		state.Path = path
		state.LastAccess = now
		ws.FilesTouched[path] = state
	case "file_write":
		path := stringPayload(payload, "path")
		if path == "" {
			return
		}
		p.applyRepoBranch(ws, payload)
		status := stringPayload(payload, "status")
		state := ws.FilesTouched[path]
		state.Path = path
		state.LastAccess = now
		switch status {
		case "writing":
			ws.CurrentFile = path
			state.State = "writing"
			state.LineCursor = intPayload(payload, "line_end")
		case "complete":
			if ws.CurrentFile == path {
				ws.CurrentFile = ""
			}
			state.State = "read"
			state.LineCursor = 0
		default:
			return
		}
		ws.FilesTouched[path] = state
	case "git_clone":
		repo := stringPayload(payload, "repo")
		if repo != "" {
			ws.CurrentRepo = repo
		}
		branch := stringPayload(payload, "branch")
		if branch != "" {
			ws.CurrentBranch = branch
		}
	case "git_branch_create":
		branch := stringPayload(payload, "branch_new")
		if branch != "" {
			ws.CurrentBranch = branch
		}
		repo := stringPayload(payload, "repo")
		if repo != "" {
			ws.CurrentRepo = repo
		}
	case "git_commit", "git_push", "pr_create", "exec", "http_request":
		p.applyRepoBranch(ws, payload)
	}
}

func (p *Projector) Get(agentName string) AgentWorkspace {
	if p == nil {
		return AgentWorkspace{AgentName: agentName, FilesTouched: map[string]FileState{}}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()

	ws, ok := p.workspaces[agentName]
	if !ok || ws == nil {
		return AgentWorkspace{AgentName: agentName, FilesTouched: map[string]FileState{}}
	}
	return cloneWorkspace(ws)
}

func (p *Projector) SetReviewContext(agentName string, pr *PRRef, reviewFile string) AgentWorkspace {
	if p == nil {
		return AgentWorkspace{AgentName: agentName, FilesTouched: map[string]FileState{}}
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	ws := p.ensureLocked(agentName)
	ws.LastUpdated = time.Now()
	if pr == nil {
		ws.CurrentPR = nil
		ws.CurrentReviewFile = ""
		return cloneWorkspace(ws)
	}
	prCopy := *pr
	ws.CurrentPR = &prCopy
	ws.CurrentReviewFile = strings.TrimSpace(reviewFile)
	return cloneWorkspace(ws)
}

func (p *Projector) SetArchitectContext(agentName, sessionID, objective string) AgentWorkspace {
	if p == nil {
		return AgentWorkspace{AgentName: agentName, FilesTouched: map[string]FileState{}}
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	ws := p.ensureLocked(agentName)
	ws.LastUpdated = time.Now()
	ws.CurrentSessionID = strings.TrimSpace(sessionID)
	ws.CurrentObjective = strings.TrimSpace(objective)
	ws.CurrentObjectiveAt = ws.LastUpdated
	return cloneWorkspace(ws)
}

func (p *Projector) SetArchitectSessionData(agentName, sessionID string, tasks []TaskRef, timeline []TimelineEvent) AgentWorkspace {
	if p == nil {
		return AgentWorkspace{AgentName: agentName, FilesTouched: map[string]FileState{}}
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	ws := p.ensureLocked(agentName)
	ws.LastUpdated = time.Now()
	ws.CurrentSessionID = strings.TrimSpace(sessionID)
	ws.SessionTasks = cloneTaskRefs(tasks)
	ws.SessionTimeline = cloneTimelineEvents(timeline)
	return cloneWorkspace(ws)
}

func (p *Projector) ensureLocked(agentName string) *AgentWorkspace {
	ws := p.workspaces[agentName]
	if ws == nil {
		ws = &AgentWorkspace{
			AgentName:    agentName,
			FilesTouched: map[string]FileState{},
		}
		p.workspaces[agentName] = ws
	}
	if ws.FilesTouched == nil {
		ws.FilesTouched = map[string]FileState{}
	}
	return ws
}

func (p *Projector) applyRepoBranch(ws *AgentWorkspace, payload map[string]interface{}) {
	repo := stringPayload(payload, "repo")
	if repo != "" {
		ws.CurrentRepo = repo
	}
	branch := stringPayload(payload, "branch")
	if branch != "" {
		ws.CurrentBranch = branch
	}
}

func cloneWorkspace(ws *AgentWorkspace) AgentWorkspace {
	out := *ws
	out.FilesTouched = make(map[string]FileState, len(ws.FilesTouched))
	for path, state := range ws.FilesTouched {
		out.FilesTouched[path] = state
	}
	if ws.Plan != nil {
		plan := *ws.Plan
		plan.Steps = append([]string(nil), ws.Plan.Steps...)
		out.Plan = &plan
	}
	if ws.CurrentPR != nil {
		pr := *ws.CurrentPR
		out.CurrentPR = &pr
	}
	out.SessionTasks = cloneTaskRefs(ws.SessionTasks)
	out.SessionTimeline = cloneTimelineEvents(ws.SessionTimeline)
	return out
}

func cloneTaskRefs(in []TaskRef) []TaskRef {
	if len(in) == 0 {
		return nil
	}
	out := make([]TaskRef, len(in))
	copy(out, in)
	return out
}

func cloneTimelineEvents(in []TimelineEvent) []TimelineEvent {
	if len(in) == 0 {
		return nil
	}
	out := make([]TimelineEvent, len(in))
	for i, evt := range in {
		out[i] = evt
		if len(evt.Metadata) > 0 {
			meta := make(map[string]interface{}, len(evt.Metadata))
			for k, v := range evt.Metadata {
				meta[k] = v
			}
			out[i].Metadata = meta
		}
	}
	return out
}

func stringPayload(payload map[string]interface{}, key string) string {
	v, ok := payload[key]
	if !ok || v == nil {
		return ""
	}
	return stringFromAny(v)
}

func intPayload(payload map[string]interface{}, key string) int {
	v, ok := payload[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	case jsonNumber:
		i, _ := n.Int64()
		return int(i)
	default:
		var out int
		_, _ = fmt.Sscanf(fmt.Sprint(v), "%d", &out)
		return out
	}
}

type jsonNumber interface {
	Int64() (int64, error)
}

func stringSlicePayload(payload map[string]interface{}, key string) []string {
	v, ok := payload[key]
	if !ok || v == nil {
		return nil
	}
	switch items := v.(type) {
	case []string:
		out := make([]string, 0, len(items))
		for _, item := range items {
			if item = stringFromAny(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(items))
		for _, item := range items {
			if text := stringFromAny(item); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func stringFromAny(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	value := strings.TrimSpace(fmt.Sprint(v))
	if value == "<nil>" {
		return ""
	}
	return value
}
