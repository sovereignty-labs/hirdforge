package main

import "strings"

// staging_controller.go is the Phase 4 dry-run controller. It assembles the A1
// pieces — the result-extraction seam (classifyRunOutcome), the checks-source
// helper (stagingChecksGreen), the eligibility policy (evaluateStagingGate), and
// a repo/branch scope allow-list — into a single dry-run decision for a finished
// agent run, and emits a staging_dry_run_gate audit log.
//
// It is DRY-RUN ONLY: it never calls maybeAutoMergeStaging, never constructs a
// merger, and never merges. It is a no-op when STAGING_DRY_RUN_GATE is off, and
// it never reads STAGING_AUTO_MERGE. This is the observation-window machinery
// from docs/a1-staging-auto-merge-activation-checklist.md (step 2); wiring it
// into the live session flow and enabling any merge is a later, separate change.

// stagingScopeEntry is one allow-listed (owner, repo, base-branch) target.
type stagingScopeEntry struct {
	Owner  string
	Repo   string
	Branch string
}

// stagingScope is the explicit allow-list of targets the dry-run controller may
// consider. Anything outside the list is refused (fail closed). No wildcards.
type stagingScope struct {
	allowed map[string]struct{}
}

func scopeKey(owner, repo, branch string) string {
	return strings.ToLower(strings.TrimSpace(owner)) + "\x00" +
		strings.ToLower(strings.TrimSpace(repo)) + "\x00" +
		strings.ToLower(strings.TrimSpace(branch))
}

// newStagingScope builds an allow-list from explicit entries.
func newStagingScope(entries ...stagingScopeEntry) *stagingScope {
	s := &stagingScope{allowed: make(map[string]struct{}, len(entries))}
	for _, e := range entries {
		if strings.TrimSpace(e.Owner) == "" || strings.TrimSpace(e.Repo) == "" || strings.TrimSpace(e.Branch) == "" {
			continue
		}
		s.allowed[scopeKey(e.Owner, e.Repo, e.Branch)] = struct{}{}
	}
	return s
}

// allows reports whether (owner, repo, branch) is in the allow-list. A nil scope
// allows nothing.
func (s *stagingScope) allows(owner, repo, branch string) bool {
	if s == nil {
		return false
	}
	_, ok := s.allowed[scopeKey(owner, repo, branch)]
	return ok
}

// stagingRunInput is the finished-run information the dry-run controller needs.
// HeadSHA may be a commit SHA or a branch ref (the Gitea combined-status endpoint
// accepts either). SessionID, when set, is echoed into the audit log.
type stagingRunInput struct {
	SessionID         string
	FinalContent      string
	TerminationReason terminationReason
	HeadSHA           string
	BaseBranch        string
	RequiredChecks    []string
}

// runStagingDryRun evaluates whether a finished run WOULD be eligible for A1
// staging auto-merge and emits a staging_dry_run_gate audit log. It builds a
// mergeCandidate (outcome via the result-extraction seam + checks via the
// checks-source helper), but never merges and never calls maybeAutoMergeStaging.
// It is a no-op (returns wouldMerge=false, logged=false) when STAGING_DRY_RUN_GATE
// is off. The returned mergeCandidate is what a future live controller would feed
// to the merge path.
func runStagingDryRun(in stagingRunInput, checks checksClient, scope *stagingScope) (candidate mergeCandidate, wouldMerge bool, logged bool) {
	if !stagingDryRunGateEnabled() {
		return mergeCandidate{}, false, false
	}

	outcome := classifyRunOutcome(in.FinalContent, in.TerminationReason)
	ref, refOK := prRefFromOutcome(outcome)

	var checksGreen bool
	var checksSummary string
	if refOK {
		checksGreen, checksSummary = stagingChecksGreen(checks, ref.Owner, ref.Repo, in.HeadSHA, in.RequiredChecks)
	} else {
		checksSummary = "checks: no PR reference to query"
	}
	candidate = mergeCandidate{Outcome: outcome, ChecksGreen: checksGreen, ChecksSummary: checksSummary}

	gate := evaluateStagingGate(outcome)
	scopeAllowed := refOK && scope.allows(ref.Owner, ref.Repo, in.BaseBranch)
	wouldMerge = gate.Eligible && refOK && scopeAllowed && checksGreen

	// Single, explicit blocking reason (first failing precondition wins).
	reason := "would auto-merge (dry-run)"
	switch {
	case !gate.Eligible:
		reason = "blocked: " + gate.Reason
	case !refOK:
		reason = "blocked: no derivable PR reference"
	case !scopeAllowed:
		reason = "blocked: outside allowed staging scope"
	case !checksGreen:
		reason = "blocked: " + checksSummary
	}

	// decision is the dry-run verdict as an enum: would_allow when the run would
	// be eligible to auto-merge, would_block otherwise. This is observation only —
	// it never implies a real merge happened.
	decision := "would_block"
	if wouldMerge {
		decision = "would_allow"
	}

	fields := map[string]interface{}{
		"dry_run":            true,
		"decision":           decision,
		"would_merge":        wouldMerge,
		"reason":             reason,
		"eligible":           gate.Eligible,
		"checks_green":       checksGreen,
		"checks_summary":     checksSummary,
		"scope_allowed":      scopeAllowed,
		"outcome_kind":       string(outcome.Kind),
		"termination_reason": outcome.TerminationReason.String(),
	}
	if in.SessionID != "" {
		fields["session_id"] = in.SessionID
	}
	if in.BaseBranch != "" {
		fields["base_branch"] = in.BaseBranch
	}
	if in.HeadSHA != "" {
		fields["head_sha"] = in.HeadSHA
	}
	if refOK {
		fields["pr_owner"] = ref.Owner
		fields["pr_repo"] = ref.Repo
		fields["pr_number"] = ref.Index
	}
	if outcome.PRURL != "" {
		fields["pr_url"] = outcome.PRURL
	}
	logJSON("info", stagingDryRunGateMsg, fields)
	return candidate, wouldMerge, true
}
