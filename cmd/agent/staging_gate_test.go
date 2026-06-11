package main

import "testing"

func prOutcome(reason terminationReason) runOutcome {
	return runOutcome{
		Kind:              outcomePR,
		PRURL:             "http://gitea.local/kit/hirdforge/pulls/7",
		PRNumber:          7,
		TerminationReason: reason,
	}
}

func TestEvaluateStagingGate(t *testing.T) {
	cases := []struct {
		name         string
		outcome      runOutcome
		wantEligible bool
	}{
		{
			name:         "eligible_pr_url_completed",
			outcome:      runOutcome{Kind: outcomePR, PRURL: "http://gitea.local/kit/hirdforge/pulls/7", TerminationReason: terminationCompleted},
			wantEligible: true,
		},
		{
			name:         "eligible_pr_number_completed",
			outcome:      runOutcome{Kind: outcomePR, PRNumber: 7, TerminationReason: terminationCompleted},
			wantEligible: true,
		},
		{
			name:         "ineligible_failed",
			outcome:      runOutcome{Kind: outcomeFailed, FailedReason: "build broke", TerminationReason: terminationNoActionableOutput},
			wantEligible: false,
		},
		{
			name:         "ineligible_noop",
			outcome:      runOutcome{Kind: outcomeNoop, NoopReason: "already done", TerminationReason: terminationCompleted},
			wantEligible: false,
		},
		{
			name:         "ineligible_unknown",
			outcome:      runOutcome{Kind: outcomeUnknown, TerminationReason: terminationCompleted},
			wantEligible: false,
		},
		{
			name:         "ineligible_pr_degraded_termination",
			outcome:      prOutcome(terminationMaxTurns),
			wantEligible: false,
		},
		{
			name:         "ineligible_pr_missing_ref",
			outcome:      runOutcome{Kind: outcomePR, TerminationReason: terminationCompleted},
			wantEligible: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := evaluateStagingGate(c.outcome)
			if d.Eligible != c.wantEligible {
				t.Errorf("evaluateStagingGate(%+v) eligible = %v, want %v (reason: %q)",
					c.outcome, d.Eligible, c.wantEligible, d.Reason)
			}
			if d.Reason == "" {
				t.Error("decision reason must never be empty")
			}
			// The decision must carry the outcome context unchanged.
			if d.Kind != c.outcome.Kind || d.TerminationReason != c.outcome.TerminationReason {
				t.Errorf("decision lost outcome context: %+v", d)
			}
		})
	}
}

func TestStagingDryRunGateDefaultsOff(t *testing.T) {
	// An empty/unset value must mean off.
	t.Setenv(stagingDryRunGateEnvVar, "")
	if stagingDryRunGateEnabled() {
		t.Error("staging dry-run gate must default to off when unset")
	}

	// With the flag off, evaluating an eligible outcome must emit nothing.
	logs := captureLogs(t)
	logStagingDryRunGate(runOutcome{Kind: outcomePR, PRNumber: 7, TerminationReason: terminationCompleted})
	if logs.hasLogMsg(stagingDryRunGateMsg) {
		t.Error("no staging_dry_run_gate event should be logged while the flag is off")
	}
}

func TestStagingDryRunGateLogsWhenEnabled(t *testing.T) {
	t.Setenv(stagingDryRunGateEnvVar, "1")
	if !stagingDryRunGateEnabled() {
		t.Fatal("expected the gate to be enabled with STAGING_DRY_RUN_GATE=1")
	}

	logs := captureLogs(t)
	logStagingDryRunGate(runOutcome{
		Kind:              outcomePR,
		PRURL:             "http://gitea.local/kit/hirdforge/pulls/7",
		PRNumber:          7,
		TerminationReason: terminationCompleted,
	})

	entries := logs.entries()
	var found map[string]interface{}
	for _, e := range entries {
		if e["msg"] == stagingDryRunGateMsg {
			found = e
			break
		}
	}
	if found == nil {
		t.Fatal("expected a staging_dry_run_gate event when the flag is on")
	}
	if eligible, _ := found["eligible"].(bool); !eligible {
		t.Errorf("expected eligible=true, got entry: %v", found)
	}
	if found["outcome_kind"] != string(outcomePR) {
		t.Errorf("outcome_kind = %v, want %q", found["outcome_kind"], outcomePR)
	}
	if found["termination_reason"] != terminationCompleted.String() {
		t.Errorf("termination_reason = %v, want %q", found["termination_reason"], terminationCompleted)
	}
	if found["pr_url"] != "http://gitea.local/kit/hirdforge/pulls/7" {
		t.Errorf("pr_url missing/wrong: %v", found["pr_url"])
	}
	// Dry-run must be explicit and must never have merged anything (we only log).
	if dryRun, _ := found["dry_run"].(bool); !dryRun {
		t.Errorf("expected dry_run=true marker, got entry: %v", found)
	}
}
