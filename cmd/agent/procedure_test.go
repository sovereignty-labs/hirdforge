package main

import (
	"strings"
	"testing"

	toolpkg "git.hirdforge.com/kit/hirdforge/pkg/tools"
)

// builderReg builds a registry holding the delivery tools a builder has.
func builderReg(t *testing.T, withTodo bool) *toolpkg.Registry {
	t.Helper()
	reg := toolpkg.NewRegistry()
	reg.Register(&flowFakeGitCommitTool{branch: "b"})
	reg.Register(&flowFakeCreatePRTool{url: "u"})
	if withTodo {
		reg.Register(&todoTool{})
	}
	return reg
}

func TestBuilderProcedureOnlyForBuilders(t *testing.T) {
	if got := builderProcedure(nil); got != "" {
		t.Error("nil registry must yield no procedure")
	}
	// A reviewer-shaped agent (no git-commit / create-pr) gets nothing.
	reviewer := toolpkg.NewRegistry()
	reviewer.Register(&fakeNoopTool{})
	if got := builderProcedure(reviewer); got != "" {
		t.Errorf("non-builder must get no procedure, got: %q", got)
	}
	// Only half the delivery path is not a builder either.
	half := toolpkg.NewRegistry()
	half.Register(&flowFakeGitCommitTool{branch: "b"})
	if got := builderProcedure(half); got != "" {
		t.Errorf("agent without create-pr must get no procedure, got: %q", got)
	}
}

func TestBuilderProcedurePinsTheDeliveryMechanics(t *testing.T) {
	p := builderProcedure(builderReg(t, true))
	if p == "" {
		t.Fatal("builder must get a procedure")
	}
	// The exact mechanics the baselines failed on.
	for _, want := range []string{
		"ORIENT", "SURVEY", "PLAN", "IMPLEMENT", "VERIFY", "DELIVER",
		"Never guess a repo path",
		"git-commit",
		"Pushed to <branch>",
		"create-pr",
		"base: main",
		"gofmt -w", // the one gate-miss in the first valid run was unformatted code
	} {
		if !strings.Contains(p, want) {
			t.Errorf("procedure missing %q", want)
		}
	}
	// Shell git for writes must be explicitly ruled out.
	if !strings.Contains(p, "shell `git commit`") && !strings.Contains(p, "Do NOT use shell") {
		t.Errorf("procedure must rule out shell git writes: %q", p)
	}
}

func TestBuilderProcedureStatesFullTerminalOutcomeSpace(t *testing.T) {
	p := builderProcedure(builderReg(t, true))
	// Anti-dilution: all four outcomes present, and no fabricated PRs.
	for _, want := range []string{"PR URL", "FAILED:", "NOOP:", "QUESTION:"} {
		if !strings.Contains(p, want) {
			t.Errorf("procedure must offer terminal outcome %q", want)
		}
	}
	if !strings.Contains(p, "Never report a PR you did not actually create") {
		t.Error("procedure must forbid fabricating a PR")
	}
	// Fail open: deduce from the repo before asking.
	if !strings.Contains(p, "Inspect the repo first") {
		t.Error("procedure must require environment-deduction before asking")
	}
}

func TestBuilderProcedureMentionsTodoOnlyWhenAvailable(t *testing.T) {
	withTodo := builderProcedure(builderReg(t, true))
	if !strings.Contains(withTodo, "`todo`") {
		t.Error("expected the todo step when the tool is registered")
	}
	withoutTodo := builderProcedure(builderReg(t, false))
	if strings.Contains(withoutTodo, "`todo`") {
		t.Error("must not instruct use of a tool the agent does not have")
	}
	if !strings.Contains(withoutTodo, "PLAN") {
		t.Error("planning step should still exist without the todo tool")
	}
}
