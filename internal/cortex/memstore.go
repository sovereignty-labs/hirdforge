package cortex

import (
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
