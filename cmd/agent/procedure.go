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
	b.WriteString("   a. Call `git-commit` with the repo directory, a clear message, and a branch.\n")
	b.WriteString("      Do NOT use shell `git commit` / `git push` — they bypass push verification and will not work here.\n")
	b.WriteString("   b. Read `git-commit`'s output for the branch it actually pushed — it may differ from what you asked for, and it prints `Pushed to <branch>`.\n")
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

// reviewerProcedure returns the operating procedure for a read-only reviewer
// agent (O-PROFILE `procedure: reviewer`). Stub for P2.6 — the full review
// procedure (Orient → Read diff → Judge → Submit verdict via create-review) is
// built in P2.7. Returns "" until then, so a reviewer dispatch simply runs
// without a procedure block rather than mis-rendering the builder's.
func reviewerProcedure(reg *toolpkg.Registry) string {
	_ = reg
	return ""
}
