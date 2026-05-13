package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithRequestBodyLimit_AllowsSmallBody(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Write(body)
	})
	limit := int64(1024)
	handler := withRequestBodyLimit(inner, limit)

	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewBufferString("hello world"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "hello world" {
		t.Fatalf("echoed body = %q", got)
	}
}

func TestWithRequestBodyLimit_RejectsOversize(t *testing.T) {
	innerCalled := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		_, err := io.ReadAll(r.Body)
		if err == nil {
			t.Errorf("expected read error on oversize body, got nil")
			http.Error(w, "no error", http.StatusInternalServerError)
			return
		}
		// http.MaxBytesReader sets the status to 413 on overflow before
		// returning the error.
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
	})
	limit := int64(16)
	handler := withRequestBodyLimit(inner, limit)

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(strings.Repeat("a", 1024)))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !innerCalled {
		t.Fatalf("inner handler was not called; middleware short-circuited")
	}
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", w.Code)
	}
}

func TestWithRequestBodyLimit_NilBodyIsPassedThrough(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := withRequestBodyLimit(inner, 1024)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
}
