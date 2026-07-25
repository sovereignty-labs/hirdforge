package steward

import (
	"sync"
	"time"
)

// Turn is one exchange in a Steward conversation, recorded so a UI can rehydrate
// the thread (§7 GET /steward/sessions/{id}) and so every issue the Steward
// created is traceable back to the words that caused it.
type Turn struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`         // what the operator said
	Reply   string    `json:"reply"`           // what the Steward answered
	Intent  Intent    `json:"intent"`          // how the turn was classified
	Issue   *Created  `json:"issue,omitempty"` // set only when one was created
}

// Created is the record of the single side effect a Steward turn may have.
type Created struct {
	Repo   string `json:"repo"`
	Number int64  `json:"number"`
	URL    string `json:"url"`
	Label  string `json:"label"`
}

// SessionStore keeps conversation history in memory. Deliberately not persisted:
// a Steward session is a conversation, not lifecycle state — the durable record
// of anything it DID is the Gitea issue and the Cortex task, both of which
// outlive this process. Losing chat scrollback on restart costs nothing that
// matters; the task record is the source of truth.
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string][]Turn
	maxTurns int
}

func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: make(map[string][]Turn), maxTurns: 50}
}

// Append records a turn, bounding history so one long-lived session cannot grow
// without limit.
func (s *SessionStore) Append(sessionID string, t Turn) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	turns := append(s.sessions[sessionID], t)
	if len(turns) > s.maxTurns {
		turns = turns[len(turns)-s.maxTurns:]
	}
	s.sessions[sessionID] = turns
}

// History returns a copy of a session's turns.
func (s *SessionStore) History(sessionID string) []Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.sessions[sessionID]
	out := make([]Turn, len(src))
	copy(out, src)
	return out
}
