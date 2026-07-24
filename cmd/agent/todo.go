package main

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	toolpkg "git.hirdforge.com/kit/hirdforge/pkg/tools"
)

// M6 — Externalized plan (todo) + re-anchoring (BUILDER_HARNESS §M6).
//
// The Phase-1 dogfood and the P2.0 full-flow baseline both showed the same
// failure: on a multi-step task (refactor -> commit -> push -> open PR) the model
// does part of the work, believes it is done, and stops early — never reaching
// create-pr. The lever is not more turns (raising the cap 40->80 didn't help);
// it is keeping the *goal* pinned in front of the model as grep output and tool
// results flow past. M6 externalizes the plan: the model writes a todo list, and
// the harness re-surfaces it as a synthetic <system-reminder> at a cheap cadence
// and — harder — exactly when the model is drifting (a wobble signal).
//
// Anti-dilution (Kit steer, 2026-07-23): the terminal condition is NOT "PR or
// bust." A task is complete on a correct PR, OR a reasoned FAILED, OR a justified
// NOOP, OR — failing open — a specific question when genuinely blocked. The
// re-anchor pins the goal and the failure criteria, never "just make a PR."

type todoStatus string

const (
	todoPending    todoStatus = "pending"
	todoInProgress todoStatus = "in_progress"
	todoCompleted  todoStatus = "completed"
)

type todoItem struct {
	Content string
	Status  todoStatus
}

// todoStore holds each session's current plan in memory (no new service, per
// spec). Keyed by sessionID; concurrency-safe.
type todoStore struct {
	mu    sync.Mutex
	lists map[string][]todoItem
}

var sessionTodos = &todoStore{lists: map[string][]todoItem{}}

func (s *todoStore) set(sessionID string, items []todoItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lists == nil {
		s.lists = map[string][]todoItem{}
	}
	s.lists[sessionID] = items
}

func (s *todoStore) get(sessionID string) []todoItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.lists[sessionID]
	out := make([]todoItem, len(items))
	copy(out, items)
	return out
}

func (s *todoStore) clear(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.lists, sessionID)
}

func (s *todoStore) hasItems(sessionID string) bool {
	return len(s.get(sessionID)) > 0
}

// hasOpenItems reports whether any item is not yet completed. An empty list has
// no open items (so a session that never planned does not trip the gate on the
// todo path alone).
func (s *todoStore) hasOpenItems(sessionID string) bool {
	for _, it := range s.get(sessionID) {
		if it.Status != todoCompleted {
			return true
		}
	}
	return false
}

// render returns the checkbox view of a session's plan, or "" if empty.
func (s *todoStore) render(sessionID string) string {
	items := s.get(sessionID)
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	for _, it := range items {
		b.WriteString(todoMarker(it.Status))
		b.WriteString(" ")
		b.WriteString(it.Content)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// remaining returns the still-open item contents, for the M1 abnormal-exit
// summary ("remaining work" writes itself).
func (s *todoStore) remaining(sessionID string) []string {
	var out []string
	for _, it := range s.get(sessionID) {
		if it.Status != todoCompleted {
			out = append(out, it.Content)
		}
	}
	return out
}

func todoMarker(st todoStatus) string {
	switch st {
	case todoCompleted:
		return "[x]"
	case todoInProgress:
		return "[~]"
	default:
		return "[ ]"
	}
}

// todoLinePattern matches a checklist line with an optional leading status box:
// "[x] ...", "[~] ...", "[ ] ...", "- [ ] ...", or a bare "1. do the thing".
var todoLinePattern = regexp.MustCompile(`^\s*(?:[-*]\s*)?(?:\[( |x|X|~|>)\]|\d+[.)])?\s*(.*\S)\s*$`)

// parseTodoItems turns the model's free-form checklist into structured items.
// Robust to the shapes local models emit: markdown checkboxes, numbered lists,
// or bare lines (each bare line is a pending item). Status comes from the box:
// x = completed, ~ or > = in_progress, space/none = pending.
func parseTodoItems(raw string) []todoItem {
	var items []todoItem
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := todoLinePattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		content := strings.TrimSpace(m[2])
		if content == "" {
			continue
		}
		status := todoPending
		switch m[1] {
		case "x", "X":
			status = todoCompleted
		case "~", ">":
			status = todoInProgress
		}
		items = append(items, todoItem{Content: content, Status: status})
	}
	return items
}

// todoTool lets the model persist and update its plan. It replaces the whole
// list each call (the simplest, most robust contract for local models — the same
// full-rewrite pattern frontier harnesses use), so the model just re-sends the
// checklist with updated boxes as it goes.
type todoTool struct{}

func (t *todoTool) Name() string { return "todo" }
func (t *todoTool) Description() string {
	return "Write or update your task plan as a checklist so it is not lost as the conversation grows. Send the WHOLE list each time, one item per line, marking each: [ ] pending, [~] in progress, [x] done. For any task with more than ~3 steps, write the plan first and make the final item the deliverable (e.g. 'open the PR'). Update statuses as you complete each step."
}
func (t *todoTool) Parameters() map[string]string {
	return map[string]string{
		"items": "The full checklist, one item per line, each prefixed with [ ] / [~] / [x].",
	}
}

func (t *todoTool) Execute(args map[string]interface{}) toolpkg.ToolResult {
	sessionID := strings.TrimSpace(stringArg(args, "_session_id"))
	raw := stringArg(args, "items")
	if strings.TrimSpace(raw) == "" {
		return toolpkg.ToolResult{Error: "items is required: send the full checklist, one item per line ([ ]/[~]/[x])"}
	}
	items := parseTodoItems(raw)
	if len(items) == 0 {
		return toolpkg.ToolResult{Error: "no checklist items parsed — send one item per line, e.g. '[ ] read the files'"}
	}
	if sessionID != "" {
		sessionTodos.set(sessionID, items)
	}
	open := 0
	for _, it := range items {
		if it.Status != todoCompleted {
			open++
		}
	}
	out := "Plan updated:\n" + renderItems(items)
	if open == 0 {
		out += "\n\nAll items complete. Report your terminal outcome now (the PR URL, or FAILED: <reason>, or NOOP: <evidence>)."
	} else {
		out += "\n\nKeep going until every item is [x] — or, if blocked, report a terminal outcome (PR / FAILED / NOOP / a specific question)."
	}
	return toolpkg.ToolResult{Output: out}
}

func renderItems(items []todoItem) string {
	var b strings.Builder
	for _, it := range items {
		b.WriteString(todoMarker(it.Status))
		b.WriteString(" ")
		b.WriteString(it.Content)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// stringArg extracts a trimmed string arg, falling back to fmt.Sprint for
// non-string values (nil yields "").
func stringArg(args map[string]interface{}, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// --- re-anchoring ---

const reanchorCadence = 6 // re-surface the plan every N tool rounds

// completionBlockedPattern recognizes a fail-open "I'm blocked, here's my
// question" outcome as a legitimate terminal — so the model is never forced to
// fabricate a PR when it genuinely needs clarification. Distinct from FAILED
// (impossibility) and NOOP (nothing to do).
var completionBlockedPattern = regexp.MustCompile(`(?i)\b(BLOCKED|INPUT[- ]NEEDED|QUESTION|NEED(?:S)?\s+CLARIFICATION|CLARIFY):`)

// contentHasTerminalOutcome reports whether the model's final message is any
// legitimate terminal outcome: a PR (URL or #N), FAILED, NOOP, or a fail-open
// question. This is the anti-dilution completion check — a superset of
// contentHasCompletionSignal that also honors "I'm blocked, here's my question".
func contentHasTerminalOutcome(content string) bool {
	return contentHasCompletionSignal(content) || completionBlockedPattern.MatchString(content)
}

// planningReminder is injected once, up front, on a task that requires a
// deliverable — nudging the model to externalize its plan before diving in.
func planningReminder() string {
	return "<system-reminder>\nThis task has multiple steps. Before you start, call the `todo` tool to write your plan as a checklist, and make the LAST item the deliverable (e.g. \"open the PR\"). Mark items [x] as you finish them. You are not done until the plan is complete — or you report a terminal outcome: the PR URL, FAILED: <reason>, NOOP: <evidence>, or a specific question if you are blocked.\n</system-reminder>"
}

// reanchorReminder re-surfaces the current plan mid-loop. Returns "" when there
// is no plan to surface (a model that never used the todo tool gets no noise;
// the terminal gate still catches an early stop).
func reanchorReminder(sessionID string) string {
	plan := sessionTodos.render(sessionID)
	if plan == "" {
		return ""
	}
	return "<system-reminder>\nYour current plan (keep working it):\n" + plan +
		"\nDo not stop until every item is [x] — or report a terminal outcome (the PR URL, FAILED: <reason>, NOOP: <evidence>, or a specific question if blocked). Update the todo as you go.\n</system-reminder>"
}

// terminalNudge is the message appended when the model tries to stop without a
// terminal outcome. It re-surfaces the plan (if any) and states the full,
// anti-dilution outcome space — never "just make a PR".
func terminalNudge(sessionID string) string {
	msg := "You are not done, and you have not called a tool. Do NOT describe or announce what you will do next — DO IT by calling the tool now. If the code is written and green, your next call is `git-commit` (then `create-pr`). Continue from the current workspace; do not reclone or change scope. The ONLY ways to finish are to call the tools through to a real PR, or to report a terminal outcome: FAILED: <reason and what you tried>, NOOP: <evidence nothing was needed>, or QUESTION: <what you need> if genuinely blocked."
	if plan := sessionTodos.render(sessionID); plan != "" {
		msg = "Your plan is not complete:\n" + plan + "\n\n" + msg
	}
	return msg
}
