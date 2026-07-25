package steward

import (
	"strings"
	"testing"
)

func TestParseChatOutputCleanJSON(t *testing.T) {
	// A capable model may emit one clean JSON object; honor it verbatim.
	c, err := ParseChatOutput(`{"reply":"hello"}`)
	if err != nil || c.Reply != "hello" || c.Plan != nil {
		t.Fatalf("clean JSON reply: %+v %v", c, err)
	}
	// A fenced whole-{reply} object with no prose is still usable.
	if c2, err := ParseChatOutput("```json\n{\"reply\":\"fenced hi\"}\n```"); err != nil || c2.Reply != "fenced hi" {
		t.Fatalf("fenced reply object: %+v %v", c2, err)
	}
	// Nested braces inside strings must not confuse the extractor.
	if c3, err := ParseChatOutput(`{"reply":"use {\"a\":1} like this"}`); err != nil || !strings.Contains(c3.Reply, `{"a":1}`) {
		t.Fatalf("nested-brace parse failed: %+v %v", c3, err)
	}
	// Plain prose with no JSON at all is now a valid reply-only turn.
	if c4, err := ParseChatOutput("no json here, just talking"); err != nil || c4.Reply != "no json here, just talking" {
		t.Fatalf("prose reply: %+v %v", c4, err)
	}
	// Empty output has no usable reply.
	if _, err := ParseChatOutput("   "); err == nil {
		t.Fatal("empty output must error")
	}
}

// A chatty/reasoning model replies in prose (no JSON wrapper); the whole prose is
// the reply. This is the qwen-reserved shape that broke the strict parser.
func TestParseChatOutputAcceptsProseReply(t *testing.T) {
	raw := "Depends on what you mean! If it's software, the \"weather\" is usually coffee-fueled. What's your build scene?"
	c, err := ParseChatOutput(raw)
	if err != nil {
		t.Fatalf("prose reply rejected: %v", err)
	}
	if c.Plan != nil || !strings.Contains(c.Reply, "coffee-fueled") {
		t.Fatalf("prose not taken as reply: %+v", c)
	}
}

// A trailing JSON echo (some models append "{\"reply\":...}" after their prose) is
// stripped so the reply is the clean prose, not a duplicate.
func TestParseChatOutputStripsTrailingJSONEcho(t *testing.T) {
	raw := "Here's my answer to you.\n{\"reply\":\"Here's my answer to you.\"}"
	c, err := ParseChatOutput(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if strings.Count(c.Reply, "Here's my answer") != 1 {
		t.Fatalf("trailing JSON echo not stripped: %q", c.Reply)
	}
}

// Prose reply plus a plan in a fenced ```json block: reply is the prose, plan is
// parsed from the fence.
func TestParseChatOutputProsePlusFencedPlan(t *testing.T) {
	raw := "Sure, here's how I'd do it:\n\n```json\n{\"id\":\"tls\",\"title\":\"Serve over TLS\",\"steps\":[{\"id\":\"s1\",\"title\":\"cert\",\"gate\":\"custom-validator\",\"needs_operator\":false}]}\n```\n\nSound good?"
	c, err := ParseChatOutput(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Plan == nil || c.Plan.ID != "tls" || len(c.Plan.Steps) != 1 {
		t.Fatalf("fenced plan not parsed: %+v", c.Plan)
	}
	if !strings.Contains(c.Reply, "here's how I'd do it") || strings.Contains(c.Reply, "```") {
		t.Fatalf("reply should be the prose with the fence removed: %q", c.Reply)
	}
}

// A malformed plan block (unescaped quotes) degrades to a reply-only turn — safe,
// no work — rather than failing the whole turn.
func TestParseChatOutputMalformedPlanDegradesToReply(t *testing.T) {
	raw := "Here's the idea.\n```json\n{\"id\":\"x\",\"title\":\"the \"broken\" plan\",\"steps\":[]}\n```"
	c, err := ParseChatOutput(raw)
	if err != nil {
		t.Fatalf("should not error, should degrade: %v", err)
	}
	if c.Plan != nil {
		t.Fatalf("malformed plan must not survive as a plan: %+v", c.Plan)
	}
	if !strings.Contains(c.Reply, "Here's the idea") {
		t.Fatalf("reply lost: %q", c.Reply)
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
