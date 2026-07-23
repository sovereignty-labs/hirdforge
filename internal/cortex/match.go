package cortex

import "fmt"

// MatchRoute is the routing decision: a pure function over (config, event).
// Ordered first-match-wins; every comparison is exact string equality. No
// regex, no model input, ever (ROUTING_SCHEMA.md determinism obligation #1).
// Returns the matched route (nil for no-match) and the human-legible reason.
func MatchRoute(cfg *Config, ev Event) (*Route, string) {
	for i := range cfg.Routes {
		r := &cfg.Routes[i]
		if r.On.Event != ev.Type {
			continue
		}
		if repo := cfg.EffectiveRepo(r); repo != "" && repo != ev.Repo {
			continue
		}
		if r.On.Label != "" && r.On.Label != ev.Label {
			continue
		}
		if r.On.State != "" && r.On.State != ev.ReviewState {
			continue
		}
		if r.On.Route != "" && r.On.Route != ev.RouteID {
			continue
		}
		return r, matchReason(r, ev)
	}
	return nil, fmt.Sprintf("no-match: no route for event %s%s", ev.Type, eventDetail(ev))
}

func matchReason(r *Route, ev Event) string {
	return fmt.Sprintf("matched %s: event %s%s", r.ID, ev.Type, eventDetail(ev))
}

func eventDetail(ev Event) string {
	switch ev.Type {
	case EventIssueLabeled:
		return fmt.Sprintf(" label %q on %s#%d", ev.Label, ev.Repo, ev.IssueNumber)
	case EventPRReviewSubmitted:
		return fmt.Sprintf(" state %s on %s#%d", ev.ReviewState, ev.Repo, ev.PRNumber)
	case EventPRMerged:
		return fmt.Sprintf(" %s#%d", ev.Repo, ev.PRNumber)
	case EventTaskGatePassed, EventTaskGateFailed:
		return fmt.Sprintf(" task %s (route %s)", ev.TaskID, ev.RouteID)
	case EventOperatorDispatch:
		return fmt.Sprintf(" route %s", ev.RouteID)
	}
	return ""
}
