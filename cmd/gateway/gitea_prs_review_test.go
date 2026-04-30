package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newGiteaPRTestMux(t *testing.T, upstream http.HandlerFunc) (*http.ServeMux, func()) {
	t.Helper()
	server := httptest.NewServer(upstream)
	mux := http.NewServeMux()
	registerGiteaPRRoutes(mux, &gateway{
		giteaURL:   server.URL,
		giteaToken: "test-token",
	}, server.Client())
	return mux, server.Close
}

func TestGiteaPRFilesReturnsUpstreamJSON(t *testing.T) {
	const body = `[{"filename":"cmd/gateway/main.go","status":"modified","additions":10,"deletions":2,"patch":"@@ -1 +1 @@"}]`
	mux, cleanup := newGiteaPRTestMux(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method=%s want GET", r.Method)
		}
		if r.URL.Path != "/api/v1/repos/kit/hirdforge/pulls/7/files" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/gitea/prs/kit/hirdforge/7/files", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != body {
		t.Fatalf("body=%s want %s", rec.Body.String(), body)
	}
}

func TestGiteaPRFilesDiffCommentsMethodGuards(t *testing.T) {
	mux, cleanup := newGiteaPRTestMux(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("upstream should not be called for method guard failures")
	})
	defer cleanup()

	tests := []struct {
		name string
		url  string
	}{
		{name: "files post", url: "/api/v1/gitea/prs/kit/hirdforge/7/files"},
		{name: "diff post", url: "/api/v1/gitea/prs/kit/hirdforge/7/diff?file=cmd%2Fgateway%2Fmain.go"},
		{name: "comments post", url: "/api/v1/gitea/prs/kit/hirdforge/7/comments"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.url, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestGiteaPRDiffMissingOrEmptyFileParam(t *testing.T) {
	mux, cleanup := newGiteaPRTestMux(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("upstream should not be called when file query is missing")
	})
	defer cleanup()

	tests := []string{
		"/api/v1/gitea/prs/kit/hirdforge/7/diff",
		"/api/v1/gitea/prs/kit/hirdforge/7/diff?file=",
	}

	for _, url := range tests {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("url=%s status=%d body=%s", url, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "file query parameter is required") {
			t.Fatalf("url=%s body=%s", url, rec.Body.String())
		}
	}
}

func TestGiteaPRDiffUnknownFileReturns404(t *testing.T) {
	mux, cleanup := newGiteaPRTestMux(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"filename":"README.md","patch":"..."},{"filename":"cmd/gateway/main.go","patch":"..."}]`))
	})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/gitea/prs/kit/hirdforge/7/diff?file=missing.go", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "file not found in pull request") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestGiteaPRDiffReturnsMatchingEntry(t *testing.T) {
	mux, cleanup := newGiteaPRTestMux(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"filename":"README.md","patch":"..."},{"filename":"cmd/gateway/main.go","status":"modified","additions":4,"deletions":1,"patch":"@@ -1 +1 @@"}]`))
	})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/gitea/prs/kit/hirdforge/7/diff?file=cmd%2Fgateway%2Fmain.go", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out["filename"] != "cmd/gateway/main.go" {
		t.Fatalf("filename=%#v", out["filename"])
	}
}

func TestGiteaPRCommentsAggregatesReviews(t *testing.T) {
	mux, cleanup := newGiteaPRTestMux(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/repos/kit/hirdforge/pulls/7/reviews":
			_, _ = w.Write([]byte(`[{"id":11},{"id":12}]`))
		case "/api/v1/repos/kit/hirdforge/pulls/7/reviews/11/comments":
			_, _ = w.Write([]byte(`[{"path":"a.go","line":10,"original_line":8,"body":"nit","user":{"login":"alice"},"created_at":"2026-04-29T00:00:00Z"}]`))
		case "/api/v1/repos/kit/hirdforge/pulls/7/reviews/12/comments":
			_, _ = w.Write([]byte(`[{"path":"a.go","line":10,"body":"follow-up","user":{"login":"bob"},"created_at":"2026-04-29T00:01:00Z"},{"path":"b.go","line":7,"body":"blocker","user":{"login":"bob"},"created_at":"2026-04-29T00:02:00Z"}]`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/gitea/prs/kit/hirdforge/7/comments", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Comments []map[string]interface{} `json:"comments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out.Comments) != 3 {
		t.Fatalf("comments=%d want 3", len(out.Comments))
	}
	if out.Comments[0]["review_id"] != float64(11) {
		t.Fatalf("first review_id=%#v", out.Comments[0]["review_id"])
	}
	if out.Comments[0]["original_line"] != float64(8) {
		t.Fatalf("first original_line=%#v", out.Comments[0]["original_line"])
	}
	if out.Comments[2]["path"] != "b.go" {
		t.Fatalf("last path=%#v", out.Comments[2]["path"])
	}
}

func TestGiteaPRReviewValidPostForwardsBody(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	mux, cleanup := newGiteaPRTestMux(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":99,"state":"APPROVED"}`))
	})
	defer cleanup()

	body := `{"event":"COMMENT","body":"Looks good","comments":[{"path":"a.go","new_position":3,"body":"nit"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/gitea/prs/kit/hirdforge/7/review", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method=%s", gotMethod)
	}
	if gotPath != "/api/v1/repos/kit/hirdforge/pulls/7/reviews" {
		t.Fatalf("path=%s", gotPath)
	}
	if strings.TrimSpace(string(gotBody)) != body {
		t.Fatalf("body=%s want %s", string(gotBody), body)
	}
}

func TestGiteaPRReviewInvalidEventReturns400(t *testing.T) {
	mux, cleanup := newGiteaPRTestMux(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("upstream should not be called for invalid event")
	})
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/gitea/prs/kit/hirdforge/7/review", bytes.NewBufferString(`{"event":"MERGE"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "event must be one of APPROVED, REQUEST_CHANGES, COMMENT") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestGiteaPRReviewMethodGuard(t *testing.T) {
	mux, cleanup := newGiteaPRTestMux(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("upstream should not be called for method guard failures")
	})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/gitea/prs/kit/hirdforge/7/review", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
