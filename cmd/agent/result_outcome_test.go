package main

import (
	"context"
	"testing"
)

func TestClassifyRunOutcome(t *testing.T) {
	cases := []struct {
		name    string
		content string
		reason  terminationReason
		want    runOutcome
	}{
		{
			name:    "pr_url",
			content: "Done. Opened http://gitea.local/kit/hirdforge/pulls/42 for review.",
			reason:  terminationCompleted,
			want: runOutcome{
				Kind:              outcomePR,
				PRURL:             "http://gitea.local/kit/hirdforge/pulls/42",
				PRNumber:          42,
				TerminationReason: terminationCompleted,
			},
		},
		{
			name:    "pr_number",
			content: "All set — PR #7 is up for review.",
			reason:  terminationCompleted,
			want: runOutcome{
				Kind:              outcomePR,
				PRNumber:          7,
				TerminationReason: terminationCompleted,
			},
		},
		{
			name:    "failed",
			content: "FAILED: the change does not build after three attempts.",
			reason:  terminationNoActionableOutput,
			want: runOutcome{
				Kind:              outcomeFailed,
				FailedReason:      "the change does not build after three attempts.",
				TerminationReason: terminationNoActionableOutput,
			},
		},
		{
			name:    "noop",
			content: "NOOP: the requested fix is already present on main.",
			reason:  terminationCompleted,
			want: runOutcome{
				Kind:              outcomeNoop,
				NoopReason:        "the requested fix is already present on main.",
				TerminationReason: terminationCompleted,
			},
		},
		{
			name:    "missing_signal",
			content: "I reviewed the code but have nothing conclusive to report.",
			reason:  terminationCompleted,
			want: runOutcome{
				Kind:              outcomeUnknown,
				TerminationReason: terminationCompleted,
			},
		},
		{
			name:    "degraded_termination_with_valid_content",
			content: "Opened http://gitea.local/kit/hirdforge/pulls/5",
			reason:  terminationMaxTurns,
			want: runOutcome{
				Kind:              outcomePR,
				PRURL:             "http://gitea.local/kit/hirdforge/pulls/5",
				PRNumber:          5,
				TerminationReason: terminationMaxTurns,
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyRunOutcome(c.content, c.reason)
			if got != c.want {
				t.Errorf("classifyRunOutcome(%q, %q) =\n  %+v\nwant\n  %+v", c.content, c.reason, got, c.want)
			}
		})
	}
}

// TestE2EFakePRToolOutcomeIsPR drives the fake-PR-tool e2e path and asserts the
// result-extraction seam maps the finished run to outcome kind=pr, carrying the
// PR URL the tool produced and the run's actual termination reason.
func TestE2EFakePRToolOutcomeIsPR(t *testing.T) {
	logs := captureLogs(t)

	srv := newStubInferenceServer(t,
		stubToolCall("gitea", `{"action":"create-pr","repo":"kit/hirdforge","head":"fix/widget","title":"Fix widget"}`),
		stubContent("Done. Opened the PR: "+fakePRURL),
	)

	tool := &fakeGiteaPRTool{}
	deps := harnessDeps(srv)
	deps.reg.Register(tool)
	deps.workspace = t.TempDir()

	proc := newConversationProcessor(deps)
	out, err := proc(context.Background(), "sess-outcome-pr", "", "Please create a PR for the widget fix.", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reasons := logs.terminationReasons()
	if len(reasons) == 0 {
		t.Fatal("expected at least one session_termination event")
	}
	oc := classifyRunOutcome(out, terminationReason(reasons[0]))

	if oc.Kind != outcomePR {
		t.Errorf("outcome kind = %q, want %q (content: %q)", oc.Kind, outcomePR, out)
	}
	if oc.PRURL != fakePRURL {
		t.Errorf("outcome PRURL = %q, want %q", oc.PRURL, fakePRURL)
	}
	if oc.TerminationReason != terminationCompleted {
		t.Errorf("outcome TerminationReason = %q, want %q", oc.TerminationReason, terminationCompleted)
	}
}
