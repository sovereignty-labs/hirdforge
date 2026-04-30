package workspace

import "testing"

func TestProjectorPlanLifecycle(t *testing.T) {
	p := NewProjector()

	ws := p.Get("jeeves")
	if ws.Plan != nil {
		t.Fatalf("expected nil plan before first event, got %+v", ws.Plan)
	}

	p.Apply("jeeves", "plan", map[string]interface{}{
		"steps": []interface{}{"read trigger context", "draft reply", "queue for approval"},
	})

	ws = p.Get("jeeves")
	if ws.Plan == nil {
		t.Fatalf("expected plan after plan event")
	}
	if ws.Plan.CurrentStep != 0 {
		t.Fatalf("CurrentStep = %d", ws.Plan.CurrentStep)
	}
	if len(ws.Plan.Steps) != 3 {
		t.Fatalf("Steps = %#v", ws.Plan.Steps)
	}

	p.Apply("jeeves", "plan_step_complete", map[string]interface{}{"step": float64(1)})
	ws = p.Get("jeeves")
	if ws.Plan.CurrentStep != 2 {
		t.Fatalf("CurrentStep after completion = %d", ws.Plan.CurrentStep)
	}

	p.Apply("jeeves", "plan_step_complete", map[string]interface{}{"step": float64(99)})
	ws = p.Get("jeeves")
	if ws.Plan.CurrentStep != len(ws.Plan.Steps) {
		t.Fatalf("CurrentStep after clamp = %d", ws.Plan.CurrentStep)
	}
}

func TestProjectorFileWriteTransitions(t *testing.T) {
	p := NewProjector()

	p.Apply("ivar", "file_write", map[string]interface{}{
		"repo":       "kit/hirdforge",
		"branch":     "feature/ui",
		"path":       "cmd/gateway/main.go",
		"line_end":   float64(42),
		"status":     "writing",
		"line_start": float64(1),
	})

	ws := p.Get("ivar")
	if ws.CurrentRepo != "kit/hirdforge" || ws.CurrentBranch != "feature/ui" {
		t.Fatalf("workspace repo/branch = %q/%q", ws.CurrentRepo, ws.CurrentBranch)
	}
	if ws.CurrentFile != "cmd/gateway/main.go" {
		t.Fatalf("CurrentFile = %q", ws.CurrentFile)
	}
	state := ws.FilesTouched["cmd/gateway/main.go"]
	if state.State != "writing" || state.LineCursor != 42 {
		t.Fatalf("state = %+v", state)
	}

	p.Apply("ivar", "file_read", map[string]interface{}{"path": "cmd/gateway/main.go"})
	ws = p.Get("ivar")
	state = ws.FilesTouched["cmd/gateway/main.go"]
	if state.State != "writing" || state.LineCursor != 42 {
		t.Fatalf("file_read downgraded writing state: %+v", state)
	}

	p.Apply("ivar", "file_write", map[string]interface{}{
		"path":   "cmd/gateway/main.go",
		"status": "complete",
	})

	ws = p.Get("ivar")
	if ws.CurrentFile != "" {
		t.Fatalf("CurrentFile after complete = %q", ws.CurrentFile)
	}
	state = ws.FilesTouched["cmd/gateway/main.go"]
	if state.State != "read" || state.LineCursor != 0 {
		t.Fatalf("state after complete = %+v", state)
	}
}

func TestProjectorGitCloneUpdatesCurrentRepo(t *testing.T) {
	p := NewProjector()

	p.Apply("ivar", "git_clone", map[string]interface{}{
		"repo":   "kit/hirdforge",
		"branch": "main",
	})

	ws := p.Get("ivar")
	if ws.CurrentRepo != "kit/hirdforge" {
		t.Fatalf("CurrentRepo = %q", ws.CurrentRepo)
	}
	if ws.CurrentBranch != "main" {
		t.Fatalf("CurrentBranch = %q", ws.CurrentBranch)
	}
	if ws.LastUpdated.IsZero() {
		t.Fatalf("LastUpdated was not set")
	}
}

func TestProjectorSnapshotDeepCopiesFilesTouched(t *testing.T) {
	p := NewProjector()
	p.Apply("ivar", "file_read", map[string]interface{}{"path": "README.md"})
	p.Apply("ivar", "plan", map[string]interface{}{"steps": []interface{}{"one", "two"}})
	p.SetReviewContext("ivar", &PRRef{Owner: "kit", Repo: "hirdforge", Index: 167}, "cmd/gateway/main.go")
	p.SetArchitectContext("ivar", "sess-1", "Coordinate rollout")

	first := p.Get("ivar")
	first.FilesTouched["README.md"] = FileState{Path: "README.md", State: "writing", LineCursor: 99}
	first.Plan.Steps[0] = "mutated"
	first.CurrentPR.Owner = "mutated"
	first.CurrentReviewFile = "changed.go"
	first.CurrentSessionID = "changed"
	first.CurrentObjective = "changed"

	second := p.Get("ivar")
	state := second.FilesTouched["README.md"]
	if state.State != "read" {
		t.Fatalf("snapshot mutation leaked into projector: %+v", state)
	}
	if state.LineCursor != 0 {
		t.Fatalf("LineCursor = %d", state.LineCursor)
	}
	if second.Plan == nil || second.Plan.Steps[0] != "one" {
		t.Fatalf("plan snapshot mutation leaked into projector: %+v", second.Plan)
	}
	if second.CurrentPR == nil || second.CurrentPR.Owner != "kit" || second.CurrentReviewFile != "cmd/gateway/main.go" {
		t.Fatalf("review context snapshot mutation leaked into projector: %+v file=%q", second.CurrentPR, second.CurrentReviewFile)
	}
	if second.CurrentSessionID != "sess-1" || second.CurrentObjective != "Coordinate rollout" {
		t.Fatalf("architect context snapshot mutation leaked: session=%q objective=%q", second.CurrentSessionID, second.CurrentObjective)
	}
}

func TestProjectorSetReviewContext(t *testing.T) {
	p := NewProjector()

	ws := p.SetReviewContext("freya", &PRRef{Owner: "kit", Repo: "hirdforge", Index: 169}, "cmd/gateway/ui.html")
	if ws.CurrentPR == nil || ws.CurrentPR.Owner != "kit" || ws.CurrentPR.Repo != "hirdforge" || ws.CurrentPR.Index != 169 {
		t.Fatalf("CurrentPR = %+v", ws.CurrentPR)
	}
	if ws.CurrentReviewFile != "cmd/gateway/ui.html" {
		t.Fatalf("CurrentReviewFile = %q", ws.CurrentReviewFile)
	}

	ws = p.SetReviewContext("freya", nil, "")
	if ws.CurrentPR != nil {
		t.Fatalf("CurrentPR after clear = %+v", ws.CurrentPR)
	}
	if ws.CurrentReviewFile != "" {
		t.Fatalf("CurrentReviewFile after clear = %q", ws.CurrentReviewFile)
	}
}

func TestProjectorDelegateSetsArchitectContext(t *testing.T) {
	p := NewProjector()

	p.Apply("rune", "delegate", map[string]interface{}{
		"session_id":        "sess-77",
		"objective_summary": "Split rollout into builder and reviewer tracks",
		"target_repo":       "kit/hirdforge",
		"from_agent":        "rune",
		"to_agent":          "ivar",
	})

	ws := p.Get("rune")
	if ws.CurrentSessionID != "sess-77" {
		t.Fatalf("CurrentSessionID = %q", ws.CurrentSessionID)
	}
	if ws.CurrentObjective != "Split rollout into builder and reviewer tracks" {
		t.Fatalf("CurrentObjective = %q", ws.CurrentObjective)
	}
	if ws.CurrentObjectiveAt.IsZero() {
		t.Fatalf("CurrentObjectiveAt was not set")
	}
}

func TestProjectorSetArchitectSessionDataReplacesAndCopies(t *testing.T) {
	p := NewProjector()
	p.SetArchitectContext("rune", "sess-1", "Coordinate rollout")

	firstTasks := []TaskRef{{
		ID:        "task-1",
		Agent:     "ivar",
		Status:    "working",
		Content:   "TASK: Update gateway",
		SessionID: "sess-1",
		PRNumber:  173,
	}}
	firstTimeline := []TimelineEvent{{
		Type:      "delegate",
		Agent:     "rune",
		Timestamp: "2026-04-30T00:00:00Z",
		Metadata:  map[string]interface{}{"target_agent": "ivar"},
	}}

	ws := p.SetArchitectSessionData("rune", "sess-1", firstTasks, firstTimeline)
	if len(ws.SessionTasks) != 1 || ws.SessionTasks[0].ID != "task-1" {
		t.Fatalf("SessionTasks = %+v", ws.SessionTasks)
	}
	if len(ws.SessionTimeline) != 1 || ws.SessionTimeline[0].Type != "delegate" {
		t.Fatalf("SessionTimeline = %+v", ws.SessionTimeline)
	}

	firstTasks[0].ID = "mutated"
	firstTimeline[0].Metadata["target_agent"] = "mutated"

	ws = p.Get("rune")
	if ws.SessionTasks[0].ID != "task-1" {
		t.Fatalf("task mutation leaked into projector: %+v", ws.SessionTasks)
	}
	if got := ws.SessionTimeline[0].Metadata["target_agent"]; got != "ivar" {
		t.Fatalf("timeline metadata mutation leaked into projector: %+v", ws.SessionTimeline)
	}

	replaced := p.SetArchitectSessionData("rune", "sess-2", []TaskRef{{
		ID:        "task-2",
		Agent:     "freya",
		Status:    "submitted",
		Content:   "TASK: Review PR",
		SessionID: "sess-2",
	}}, nil)
	if replaced.CurrentSessionID != "sess-2" {
		t.Fatalf("CurrentSessionID = %q", replaced.CurrentSessionID)
	}
	if len(replaced.SessionTasks) != 1 || replaced.SessionTasks[0].ID != "task-2" {
		t.Fatalf("SessionTasks after replace = %+v", replaced.SessionTasks)
	}
	if len(replaced.SessionTimeline) != 0 {
		t.Fatalf("SessionTimeline after replace = %+v", replaced.SessionTimeline)
	}
}
