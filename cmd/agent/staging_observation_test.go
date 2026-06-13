package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeHeadResolver is an in-memory prHeadResolver for the observation tests.
type fakeHeadResolver struct {
	ref   string
	err   error
	calls int
}

func (f *fakeHeadResolver) HeadRef(owner, repo string, index int) (string, error) {
	f.calls++
	return f.ref, f.err
}

// validObserver builds a fully-configured observer with injected fakes.
func validObserver(checks checksClient, heads prHeadResolver) *stagingDryRunObserver {
	return &stagingDryRunObserver{
		checks:         checks,
		heads:          heads,
		scope:          newStagingScope(stagingScopeEntry{Owner: "kit", Repo: "hirdforge", Branch: "staging"}),
		requiredChecks: []string{"ci/build"},
		baseBranch:     "staging",
	}
}

const prCompletion = "Done. Opened the PR: http://gitea.local/kit/hirdforge/pulls/7"

// Case 1: gate disabled -> no dry-run, no log, behavior unchanged.
func TestObserveDryRunDisabledIsNoop(t *testing.T) {
	t.Setenv(stagingDryRunGateEnvVar, "") // off
	logs := captureLogs(t)
	heads := &fakeHeadResolver{ref: "deadbeef"}
	obs := validObserver(&fakeChecksClient{status: green("ci/build")}, heads)

	observeCompletedSessionDryRun(obs, "sess-1", "task-1", prCompletion)

	if findLogEntry(logs, stagingDryRunGateMsg) != nil {
		t.Error("no staging_dry_run_gate event should be emitted while the gate is off")
	}
	if heads.calls != 0 {
		t.Error("must not touch any source while the gate is off")
	}
}

// Case 2: gate enabled, invalid candidate (no PR ref) -> fail_closed.
func TestObserveDryRunInvalidCandidateFailsClosed(t *testing.T) {
	t.Setenv(stagingDryRunGateEnvVar, "1")
	logs := captureLogs(t)
	obs := validObserver(&fakeChecksClient{status: green("ci/build")}, &fakeHeadResolver{ref: "deadbeef"})

	observeCompletedSessionDryRun(obs, "sess-2", "task-2", "NOOP: nothing to do here")

	entry := findLogEntry(logs, stagingDryRunGateMsg)
	if entry == nil || entry["decision"] != "fail_closed" {
		t.Fatalf("expected fail_closed decision; entry: %v", entry)
	}
	if entry["session_id"] != "sess-2" {
		t.Errorf("expected session_id in observation; entry: %v", entry)
	}
}

// Case 3: gate enabled, missing checks source/config -> fail_closed.
func TestObserveDryRunMissingConfigFailsClosed(t *testing.T) {
	t.Setenv(stagingDryRunGateEnvVar, "1")
	logs := captureLogs(t)

	// nil observer (nothing configured)
	observeCompletedSessionDryRun(nil, "sess-3", "task-3", prCompletion)
	entry := findLogEntry(logs, stagingDryRunGateMsg)
	if entry == nil || entry["decision"] != "fail_closed" {
		t.Fatalf("nil observer must fail closed; entry: %v", entry)
	}

	// partially-configured observer (no checks client) also fails closed
	logs2 := captureLogs(t)
	partial := &stagingDryRunObserver{
		heads:          &fakeHeadResolver{ref: "x"},
		scope:          newStagingScope(stagingScopeEntry{Owner: "kit", Repo: "hirdforge", Branch: "staging"}),
		requiredChecks: []string{"ci/build"},
		baseBranch:     "staging",
	}
	observeCompletedSessionDryRun(partial, "sess-3b", "task-3b", prCompletion)
	if e := findLogEntry(logs2, stagingDryRunGateMsg); e == nil || e["decision"] != "fail_closed" {
		t.Fatalf("partial config must fail closed; entry: %v", e)
	}
}

// Unresolvable head ref -> fail_closed (and does not query checks).
func TestObserveDryRunHeadResolveErrorFailsClosed(t *testing.T) {
	t.Setenv(stagingDryRunGateEnvVar, "1")
	logs := captureLogs(t)
	heads := &fakeHeadResolver{err: errors.New("pr lookup failed")}
	obs := validObserver(&fakeChecksClient{status: green("ci/build")}, heads)

	observeCompletedSessionDryRun(obs, "sess-4", "task-4", prCompletion)

	entry := findLogEntry(logs, stagingDryRunGateMsg)
	if entry == nil || entry["decision"] != "fail_closed" {
		t.Fatalf("head resolve failure must fail closed; entry: %v", entry)
	}
	// Reason must not leak upstream error detail.
	if r, _ := entry["reason"].(string); r != "could not resolve PR head ref" {
		t.Errorf("unexpected reason (must be generic): %q", r)
	}
}

// Case 4: gate enabled, valid candidate, passing checks -> would_allow, no merge.
func TestObserveDryRunWouldAllow(t *testing.T) {
	t.Setenv(stagingDryRunGateEnvVar, "1")
	t.Setenv(stagingAutoMergeEnvVar, "1") // even if this were on, observation must not act on it
	logs := captureLogs(t)
	obs := validObserver(&fakeChecksClient{status: green("ci/build")}, &fakeHeadResolver{ref: "deadbeef"})

	observeCompletedSessionDryRun(obs, "sess-5", "task-5", prCompletion)

	entry := findLogEntry(logs, stagingDryRunGateMsg)
	if entry == nil {
		t.Fatal("expected a dry-run observation")
	}
	if entry["decision"] != "would_allow" {
		t.Errorf("decision = %v, want would_allow; entry: %v", entry["decision"], entry)
	}
	if m, _ := entry["would_merge"].(bool); !m {
		t.Errorf("would_merge should be true; entry: %v", entry)
	}
	// Case 6 regression: the real merge event must never be emitted.
	if findLogEntry(logs, stagingAutoMergeMsg) != nil {
		t.Error("observation must never emit a staging_auto_merge event")
	}
}

// Case 5: gate enabled, valid candidate, failing checks -> would_block.
func TestObserveDryRunWouldBlockOnRedChecks(t *testing.T) {
	t.Setenv(stagingDryRunGateEnvVar, "1")
	logs := captureLogs(t)
	red := combinedStatus{State: "failure", Statuses: []commitStatus{{Context: "ci/build", State: "failure"}}}
	obs := validObserver(&fakeChecksClient{status: red}, &fakeHeadResolver{ref: "deadbeef"})

	observeCompletedSessionDryRun(obs, "sess-6", "task-6", prCompletion)

	entry := findLogEntry(logs, stagingDryRunGateMsg)
	if entry == nil || entry["decision"] != "would_block" {
		t.Fatalf("red checks must be would_block; entry: %v", entry)
	}
	if cg, _ := entry["checks_green"].(bool); cg {
		t.Errorf("checks_green should be false; entry: %v", entry)
	}
	if findLogEntry(logs, stagingAutoMergeMsg) != nil {
		t.Error("observation must never emit a staging_auto_merge event")
	}
}

func TestNewStagingDryRunObserverFromEnv(t *testing.T) {
	// Fully configured -> observer built.
	t.Setenv("STAGING_DRY_RUN_SCOPE", "kit/hirdforge/staging")
	t.Setenv("STAGING_DRY_RUN_REQUIRED_CHECKS", "ci/build, ci/test")
	t.Setenv("STAGING_DRY_RUN_BASE_BRANCH", "staging")
	obs, ok := newStagingDryRunObserverFromEnv("http://gitea.local", "tok")
	if !ok || !obs.complete() {
		t.Fatalf("expected a complete observer; ok=%v", ok)
	}
	if len(obs.requiredChecks) != 2 || obs.baseBranch != "staging" {
		t.Errorf("config parsed wrong: %+v", obs)
	}
	if !obs.scope.allows("kit", "hirdforge", "staging") {
		t.Error("scope should allow the configured target")
	}

	// Missing token -> fail closed (nil, false).
	if _, ok := newStagingDryRunObserverFromEnv("http://gitea.local", ""); ok {
		t.Error("missing token must yield no observer")
	}
	// Missing scope -> fail closed.
	t.Setenv("STAGING_DRY_RUN_SCOPE", "")
	if _, ok := newStagingDryRunObserverFromEnv("http://gitea.local", "tok"); ok {
		t.Error("missing scope must yield no observer")
	}
}

func TestGiteaPRHeadResolverAdapter(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"head":{"sha":"abc123","ref":"fix/widget"}}`))
	}))
	defer srv.Close()

	ref, err := newGiteaPRHeadResolver(srv.URL, "tok").HeadRef("kit", "hirdforge", 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref != "abc123" {
		t.Errorf("head ref = %q, want abc123", ref)
	}
	if gotPath != "/api/v1/repos/kit/hirdforge/pulls/7" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "token tok" {
		t.Errorf("auth = %q, want injected token", gotAuth)
	}

	// Error paths fail closed.
	if _, err := newGiteaPRHeadResolver(srv.URL, "tok").HeadRef("kit", "", 7); err == nil {
		t.Error("incomplete ref should error")
	}
	if _, err := newGiteaPRHeadResolver("", "").HeadRef("kit", "hirdforge", 7); err == nil {
		t.Error("unconfigured resolver should error")
	}
}

func TestGiteaPRHeadResolverEscapesPath(t *testing.T) {
	var gotURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.RequestURI
		_, _ = w.Write([]byte(`{"head":{"sha":"abc"}}`))
	}))
	defer srv.Close()

	// Owner/repo containing characters that must be path-escaped.
	if _, err := newGiteaPRHeadResolver(srv.URL, "tok").HeadRef("weird owner", "re/po", 7); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/api/v1/repos/weird%20owner/re%2Fpo/pulls/7"
	if gotURI != want {
		t.Errorf("request URI = %q, want escaped %q", gotURI, want)
	}
}
