package tasklife

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFormatSovereignMessage(t *testing.T) {
	event := TaskEvent{
		State:  StateCompleted,
		Result: "done",
	}
	if got := formatSovereignMessage(event); got != "✅ completed: done" {
		t.Fatalf("formatSovereignMessage() = %q", got)
	}
}

func TestFormatSovereignMessageTruncatesPreview(t *testing.T) {
	event := TaskEvent{
		State:  StateFailedNoPR,
		Result: strings.Repeat("x", 250),
	}
	got := formatSovereignMessage(event)
	if !strings.HasPrefix(got, "❌ failed: ") {
		t.Fatalf("unexpected prefix: %q", got)
	}
	if len([]rune(got)) > len([]rune("❌ failed: "))+203 {
		t.Fatalf("expected truncated preview, got length %d", len([]rune(got)))
	}
}

func TestSovereignReporterReportPostsNotification(t *testing.T) {
	received := make(chan map[string]string, 1)
	reporter := NewSovereignReporter("http://gateway.test", "sovereign", true)
	reporter.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/notify" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		received <- payload
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBuffer(nil)),
			Header:     make(http.Header),
		}, nil
	})}
	reporter.Report(TaskEvent{
		TaskID:    "task-123",
		State:     StateNudged,
		Result:    "Add the PR URL.",
		Timestamp: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	})

	select {
	case payload := <-received:
		if payload["from"] != "sovereign" {
			t.Fatalf("from = %q", payload["from"])
		}
		if payload["agent"] != "sovereign" {
			t.Fatalf("agent = %q", payload["agent"])
		}
		if payload["task_id"] != "task-123" {
			t.Fatalf("task_id = %q", payload["task_id"])
		}
		if payload["state"] != string(StateNudged) {
			t.Fatalf("state = %q", payload["state"])
		}
		if payload["result"] != "⚠️ nudged: Add the PR URL." {
			t.Fatalf("result = %q", payload["result"])
		}
		if payload["timestamp"] != "2026-03-01T12:00:00Z" {
			t.Fatalf("timestamp = %q", payload["timestamp"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notification")
	}
}

func TestSovereignReporterDisabled(t *testing.T) {
	called := false
	reporter := NewSovereignReporter("http://gateway.test", "sovereign", false)
	reporter.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBuffer(nil)),
			Header:     make(http.Header),
		}, nil
	})}
	reporter.Report(TaskEvent{TaskID: "task-123", State: StateCompleted, Result: "done"})
	time.Sleep(100 * time.Millisecond)
	if called {
		t.Fatal("expected disabled reporter not to send requests")
	}
}
