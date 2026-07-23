// Package cortex is the v2 deterministic event router (PRD §4, D-PORT: built
// fresh, not ported from Workbench). Routing, dispatch construction, and
// lifecycle tracking are pure mechanics — nothing in this package calls a
// model, reads a transcript, or parses prose. See
// docs/specs/contracts/ROUTING_SCHEMA.md (approved 2026-07-23, PR #330).
package cortex

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the parsed cortex.yaml. Loaded at gateway start; a parse or
// validation error keeps the previous config (the caller enforces that —
// never a silent half-load).
type Config struct {
	Version  int               `yaml:"version" json:"version"`
	Defaults Defaults          `yaml:"defaults" json:"defaults"`
	Bundles  map[string]Bundle `yaml:"bundles" json:"bundles"`
	Routes   []Route           `yaml:"routes" json:"routes"`
}

type Defaults struct {
	Repo           string `yaml:"repo" json:"repo"`
	TimeoutMinutes int    `yaml:"timeout_minutes" json:"timeout_minutes"`
}

// Bundle is {skills, memory_scopes, profile} per the BUILDER_HARNESS v2
// amendment. Phase 1 dispatches empty skills/scopes with profile "default".
type Bundle struct {
	Skills       []string `yaml:"skills" json:"skills"`
	MemoryScopes []string `yaml:"memory_scopes" json:"memory_scopes"`
	Profile      string   `yaml:"profile" json:"profile"`
}

// Route is one ordered routing rule. Exactly one of Dispatch or Action is set.
type Route struct {
	ID             string      `yaml:"id" json:"id"`
	On             Match       `yaml:"on" json:"on"`
	Dispatch       *Dispatch   `yaml:"dispatch,omitempty" json:"dispatch,omitempty"`
	Action         string      `yaml:"action,omitempty" json:"action,omitempty"`
	To             string      `yaml:"to,omitempty" json:"to,omitempty"`
	DoneGate       *GateConfig `yaml:"done_gate,omitempty" json:"done_gate,omitempty"`
	OnGatePassed   string      `yaml:"on_gate_passed,omitempty" json:"on_gate_passed,omitempty"`
	TimeoutMinutes int         `yaml:"timeout_minutes,omitempty" json:"timeout_minutes,omitempty"`
}

// Match is the exact-equality match clause. Event is required; every other
// set field must equal the event's field exactly. Which keys are admissible
// per event type is fixed in code (admissibleMatchKeys) — an inadmissible key
// is a config error at load, not a silent skip.
type Match struct {
	Event string `yaml:"event" json:"event"`
	Label string `yaml:"label,omitempty" json:"label,omitempty"`
	Repo  string `yaml:"repo,omitempty" json:"repo,omitempty"`
	Route string `yaml:"route,omitempty" json:"route,omitempty"`
	State string `yaml:"state,omitempty" json:"state,omitempty"`
}

type Dispatch struct {
	Role     string `yaml:"role" json:"role"`
	Bundle   string `yaml:"bundle" json:"bundle"`
	Artifact string `yaml:"artifact,omitempty" json:"artifact,omitempty"`
	Carry    string `yaml:"carry,omitempty" json:"carry,omitempty"`
}

// GateConfig declares the route's mechanical done-gate (D-GATE). See
// docs/specs/contracts/DONE_GATE.md.
type GateConfig struct {
	Type           string   `yaml:"type" json:"type"`
	Command        string   `yaml:"command,omitempty" json:"command,omitempty"`
	TimeoutMinutes int      `yaml:"timeout_minutes,omitempty" json:"timeout_minutes,omitempty"`
	Contexts       []string `yaml:"contexts,omitempty" json:"contexts,omitempty"`
	Path           string   `yaml:"path,omitempty" json:"path,omitempty"`
}

const (
	EventIssueLabeled      = "issue.labeled"
	EventPRReviewSubmitted = "pr.review_submitted"
	EventTaskGatePassed    = "task.gate_passed"
	EventTaskGateFailed    = "task.gate_failed"
	EventPRMerged          = "pr.merged"
	EventOperatorDispatch  = "operator.dispatch"
)

const (
	GateTestCommand     = "test-command"
	GateCIStatus        = "ci-status"
	GateCustomValidator = "custom-validator"

	RoleBuilder  = "builder"
	RoleReviewer = "reviewer"

	ActionAdvance = "advance"
)

// admissibleMatchKeys fixes, per event type, which Match fields may be set.
// "repo" is admissible everywhere; the rest are type-specific.
var admissibleMatchKeys = map[string]map[string]bool{
	EventIssueLabeled:      {"label": true, "repo": true},
	EventPRReviewSubmitted: {"state": true, "repo": true},
	EventTaskGatePassed:    {"route": true, "repo": true},
	EventTaskGateFailed:    {"route": true, "repo": true},
	EventPRMerged:          {"repo": true},
	EventOperatorDispatch:  {"route": true, "repo": true},
}

// LoadConfig reads, strictly decodes, and validates a cortex.yaml.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cortex config: %w", err)
	}
	return ParseConfig(raw)
}

// ParseConfig strictly decodes and validates cortex.yaml bytes. Unknown YAML
// keys are an error (KnownFields), not a silent skip.
func ParseConfig(raw []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(newBytesReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("cortex config: parse: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("cortex config: %w", err)
	}
	return &cfg, nil
}

// Validate enforces the routing-schema contract's structural rules.
func (c *Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported version %d (want 1)", c.Version)
	}
	if len(c.Routes) == 0 {
		return fmt.Errorf("no routes defined")
	}
	seen := map[string]bool{}
	for i := range c.Routes {
		r := &c.Routes[i]
		if r.ID == "" {
			return fmt.Errorf("route %d: missing id", i)
		}
		if seen[r.ID] {
			return fmt.Errorf("route %q: duplicate id", r.ID)
		}
		seen[r.ID] = true

		admissible, ok := admissibleMatchKeys[r.On.Event]
		if !ok {
			return fmt.Errorf("route %q: unknown event type %q", r.ID, r.On.Event)
		}
		for key, set := range map[string]bool{
			"label": r.On.Label != "",
			"repo":  r.On.Repo != "",
			"route": r.On.Route != "",
			"state": r.On.State != "",
		} {
			if set && !admissible[key] {
				return fmt.Errorf("route %q: match key %q not admissible for event %q", r.ID, key, r.On.Event)
			}
		}

		hasDispatch := r.Dispatch != nil
		hasAction := r.Action != ""
		if hasDispatch == hasAction {
			return fmt.Errorf("route %q: exactly one of dispatch or action required", r.ID)
		}
		if hasAction {
			if r.Action != ActionAdvance {
				return fmt.Errorf("route %q: unknown action %q", r.ID, r.Action)
			}
			if !IsValidStatus(r.To) {
				return fmt.Errorf("route %q: action target %q is not a lifecycle status", r.ID, r.To)
			}
		}
		if hasDispatch {
			if r.Dispatch.Role != RoleBuilder && r.Dispatch.Role != RoleReviewer {
				return fmt.Errorf("route %q: unknown role %q", r.ID, r.Dispatch.Role)
			}
			if _, ok := c.Bundles[r.Dispatch.Bundle]; !ok {
				return fmt.Errorf("route %q: unknown bundle %q", r.ID, r.Dispatch.Bundle)
			}
			if r.Dispatch.Role == RoleBuilder && r.DoneGate == nil {
				return fmt.Errorf("route %q: builder dispatch requires done_gate", r.ID)
			}
		}
		if r.DoneGate != nil {
			if err := r.DoneGate.validate(); err != nil {
				return fmt.Errorf("route %q: done_gate: %w", r.ID, err)
			}
		}
		if r.OnGatePassed != "" && !routeIDWillExist(c.Routes, r.OnGatePassed) {
			return fmt.Errorf("route %q: on_gate_passed references unknown route %q", r.ID, r.OnGatePassed)
		}
	}
	return nil
}

func (g *GateConfig) validate() error {
	switch g.Type {
	case GateTestCommand:
		if g.Command == "" {
			return fmt.Errorf("test-command gate requires command")
		}
	case GateCIStatus:
		if len(g.Contexts) == 0 {
			return fmt.Errorf("ci-status gate requires contexts")
		}
	case GateCustomValidator:
		if g.Path == "" {
			return fmt.Errorf("custom-validator gate requires path")
		}
	default:
		return fmt.Errorf("unknown gate type %q", g.Type)
	}
	return nil
}

func routeIDWillExist(routes []Route, id string) bool {
	for i := range routes {
		if routes[i].ID == id {
			return true
		}
	}
	return false
}

// RouteByID returns the route with the given id, or nil.
func (c *Config) RouteByID(id string) *Route {
	for i := range c.Routes {
		if c.Routes[i].ID == id {
			return &c.Routes[i]
		}
	}
	return nil
}

// EffectiveRepo resolves a route's repo constraint: the route's own, else the
// config default, else "" (unconstrained).
func (c *Config) EffectiveRepo(r *Route) string {
	if r.On.Repo != "" {
		return r.On.Repo
	}
	return c.Defaults.Repo
}
