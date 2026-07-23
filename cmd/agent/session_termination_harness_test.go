package main

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"

	toolpkg "git.hirdforge.com/kit/hirdforge/pkg/tools"
)

// This is the first full session-loop test: it drives the real
// newConversationProcessor against the deterministic inference stub (from
// inference_stub_test.go) and asserts the canonical session_termination event
// the loop emits — exercising the runtime, not just the classifiers.

// logCapture is a concurrency-safe io.Writer that records structured log lines
// emitted via logJSON, so tests can assert on session_termination events.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

// entries parses the captured lines into structured log records.
func (c *logCapture) entries() []map[string]interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]interface{}
	for _, line := range bytes.Split(c.buf.Bytes(), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec map[string]interface{}
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// terminationReasons returns the `reason` of every session_termination event,
// in order.
func (c *logCapture) terminationReasons() []string {
	var reasons []string
	for _, rec := range c.entries() {
		if rec["msg"] == sessionTerminationMsg {
			if r, ok := rec["reason"].(string); ok {
				reasons = append(reasons, r)
			}
		}
	}
	return reasons
}

func (c *logCapture) hasTerminationReason(reason terminationReason) bool {
	for _, r := range c.terminationReasons() {
		if r == reason.String() {
			return true
		}
	}
	return false
}

// captureLogs redirects logJSON output to a buffer for the duration of the test
// and restores the previous sink on cleanup.
func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	prev := logWriter
	logWriter = c
	t.Cleanup(func() { logWriter = prev })
	return c
}

// harnessDeps builds the minimal conversationDeps needed to run the loop against
// the stub: a real (empty) tool registry, a positive inference timeout, and the
// stub URL. Everything else is left zero-valued; memoryURL/gatewayURL are empty
// so no external calls are made.
func harnessDeps(srv *stubInferenceServer) conversationDeps {
	return conversationDeps{
		inferenceURL:     srv.URL(),
		model:            stubModel, // routes to /v1/chat/completions
		inferenceTimeout: 30,
		maxToolRounds:    4,
		maxContext:       0, // disables trimming
		agentName:        "harness-agent",
		reg:              toolpkg.NewRegistry(),
	}
}

// runSession runs one turn of the processor against the stub and returns the
// final content and error. emit and logTool are nil; the processor falls back to
// a no-op emitter and skips tool logging.
func runSession(t *testing.T, srv *stubInferenceServer, content string) (string, error) {
	t.Helper()
	proc := newConversationProcessor(harnessDeps(srv))
	return proc(context.Background(), "sess-harness", "", content, nil, nil)
}

func TestSessionLoopNormalCompletion(t *testing.T) {
	logs := captureLogs(t)
	srv := newStubInferenceServer(t, stubContent("Done: opened PR #1"))

	out, err := runSession(t, srv, "please open the PR")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "Done: opened PR #1" {
		t.Errorf("final content = %q, want %q", out, "Done: opened PR #1")
	}
	if !logs.hasTerminationReason(terminationCompleted) {
		t.Errorf("expected a session_termination with reason %q; saw reasons %v",
			terminationCompleted, logs.terminationReasons())
	}
	// The model produced no tool calls, so the loop must not report max_turns.
	if logs.hasTerminationReason(terminationMaxTurns) {
		t.Errorf("did not expect max_turns_reached; saw reasons %v", logs.terminationReasons())
	}
}

func TestSessionLoopNoActionableOutput(t *testing.T) {
	logs := captureLogs(t)
	srv := newStubInferenceServer(t, stubEmpty())

	_, err := runSession(t, srv, "please open the PR")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !logs.hasTerminationReason(terminationNoActionableOutput) {
		t.Errorf("expected a session_termination with reason %q; saw reasons %v",
			terminationNoActionableOutput, logs.terminationReasons())
	}
}
