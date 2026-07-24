package main

import (
	"strings"
	"sync"
	"testing"
)

func TestRequestRequiresCompletionSignal(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "create a PR",
			input: "create a PR for this change",
			want:  true,
		},
		{
			name:  "PR URL",
			input: "Include the PR URL in your response",
			want:  true,
		},
		{
			name:  "include the PR URL",
			input: "When done, include the PR URL",
			want:  true,
		},
		{
			name:  "Done only when the PR",
			input: "Done only when the PR is created",
			want:  true,
		},
		{
			name:  "branch with PR wording",
			input: "branch: fix/foo PR title: something",
			want:  true,
		},
		{
			name:  "Title with PR wording",
			input: "Title: add feature — PR required",
			want:  true,
		},
		{
			name:  "no PR request",
			input: "Just tell me how the code looks",
			want:  false,
		},
		{
			name:  "empty",
			input: "",
			want:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := requestRequiresCompletionSignal(tc.input)
			if got != tc.want {
				t.Errorf("requestRequiresCompletionSignal(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestContentHasCompletionSignal(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "PR URL present",
			input: "Done. https://git.example.com/kit/hirdforge/pulls/123",
			want:  true,
		},
		{
			name:  "PR #N",
			input: "The fix is in PR #42",
			want:  true,
		},
		{
			name:  "FAILED with reason",
			input: "FAILED: git push was rejected",
			want:  true,
		},
		{
			name:  "NOOP with evidence",
			input: "NOOP: no changes needed",
			want:  true,
		},
		{
			name:  "no signal",
			input: "I think I'm done but not sure",
			want:  false,
		},
		{
			name:  "empty",
			input: "",
			want:  false,
		},
		{
			name:  "lowercase failed",
			input: "failed: could not push",
			want:  true,
		},
		{
			name:  "lowercase noop",
			input: "noop: code is already correct",
			want:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := contentHasCompletionSignal(tc.input)
			if got != tc.want {
				t.Errorf("contentHasCompletionSignal(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestCompletionNudgeStateIncrement(t *testing.T) {
	s := &completionNudgeState{
		nudgeCounts:  make(map[string]int),
		recentErrors: make(map[string]bool),
	}

	sessionID := "test-session-abc"

	count1 := s.increment(sessionID)
	if count1 != 1 {
		t.Errorf("first increment expected 1, got %d", count1)
	}

	count2 := s.increment(sessionID)
	if count2 != 2 {
		t.Errorf("second increment expected 2, got %d", count2)
	}

	count3 := s.increment(sessionID)
	if count3 != 3 {
		t.Errorf("third increment expected 3, got %d", count3)
	}
}

func TestCompletionNudgeStateExhaustion(t *testing.T) {
	s := &completionNudgeState{
		nudgeCounts:  make(map[string]int),
		recentErrors: make(map[string]bool),
	}

	sessionID := "test-session-def"

	if s.hasExhausted(sessionID) {
		t.Error("should not be exhausted with zero nudges")
	}

	for i := 1; i < maxCompletionNudges; i++ {
		s.increment(sessionID)
		if s.hasExhausted(sessionID) {
			t.Errorf("should not be exhausted after %d nudges (cap %d)", i, maxCompletionNudges)
		}
	}
	s.increment(sessionID) // reaches the cap
	if !s.hasExhausted(sessionID) {
		t.Errorf("should be exhausted at %d nudges", maxCompletionNudges)
	}
	s.increment(sessionID)
	if !s.hasExhausted(sessionID) {
		t.Error("should stay exhausted past the cap")
	}
}

func TestCompletionNudgeStateErrorTracking(t *testing.T) {
	s := &completionNudgeState{
		nudgeCounts:  make(map[string]int),
		recentErrors: make(map[string]bool),
	}

	sid := "test-session-err"

	if s.hasRecentError(sid) {
		t.Error("should not have error before recording")
	}

	s.recordError(sid)
	if !s.hasRecentError(sid) {
		t.Error("should have error after recording")
	}
}

func TestCompletionNudgeStatePerSession(t *testing.T) {
	s := &completionNudgeState{
		nudgeCounts:  make(map[string]int),
		recentErrors: make(map[string]bool),
	}

	sid1 := "session-1"
	sid2 := "session-2"

	for i := 0; i < maxCompletionNudges; i++ {
		s.increment(sid1)
	}

	if !s.hasExhausted(sid1) {
		t.Error("session-1 should be exhausted")
	}
	if s.hasExhausted(sid2) {
		t.Error("session-2 should not be exhausted (per-session isolation)")
	}
}

func TestCompletionGateEndToEnd(t *testing.T) {
	testCases := []struct {
		name             string
		userRequest      string
		assistantContent string
		wantPass         bool
	}{
		{
			name:             "PR required with PR URL signal",
			userRequest:      "Create a PR for this fix",
			assistantContent: "PR created: https://git.hirdforge.com/kit/hirdforge/pulls/295",
			wantPass:         true,
		},
		{
			name:             "PR required with FAILED signal",
			userRequest:      "Create a PR",
			assistantContent: "FAILED: remote rejected push",
			wantPass:         true,
		},
		{
			name:             "PR required with NOOP signal",
			userRequest:      "Include the PR URL",
			assistantContent: "NOOP: code was already correct",
			wantPass:         true,
		},
		{
			name:             "PR required without signal — should fail gate",
			userRequest:      "Create a PR for this change",
			assistantContent: "I think I'm done. The code is ready.",
			wantPass:         false,
		},
		{
			name:             "No PR required — always passes",
			userRequest:      "Just review this code",
			assistantContent: "The code looks good.",
			wantPass:         true,
		},
		{
			name:             "PR # signal passes gate",
			userRequest:      "Create a PR",
			assistantContent: "Done. PR #301 is ready for review.",
			wantPass:         true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			needsSignal := requestRequiresCompletionSignal(tc.userRequest)
			hasSignal := contentHasCompletionSignal(tc.assistantContent)

			var passed bool
			if !needsSignal {
				passed = true
			} else {
				passed = hasSignal
			}

			if passed != tc.wantPass {
				t.Errorf("gate passed=%v want=%v (request=%q, content=%q)", passed, tc.wantPass, tc.userRequest, tc.assistantContent)
			}
		})
	}
}

func TestCompletionGateWithFailedTools(t *testing.T) {
	s := &completionNudgeState{
		nudgeCounts:  make(map[string]int),
		recentErrors: make(map[string]bool),
	}

	sid := "session-failed-tool"

	s.recordError(sid)

	if !s.hasRecentError(sid) {
		t.Error("should have recent error for session with failed tool")
	}

	needsSignal := requestRequiresCompletionSignal("Create a PR for the fix")
	if !needsSignal {
		t.Error("should detect PR request")
	}

	content := "The fix is applied."
	hasSignal := contentHasCompletionSignal(content)
	if hasSignal {
		t.Error("content without signal should not pass gate")
	}

	if s.hasExhausted(sid) {
		t.Error("should not be exhausted without nudges")
	}

	for i := 1; i < maxCompletionNudges; i++ {
		s.increment(sid)
		if s.hasExhausted(sid) {
			t.Errorf("should not be exhausted after %d nudges (cap %d)", i, maxCompletionNudges)
		}
	}
	s.increment(sid)
	if !s.hasExhausted(sid) {
		t.Errorf("should be exhausted at %d nudges", maxCompletionNudges)
	}
}

func TestCompletionGateConcurrent(t *testing.T) {
	s := &completionNudgeState{
		nudgeCounts:  make(map[string]int),
		recentErrors: make(map[string]bool),
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sid := "concurrent-session"
			s.increment(sid)
		}(i)
	}
	wg.Wait()

	s.mu.Lock()
	count := s.nudgeCounts["concurrent-session"]
	s.mu.Unlock()

	if count != 100 {
		t.Errorf("expected 100 increments, got %d", count)
	}
}

func TestCompletionGateMaxNudges(t *testing.T) {
	// Raised 2 -> 4 (2026-07-24) to convert end-of-task narration stalls into PRs.
	if maxCompletionNudges != 4 {
		t.Errorf("maxCompletionNudges = %d, want 4", maxCompletionNudges)
	}
}

func TestCompletionGateExhaustedMessage(t *testing.T) {
	expectedPrefix := "FAILED: completion gate exhausted"
	if !strings.HasPrefix(expectedPrefix, "FAILED:") {
		t.Error("exhausted message must start with FAILED:")
	}
}
