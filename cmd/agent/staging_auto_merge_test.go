package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeMerger is a test stagingMerger. It records calls and never touches a real
// backend, so policy/controller tests merge nothing real.
type fakeMerger struct {
	calls   int
	lastRef prRef
	err     error
}

func (f *fakeMerger) MergePR(ref prRef) error {
	f.calls++
	f.lastRef = ref
	return f.err
}

func eligiblePROutcome() runOutcome {
	return runOutcome{
		Kind:              outcomePR,
		PRURL:             "http://gitea.local/kit/hirdforge/pulls/7",
		PRNumber:          7,
		TerminationReason: terminationCompleted,
	}
}

func TestAutoMergeMergesWhenEnabledAndEligible(t *testing.T) {
	t.Setenv(stagingAutoMergeEnvVar, "1")
	logs := captureLogs(t)
	merger := &fakeMerger{}

	merged, err := maybeAutoMergeStaging(eligiblePROutcome(), merger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !merged {
		t.Error("expected merged=true for an eligible outcome with the flag on")
	}
	if merger.calls != 1 {
		t.Errorf("merger called %d times, want 1", merger.calls)
	}
	if merger.lastRef != (prRef{Owner: "kit", Repo: "hirdforge", Index: 7}) {
		t.Errorf("merger got ref %+v, want kit/hirdforge#7", merger.lastRef)
	}
	entry := findLogEntry(logs, stagingAutoMergeMsg)
	if entry == nil {
		t.Fatal("expected a staging_auto_merge log entry")
	}
	if merged, _ := entry["merged"].(bool); !merged {
		t.Errorf("log should record merged=true; entry: %v", entry)
	}
}

func TestAutoMergeDoesNotMergeWhenFlagDisabled(t *testing.T) {
	t.Setenv(stagingAutoMergeEnvVar, "") // off (default)
	if stagingAutoMergeEnabled() {
		t.Fatal("flag should be off when unset/empty")
	}
	logs := captureLogs(t)
	merger := &fakeMerger{}

	merged, err := maybeAutoMergeStaging(eligiblePROutcome(), merger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if merged {
		t.Error("must not merge when the flag is disabled")
	}
	if merger.calls != 0 {
		t.Errorf("merger must not be called when the flag is off; calls=%d", merger.calls)
	}
	if findLogEntry(logs, stagingAutoMergeMsg) != nil {
		t.Error("no staging_auto_merge event should be logged while the flag is off")
	}
}

func TestAutoMergeNeverMergesIneligibleOutcomes(t *testing.T) {
	t.Setenv(stagingAutoMergeEnvVar, "1")
	captureLogs(t) // silence decision logs; this test asserts on merge behavior
	ineligible := []struct {
		name    string
		outcome runOutcome
	}{
		{"failed", runOutcome{Kind: outcomeFailed, FailedReason: "broke", TerminationReason: terminationNoActionableOutput}},
		{"noop", runOutcome{Kind: outcomeNoop, NoopReason: "nothing", TerminationReason: terminationCompleted}},
		{"unknown", runOutcome{Kind: outcomeUnknown, TerminationReason: terminationCompleted}},
		{"pr_degraded_termination", runOutcome{Kind: outcomePR, PRURL: "http://gitea.local/kit/hirdforge/pulls/7", PRNumber: 7, TerminationReason: terminationMaxTurns}},
	}
	for _, c := range ineligible {
		t.Run(c.name, func(t *testing.T) {
			merger := &fakeMerger{}
			merged, err := maybeAutoMergeStaging(c.outcome, merger)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if merged || merger.calls != 0 {
				t.Errorf("ineligible outcome must not merge (merged=%v calls=%d)", merged, merger.calls)
			}
		})
	}
}

func TestAutoMergeFailsClosedOnMissingRef(t *testing.T) {
	t.Setenv(stagingAutoMergeEnvVar, "1")
	captureLogs(t)
	merger := &fakeMerger{}
	// Eligible by the gate (PR + completed + a PR number) but no URL, so no
	// owner/repo can be derived: must fail closed without calling the merger.
	outcome := runOutcome{Kind: outcomePR, PRNumber: 7, TerminationReason: terminationCompleted}

	merged, err := maybeAutoMergeStaging(outcome, merger)
	if merged {
		t.Error("must not merge when no concrete PR ref can be derived")
	}
	if !errors.Is(err, errStagingMergeNoRef) {
		t.Errorf("expected errStagingMergeNoRef, got %v", err)
	}
	if merger.calls != 0 {
		t.Errorf("merger must not be called when ref is missing; calls=%d", merger.calls)
	}
}

func TestAutoMergeReportsMergeErrorWithoutPanic(t *testing.T) {
	t.Setenv(stagingAutoMergeEnvVar, "1")
	logs := captureLogs(t)
	merger := &fakeMerger{err: errors.New("gitea rejected merge: checks pending")}

	merged, err := maybeAutoMergeStaging(eligiblePROutcome(), merger)
	if merged {
		t.Error("merge reported success despite merger error")
	}
	if err == nil || !strings.Contains(err.Error(), "checks pending") {
		t.Errorf("expected the merge error to be returned, got %v", err)
	}
	entry := findLogEntry(logs, stagingAutoMergeMsg)
	if entry == nil {
		t.Fatal("expected a staging_auto_merge log entry for the failed merge")
	}
	if e, _ := entry["error"].(string); !strings.Contains(e, "checks pending") {
		t.Errorf("log should record the merge error; entry: %v", entry)
	}
	if m, _ := entry["merged"].(bool); m {
		t.Errorf("log should record merged=false on error; entry: %v", entry)
	}
}

func TestAutoMergeNilMergerFailsClosed(t *testing.T) {
	t.Setenv(stagingAutoMergeEnvVar, "1")
	captureLogs(t)
	merged, err := maybeAutoMergeStaging(eligiblePROutcome(), nil)
	if merged || !errors.Is(err, errStagingMergeNoMerger) {
		t.Errorf("nil merger must fail closed; merged=%v err=%v", merged, err)
	}
}

// TestDryRunGateStillWorksSeparately confirms the step-4 dry-run logging is
// independent of auto-merge: with the dry-run flag on and the auto-merge flag
// off, the dry-run decision is logged and nothing merges.
func TestDryRunGateStillWorksSeparately(t *testing.T) {
	t.Setenv(stagingDryRunGateEnvVar, "1")
	t.Setenv(stagingAutoMergeEnvVar, "") // auto-merge off
	logs := captureLogs(t)
	merger := &fakeMerger{}

	logStagingDryRunGate(eligiblePROutcome())
	merged, err := maybeAutoMergeStaging(eligiblePROutcome(), merger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if findLogEntry(logs, stagingDryRunGateMsg) == nil {
		t.Error("expected the dry-run gate to log independently")
	}
	if merged || merger.calls != 0 {
		t.Error("auto-merge must stay off while only the dry-run flag is on")
	}
	if findLogEntry(logs, stagingAutoMergeMsg) != nil {
		t.Error("no auto-merge event should be logged while auto-merge is off")
	}
}

// TestGiteaStagingMergerAdapter exercises the isolated real adapter against a
// fake HTTP server (httptest) — the only place a real merge call shape is tested.
// Nothing real is merged.
func TestGiteaStagingMergerAdapter(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := newGiteaStagingMerger(srv.URL, "tok123")
	if err := m.MergePR(prRef{Owner: "kit", Repo: "hirdforge", Index: 7}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/repos/kit/hirdforge/pulls/7/merge" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "token tok123" {
		t.Errorf("auth = %q, want injected token", gotAuth)
	}
	if !strings.Contains(gotBody, `"Do":"merge"`) {
		t.Errorf("body = %q, want merge style", gotBody)
	}
}

func TestGiteaStagingMergerAdapterErrors(t *testing.T) {
	// Non-2xx from gitea -> error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte("merge conflict"))
	}))
	defer srv.Close()
	m := newGiteaStagingMerger(srv.URL, "tok")
	if err := m.MergePR(prRef{Owner: "kit", Repo: "hirdforge", Index: 7}); err == nil {
		t.Error("expected an error for a non-2xx merge response")
	}

	// Incomplete ref -> error, no HTTP call needed.
	if err := m.MergePR(prRef{Owner: "kit", Index: 7}); err == nil {
		t.Error("expected an error for an incomplete PR ref")
	}

	// Unconfigured adapter (no token) -> error.
	unconfigured := newGiteaStagingMerger("http://gitea.local", "")
	if err := unconfigured.MergePR(prRef{Owner: "kit", Repo: "hirdforge", Index: 7}); err == nil {
		t.Error("expected an error for an unconfigured adapter")
	}
}

// findLogEntry returns the first captured log entry with the given msg key.
func findLogEntry(c *logCapture, msg string) map[string]interface{} {
	for _, e := range c.entries() {
		if e["msg"] == msg {
			return e
		}
	}
	return nil
}
