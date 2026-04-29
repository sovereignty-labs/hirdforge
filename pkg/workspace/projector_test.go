package workspace

import "testing"

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

	first := p.Get("ivar")
	first.FilesTouched["README.md"] = FileState{Path: "README.md", State: "writing", LineCursor: 99}

	second := p.Get("ivar")
	state := second.FilesTouched["README.md"]
	if state.State != "read" {
		t.Fatalf("snapshot mutation leaked into projector: %+v", state)
	}
	if state.LineCursor != 0 {
		t.Fatalf("LineCursor = %d", state.LineCursor)
	}
}
