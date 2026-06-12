package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeChecksClient is an in-memory checksClient for the evaluator tests; it
// never touches a real backend.
type fakeChecksClient struct {
	status combinedStatus
	err    error
}

func (f *fakeChecksClient) CombinedStatus(owner, repo, sha string) (combinedStatus, error) {
	return f.status, f.err
}

func green(contexts ...string) combinedStatus {
	cs := combinedStatus{State: "success"}
	for _, c := range contexts {
		cs.Statuses = append(cs.Statuses, commitStatus{Context: c, State: "success"})
	}
	return cs
}

func TestStagingChecksGreen(t *testing.T) {
	required := []string{"ci/build", "ci/test"}

	cases := []struct {
		name       string
		client     checksClient
		wantGreen  bool
		summarySub string
	}{
		{
			name:       "all required green",
			client:     &fakeChecksClient{status: green("ci/build", "ci/test")},
			wantGreen:  true,
			summarySub: "2/2 required green",
		},
		{
			name: "pending required context",
			client: &fakeChecksClient{status: combinedStatus{State: "pending", Statuses: []commitStatus{
				{Context: "ci/build", State: "success"},
				{Context: "ci/test", State: "pending"},
			}}},
			wantGreen:  false,
			summarySub: "ci/test=pending",
		},
		{
			name: "failed required context",
			client: &fakeChecksClient{status: combinedStatus{State: "failure", Statuses: []commitStatus{
				{Context: "ci/build", State: "success"},
				{Context: "ci/test", State: "failure"},
			}}},
			wantGreen:  false,
			summarySub: "ci/test=failure",
		},
		{
			name: "errored required context",
			client: &fakeChecksClient{status: combinedStatus{State: "error", Statuses: []commitStatus{
				{Context: "ci/build", State: "success"},
				{Context: "ci/test", State: "error"},
			}}},
			wantGreen:  false,
			summarySub: "ci/test=error",
		},
		{
			name:       "missing required context",
			client:     &fakeChecksClient{status: green("ci/build")}, // ci/test absent
			wantGreen:  false,
			summarySub: "missing:ci/test",
		},
		{
			name:       "api error",
			client:     &fakeChecksClient{err: errors.New("boom")},
			wantGreen:  false,
			summarySub: "api error",
		},
		{
			name:       "empty statuses",
			client:     &fakeChecksClient{status: combinedStatus{State: "success"}},
			wantGreen:  false,
			summarySub: "no statuses reported",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			green, summary := stagingChecksGreen(c.client, "kit", "hirdforge", "deadbeef", required)
			if green != c.wantGreen {
				t.Errorf("checksGreen = %v, want %v (summary: %q)", green, c.wantGreen, summary)
			}
			if c.summarySub != "" && !strings.Contains(summary, c.summarySub) {
				t.Errorf("summary = %q, want it to contain %q", summary, c.summarySub)
			}
			if summary == "" {
				t.Error("summary must never be empty")
			}
		})
	}
}

func TestStagingChecksGreenFailClosedEdges(t *testing.T) {
	// Nil client fails closed.
	if g, s := stagingChecksGreen(nil, "kit", "hirdforge", "sha", []string{"ci/build"}); g || !strings.Contains(s, "no client") {
		t.Errorf("nil client must fail closed; green=%v summary=%q", g, s)
	}
	// No required contexts configured fails closed (cannot assert "all green").
	if g, s := stagingChecksGreen(&fakeChecksClient{status: green("ci/build")}, "kit", "hirdforge", "sha", nil); g || !strings.Contains(s, "no required") {
		t.Errorf("empty required set must fail closed; green=%v summary=%q", g, s)
	}
}

// TestGiteaChecksClientAdapter exercises the isolated real adapter against a fake
// HTTP server. Nothing real is queried.
func TestGiteaChecksClientAdapter(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"success","statuses":[{"context":"ci/build","status":"success"},{"context":"ci/test","status":"pending"}]}`))
	}))
	defer srv.Close()

	c := newGiteaChecksClient(srv.URL, "tok123")
	cs, err := c.CombinedStatus("kit", "hirdforge", "deadbeef")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v1/repos/kit/hirdforge/commits/deadbeef/status" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "token tok123" {
		t.Errorf("auth = %q, want injected token", gotAuth)
	}
	if len(cs.Statuses) != 2 || cs.Statuses[0].Context != "ci/build" || cs.Statuses[1].State != "pending" {
		t.Errorf("parsed statuses wrong: %+v", cs.Statuses)
	}

	// End-to-end through the evaluator: ci/test pending -> not green.
	if g, summary := stagingChecksGreen(c, "kit", "hirdforge", "deadbeef", []string{"ci/build", "ci/test"}); g {
		t.Errorf("expected not green via real adapter; summary=%q", summary)
	}
}

func TestGiteaChecksClientAdapterErrors(t *testing.T) {
	// Non-2xx -> error (callers fail closed).
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("no such commit"))
	}))
	defer bad.Close()
	if _, err := newGiteaChecksClient(bad.URL, "tok").CombinedStatus("kit", "hirdforge", "sha"); err == nil {
		t.Error("expected error for non-2xx response")
	}

	// Malformed body -> error.
	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer malformed.Close()
	mc := newGiteaChecksClient(malformed.URL, "tok")
	if _, err := mc.CombinedStatus("kit", "hirdforge", "sha"); err == nil {
		t.Error("expected error for malformed response")
	}
	// And via the helper, a malformed response fails closed.
	if g, s := stagingChecksGreen(mc, "kit", "hirdforge", "sha", []string{"ci/build"}); g || !strings.Contains(s, "api error") {
		t.Errorf("malformed response must fail closed; green=%v summary=%q", g, s)
	}

	// Incomplete ref -> error, no HTTP call.
	if _, err := newGiteaChecksClient("http://gitea.local", "tok").CombinedStatus("kit", "", "sha"); err == nil {
		t.Error("expected error for incomplete ref")
	}
	// Unconfigured adapter -> error.
	if _, err := newGiteaChecksClient("", "").CombinedStatus("kit", "hirdforge", "sha"); err == nil {
		t.Error("expected error for unconfigured adapter")
	}
}
