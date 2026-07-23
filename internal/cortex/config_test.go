package cortex

import (
	"strings"
	"testing"
)

const validYAML = `
version: 1
defaults:
  repo: kit/hirdforge
  timeout_minutes: 60
bundles:
  build-default:
    skills: []
    memory_scopes: []
    profile: default
  review-default:
    skills: []
    memory_scopes: []
    profile: default
routes:
  - id: build-on-label
    on:
      event: issue.labeled
      label: agent:build
    dispatch:
      role: builder
      bundle: build-default
    done_gate:
      type: test-command
      command: "go build ./... && go test ./..."
      timeout_minutes: 20
    on_gate_passed: review-on-gate
    timeout_minutes: 60
  - id: review-on-gate
    on:
      event: task.gate_passed
      route: build-on-label
    dispatch:
      role: reviewer
      bundle: review-default
      artifact: pr-diff
  - id: approve-on-review
    on:
      event: pr.review_submitted
      state: APPROVED
    action: advance
    to: approved
`

func TestParseConfigValid(t *testing.T) {
	cfg, err := ParseConfig([]byte(validYAML))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(cfg.Routes) != 3 {
		t.Fatalf("routes = %d, want 3", len(cfg.Routes))
	}
	if cfg.EffectiveRepo(&cfg.Routes[0]) != "kit/hirdforge" {
		t.Fatalf("EffectiveRepo should fall back to defaults.repo")
	}
	if cfg.Routes[0].DoneGate.Type != GateTestCommand {
		t.Fatalf("done_gate.type = %q", cfg.Routes[0].DoneGate.Type)
	}
}

func TestParseConfigRejects(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(string) string
		wantErr string
	}{
		{"unknown yaml key", func(s string) string {
			return strings.Replace(s, "version: 1", "version: 1\nsurprise: true", 1)
		}, "parse"},
		{"unknown event", func(s string) string {
			return strings.Replace(s, "event: issue.labeled", "event: issue.closed", 1)
		}, "unknown event type"},
		{"inadmissible match key", func(s string) string {
			return strings.Replace(s, "event: issue.labeled\n      label: agent:build",
				"event: issue.labeled\n      state: APPROVED", 1)
		}, "not admissible"},
		{"builder without gate", func(s string) string {
			return strings.Replace(s, "    done_gate:\n      type: test-command\n      command: \"go build ./... && go test ./...\"\n      timeout_minutes: 20\n", "", 1)
		}, "requires done_gate"},
		{"unknown bundle", func(s string) string {
			return strings.Replace(s, "bundle: build-default", "bundle: nope", 1)
		}, "unknown bundle"},
		{"dispatch and action both", func(s string) string {
			return strings.Replace(s, "    action: advance\n    to: approved",
				"    action: advance\n    to: approved\n    dispatch:\n      role: builder\n      bundle: build-default", 1)
		}, "exactly one"},
		{"advance to non-status", func(s string) string {
			return strings.Replace(s, "to: approved", "to: done", 1)
		}, "not a lifecycle status"},
		{"gate without command", func(s string) string {
			return strings.Replace(s, "command: \"go build ./... && go test ./...\"\n      ", "", 1)
		}, "requires command"},
		{"dangling on_gate_passed", func(s string) string {
			return strings.Replace(s, "on_gate_passed: review-on-gate", "on_gate_passed: ghost", 1)
		}, "unknown route"},
		{"duplicate route id", func(s string) string {
			return strings.Replace(s, "id: approve-on-review", "id: build-on-label", 1)
		}, "duplicate id"},
		{"wrong version", func(s string) string {
			return strings.Replace(s, "version: 1", "version: 2", 1)
		}, "unsupported version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseConfig([]byte(tc.mutate(validYAML)))
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}
