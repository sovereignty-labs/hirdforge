package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.hirdforge.com/kit/hirdforge/internal/steward"
)

// TestStreamStewardChatHeartbeatSurvivesSilentRound proves the fix for the "it
// glitches, keeps trying, then dies" symptom: a deep grounding turn goes silent
// for a long stretch while the model prefills a large context, and an idle
// intermediary (the Cloudflare tunnel fronting the cockpit) drops a quiet stream.
// The relay must emit keepalive heartbeats through the silence AND still deliver
// the final reply once the agent resumes.
func TestStreamStewardChatHeartbeatSurvivesSilentRound(t *testing.T) {
	old := stewardHeartbeatInterval
	stewardHeartbeatInterval = 20 * time.Millisecond
	defer func() { stewardHeartbeatInterval = old }()

	// Fake interlocutor agent: a tool_call, then an 80ms SILENT gap (four
	// heartbeat intervals), then the final answer, then close.
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fl := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"tool_call\",\"tool\":\"read\"}\n\n")
		fl.Flush()
		time.Sleep(80 * time.Millisecond)
		_, _ = io.WriteString(w, "data: {\"type\":\"replace\",\"content\":\"Here is the full answer.\"}\n\n")
		fl.Flush()
	}))
	defer agent.Close()

	gw := &gateway{stewardEngine: steward.NewEngine(&scriptedRunner{outs: []string{""}}), stewardURL: agent.URL}
	mux := http.NewServeMux()
	registerStewardRoutes(mux, gw)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/steward/chat/stream", "application/json",
		strings.NewReader(`{"session_id":"s","message":"investigate"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	got := string(raw)

	if !strings.Contains(got, ": ping") {
		t.Errorf("expected a heartbeat during the silent gap; stream had none:\n%s", got)
	}
	if !strings.Contains(got, "Here is the full answer.") {
		t.Errorf("final reply not delivered through the stream:\n%s", got)
	}
	if !strings.Contains(got, `"done":true`) {
		t.Errorf("missing terminal done event:\n%s", got)
	}
	// The heartbeat is a comment line, so the cockpit (which only acts on `data:`
	// lines) must never see it as an event — guard against a regression that makes
	// it `data:`-shaped.
	if strings.Contains(got, "data: : ping") {
		t.Errorf("heartbeat leaked as a data event:\n%s", got)
	}
}

// TestStreamStewardChatForwardsToolThenReplace proves the relay still forwards a
// tool-activity event and then finalizes on a replace snapshot (buffer reset), so
// the heartbeat restructure did not change content handling.
func TestStreamStewardChatForwardsToolThenReplace(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fl := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"content\",\"content\":\"Let me look...\"}\n\n")
		fl.Flush()
		_, _ = io.WriteString(w, "data: {\"type\":\"tool_call\",\"tool\":\"list-dir\"}\n\n")
		fl.Flush()
		_, _ = io.WriteString(w, "data: {\"type\":\"replace\",\"content\":\"Final grounded answer.\"}\n\n")
		fl.Flush()
	}))
	defer agent.Close()

	gw := &gateway{stewardEngine: steward.NewEngine(&scriptedRunner{outs: []string{""}}), stewardURL: agent.URL}
	mux := http.NewServeMux()
	registerStewardRoutes(mux, gw)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/steward/chat/stream", "application/json",
		strings.NewReader(`{"session_id":"s","message":"go"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	got := string(raw)

	if !strings.Contains(got, `"tool":"list-dir"`) {
		t.Errorf("tool activity not forwarded:\n%s", got)
	}
	// The replace snapshot resets the buffer, so the interim "Let me look..." must
	// not survive into the final reply.
	if !strings.Contains(got, "Final grounded answer.") || strings.Contains(got, "Let me look...Final") {
		t.Errorf("replace snapshot not honored:\n%s", got)
	}
}
