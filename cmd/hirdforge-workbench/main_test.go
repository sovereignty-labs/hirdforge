package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWorkbenchHealth(t *testing.T) {
	mux := newWorkbench().mux
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json content type, got %q", got)
	}

	var body struct {
		Status string `json:"status"`
		Mode   string `json:"mode"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal body: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("expected status=ok, got %q", body.Status)
	}
	if body.Mode != "workbench" {
		t.Errorf("expected mode=workbench, got %q", body.Mode)
	}
}

func TestWorkbenchIndex(t *testing.T) {
	mux := newWorkbench().mux
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("expected text/html content type, got %q", got)
	}
	if !strings.Contains(w.Body.String(), "Hirdforge Workbench") {
		t.Errorf("expected body to contain title, got: %q", w.Body.String())
	}
}

func TestWorkbenchRejectsNonGET(t *testing.T) {
	mux := newWorkbench().mux
	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", w.Code)
	}
}

func TestWorkbenchUnknownPath(t *testing.T) {
	mux := newWorkbench().mux
	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", w.Code)
	}
}

func TestEventStoreAppendList(t *testing.T) {
	s := newEventStore()

	first := s.Append("alpha", "first message", json.RawMessage(`{"k":1}`))
	second := s.Append("beta", "", nil)

	if first.ID == "" || second.ID == "" {
		t.Fatal("expected ids to be set")
	}
	if first.ID == second.ID {
		t.Fatalf("expected distinct ids, got %q twice", first.ID)
	}
	if first.TS.IsZero() || second.TS.IsZero() {
		t.Fatal("expected timestamps to be set")
	}
	if !first.TS.Before(second.TS) && !first.TS.Equal(second.TS) {
		t.Errorf("expected non-decreasing timestamps, got first=%v second=%v", first.TS, second.TS)
	}
	if first.Type != "alpha" || second.Type != "beta" {
		t.Errorf("unexpected types: %q %q", first.Type, second.Type)
	}
	if first.Message != "first message" {
		t.Errorf("expected first message to round-trip, got %q", first.Message)
	}
	if string(first.Data) != `{"k":1}` {
		t.Errorf("expected first data to round-trip, got %s", first.Data)
	}
	if second.Message != "" {
		t.Errorf("expected empty message, got %q", second.Message)
	}
	if second.Data != nil {
		t.Errorf("expected nil data, got %s", second.Data)
	}

	list := s.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 events, got %d", len(list))
	}
	if list[0].ID != first.ID || list[1].ID != second.ID {
		t.Errorf("expected insertion order, got %+v", list)
	}

	list[0].Type = "mutated"
	again := s.List()
	if again[0].Type != "alpha" {
		t.Errorf("expected List to return a defensive copy, got %q", again[0].Type)
	}
}

func TestWorkbenchHasStartupEvent(t *testing.T) {
	wb := newWorkbench()
	events := wb.store.List()
	if len(events) != 1 {
		t.Fatalf("expected 1 event after startup, got %d", len(events))
	}
	if events[0].Type != startupType {
		t.Errorf("expected type=%q, got %q", startupType, events[0].Type)
	}
	if events[0].ID == "" {
		t.Error("expected startup event id to be set")
	}
	if events[0].TS.IsZero() {
		t.Error("expected startup event ts to be set")
	}
}

func TestWorkbenchEventsGET(t *testing.T) {
	wb := newWorkbench()
	wb.store.Append("custom.event", "hello", json.RawMessage(`{"foo":"bar"}`))

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/events", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json content type, got %q", got)
	}

	var got []WorkbenchEvent
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got))
	}
	if got[0].Type != startupType {
		t.Errorf("expected first event %q, got %q", startupType, got[0].Type)
	}
	if got[1].Type != "custom.event" {
		t.Errorf("expected second event custom.event, got %q", got[1].Type)
	}
	if got[1].Message != "hello" {
		t.Errorf("expected message=hello, got %q", got[1].Message)
	}
	if string(got[1].Data) != `{"foo":"bar"}` {
		t.Errorf("expected data to round-trip, got %s", got[1].Data)
	}
}

func TestWorkbenchEventsGETEmpty(t *testing.T) {
	wb := &workbench{store: newEventStore()}
	wb.mux = wb.registerRoutes()

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/events", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := strings.TrimSpace(w.Body.String())
	if body != "[]" {
		t.Errorf("expected body to be [] for empty store, got %q", body)
	}
}

func TestWorkbenchEventsPOST(t *testing.T) {
	wb := newWorkbench()
	payload := `{"type":"user.action","message":"clicked","data":{"button":"save"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/workbench/events", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json content type, got %q", got)
	}

	var ev WorkbenchEvent
	if err := json.Unmarshal(w.Body.Bytes(), &ev); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ev.Type != "user.action" {
		t.Errorf("expected type=user.action, got %q", ev.Type)
	}
	if ev.Message != "clicked" {
		t.Errorf("expected message=clicked, got %q", ev.Message)
	}
	if string(ev.Data) != `{"button":"save"}` {
		t.Errorf("expected data round-trip, got %s", ev.Data)
	}
	if ev.ID == "" {
		t.Error("expected id to be set on response")
	}
	if ev.TS.IsZero() {
		t.Error("expected ts to be set on response")
	}
	if time.Since(ev.TS) > time.Minute {
		t.Errorf("expected ts to be recent, got %v", ev.TS)
	}

	list := wb.store.List()
	if len(list) != 2 {
		t.Fatalf("expected store to have 2 events, got %d", len(list))
	}
	if list[1].ID != ev.ID {
		t.Errorf("expected returned event to match store tail, got %q vs %q", ev.ID, list[1].ID)
	}
}

func TestWorkbenchEventsPOSTBadJSON(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodPost, "/api/workbench/events", strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestWorkbenchEventsPOSTMissingType(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodPost, "/api/workbench/events", strings.NewReader(`{"message":"no type"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestWorkbenchEventsMethodNotAllowed(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodPut, "/api/workbench/events", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}
