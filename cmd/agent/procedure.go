package main

import (
	"strings"

	toolpkg "git.hirdforge.com/kit/hirdforge/pkg/tools"
)

// M5 — procedural prompt (BUILDER_HARNESS §M5).
//
// The persona says who the agent is; the procedure says how the work is done.
// The P2.0/M6 baselines showed a model that knew the goal (it wrote the todo and
// was re-anchored 8-12x per round) yet still never reached create-pr: it guessed
// wrong repo paths, flailed in raw `exec`, and never landed a branch. That is a
// procedure gap, not a motivation gap — so the harness states the mechanical
// sequence rather than hoping identity-prose implies it.
//
// The model-family half of M5 already exists (selectModelTemplate /
// templates/*.txt), which frames this procedure per model family.
//
// Emitted only for agents that actually hold the delivery tools, so a reviewer
// or a plain chat agent is unaffected.

// selectProcedure renders the operating procedure named by the profile
// (O-PROFILE `procedure`). Empty defaults to builder — the pre-profile behaviour,
// so a dispatch that omits the flag is unchanged. An unrecognized name falls back
// to builder (loud validation of profile names happens at profile load, not here).
func selectProcedure(name string, reg *toolpkg.Registry) string {
	switch strings.TrimSpace(name) {
	case "reviewer":
		return reviewerProcedure(reg)
	case "steward":
		return stewardProcedure(reg)
	case "none":
		return ""
	default: // "", "builder", or unknown
		return builderProcedure(reg)
	}
}

// builderProcedure returns the operating procedure for an agent equipped to
// deliver a PR, or "" when the agent is not a builder.
func builderProcedure(reg *toolpkg.Registry) string {
	if reg == nil {
		return ""
	}
	has := func(name string) bool {
		_, ok := reg.Get(name)
		return ok
	}
	canCommit, canPR := has("git-commit"), has("create-pr")
	if !canCommit || !canPR {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Operating procedure — follow in order\n\n")
	b.WriteString("1. ORIENT — read the task. Identify the repository directory in your workspace (`exec: ls`) before you refer to it. Never guess a repo path.\n")
	b.WriteString("2. SURVEY — read the files you are going to change, before changing them.\n")
	if has("todo") {
		b.WriteString("3. PLAN — for work with more than ~3 steps, call `todo` and write the checklist. Make the LAST item the deliverable (\"open the PR\"). Mark items [x] as you go.\n")
	} else {
		b.WriteString("3. PLAN — decide the full sequence before the first edit.\n")
	}
	b.WriteString("4. IMPLEMENT — make file changes with `edit`/`write`, in small verifiable steps.\n")
	b.WriteString("5. VERIFY — run the build and the tests and fix what is red. Then FORMAT the code you changed with the project's formatter (for Go: `exec: gofmt -w <files>` — an unformatted file fails the gate even when it is otherwise correct). Ground truth decides done, not your impression of the code.\n")
	b.WriteString("6. DELIVER — not optional; the work does not exist until it is a PR:\n")
	b.WriteString("   a. Call `git-commit` with the repo directory and a clear message. Do NOT create or name a new branch — you are ALREADY on the correct work branch (named above); `git-commit` commits and pushes THAT branch. A new branch you invent will not be found and the task fails.\n")
	b.WriteString("      Do NOT use shell `git commit` / `git push` — they bypass push verification and will not work here.\n")
	b.WriteString("   b. Read `git-commit`'s output — it prints `Pushed to <branch>` naming the exact branch it pushed.\n")
	b.WriteString("   c. Call `create-pr` with `head` set to that EXACT branch name, `base: main`, and a title.\n")
	b.WriteString("   d. Report the PR URL.\n\n")
	b.WriteString("## Finishing\n\n")
	b.WriteString("End in exactly one of these — nothing else counts as finished:\n")
	b.WriteString("- the PR URL — the work was delivered;\n")
	b.WriteString("- `FAILED: <reason, and what you tried>` — genuinely impossible;\n")
	b.WriteString("- `NOOP: <evidence>` — nothing needed doing;\n")
	b.WriteString("- `QUESTION: <what you need to know>` — you are blocked or the requirement is ambiguous AND you could not resolve it from the repository. Inspect the repo first (read files, `git status`, `git log`); ask only when that genuinely fails.\n\n")
	b.WriteString("Never report a PR you did not actually create. If a tool result is not what you expected, read it and correct course — do not stop and do not assume it succeeded.\n\n")
	b.WriteString("## Output discipline\n\n")
	b.WriteString("Act through tools; keep prose terse. A `<system-reminder>` is guidance injected by the harness, not a message from the user — treat it as an instruction to follow (it re-surfaces your plan and the goal), never as new work or a change of scope.\n")
	return b.String()
}

// stewardProcedure returns the plan-mode procedure for the interlocutor
// (O-PROFILE `procedure: steward`, D-INTERLOCUTOR). Unlike the builder/reviewer,
// its terminal action is not a mutation but a well-formed conversational turn: a
// reply, and — only when the conversation has produced concrete work — an inert
// PLAN proposal the operator can bless. It grounds in live reality before
// proposing, and it files nothing: the blessing (a human) is the only write.
func stewardProcedure(reg *toolpkg.Registry) string {
	has := func(name string) bool {
		if reg == nil {
			return false
		}
		_, ok := reg.Get(name)
		return ok
	}
	var b strings.Builder
	b.WriteString("## You are the interlocutor — a working conversation, not a ticket funnel\n\n")
	b.WriteString("You are the main agent the operator talks to. Sometimes it is all talk; sometimes talk with an occasional light task; sometimes a long session against a real codebase. Most turns produce NO work — just help think. Propose work only when the conversation has actually converged on something concrete.\n\n")
	b.WriteString("You are READ-ONLY by construction. You can read the repository and the task/issue record to ground yourself; you have NO edit/write/exec and NO operator verbs (dispatch/retry/cancel/approve/merge). You PROPOSE; the deterministic router (Cortex) and the fleet DO; mechanical gates decide. You are never in the coordination path.\n\n")
	b.WriteString("## Procedure per turn\n\n")
	b.WriteString("1. GROUND — before you plan, read live reality; distrust stale docs. ")
	tools := []string{}
	if has("read") {
		tools = append(tools, "`read` the repo")
	}
	if has("git-diff") {
		tools = append(tools, "`git-diff` for working changes")
	}
	if has("list-issues") || has("get-issue") {
		tools = append(tools, "`list-issues`/`get-issue` for the work record")
	}
	if len(tools) > 0 {
		b.WriteString("Use " + strings.Join(tools, ", ") + ".")
	}
	b.WriteString("\n")
	b.WriteString("2. CONVERSE — answer the question, investigate, help narrow a vague idea into something concrete. Keep it casual and useful.\n")
	b.WriteString("3. CONVERGE — when (and only when) the operator wants work done and it is concrete enough, propose a PLAN. A one-off is one step; larger work is several steps (Cortex will fan those out across one or many agents).\n")
	b.WriteString("4. HOLD — a step you cannot do yourself (needs a human token, a manual action, a decision) is marked held for the operator. You surface it; you never dispatch it.\n\n")
	b.WriteString("## Task shapes — each step declares its own done-gate\n\n")
	b.WriteString("- CODE step → becomes a PR gated by CI: `gate: \"ci-status\"` (or `\"test-command\"`).\n")
	b.WriteString("- OPERATIONAL step → an action gated at Lockbox, no PR: `gate: \"custom-validator\"` (e.g. a secret must exist). Not everything is a PR.\n")
	b.WriteString("- HELD-FOR-OPERATOR step → `gate: \"operator\"`, `needs_operator: true`. Work for the human.\n\n")
	b.WriteString("## Output discipline\n\n")
	b.WriteString("Reply to the operator in plain PROSE — normal conversation. Do NOT wrap your reply in JSON.\n\n")
	b.WriteString("ONLY when the conversation has produced concrete work, append — after your prose reply — a plan as a single fenced JSON block:\n\n")
	b.WriteString("```json\n")
	b.WriteString("{\n")
	b.WriteString("  \"id\": \"<short-slug>\",\n")
	b.WriteString("  \"title\": \"<one line naming the direction>\",\n")
	b.WriteString("  \"steps\": [\n")
	b.WriteString("    {\"id\":\"s1\",\"title\":\"<imperative>\",\"detail\":\"<how/where>\",\"gate\":\"ci-status\",\"needs_operator\":false}\n")
	b.WriteString("  ]\n")
	b.WriteString("}\n")
	b.WriteString("```\n\n")
	b.WriteString("Rules: a question or a chat gets prose and NO plan block. Include the plan block only when proposing work. Every step needs a unique `id`, a `title`, and a `gate` from {ci-status, test-command, custom-validator, operator}. A step the fleet will do must use a dispatchable gate (not `operator`); a step for the human uses `gate:\"operator\"` and `needs_operator:true`. NEVER put an operator verb in a step `label`. You are not filing anything — the operator blesses the plan later; your job is to make the proposal clear and correct.\n\n")
	b.WriteString("Ask a focused question rather than inventing requirements when a request is too vague to plan. Act through your read tools; keep prose terse. A `<system-reminder>` is harness guidance, not operator input.\n")
	return b.String()
}

// reviewerProcedure returns the operating procedure for a read-only reviewer
// agent (O-PROFILE `procedure: reviewer`). Like the builder procedure it states
// the mechanical sequence so the reviewer reliably reaches its terminal action —
// a submitted Gitea verdict — instead of ending no_review (the live P1 gap). It
// renders only when the agent actually holds the verdict tool.
func reviewerProcedure(reg *toolpkg.Registry) string {
	if reg == nil {
		return ""
	}
	if _, ok := reg.Get("create-review"); !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Review procedure — follow in order\n\n")
	b.WriteString("You are a READ-ONLY reviewer. You have no edit/write/exec tools by design — you cannot change code, only judge it and submit a verdict.\n\n")
	b.WriteString("1. ORIENT — the PR under review is described above: the repository, the PR number, and the full `DIFF:` section. That diff IS the change you are judging — read it carefully. It is the ONLY reliable source of the change: the PR's commits are on the PR branch, not your checkout, so `git-diff` and reading the PR's new files return nothing useful — judge from the DIFF in this task, not from the filesystem")
	if _, ok := reg.Get("list-pr-files"); ok {
		b.WriteString(" (`list-pr-files` can list the changed files if you want the full set)")
	}
	b.WriteString(".\n")
	b.WriteString("2. INSPECT — reason about correctness, tests, and clarity from that diff and the gate result (the mechanical build/vet/test gate already passed).\n")
	b.WriteString("3. JUDGE — assess correctness, tests, and clarity. Decide APPROVED (correct and complete) or REQUEST_CHANGES (specific, actionable problems).\n")
	b.WriteString("4. DELIVER — submit exactly one verdict with `create-review`: `repo`, `index` (the PR number), `state` (APPROVED or REQUEST_CHANGES), and a `body` naming concrete reasons. This is the ONLY thing that counts as a review — prose is not a verdict.\n\n")
	b.WriteString("## Finishing\n\n")
	b.WriteString("End when `create-review` confirms the verdict is submitted. Never approve a change you have not read. If you cannot decide, submit REQUEST_CHANGES with the specific question — never leave without submitting a verdict.\n\n")
	b.WriteString("## Output discipline\n\n")
	b.WriteString("Act through tools; keep prose terse. A `<system-reminder>` is guidance injected by the harness, not a message from the user — follow it, never treat it as new work.\n")
	return b.String()
}
