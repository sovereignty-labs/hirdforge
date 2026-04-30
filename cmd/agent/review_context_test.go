package main

import (
	"testing"
	"time"

	toolpkg "github.com/kitporath/project_valhalla/pkg/tools"
)

func TestDirectPRToolContext(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		args     map[string]interface{}
		wantPR   *prRef
		wantFile string
		wantOK   bool
	}{
		{
			name:     "list pr files from owner repo",
			toolName: "list-pr-files",
			args:     map[string]interface{}{"repo": "kit/hirdforge", "index": float64(167)},
			wantPR:   &prRef{Owner: "kit", Repo: "hirdforge", Index: 167},
			wantOK:   true,
		},
		{
			name:     "review tool with explicit owner and repo",
			toolName: "create-review",
			args:     map[string]interface{}{"owner": "kit", "repo": "hirdforge", "pull_number": "168"},
			wantPR:   &prRef{Owner: "kit", Repo: "hirdforge", Index: 168},
			wantOK:   true,
		},
		{
			name:     "diff style tool captures file",
			toolName: "get-pr-diff",
			args:     map[string]interface{}{"repo": "kit/hirdforge", "number": "169", "file": "cmd/gateway/ui.html"},
			wantPR:   &prRef{Owner: "kit", Repo: "hirdforge", Index: 169},
			wantFile: "cmd/gateway/ui.html",
			wantOK:   true,
		},
		{
			name:     "non pr tool ignored",
			toolName: "read",
			args:     map[string]interface{}{"repo": "kit/hirdforge", "index": float64(1)},
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPR, gotFile, gotOK := directPRToolContext(tt.toolName, tt.args)
			if gotOK != tt.wantOK {
				t.Fatalf("directPRToolContext() ok=%v want %v", gotOK, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if gotPR == nil || *gotPR != *tt.wantPR {
				t.Fatalf("directPRToolContext() pr=%+v want %+v", gotPR, tt.wantPR)
			}
			if gotFile != tt.wantFile {
				t.Fatalf("directPRToolContext() file=%q want %q", gotFile, tt.wantFile)
			}
		})
	}
}

func TestParseGatewayPRRequest(t *testing.T) {
	tests := []struct {
		name     string
		args     map[string]interface{}
		wantPR   *prRef
		wantFile string
		wantOK   bool
	}{
		{
			name:   "files endpoint",
			args:   map[string]interface{}{"url": "http://gateway/api/v1/gitea/prs/kit/hirdforge/167/files"},
			wantPR: &prRef{Owner: "kit", Repo: "hirdforge", Index: 167},
			wantOK: true,
		},
		{
			name:     "diff endpoint with file",
			args:     map[string]interface{}{"url": "http://gateway/api/v1/gitea/prs/kit/hirdforge/167/diff?file=cmd%2Fgateway%2Fmain.go"},
			wantPR:   &prRef{Owner: "kit", Repo: "hirdforge", Index: 167},
			wantFile: "cmd/gateway/main.go",
			wantOK:   true,
		},
		{
			name:   "non matching path",
			args:   map[string]interface{}{"url": "http://gateway/api/v1/cluster/pods"},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPR, gotFile, gotOK := parseGatewayPRRequest(tt.args)
			if gotOK != tt.wantOK {
				t.Fatalf("parseGatewayPRRequest() ok=%v want %v", gotOK, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if gotPR == nil || *gotPR != *tt.wantPR {
				t.Fatalf("parseGatewayPRRequest() pr=%+v want %+v", gotPR, tt.wantPR)
			}
			if gotFile != tt.wantFile {
				t.Fatalf("parseGatewayPRRequest() file=%q want %q", gotFile, tt.wantFile)
			}
		})
	}
}

func TestReviewContextTrackerUpdateFromToolSetsCurrentPR(t *testing.T) {
	tracker := newReviewContextTracker("freya", "", 30*time.Minute)
	defer tracker.Stop()

	tracker.UpdateFromTool("list-pr-files", map[string]interface{}{
		"repo":  "kit/hirdforge",
		"index": float64(169),
	}, toolpkg.ToolResult{Output: "ok"})

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.state.CurrentPR == nil || *tracker.state.CurrentPR != (prRef{Owner: "kit", Repo: "hirdforge", Index: 169}) {
		t.Fatalf("CurrentPR = %+v", tracker.state.CurrentPR)
	}
}

func TestReviewContextTrackerClearIfTaskWithoutPR(t *testing.T) {
	tracker := newReviewContextTracker("freya", "", 30*time.Minute)
	defer tracker.Stop()
	tracker.state.CurrentPR = &prRef{Owner: "kit", Repo: "hirdforge", Index: 169}
	tracker.state.CurrentReviewFile = "cmd/gateway/ui.html"

	tracker.ClearIfTaskWithoutPR("Please audit the current cluster health and report back.")

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.state.CurrentPR != nil {
		t.Fatalf("CurrentPR after non-PR dispatch = %+v", tracker.state.CurrentPR)
	}
	if tracker.state.CurrentReviewFile != "" {
		t.Fatalf("CurrentReviewFile after non-PR dispatch = %q", tracker.state.CurrentReviewFile)
	}
}

func TestReviewContextTrackerIdleTimeoutClearsState(t *testing.T) {
	tracker := newReviewContextTracker("freya", "", time.Minute)
	defer tracker.Stop()
	tracker.state.CurrentPR = &prRef{Owner: "kit", Repo: "hirdforge", Index: 169}
	tracker.state.CurrentReviewFile = "cmd/gateway/ui.html"
	tracker.lastPRTouch = time.Now().Add(-2 * time.Minute)

	if !tracker.shouldClearForIdle() {
		t.Fatalf("expected shouldClearForIdle to be true")
	}
	tracker.clear()

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.state.CurrentPR != nil {
		t.Fatalf("CurrentPR after idle clear = %+v", tracker.state.CurrentPR)
	}
	if tracker.state.CurrentReviewFile != "" {
		t.Fatalf("CurrentReviewFile after idle clear = %q", tracker.state.CurrentReviewFile)
	}
}
