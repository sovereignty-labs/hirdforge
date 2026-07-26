package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestDoInferenceRequestRetriesTransientDrop proves the fix for O-149's operator
// symptom: a per-host llama-server that drops the socket (EOF/reset) under slot
// contention no longer kills the whole turn — the client retries the connection.
func TestDoInferenceRequestRetriesTransientDrop(t *testing.T) {
	old := inferenceRetryBaseDelay
	inferenceRetryBaseDelay = time.Millisecond
	defer func() { inferenceRetryBaseDelay = old }()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drop the socket without responding on the first two attempts, exactly
		// as a contended/bounced llama-server does; succeed on the third.
		if atomic.AddInt32(&calls, 1) < 3 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("test server does not support hijacking")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			conn.Close()
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	resp, err := doInferenceRequest(context.Background(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL, nil)
	})
	if err != nil {
		t.Fatalf("expected success after transient drops, got error: %v", err)
	}
	defer resp.Body.Close()
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected 3 attempts (2 drops + 1 success), got %d", got)
	}
}

// TestDoInferenceRequestGivesUpAfterMax proves the retry is BOUNDED and fails
// loudly rather than looping forever when the fabric is genuinely down.
func TestDoInferenceRequestGivesUpAfterMax(t *testing.T) {
	old := inferenceRetryBaseDelay
	inferenceRetryBaseDelay = time.Millisecond
	defer func() { inferenceRetryBaseDelay = old }()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("test server does not support hijacking")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	}))
	defer srv.Close()

	_, err := doInferenceRequest(context.Background(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL, nil)
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if got := atomic.LoadInt32(&calls); got != maxInferenceConnRetries+1 {
		t.Fatalf("expected %d attempts, got %d", maxInferenceConnRetries+1, got)
	}
}

// TestDoInferenceRequestNoRetryOnSuccess proves a healthy fabric pays no retry
// tax: one attempt, no added latency.
func TestDoInferenceRequestNoRetryOnSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := doInferenceRequest(context.Background(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL, nil)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 attempt on success, got %d", got)
	}
}
