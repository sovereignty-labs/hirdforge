package steward

import (
	"sync"
	"time"
)

// Turn is one exchange in an interlocutor conversation, recorded so a UI can
// rehydrate the thread (§7 GET /steward/sessions/{id}) and so any plan — and later
// any issue a blessing files from it — is traceable back to the words that caused
// it. A turn is inert: it carries at most a PROPOSED plan, never a side effect.
type Turn struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`        // what the operator said
	Reply   string    `json:"reply"`          // what the interlocutor answered
	Plan    *Plan     `json:"plan,omitempty"` // set only when the turn proposed one (inert until blessed)
}

// Created is the record of one issue a blessing filed from a plan step (§7
// /steward/handoff). It lives here because a session's history is where the UI
// traces a dispatched step back to the conversation that produced it.
type Created struct {
	StepID string `json:"step_id"`
	Repo   string `json:"repo"`
	Number int64  `json:"number"`
	URL    string `json:"url"`
	Label  string `json:"label"`
}

// SessionStore keeps conversation history in memory. Deliberately not persisted:
// an interlocutor session is a conversation, not lifecycle state — the durable
// record of anything it DID is the Gitea issue and the Cortex task, both of which
// outlive this process. Losing chat scrollback on restart costs nothing that
// matters; the task record is the source of truth. It also tracks what each
// session filed (for traceability) and which plans have been blessed (so a plan
// cannot be dispatched twice).
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string][]Turn
	created  map[string][]Created // per-session record of issues a blessing filed
	blessed  map[string]bool      // sessionID+"\x00"+planID -> already handed off
	maxTurns int
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[string][]Turn),
		created:  make(map[string][]Created),
		blessed:  make(map[string]bool),
		maxTurns: 50,
	}
}

// AppendCreated records an issue a blessing filed, so the session can trace a
// dispatched step back to the conversation (§7).
func (s *SessionStore) AppendCreated(sessionID string, c Created) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created[sessionID] = append(s.created[sessionID], c)
}

// Created returns a copy of the issues filed in a session.
func (s *SessionStore) Created(sessionID string) []Created {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.created[sessionID]
	out := make([]Created, len(src))
	copy(out, src)
	return out
}

// MarkBlessed records that a plan has been handed off, returning false if it was
// already blessed — the guard against a double-dispatch (e.g. a double-click).
func (s *SessionStore) MarkBlessed(sessionID, planID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionID + "\x00" + planID
	if s.blessed[key] {
		return false
	}
	s.blessed[key] = true
	return true
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
