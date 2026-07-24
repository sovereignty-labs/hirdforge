package main

import (
	"testing"

	toolpkg "git.hirdforge.com/kit/hirdforge/pkg/tools"
)

func TestToolResultSignalsWobble(t *testing.T) {
	cases := []struct {
		tool, out string
		want      bool
	}{
		{"git-commit", toolpkg.GitCommitNoChangesPrefix + " — nothing staged", true},
		{"exec", toolpkg.ExecGitRedirectPrefix + " for `git push`.", true},
		{"edit", "Edited f.go: replaced 3 bytes (" + toolpkg.EditRescueMarker + "whitespace-normalized strategy)", true},
		{"git-commit", "Committed: x\nPushed to b", false},
		{"exec", "total 4\nfile.go", false},
		{"edit", "Edited f.go: replaced 3 bytes with 4 bytes", false},
		{"read", "1: package main", false},
	}
	for _, c := range cases {
		if got := toolResultSignalsWobble(c.tool, c.out); got != c.want {
			t.Errorf("toolResultSignalsWobble(%q, %.30q) = %v, want %v", c.tool, c.out, got, c.want)
		}
	}
}
