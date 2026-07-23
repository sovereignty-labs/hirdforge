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

	// OnTaskQueued, when set, is invoked after a dispatch route creates a
	// queued task, and on a revise re-dispatch (the gateway hooks the
	// Dispatcher here, in a goroutine).
	OnTaskQueued func(route *Route, taskID string)
	// OnReviewerDispatch runs the reviewer leg for a task already in review.
	OnReviewerDispatch func(route *Route, ev Event)
	// OnTaskAdvanced fires after an advance route moved a task (the gateway
	// hooks Lockbox enqueueing on `approved` and issue-close on `validated`).
	OnTaskAdvanced func(taskID, to string)
	// OnDecision fires for every recorded decision (the cortex.decision
	// live stream; the ring + table remain the poll/rehydrate sources).
	OnDecision func(Decision)

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
	case route.Dispatch != nil && route.Dispatch.Role == RoleReviewer && ev.TaskID != "":
		// Task-scoped reviewer dispatch (P1.6): the reviewer attaches to THE
		// task — no second task row. Verdict routing arrives later as a
		// pr.review_submitted webhook.
		d.TaskID = ev.TaskID
		d.Reason = fmt.Sprintf("%s -> reviewer dispatch for task %s", reason, ev.TaskID)
		if c.OnReviewerDispatch != nil {
			c.OnReviewerDispatch(route, ev)
		}

	case route.Dispatch != nil && route.Dispatch.Role == RoleBuilder && ev.Type == EventPRReviewSubmitted:
		// The sanctioned revise path (D-LESSONS #3): REQUEST_CHANGES routes
		// back to a builder on the SAME task, with the reviewer's feedback as
		// failure context — never a dead end, never a blind retry.
		task, err := c.store.FindTaskByPR(ev.Repo, ev.PRNumber)
		if err != nil {
			d.Reason = fmt.Sprintf("%s -> no task for PR %s#%d: %v", reason, ev.Repo, ev.PRNumber, err)
			break
		}
		fc, _ := json.Marshal(FailureContext{
			PriorAgent:       task.Agent,
			PriorAttempt:     task.Attempt,
			Reason:           "changes_requested",
			ReviewerFeedback: nonEmpty(ev.ReviewBody),
		})
		if err := c.store.PrepareRetry(task.ID, fc); err != nil {
			return d, fmt.Errorf("cortex: prepare revise: %w", err)
		}
		cause := Cause{Kind: CauseWebhook, Detail: map[string]any{"review_state": ev.ReviewState, "reviewer": ev.Actor}}
		if err := c.store.Transition(task.ID, StatusFailed,
			fmt.Sprintf("changes_requested by %s on PR #%d — revise dispatch follows", orDash(ev.Actor), ev.PRNumber),
			cause); err != nil {
			return d, fmt.Errorf("cortex: revise transition: %w", err)
		}
		d.TaskID = task.ID
		d.Reason = fmt.Sprintf("%s -> revise dispatch for task %s (attempt %d)", reason, task.ID, task.Attempt+1)
		if c.OnTaskQueued != nil {
			c.OnTaskQueued(route, task.ID)
		}

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
		if c.OnTaskQueued != nil {
			c.OnTaskQueued(route, task.ID)
		}

	case route.Action == ActionAdvance:
		// Pure lifecycle advance (P1.6/P1.7): find THE task by the PR the
		// webhook names, transition with the webhook as the mechanical cause.
		task, err := c.store.FindTaskByPR(ev.Repo, ev.PRNumber)
		if err != nil {
			d.Reason = fmt.Sprintf("%s -> advance to %s impossible: %v", reason, route.To, err)
			break
		}
		cause := Cause{Kind: CauseWebhook, Detail: map[string]any{"event": ev.Type, "actor": ev.Actor, "review_state": ev.ReviewState}}
		why := fmt.Sprintf("%s by %s on PR #%d", ev.Type, orDash(ev.Actor), ev.PRNumber)
		if err := c.store.Transition(task.ID, route.To, why, cause); err != nil {
			// An advance that violates the lifecycle table is loud, not silent.
			d.Reason = fmt.Sprintf("%s -> advance to %s REJECTED: %v", reason, route.To, err)
			break
		}
		d.TaskID = task.ID
		d.Reason = fmt.Sprintf("%s -> task %s advanced to %s", reason, task.ID, route.To)
		if c.OnTaskAdvanced != nil {
			c.OnTaskAdvanced(task.ID, route.To)
		}
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
	onDecision := c.OnDecision
	c.mu.Unlock()
	if onDecision != nil {
		onDecision(d)
	}
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

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

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
