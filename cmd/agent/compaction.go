package main

import "fmt"

// P3.3 (scoped) — safe, observable context compaction.
//
// The loop has always compacted: progressiveTrim drops the oldest message groups
// once history passes ~80% of the context budget. Two defects made that
// dangerous rather than helpful:
//
//  1. It pinned only messages[0] (the system prompt: persona + procedure), so on
//     a long task it could drop messages[1] — which in TASK mode is the task
//     ITSELF (issue title/body + DONE WHEN). The agent kept its generic
//     procedure while losing the actual requirements.
//  2. It was silent. Nothing logged, and the model was never told history had
//     been dropped — it just reasoned from a hole. That is exactly the silent
//     degradation the doctrine forbids.
//
// This fixes both: the task statement is pinned alongside the system prompt, and
// every compaction is logged AND announced to the model. Summarization of the
// dropped span (full M7) is deliberately NOT built — see DECISIONS O-M7-SCOPE:
// it needs evidence from real long tasks, and the pin + announcement remove the
// correctness risk that made it urgent.

// compactionThreshold is the fraction of the context budget at which the sliding
// window engages (matching the long-standing behaviour).
const compactionThreshold = 0.8

// compactIfNeeded applies the sliding-window compaction when history exceeds the
// budget. It pins the system prompt and, in task mode, the task statement, then
// logs and announces anything it dropped. Returns the messages to send.
//
// Callers pass taskID; a non-empty taskID means TASK mode (one-shot dispatch),
// where messages[1] is the task statement and must never be trimmed away.
func compactIfNeeded(messages []message, maxContext int, agentName, sessionID, taskID string) []message {
	if maxContext <= 0 || len(messages) == 0 {
		return messages
	}
	if len(messages)-1 <= int(float64(maxContext)*compactionThreshold) {
		return messages
	}

	// Pin the system prompt; in task mode pin the task statement with it.
	pin := 1
	if taskID != "" && len(messages) > 1 {
		pin = 2
	}
	if len(messages) <= pin {
		return messages
	}

	rest := messages[pin:]
	trimmed := progressiveTrim(rest, maxContext)
	dropped := len(rest) - len(trimmed)
	if dropped <= 0 {
		return messages
	}

	out := make([]message, 0, pin+len(trimmed)+1)
	out = append(out, messages[:pin]...)
	out = append(out, trimmed...)
	out = append(out, message{Role: "user", Content: compactionNotice(dropped, pin == 2)})

	logJSON("info", "context_compacted", map[string]interface{}{
		"agent":       agentName,
		"session_id":  sessionID,
		"task_id":     taskID,
		"dropped":     dropped,
		"kept":        len(trimmed),
		"pinned_task": pin == 2,
		"max_context": maxContext,
	})
	return out
}

// compactionNotice tells the model, in its own context, that earlier turns were
// dropped — so it re-reads rather than silently assuming it still has them.
func compactionNotice(dropped int, taskPinned bool) string {
	pinned := "Your original instructions are still above."
	if taskPinned {
		pinned = "Your original task statement and instructions are still above — re-read them."
	}
	return fmt.Sprintf(
		"<system-reminder>\nContext limit reached: %d earlier message(s) were dropped to make room. %s "+
			"Anything else from earlier turns is GONE — do not assume you still know it. "+
			"If you need a file's contents, a command's output, or your plan, re-read it (`read`, `exec`, or the `todo` tool) rather than recalling it.\n</system-reminder>",
		dropped, pinned)
}
