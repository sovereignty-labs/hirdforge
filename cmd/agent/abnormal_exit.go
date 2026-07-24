package main

// M1 — abnormal-exit summary (BUILDER_HARNESS §M1, source spec).
//
// Structured exit *reasons* were already built (termination.go). The missing
// half of M1 is the model-authored handoff: when the loop ends abnormally
// (round cap, repetition, exhausted tool errors, no actionable output), the
// spec calls for one final tools-disabled turn where the model states why it
// stopped, what it did, what remains, and the next step — so the run reports
// something useful to the operator instead of silence or a bare truncation.
//
// The loop already makes a final tools-disabled streaming call to produce the
// returned content; this steers THAT call's prompt on abnormal exits. It also
// carries the M6 plan verbatim, so the "remaining work" section writes itself
// and a re-dispatch can resume from it.

// appendAbnormalExitSummaryRequest appends the M1 handoff-report reminder to the
// message list when the loop is ending abnormally, and returns the (possibly
// unchanged) list. Extracted from the conversation loop so the injection lives in
// one cohesive place — and so the loop's already-high cyclomatic complexity does
// not carry this branch.
func appendAbnormalExitSummaryRequest(messages []message, agentName, sessionID, taskID string, reason terminationReason) []message {
	if !reason.IsAbnormal() {
		return messages
	}
	messages = append(messages, message{Role: "user", Content: abnormalExitSummaryRequest(sessionID, reason)})
	logJSON("info", "abnormal_exit_summary_requested", map[string]interface{}{
		"agent":      agentName,
		"session_id": sessionID,
		"task_id":    taskID,
		"reason":     reason.String(),
	})
	return messages
}

// abnormalExitSummaryRequest is the synthetic reminder appended before the final
// (tools-disabled) turn on an abnormal exit. It ends by requiring a terminal
// marker so the result still classifies (PR / FAILED / NOOP / QUESTION) — and,
// per the anti-dilution rule, never invites a fabricated PR.
func abnormalExitSummaryRequest(sessionID string, reason terminationReason) string {
	plan := sessionTodos.render(sessionID)
	if plan == "" {
		plan = "(no plan was written)"
	}
	return "<system-reminder>\n" +
		"You are stopping now (reason: " + reason.String() + "). Tools are disabled for this final turn. " +
		"Write a short handoff report for the operator, in exactly this shape:\n" +
		"- WHY: one sentence on why you are stopping.\n" +
		"- DONE: what you actually accomplished — files changed, commits made, branch pushed, PR if any.\n" +
		"- REMAINING: the still-open work, as a checklist.\n" +
		"- NEXT: the single most useful next step.\n" +
		"Then end with exactly one terminal marker: the real PR URL if you opened one, or FAILED: <reason>, " +
		"or NOOP: <evidence>, or QUESTION: <what you need>. Do not claim a PR you did not create.\n\n" +
		"Your plan:\n" + plan + "\n</system-reminder>"
}
