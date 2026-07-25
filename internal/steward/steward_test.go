package steward

import (
	"strings"
	"testing"
)

func TestParseProposalExtractsFromProse(t *testing.T) {
	// Models wrap JSON in fences and commentary; the object must still be found.
	raw := "Sure!\n```json\n{\"intent\":\"chat\",\"reply\":\"hello\"}\n```\nHope that helps."
	p, err := ParseProposal(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Intent != IntentChat || p.Reply != "hello" {
		t.Fatalf("parsed = %+v", p)
	}
	// Nested braces inside strings must not confuse the extractor.
	raw2 := `{"intent":"chat","reply":"use {\"a\":1} like this"}`
	if p2, err := ParseProposal(raw2); err != nil || !strings.Contains(p2.Reply, `{"a":1}`) {
		t.Fatalf("nested-brace parse failed: %+v %v", p2, err)
	}
	if _, err := ParseProposal("no json here"); err == nil {
		t.Fatal("output with no JSON object must error")
	}
}

// TestValidateRejectsUnexecutableProposals is the safety test: anything the
// validator lets through is about to be ACTED ON, so every malformed or hostile
// shape must be refused.
func TestValidateRejectsUnexecutableProposals(t *testing.T) {
	good := Proposal{Intent: IntentCreateIssue, Reply: "filed it",
		Issue: &IssueProposal{Title: "Add X", Body: "Add X to pkg/tools. Acceptance: tests pass."}}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid proposal rejected: %v", err)
	}

	bad := []struct {
		name string
		p    Proposal
	}{
		{"unknown intent", Proposal{Intent: "delete_everything", Reply: "ok"}},
		{"create without issue", Proposal{Intent: IntentCreateIssue, Reply: "ok"}},
		{"issue without title", Proposal{Intent: IntentCreateIssue, Reply: "ok", Issue: &IssueProposal{Body: "b"}}},
		{"issue without body", Proposal{Intent: IntentCreateIssue, Reply: "ok", Issue: &IssueProposal{Title: "t"}}},
		{"chat carrying an issue", Proposal{Intent: IntentChat, Reply: "ok", Issue: &IssueProposal{Title: "t", Body: "b"}}},
		{"status carrying an issue", Proposal{Intent: IntentStatusQuery, Reply: "ok", Issue: &IssueProposal{Title: "t", Body: "b"}}},
		{"no reply", Proposal{Intent: IntentChat}},
		{"oversized body", Proposal{Intent: IntentCreateIssue, Reply: "ok",
			Issue: &IssueProposal{Title: "t", Body: strings.Repeat("x", maxIssueBody+1)}}},
		{"reaches for an operator verb", Proposal{Intent: IntentCreateIssue, Reply: "ok",
			Issue: &IssueProposal{Title: "t", Body: "b", Label: "agent:merge"}}},
	}
	for _, tc := range bad {
		if err := tc.p.Validate(); err == nil {
			t.Errorf("%s: must be rejected, was accepted", tc.name)
		}
	}
}

// TestValidateAllowsDiscussingControlVerbs: refusing an operator verb as an
// ACTION must not stop the Steward from talking about one.
func TestValidateAllowsDiscussingControlVerbs(t *testing.T) {
	p := Proposal{Intent: IntentChat, Reply: "You can retry that task from the task detail view."}
	if err := p.Validate(); err != nil {
		t.Fatalf("discussing a control verb in prose must be fine: %v", err)
	}
}
