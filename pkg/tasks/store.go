package tasks

import (
	"sync"
	"time"
)

type ToolLog struct {
	Name   string `json:"name"`
	Input  string `json:"input"`
	Output string `json:"output"`
}

type Task struct {
	ID        string    `json:"id"`
	Agent     string    `json:"agent"`
	From      string    `json:"from"`
	SessionID string    `json:"session_id"`
	Content   string    `json:"content"`
	Status    string    `json:"status"`
	Result    string    `json:"result"`
	Tools     []ToolLog `json:"tools"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Error     string    `json:"error,omitempty"`
}

type Store struct {
	mu    sync.RWMutex
	tasks map[string]Task
}

func NewStore() *Store {
	return &Store{tasks: map[string]Task{}}
}

func (s *Store) Create(t Task) Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	if t.Tools == nil {
		t.Tools = []ToolLog{}
	}
	s.tasks[t.ID] = t
	return t
}

func (s *Store) Get(id string) (Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, false
	}
	if t.Tools == nil {
		t.Tools = []ToolLog{}
	}
	return t, true
}

func (s *Store) List(agent, status string) []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		if agent != "" && t.Agent != agent {
			continue
		}
		if status != "" && t.Status != status {
			continue
		}
		if t.Tools == nil {
			t.Tools = []ToolLog{}
		}
		out = append(out, t)
	}
	return out
}

func (s *Store) Update(t Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.Tools == nil {
		t.Tools = []ToolLog{}
	}
	t.UpdatedAt = time.Now().UTC()
	s.tasks[t.ID] = t
}
