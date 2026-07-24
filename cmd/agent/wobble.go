package main

import (
	"strings"

	toolpkg "git.hirdforge.com/kit/hirdforge/pkg/tools"
)

// toolResultSignalsWobble reports whether a SUCCESSFUL tool result is
// nonetheless a drift signal that should fire an M6 re-anchor: a git-commit that
// staged nothing (the model may misread it as done), a shell-git write redirected
// to the verified tool, or an M3 edit rescue (the model's precision is slipping).
//
// Extracted from the conversation loop so those branches don't inflate its
// cyclomatic complexity (it sits at the gocyclo ceiling until it is decomposed).
func toolResultSignalsWobble(toolName, output string) bool {
	trimmed := strings.TrimSpace(output)
	switch toolName {
	case "git-commit":
		return strings.HasPrefix(trimmed, toolpkg.GitCommitNoChangesPrefix)
	case "exec":
		return strings.HasPrefix(trimmed, toolpkg.ExecGitRedirectPrefix)
	case "edit":
		return strings.Contains(output, toolpkg.EditRescueMarker)
	default:
		return false
	}
}
