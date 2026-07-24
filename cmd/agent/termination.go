package main

import (
	"context"
	"errors"
	"strings"
)

// terminationReason is a canonical, structured label for why an agent session's
// tool loop ended. It exists so session termination is reportable and
// diagnosable — a prerequisite for building staging / e2e autonomy gates that
// reason about how runs end. The string values are stable: downstream log
// analysis and future gates key off them, so do not rename without a migration.
type terminationReason string

const (
	// terminationCompleted: the model finished and returned usable final content
	// with no further tool calls (normal completion).
	terminationCompleted terminationReason = "completed"
	// terminationMaxTurns: the tool loop hit its per-session turn limit
	// (deps.maxToolRounds) before the model signalled completion.
	terminationMaxTurns terminationReason = "max_turns_reached"
	// terminationMaxToolCalls: a per-session cap on total tool invocations was
	// reached. Reserved: the runtime currently bounds work by turns, not by a
	// global tool-call count, so this is part of the vocabulary for future gates.
	terminationMaxToolCalls terminationReason = "max_tool_calls_reached"
	// terminationRepeatedToolCall: the model issued the same tool call (name +
	// arguments) repeatedly, indicating a stuck loop.
	terminationRepeatedToolCall terminationReason = "repeated_tool_call_loop"
	// terminationNoActionableOutput: the model returned no tool calls and no
	// usable content (empty/blank response, or no choices from the backend).
	terminationNoActionableOutput terminationReason = "no_actionable_output"
	// terminationToolErrorsExhausted: a tool kept failing after its retries were
	// exhausted; recorded as a signal on the terminating event.
	terminationToolErrorsExhausted terminationReason = "tool_errors_exhausted"
	// terminationContextExhaustion: the inference backend rejected the request
	// because the prompt/context exceeded the model's context window.
	terminationContextExhaustion terminationReason = "context_exhaustion"
	// terminationContextCanceled: the parent context was canceled (client
	// disconnect, shutdown, deadline) mid-loop.
	terminationContextCanceled terminationReason = "context_canceled"
	// terminationInferenceError: the inference call failed for some other reason.
	terminationInferenceError terminationReason = "inference_error"
	// terminationStallFatal: the model produced no output and stall recovery also
	// failed to produce output.
	terminationStallFatal terminationReason = "stall_fatal"
	// terminationUnknown: fallback when no specific reason was determined.
	terminationUnknown terminationReason = "unknown"
)

// allTerminationReasons enumerates every canonical reason. Used by tests and by
// Valid() to guard against typos/drift.
var allTerminationReasons = []terminationReason{
	terminationCompleted,
	terminationMaxTurns,
	terminationMaxToolCalls,
	terminationRepeatedToolCall,
	terminationNoActionableOutput,
	terminationToolErrorsExhausted,
	terminationContextExhaustion,
	terminationContextCanceled,
	terminationInferenceError,
	terminationStallFatal,
	terminationUnknown,
}

// repeatedToolCallThreshold is how many consecutive identical tool calls (same
// name + arguments) constitute a "repeated tool-call loop". Three identical
// calls in a row is a strong stuck-loop signal while leaving room for a
// legitimate retry-once pattern.
const repeatedToolCallThreshold = 3

func (r terminationReason) String() string { return string(r) }

// IsAbnormal reports whether the loop ended for a degraded reason that warrants
// a structured handoff report (M1's abnormal-exit summary) rather than silence.
// Context exhaustion is deliberately excluded — the context is already over the
// window, so asking for one more message would just fail again; and the pure
// error/cancel reasons return early with an error before any summary phase.
func (r terminationReason) IsAbnormal() bool {
	switch r {
	case terminationMaxTurns, terminationRepeatedToolCall,
		terminationToolErrorsExhausted, terminationNoActionableOutput:
		return true
	default:
		return false
	}
}

// Valid reports whether r is one of the canonical reasons.
func (r terminationReason) Valid() bool {
	for _, x := range allTerminationReasons {
		if x == r {
			return true
		}
	}
	return false
}

// contextLengthErrorMarkers are substrings inference backends (OpenAI-style,
// Ollama, vLLM, llama.cpp) use when the prompt/context exceeds the model window.
// Matched case-insensitively.
var contextLengthErrorMarkers = []string{
	"context length",
	"context window",
	"maximum context",
	"context_length_exceeded",
	"too many tokens",
	"token limit",
	"exceeds the maximum",
	"reduce the length",
	"prompt is too long",
	"input is too long",
}

// isContextLengthError reports whether err looks like a context/token-window
// overflow from the inference backend.
func isContextLengthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, m := range contextLengthErrorMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// classifyInferenceError maps a failed inference call to a termination reason.
// ctxErr is the loop context's Err() so a canceled/expired parent context is
// distinguished from a backend failure. A context/token overflow is reported as
// context exhaustion; everything else is a generic inference error.
func classifyInferenceError(ctxErr, err error) terminationReason {
	if errors.Is(ctxErr, context.Canceled) || errors.Is(err, context.Canceled) ||
		errors.Is(ctxErr, context.DeadlineExceeded) {
		return terminationContextCanceled
	}
	if isContextLengthError(err) {
		return terminationContextExhaustion
	}
	return terminationInferenceError
}

// classifyModelTurn decides the reason when the model returned no tool calls:
// usable content means normal completion, otherwise no actionable output.
func classifyModelTurn(content string) terminationReason {
	if strings.TrimSpace(content) == "" {
		return terminationNoActionableOutput
	}
	return terminationCompleted
}

// toolCallSignature returns a stable identity for a tool call (name + raw
// arguments) so repeated identical calls can be detected. Surrounding
// whitespace is trimmed; the NUL separator avoids accidental collisions between
// name and arguments.
func toolCallSignature(name, arguments string) string {
	return strings.TrimSpace(name) + "\x00" + strings.TrimSpace(arguments)
}

// hasRepeatedToolCallLoop reports whether the last `threshold` signatures in
// history are all identical and non-empty — i.e. the agent is stuck repeating
// the same tool call. A threshold below 2, or a history shorter than the
// threshold, never trips.
func hasRepeatedToolCallLoop(history []string, threshold int) bool {
	if threshold < 2 || len(history) < threshold {
		return false
	}
	last := history[len(history)-1]
	if strings.TrimSpace(last) == "" {
		return false
	}
	for i := len(history) - threshold; i < len(history)-1; i++ {
		if history[i] != last {
			return false
		}
	}
	return true
}

// terminationLogLevel maps a reason to the structured-log level it should be
// emitted at. Clean completion is informational; degraded/abnormal endings are
// warnings; hard failures are errors.
func terminationLogLevel(reason terminationReason) string {
	switch reason {
	case terminationInferenceError, terminationContextExhaustion, terminationStallFatal:
		return "error"
	case terminationMaxTurns, terminationMaxToolCalls, terminationRepeatedToolCall,
		terminationNoActionableOutput, terminationToolErrorsExhausted, terminationContextCanceled:
		return "warn"
	default:
		return "info"
	}
}

// sessionTerminationMsg is the canonical structured-log message key for every
// session termination event. Log consumers filter on this.
const sessionTerminationMsg = "session_termination"

// streamTerminationFields builds the canonical structured-log field set for a
// termination that happens in the streaming phase (client disconnect, stream
// error, or a mid-stream error return). It stamps phase="streaming" and a
// `stage` label so the specific streaming exit point can be told apart in logs.
// extra is merged last for any path-specific detail (e.g. the backend error).
func streamTerminationFields(agentName, model, sessionID, taskID, stage string, extra map[string]interface{}) map[string]interface{} {
	fields := map[string]interface{}{
		"agent":      agentName,
		"model":      model,
		"session_id": sessionID,
		"task_id":    taskID,
		"phase":      "streaming",
		"stage":      stage,
	}
	for k, v := range extra {
		fields[k] = v
	}
	return fields
}

// logSessionTermination emits the canonical structured termination event. The
// reason is always present as "reason"; callers supply session context and any
// signal fields (e.g. repeated_tool_call, tool_errors_exhausted). Level is
// derived from the reason so dashboards can split clean vs degraded endings.
func logSessionTermination(reason terminationReason, fields map[string]interface{}) {
	merged := make(map[string]interface{}, len(fields)+1)
	merged["reason"] = reason.String()
	for k, v := range fields {
		merged[k] = v
	}
	logJSON(terminationLogLevel(reason), sessionTerminationMsg, merged)
}
