package main

import (
	"fmt"
	"strings"
	"unicode"
)

// failOpenRetryThreshold is how many times the SAME tool may fail with the SAME
// error class before the loop stops re-anchoring and pushes the model to fail
// open — fix the root cause once, or report a terminal FAILED with a proposed
// solution. It sits above the re-anchor trigger (2 consecutive failures) so a
// transient wobble gets a re-anchor first; only a genuinely stuck retry loop
// (e.g. create-pr returning 404 "branch not on remote" over and over because the
// branch was never pushed) escalates.
//
// This is the mechanical guardrail behind the "not PR-or-bust" doctrine: the
// builder must honor failure criteria and escalate with proposed solutions
// rather than burn the whole deadline chasing the PR artifact.
const failOpenRetryThreshold = 3

// failureSignature groups a tool's failures by class so retrying the same call
// with the same error is detected regardless of the variable bits (branch names,
// numbers, quotes) in the message.
func failureSignature(tool, errText string) string {
	line := strings.ToLower(strings.TrimSpace(firstLine(errText)))
	var b strings.Builder
	for _, r := range line {
		if unicode.IsLetter(r) || r == ' ' {
			b.WriteRune(r)
		}
	}
	sig := strings.Join(strings.Fields(b.String()), " ")
	if len(sig) > 80 {
		sig = sig[:80]
	}
	return tool + "|" + sig
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// failOpenEscalation is the decisive message injected once a tool has failed the
// same way failOpenRetryThreshold times. Unlike a re-anchor (which re-surfaces
// the plan), it names the stuck tool + error and forces a branch: fix the root
// cause once, or report FAILED now with a proposed solution. It deliberately
// spells out the create-pr "branch not on remote" case because that is the
// canonical unpushed-branch trap.
func failOpenEscalation(tool, errExcerpt string) string {
	errExcerpt = strings.TrimSpace(errExcerpt)
	if len(errExcerpt) > 200 {
		errExcerpt = errExcerpt[:200] + "…"
	}
	var b strings.Builder
	b.WriteString("<system-reminder>\n")
	fmt.Fprintf(&b, "STOP retrying `%s`. It has failed %d times with the same error:\n  %s\n",
		tool, failOpenRetryThreshold, errExcerpt)
	b.WriteString("Retrying the same call will not change the outcome. Do exactly one of:\n")
	b.WriteString("1. Fix the ROOT CAUSE, then make ONE more attempt. ")
	b.WriteString("(A create-pr \"branch may not exist on remote\" almost always means the branch was never pushed — call `git-commit` to commit AND push it, then create-pr once.)\n")
	b.WriteString("2. If you cannot resolve it, FAIL OPEN now: end with `FAILED: <root cause and what you tried>` and a concrete proposed fix, or `QUESTION: <exactly what you need>` if a human must decide. ")
	b.WriteString("A reasoned FAILED with a proposed solution is a correct outcome — do not fabricate a PR and do not keep looping.\n")
	b.WriteString("</system-reminder>")
	return b.String()
}
