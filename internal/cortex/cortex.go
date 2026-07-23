package cortex

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

// Cortex is the engine: config + store + the in-memory decision ring buffer
// that backs GET /log (the table is the reload-rehydration source).
type Cortex struct {
	mu    sync.RWMutex
	cfg   *Config
	store Store

	ring    []Decision
	ringCap int
}

func New(cfg *Config, store Store) *Cortex {
	return &Cortex{cfg: cfg, store: store, ringCap: 500}
}

// Config returns the currently loaded config (GET /routes renders it verbatim).
func (c *Cortex) Config() *Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg
}

// Reload swaps in a new validated config. On error the previous config stays —
// never a silent half-load (ROUTING_SCHEMA.md).
func (c *Cortex) Reload(path string) error {
	cfg, err := LoadConfig(path)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
	log.Printf("cortex: config reloaded: %d routes", len(cfg.Routes))
	return nil
}

// HandleEvent is the single entry point for every event Cortex consumes.
// It records a decision for every evaluation — match or no-match — and for
// dispatch routes creates the queued task (P1.1 scope; dispatch execution is
// P1.3, gate/advance wiring P1.5+).
func (c *Cortex) HandleEvent(ev Event) (Decision, error) {
	c.mu.RLock()
	cfg := c.cfg
	c.mu.RUnlock()

	route, reason := MatchRoute(cfg, ev)
	d := Decision{Event: ev, Reason: reason, At: time.Now()}

	if route == nil {
		c.recordDecision(d)
		return d, nil
	}
	d.MatchedRoute = route.ID

	switch {
	case route.Dispatch != nil:
		bundle := cfg.Bundles[route.Dispatch.Bundle]
		gateSnapshot := json.RawMessage(`{}`)
		if route.DoneGate != nil {
			if b, err := json.Marshal(struct {
				Config *GateConfig `json:"config"`
			}{route.DoneGate}); err == nil {
				gateSnapshot = b
			}
		}
		task := &TaskRecord{
			ID:          NewTaskID(),
			RouteID:     route.ID,
			IssueRepo:   ev.Repo,
			IssueNumber: ev.IssueNumber,
			IssueTitle:  ev.IssueTitle,
			Bundle:      bundle,
			DoneGate:    gateSnapshot,
		}
		if minutes := effectiveTimeout(cfg, route); minutes > 0 {
			t := time.Now().Add(time.Duration(minutes) * time.Minute)
			task.TimeoutAt = &t
		}
		cause := Cause{Kind: causeKindForEvent(ev.Type), Detail: map[string]any{"event": ev.Type, "route": route.ID}}
		if err := c.store.CreateTask(task, reason, cause); err != nil {
			return d, fmt.Errorf("cortex: create task: %w", err)
		}
		d.TaskID = task.ID
		d.Reason = fmt.Sprintf("%s -> task %s queued", reason, task.ID)

	case route.Action == ActionAdvance:
		// Lifecycle-advance routes are wired when their producing mechanics
		// land (P1.5 gate events, P1.6 review webhook, P1.7 merge webhook).
		// Until then the decision log says so explicitly — matched, not acted.
		d.Reason = fmt.Sprintf("%s -> advance to %s (not yet wired in P1.1)", reason, route.To)
	}

	c.recordDecision(d)
	return d, nil
}

func (c *Cortex) recordDecision(d Decision) {
	if err := c.store.RecordDecision(d); err != nil {
		// A decision that cannot be persisted is still observable via the
		// ring + log line — and the failure itself is loud, never swallowed.
		log.Printf("cortex: ERROR persisting decision: %v (decision: %s)", err, d.Reason)
	}
	c.mu.Lock()
	c.ring = append(c.ring, d)
	if len(c.ring) > c.ringCap {
		c.ring = c.ring[len(c.ring)-c.ringCap:]
	}
	c.mu.Unlock()
	log.Printf("cortex: %s", d.Reason)
}

// RecentDecisions returns the newest-first ring buffer contents for GET /log.
func (c *Cortex) RecentDecisions(limit int) []Decision {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if limit <= 0 || limit > c.ringCap {
		limit = c.ringCap
	}
	n := len(c.ring)
	start := n - limit
	if start < 0 {
		start = 0
	}
	out := make([]Decision, 0, n-start)
	for i := n - 1; i >= start; i-- {
		out = append(out, c.ring[i])
	}
	return out
}

// Store exposes the underlying store for the gateway's read endpoints.
func (c *Cortex) Store() Store { return c.store }

func effectiveTimeout(cfg *Config, r *Route) int {
	if r.TimeoutMinutes > 0 {
		return r.TimeoutMinutes
	}
	return cfg.Defaults.TimeoutMinutes
}

func causeKindForEvent(eventType string) string {
	switch eventType {
	case EventOperatorDispatch:
		return CauseOperator
	case EventTaskGatePassed, EventTaskGateFailed:
		return CauseGate
	default:
		return CauseWebhook
	}
}
