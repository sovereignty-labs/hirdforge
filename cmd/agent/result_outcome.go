package main

import (
	"regexp"
	"strconv"
	"strings"
)

// result_outcome.go is the result-extraction seam from the deterministic e2e
// harness contract (docs/e2e-staging-harness-contract.md). It maps a finished
// agent run — its final content plus the canonical session_termination reason —
// into a typed outcome that a future staging controller can consume. It makes no
// merge/eligibility decision itself; it only reports what the run produced and
// how it ended, so the controller can apply its own policy.

// outcomeKind is the high-level result of an agent run.
type outcomeKind string

const (
	// outcomePR: the run reported a created PR (a PR URL and/or PR number).
	outcomePR outcomeKind = "pr"
	// outcomeFailed: the run reported an explicit FAILED: result.
	outcomeFailed outcomeKind = "failed"
	// outcomeNoop: the run reported an explicit NOOP: result.
	outcomeNoop outcomeKind = "noop"
	// outcomeUnknown: the final content carried no recognized completion signal.
	outcomeUnknown outcomeKind = "unknown"
)

// runOutcome is the typed result of an agent run.
type runOutcome struct {
	Kind outcomeKind
	// PRURL / PRNumber are set when Kind == outcomePR (either may be empty/zero
	// if only one form was present).
	PRURL    string
	PRNumber int
	// FailedReason is the text after "FAILED:" when Kind == outcomeFailed.
	FailedReason string
	// NoopReason is the text after "NOOP:" when Kind == outcomeNoop.
	NoopReason string
	// TerminationReason is the canonical session_termination reason for the run.
	// It is always carried so the controller can reject otherwise-valid content
	// that ended in a degraded way (e.g. Kind == pr but reason == max_turns).
	TerminationReason terminationReason
}

// Completion-signal extraction patterns. These mirror the production
// completionSignals (session.go) but add capture groups so the PR URL/number and
// the FAILED/NOOP reason text can be pulled out.
var (
	outcomePRURLPattern  = regexp.MustCompile(`(?i)https?://[^\s]+/[^/]+/[^/]+/pulls/(\d+)`)
	outcomePRNumPattern  = regexp.MustCompile(`(?i)\bPR\s+#(\d+)\b`)
	outcomeFailedPattern = regexp.MustCompile(`(?i)\bFAILED:\s*(.*)`)
	outcomeNoopPattern   = regexp.MustCompile(`(?i)\bNOOP:\s*(.*)`)
)

// classifyRunOutcome maps a finished run's final content and termination reason
// into a typed runOutcome. Precedence among signals is PR (URL, then number),
// then FAILED, then NOOP — a reported PR is treated as the success outcome. The
// termination reason is always carried through unchanged.
func classifyRunOutcome(finalContent string, reason terminationReason) runOutcome {
	out := runOutcome{Kind: outcomeUnknown, TerminationReason: reason}
	content := strings.TrimSpace(finalContent)

	// Reuse the production gate: no recognized signal -> unknown.
	if !contentHasCompletionSignal(content) {
		return out
	}

	if m := outcomePRURLPattern.FindStringSubmatch(content); m != nil {
		out.Kind = outcomePR
		out.PRURL = m[0]
		out.PRNumber, _ = strconv.Atoi(m[1])
		return out
	}
	if m := outcomePRNumPattern.FindStringSubmatch(content); m != nil {
		out.Kind = outcomePR
		out.PRNumber, _ = strconv.Atoi(m[1])
		return out
	}
	if m := outcomeFailedPattern.FindStringSubmatch(content); m != nil {
		out.Kind = outcomeFailed
		out.FailedReason = strings.TrimSpace(m[1])
		return out
	}
	if m := outcomeNoopPattern.FindStringSubmatch(content); m != nil {
		out.Kind = outcomeNoop
		out.NoopReason = strings.TrimSpace(m[1])
		return out
	}

	// contentHasCompletionSignal matched but no specific pattern did — keep
	// unknown rather than guessing.
	return out
}
