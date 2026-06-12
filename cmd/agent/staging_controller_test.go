package main

import "testing"

// allowedScope permits the fixture PR target used across these tests.
func allowedScope() *stagingScope {
	return newStagingScope(stagingScopeEntry{Owner: "kit", Repo: "hirdforge", Branch: "staging"})
}

func prRunInput(required ...string) stagingRunInput {
	return stagingRunInput{
		FinalContent:      "Done. Opened the PR: http://gitea.local/kit/hirdforge/pulls/7",
		TerminationReason: terminationCompleted,
		HeadSHA:           "deadbeef",
		BaseBranch:        "staging",
		RequiredChecks:    required,
	}
}

func TestDryRunControllerNoopWhenFlagOff(t *testing.T) {
	t.Setenv(stagingDryRunGateEnvVar, "") // off
	logs := captureLogs(t)

	cand, would, logged := runStagingDryRun(prRunInput("ci/build"), &fakeChecksClient{status: green("ci/build")}, allowedScope())
	if would || logged {
		t.Errorf("flag off must be a silent no-op; would=%v logged=%v", would, logged)
	}
	if cand.Outcome.Kind != "" {
		t.Errorf("expected zero candidate when off, got %+v", cand)
	}
	if findLogEntry(logs, stagingDryRunGateMsg) != nil {
		t.Error("no staging_dry_run_gate event should be emitted while the flag is off")
	}
}

func TestDryRunControllerGreenChecksWouldMerge(t *testing.T) {
	t.Setenv(stagingDryRunGateEnvVar, "1")
	logs := captureLogs(t)

	cand, would, logged := runStagingDryRun(
		prRunInput("ci/build", "ci/test"),
		&fakeChecksClient{status: green("ci/build", "ci/test")},
		allowedScope(),
	)
	if !logged {
		t.Fatal("expected a dry-run decision to be logged")
	}
	if !would {
		t.Error("eligible PR + green checks + in scope should be would_merge=true")
	}
	if cand.Outcome.Kind != outcomePR || !cand.ChecksGreen {
		t.Errorf("candidate should be a PR with checks green: %+v", cand)
	}
	entry := findLogEntry(logs, stagingDryRunGateMsg)
	if entry == nil {
		t.Fatal("expected staging_dry_run_gate entry")
	}
	for k, want := range map[string]interface{}{
		"dry_run": true, "would_merge": true, "checks_green": true, "scope_allowed": true, "eligible": true,
	} {
		if got, _ := entry[k].(bool); got != want.(bool) {
			t.Errorf("log field %q = %v, want %v", k, entry[k], want)
		}
	}
	if entry["pr_number"] == nil || entry["pr_url"] == nil {
		t.Errorf("audit log should carry pr ref fields; entry: %v", entry)
	}
}

func TestDryRunControllerRedChecksDoesNotMerge(t *testing.T) {
	t.Setenv(stagingDryRunGateEnvVar, "1")
	logs := captureLogs(t)

	status := combinedStatus{State: "failure", Statuses: []commitStatus{
		{Context: "ci/build", State: "success"},
		{Context: "ci/test", State: "failure"},
	}}
	_, would, _ := runStagingDryRun(prRunInput("ci/build", "ci/test"), &fakeChecksClient{status: status}, allowedScope())
	if would {
		t.Error("red checks must not be would_merge")
	}
	entry := findLogEntry(logs, stagingDryRunGateMsg)
	if cg, _ := entry["checks_green"].(bool); cg {
		t.Errorf("expected checks_green=false; entry: %v", entry)
	}
}

func TestDryRunControllerMissingChecksDoesNotMerge(t *testing.T) {
	t.Setenv(stagingDryRunGateEnvVar, "1")
	captureLogs(t)
	// Require ci/test but the status set omits it.
	_, would, _ := runStagingDryRun(prRunInput("ci/build", "ci/test"), &fakeChecksClient{status: green("ci/build")}, allowedScope())
	if would {
		t.Error("missing required check must not be would_merge")
	}
}

func TestDryRunControllerNonPROutcomesDoNotMerge(t *testing.T) {
	t.Setenv(stagingDryRunGateEnvVar, "1")
	captureLogs(t)
	greenClient := &fakeChecksClient{status: green("ci/build")}

	cases := []struct {
		name    string
		content string
		reason  terminationReason
	}{
		{"failed", "FAILED: the build does not compile", terminationNoActionableOutput},
		{"noop", "NOOP: nothing to change", terminationCompleted},
		{"unknown", "I looked around but have nothing to report.", terminationCompleted},
		{"degraded_termination", "Opened http://gitea.local/kit/hirdforge/pulls/7", terminationMaxTurns},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := stagingRunInput{
				FinalContent:      c.content,
				TerminationReason: c.reason,
				HeadSHA:           "deadbeef",
				BaseBranch:        "staging",
				RequiredChecks:    []string{"ci/build"},
			}
			_, would, logged := runStagingDryRun(in, greenClient, allowedScope())
			if !logged {
				t.Fatal("expected a dry-run decision to be logged")
			}
			if would {
				t.Errorf("%s outcome must not be would_merge", c.name)
			}
		})
	}
}

func TestDryRunControllerScopeRestriction(t *testing.T) {
	t.Setenv(stagingDryRunGateEnvVar, "1")
	captureLogs(t)
	greenClient := &fakeChecksClient{status: green("ci/build")}

	// Eligible + green, but the base branch is not in scope.
	in := prRunInput("ci/build")
	in.BaseBranch = "main" // allow-list only has staging
	_, would, _ := runStagingDryRun(in, greenClient, allowedScope())
	if would {
		t.Error("out-of-scope base branch must not be would_merge")
	}

	// A different repo, also out of scope.
	in2 := stagingRunInput{
		FinalContent:      "Opened http://gitea.local/other/repo/pulls/3",
		TerminationReason: terminationCompleted,
		HeadSHA:           "cafe",
		BaseBranch:        "staging",
		RequiredChecks:    []string{"ci/build"},
	}
	_, would2, _ := runStagingDryRun(in2, greenClient, allowedScope())
	if would2 {
		t.Error("out-of-scope repo must not be would_merge")
	}

	// nil scope allows nothing.
	_, would3, _ := runStagingDryRun(prRunInput("ci/build"), greenClient, nil)
	if would3 {
		t.Error("nil scope must allow nothing")
	}
}

func TestDryRunControllerNeverEmitsAutoMergeEvent(t *testing.T) {
	t.Setenv(stagingDryRunGateEnvVar, "1")
	t.Setenv(stagingAutoMergeEnvVar, "1") // even if auto-merge were on, the controller must not act on it
	logs := captureLogs(t)

	runStagingDryRun(prRunInput("ci/build"), &fakeChecksClient{status: green("ci/build")}, allowedScope())

	// The controller is dry-run only: it must never emit the real-merge event.
	if findLogEntry(logs, stagingAutoMergeMsg) != nil {
		t.Error("dry-run controller must not emit a staging_auto_merge event")
	}
	if findLogEntry(logs, stagingDryRunGateMsg) == nil {
		t.Error("expected the dry-run decision to be logged")
	}
}

func TestStagingScopeAllows(t *testing.T) {
	s := newStagingScope(stagingScopeEntry{Owner: "kit", Repo: "hirdforge", Branch: "staging"})
	if !s.allows("kit", "hirdforge", "staging") {
		t.Error("expected exact match to be allowed")
	}
	if !s.allows("KIT", "Hirdforge", "STAGING") {
		t.Error("expected case-insensitive match")
	}
	if s.allows("kit", "hirdforge", "main") {
		t.Error("different branch must be refused")
	}
	if s.allows("kit", "other", "staging") {
		t.Error("different repo must be refused")
	}
	var nilScope *stagingScope
	if nilScope.allows("kit", "hirdforge", "staging") {
		t.Error("nil scope must allow nothing")
	}
}
