package cortex

import (
	"strings"
	"testing"
)

func mustConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := ParseConfig([]byte(validYAML))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	return cfg
}

func TestMatchRouteTable(t *testing.T) {
	cfg := mustConfig(t)
	cases := []struct {
		name      string
		ev        Event
		wantRoute string // "" = no-match
	}{
		{"build label matches", Event{Type: EventIssueLabeled, Repo: "kit/hirdforge", Label: "agent:build", IssueNumber: 7}, "build-on-label"},
		{"wrong label no-match", Event{Type: EventIssueLabeled, Repo: "kit/hirdforge", Label: "bug", IssueNumber: 7}, ""},
		{"wrong repo no-match (defaults.repo constrains)", Event{Type: EventIssueLabeled, Repo: "kit/other", Label: "agent:build"}, ""},
		{"gate-passed routes to reviewer", Event{Type: EventTaskGatePassed, Repo: "kit/hirdforge", RouteID: "build-on-label", TaskID: "hf-x"}, "review-on-gate"},
		{"gate-passed from other route no-match", Event{Type: EventTaskGatePassed, Repo: "kit/hirdforge", RouteID: "other"}, ""},
		{"review approved advances", Event{Type: EventPRReviewSubmitted, Repo: "kit/hirdforge", ReviewState: "APPROVED", PRNumber: 9}, "approve-on-review"},
		{"review changes-requested no-match here", Event{Type: EventPRReviewSubmitted, Repo: "kit/hirdforge", ReviewState: "REQUEST_CHANGES"}, ""},
		{"unknown event type no-match", Event{Type: "issue.closed", Repo: "kit/hirdforge"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			route, reason := MatchRoute(cfg, tc.ev)
			got := ""
			if route != nil {
				got = route.ID
			}
			if got != tc.wantRoute {
				t.Fatalf("matched %q, want %q (reason: %s)", got, tc.wantRoute, reason)
			}
			if route == nil && !strings.HasPrefix(reason, "no-match:") {
				t.Fatalf("no-match reason must be labeled, got %q", reason)
			}
			if route != nil && !strings.HasPrefix(reason, "matched "+route.ID) {
				t.Fatalf("match reason must name the route, got %q", reason)
			}
		})
	}
}

// TestMatchRouteDeterministic pins ROUTING_SCHEMA determinism obligation #1:
// same config + same event => same decision, every time.
func TestMatchRouteDeterministic(t *testing.T) {
	cfg := mustConfig(t)
	ev := Event{Type: EventIssueLabeled, Repo: "kit/hirdforge", Label: "agent:build", IssueNumber: 42}
	firstRoute, firstReason := MatchRoute(cfg, ev)
	for i := 0; i < 100; i++ {
		route, reason := MatchRoute(cfg, ev)
		if route != firstRoute || reason != firstReason {
			t.Fatalf("iteration %d: decision changed: %v %q", i, route, reason)
		}
	}
}

// TestMatchRouteFirstMatchWins pins ordered evaluation.
func TestMatchRouteFirstMatchWins(t *testing.T) {
	yaml := strings.Replace(validYAML,
		"routes:",
		`routes:
  - id: shadow-first
    on:
      event: issue.labeled
      label: agent:build
    dispatch:
      role: builder
      bundle: build-default
    done_gate:
      type: test-command
      command: "true"`, 1)
	cfg, err := ParseConfig([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	route, _ := MatchRoute(cfg, Event{Type: EventIssueLabeled, Repo: "kit/hirdforge", Label: "agent:build"})
	if route == nil || route.ID != "shadow-first" {
		t.Fatalf("first matching route must win, got %v", route)
	}
}
