package steward

import (
	"strings"
	"testing"
)

func TestParseChatOutputExtractsFromProse(t *testing.T) {
	// Models wrap JSON in fences and commentary; the object must still be found.
	raw := "Sure!\n```json\n{\"reply\":\"hello\"}\n```\nHope that helps."
	c, err := ParseChatOutput(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Reply != "hello" || c.Plan != nil {
		t.Fatalf("parsed = %+v", c)
	}
	// Nested braces inside strings must not confuse the extractor.
	raw2 := `{"reply":"use {\"a\":1} like this"}`
	if c2, err := ParseChatOutput(raw2); err != nil || !strings.Contains(c2.Reply, `{"a":1}`) {
		t.Fatalf("nested-brace parse failed: %+v %v", c2, err)
	}
	if _, err := ParseChatOutput("no json here"); err == nil {
		t.Fatal("output with no JSON object must error")
	}
}

func TestParseChatOutputDecodesAPlan(t *testing.T) {
	raw := `{"reply":"here is the plan","plan":{"id":"p1","title":"Serve studio over TLS","steps":[
		{"id":"s1","title":"Issue cert","detail":"cert-manager Certificate","gate":"custom-validator","needs_operator":false},
		{"id":"s2","title":"Caddy vhost","detail":"agent PR","gate":"ci-status","needs_operator":false},
		{"id":"s3","title":"DNS A record","detail":"Technitium","gate":"operator","needs_operator":true}
	]}}`
	c, err := ParseChatOutput(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
	if c.Plan == nil || len(c.Plan.Steps) != 3 {
		t.Fatalf("plan not decoded: %+v", c.Plan)
	}
	// Partial dispatch: the two fleet steps are dispatchable, the operator step held.
	disp := c.Plan.DispatchSteps()
	if len(disp) != 2 {
		t.Fatalf("DispatchSteps = %d, want 2 (s3 is held)", len(disp))
	}
	for _, s := range disp {
		if s.ID == "s3" {
			t.Fatal("operator-held step s3 must not be dispatchable")
		}
	}
	// Task shapes: s1 is operational (Lockbox), s2 is code (PR).
	if !c.Plan.Steps[0].IsOperational() || !c.Plan.Steps[1].IsCode() {
		t.Fatalf("task-shape classification wrong: %+v", c.Plan.Steps)
	}
}

// TestValidateRejectsMalformedPlans: a plan the UI or a blessing would consume
// must be well-formed. Every hostile or broken shape is refused.
func TestValidateRejectsMalformedPlans(t *testing.T) {
	step := func(id, title string, g Gate, held bool) Step {
		return Step{ID: id, Title: title, Gate: g, NeedsOperator: held}
	}
	bad := []struct {
		name string
		c    ChatOutput
	}{
		{"no reply", ChatOutput{}},
		{"plan without title", ChatOutput{Reply: "ok", Plan: &Plan{Steps: []Step{step("s1", "t", GateCIStatus, false)}}}},
		{"plan without steps", ChatOutput{Reply: "ok", Plan: &Plan{Title: "t"}}},
		{"step without id", ChatOutput{Reply: "ok", Plan: &Plan{Title: "t", Steps: []Step{step("", "t", GateCIStatus, false)}}}},
		{"step without title", ChatOutput{Reply: "ok", Plan: &Plan{Title: "t", Steps: []Step{step("s1", "", GateCIStatus, false)}}}},
		{"duplicate step id", ChatOutput{Reply: "ok", Plan: &Plan{Title: "t", Steps: []Step{step("s1", "a", GateCIStatus, false), step("s1", "b", GateTestCmd, false)}}}},
		{"unknown gate", ChatOutput{Reply: "ok", Plan: &Plan{Title: "t", Steps: []Step{step("s1", "t", Gate("teleport"), false)}}}},
		{"fleet step, non-dispatchable gate", ChatOutput{Reply: "ok", Plan: &Plan{Title: "t", Steps: []Step{step("s1", "t", GateOperator, false)}}}},
		{"oversized detail", ChatOutput{Reply: "ok", Plan: &Plan{Title: "t", Steps: []Step{{ID: "s1", Title: "t", Gate: GateCIStatus, Detail: strings.Repeat("x", maxStepDetail+1)}}}}},
		{"step reaches for an operator verb", ChatOutput{Reply: "ok", Plan: &Plan{Title: "t", Steps: []Step{{ID: "s1", Title: "t", Gate: GateCIStatus, Label: "agent:merge"}}}}},
	}
	for _, tc := range bad {
		if err := tc.c.Validate(); err == nil {
			t.Errorf("%s: must be rejected, was accepted", tc.name)
		}
	}
}

// A plain conversational turn — reply, no plan — is valid. Most turns are this.
func TestValidateAllowsPlainReply(t *testing.T) {
	c := ChatOutput{Reply: "You can retry that task from the task detail view."}
	if err := c.Validate(); err != nil {
		t.Fatalf("a plain reply must be valid: %v", err)
	}
}

// An operator-held step may carry the GateOperator marker (no automated gate) and
// must be accepted — it is surfaced, not dispatched.
func TestValidateAllowsOperatorHeldStep(t *testing.T) {
	c := ChatOutput{Reply: "ok", Plan: &Plan{Title: "t", Steps: []Step{
		{ID: "s1", Title: "Add your DNS token", Gate: GateOperator, NeedsOperator: true},
	}}}
	if err := c.Validate(); err != nil {
		t.Fatalf("operator-held step rejected: %v", err)
	}
	if c.Plan.Steps[0].Dispatchable() {
		t.Fatal("operator-held step must not be dispatchable")
	}
}
