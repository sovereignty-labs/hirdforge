package cortex

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemStore is the in-memory Store used by tests (and only tests). It enforces
// the same transition invariants as PGStore — a test passing against MemStore
// must not be passable by code that skips CheckTransition.
type MemStore struct {
	mu          sync.Mutex
	tasks       map[string]*TaskRecord
	transitions map[string][]TransitionRecord
	decisions   []Decision
}

func NewMemStore() *MemStore {
	return &MemStore{
		tasks:       map[string]*TaskRecord{},
		transitions: map[string][]TransitionRecord{},
	}
}

func (m *MemStore) CreateTask(t *TaskRecord, reason string, cause Cause) error {
	if err := CheckTransition("", StatusQueued, cause); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.tasks[t.ID]; exists {
		return fmt.Errorf("cortex memstore: duplicate task id %s", t.ID)
	}
	cp := *t
	cp.Status = StatusQueued
	cp.Attempt = 1
	now := time.Now()
	cp.CreatedAt = now
	cp.UpdatedAt = now
	m.tasks[t.ID] = &cp
	m.transitions[t.ID] = []TransitionRecord{{
		TaskID: t.ID, FromStatus: "", ToStatus: StatusQueued, Reason: reason, Cause: cause, At: now,
	}}
	return nil
}

func (m *MemStore) Transition(taskID, to, reason string, cause Cause) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("cortex memstore: task %s: not found", taskID)
	}
	if err := CheckTransition(t.Status, to, cause); err != nil {
		return err
	}
	from := t.Status
	t.Status = to
	t.UpdatedAt = time.Now()
	m.transitions[taskID] = append(m.transitions[taskID], TransitionRecord{
		TaskID: taskID, FromStatus: from, ToStatus: to, Reason: reason, Cause: cause, At: t.UpdatedAt,
	})
	return nil
}

func (m *MemStore) GetTask(id string) (*TaskRecord, []TransitionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return nil, nil, fmt.Errorf("cortex memstore: task %s: not found", id)
	}
	cp := *t
	history := append([]TransitionRecord(nil), m.transitions[id]...)
	return &cp, history, nil
}

func (m *MemStore) ListTasks(f TaskFilter) ([]TaskRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []TaskRecord
	for _, t := range m.tasks {
		if f.Status != "" && t.Status != f.Status {
			continue
		}
		if f.Agent != "" && t.Agent != f.Agent {
			continue
		}
		if f.Repo != "" && t.IssueRepo != f.Repo {
			continue
		}
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) SetPR(taskID, prRepo string, prNumber int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("cortex memstore: task %s: not found", taskID)
	}
	t.PRRepo, t.PRNumber = prRepo, prNumber
	t.UpdatedAt = time.Now()
	return nil
}

func (m *MemStore) SetGateResult(taskID string, lastResult []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("cortex memstore: task %s: not found", taskID)
	}
	var gate map[string]json.RawMessage
	if err := json.Unmarshal(t.DoneGate, &gate); err != nil || gate == nil {
		gate = map[string]json.RawMessage{}
	}
	gate["last_result"] = json.RawMessage(lastResult)
	merged, err := json.Marshal(gate)
	if err != nil {
		return err
	}
	t.DoneGate = merged
	t.UpdatedAt = time.Now()
	return nil
}

func (m *MemStore) SetReviewer(taskID, reviewer string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("cortex memstore: task %s: not found", taskID)
	}
	t.Reviewer = reviewer
	t.UpdatedAt = time.Now()
	return nil
}

func (m *MemStore) PrepareRetry(taskID string, failureContext []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("cortex memstore: task %s: not found", taskID)
	}
	t.Attempt++
	t.FailureContext = json.RawMessage(failureContext)
	t.UpdatedAt = time.Now()
	return nil
}

func (m *MemStore) SetTimeoutAt(taskID string, at *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("cortex memstore: task %s: not found", taskID)
	}
	t.TimeoutAt = at
	t.UpdatedAt = time.Now()
	return nil
}

func (m *MemStore) FindTaskByPR(prRepo string, prNumber int64) (*TaskRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *TaskRecord
	for _, t := range m.tasks {
		if t.PRRepo == prRepo && t.PRNumber == prNumber {
			if best == nil || t.CreatedAt.After(best.CreatedAt) {
				best = t
			}
		}
	}
	if best == nil {
		return nil, fmt.Errorf("cortex memstore: task for PR %s#%d: not found", prRepo, prNumber)
	}
	cp := *best
	return &cp, nil
}

func (m *MemStore) ListActive() ([]TaskRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []TaskRecord
	for _, t := range m.tasks {
		if t.Status != StatusValidated && t.Status != StatusFailed {
			out = append(out, *t)
		}
	}
	return out, nil
}

func (m *MemStore) RecordDecision(d Decision) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.decisions = append(m.decisions, d)
	return nil
}

func (m *MemStore) ListDecisions(limit int) ([]Decision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	n := len(m.decisions)
	start := n - limit
	if start < 0 {
		start = 0
	}
	out := make([]Decision, 0, n-start)
	for i := n - 1; i >= start; i-- { // newest first, matching PGStore
		out = append(out, m.decisions[i])
	}
	return out, nil
}
