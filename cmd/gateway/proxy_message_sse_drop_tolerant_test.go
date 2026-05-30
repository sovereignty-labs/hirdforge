package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

type failingSSEWriter struct {
	header         http.Header
	status         int
	writeCnt       int
	failAfter      int
	err            error
	firstWriteDone chan struct{}
}

func newFailingSSEWriter(failAfter int) *failingSSEWriter {
	return &failingSSEWriter{
		header:         make(http.Header),
		failAfter:      failAfter,
		err:            errors.New("simulated client disconnect"),
		firstWriteDone: make(chan struct{}),
	}
}

func (w *failingSSEWriter) Header() http.Header { return w.header }

func (w *failingSSEWriter) WriteHeader(statusCode int) { w.status = statusCode }

func (w *failingSSEWriter) Write(p []byte) (int, error) {
	w.writeCnt++
	if w.writeCnt > w.failAfter {
		return 0, w.err
	}
	if w.writeCnt == 1 {
		close(w.firstWriteDone)
	}
	return len(p), nil
}

func (w *failingSSEWriter) Flush() {}

type scriptedSSETransport struct {
	proceed  chan struct{}
	finished chan error
	canceled chan error
}

func newScriptedSSETransport() *scriptedSSETransport {
	return &scriptedSSETransport{
		proceed:  make(chan struct{}),
		finished: make(chan error, 1),
		canceled: make(chan error, 1),
	}
}

func (t *scriptedSSETransport) RoundTrip(req *http.Request) (*http.Response, error) {
	pr, pw := io.Pipe()
	go func() {
		defer req.Body.Close()

		write := func(s string) error {
			_, err := io.WriteString(pw, s)
			return err
		}

		if err := write("data: {\"type\":\"content\",\"content\":\"hello\"}\n\n"); err != nil {
			if req.Context().Err() != nil {
				t.canceled <- req.Context().Err()
			}
			_ = pw.Close()
			return
		}

		select {
		case <-t.proceed:
		case <-req.Context().Done():
			t.canceled <- req.Context().Err()
			_ = pw.CloseWithError(req.Context().Err())
			return
		}

		if err := write("data: {\"type\":\"content\",\"content\":\" world\"}\n\n"); err != nil {
			if req.Context().Err() != nil {
				t.canceled <- req.Context().Err()
			}
			_ = pw.Close()
			return
		}
		if err := write("data: {\"type\":\"done\",\"done\":true}\n\n"); err != nil {
			if req.Context().Err() != nil {
				t.canceled <- req.Context().Err()
			}
			_ = pw.Close()
			return
		}

		_ = pw.Close()
		t.finished <- nil
	}()

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       pr,
		Request:    req,
	}, nil
}

func TestMessageEndpointDrainsUpstreamAfterClientWriteFailure(t *testing.T) {
	upstream := newScriptedSSETransport()
	streamClient := &http.Client{Transport: upstream}

	gw := &gateway{
		agents: map[string]*Agent{
			"val": {Name: "val", URL: "http://agent.example", Healthy: true},
		},
		order:               []string{"val"},
		events:              make([]Event, 0, 16),
		eventCap:            16,
		sessionStore:        newSessionStore(),
		lastSession:         map[string]string{},
		activeRequests:      map[string]*ActiveRequest{},
		pausedAgents:        map[string]bool{},
		injections:          map[string][]InjectionMessage{},
		delegationTimelines: map[string][]delegationTimelineEvent{},
	}

	mux := http.NewServeMux()
	registerDelegationRoutes(mux, gw, http.DefaultClient, streamClient)

	sessionID := "sess-drop-tolerant"
	body := fmt.Sprintf(`{"agent":"val","content":"hello","session_id":"%s"}`, sessionID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/message", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := newFailingSSEWriter(1)
	handlerDone := make(chan struct{})
	go func() {
		mux.ServeHTTP(w, req)
		close(handlerDone)
	}()

	select {
	case <-w.firstWriteDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first SSE write")
	}

	close(upstream.proceed)

	select {
	case err := <-upstream.finished:
		if err != nil {
			t.Fatalf("upstream stream finished with error: %v", err)
		}
	case err := <-upstream.canceled:
		t.Fatalf("upstream stream was canceled before EOF: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream stream to drain")
	}

	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the gateway handler to return")
	}

	deadline := time.Now().Add(5 * time.Second)
	var sess *Session
	for time.Now().Before(deadline) {
		if got := gw.activeSessionID("val"); got == "" {
			if current, ok := gw.sessionStore.get(sessionID); ok && len(current.Messages) == 2 {
				sess = current
				break
			}
		}
		runtime.Gosched()
	}
	if sess == nil {
		current, ok := gw.sessionStore.get(sessionID)
		if !ok {
			t.Fatalf("session %q was not persisted", sessionID)
		}
		t.Fatalf("persisted message count = %d, want 2", len(current.Messages))
	}
	if got, want := sess.Messages[1].Content, "hello world"; got != want {
		t.Fatalf("persisted assistant content = %q, want %q", got, want)
	}
	if got := gw.activeSessionID("val"); got != "" {
		t.Fatalf("active request still present after drain: %q", got)
	}
	if w.status != http.StatusOK {
		t.Fatalf("response status = %d, want %d", w.status, http.StatusOK)
	}
	if w.writeCnt < 2 {
		t.Fatalf("expected at least two write attempts, got %d", w.writeCnt)
	}
}
