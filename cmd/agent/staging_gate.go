package main

import (
	"os"
	"strings"
)

// staging_gate.go is step 4 of the e2e -> A1 sequence
// (docs/e2e-staging-harness-contract.md): a feature-flagged, DRY-RUN staging
// gate. Given a typed runOutcome (from the result-extraction seam), it evaluates
// whether the run would be eligible for A1 staging auto-merge and, when the flag
// is on, LOGS that decision. It never merges, never calls the Gitea API, and
// never creates a branch — auto-merge is a later, separately-flagged PR.

// stagingDryRunGateEnvVar toggles dry-run gate evaluation/logging. Unset or any
// non-truthy value means OFF (the default).
const stagingDryRunGateEnvVar = "STAGING_DRY_RUN_GATE"

// stagingDryRunGateMsg is the structured-log message key for a dry-run decision.
const stagingDryRunGateMsg = "staging_dry_run_gate"

// stagingGateDecision is the dry-run evaluation of a runOutcome. It is advisory
// only — nothing acts on it yet.
type stagingGateDecision struct {
	Eligible          bool
	Reason            string
	Kind              outcomeKind
	PRURL             string
	PRNumber          int
	TerminationReason terminationReason
}

// stagingDryRunGateEnabled reports whether dry-run gate evaluation is turned on.
// It defaults to false; only an explicit truthy value enables it.
func stagingDryRunGateEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(stagingDryRunGateEnvVar))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// evaluateStagingGate computes the dry-run eligibility decision for a runOutcome.
// It is a pure policy function with no side effects and makes no merge decision.
//
// A run is eligible for A1 staging auto-merge only if it represents a real,
// cleanly-completed PR:
//   - the outcome kind is pr,
//   - the run terminated as completed (not a degraded reason), and
//   - a PR reference (URL or number) is present.
func evaluateStagingGate(o runOutcome) stagingGateDecision {
	d := stagingGateDecision{
		Kind:              o.Kind,
		PRURL:             o.PRURL,
		PRNumber:          o.PRNumber,
		TerminationReason: o.TerminationReason,
	}
	switch {
	case o.Kind != outcomePR:
		d.Reason = "ineligible: outcome kind is " + string(o.Kind) + ", want pr"
	case o.TerminationReason != terminationCompleted:
		d.Reason = "ineligible: termination reason is " + o.TerminationReason.String() + ", want completed"
	case o.PRURL == "" && o.PRNumber <= 0:
		d.Reason = "ineligible: no PR reference (URL or number)"
	default:
		d.Eligible = true
		d.Reason = "eligible: pr outcome completed with a PR reference"
	}
	return d
}

// logStagingDryRunGate evaluates the outcome and, when the dry-run flag is on,
// emits a structured staging_dry_run_gate decision. It is a no-op when the flag
// is off, and never merges or mutates anything.
func logStagingDryRunGate(o runOutcome) {
	if !stagingDryRunGateEnabled() {
		return
	}
	d := evaluateStagingGate(o)
	fields := map[string]interface{}{
		"dry_run":            true,
		"eligible":           d.Eligible,
		"reason":             d.Reason,
		"outcome_kind":       string(d.Kind),
		"termination_reason": d.TerminationReason.String(),
	}
	if d.PRURL != "" {
		fields["pr_url"] = d.PRURL
	}
	if d.PRNumber > 0 {
		fields["pr_number"] = d.PRNumber
	}
	logJSON("info", stagingDryRunGateMsg, fields)
}
