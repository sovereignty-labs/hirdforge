package steward

import (
	"strings"
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
//
// For endurance (P4.5) it holds only the UNFOLDED tail of turns: older turns are
// folded into a running summary (FoldOldest) and removed, so an hours-long session
// stays bounded WITHOUT losing coverage. Proposed plans are kept separately from
// the turn tail, so a plan survives folding — it can still be pinned into context
// and blessed after its originating turn has been summarized away.
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string][]Turn    // unfolded tail only
	summary  map[string]string    // running summary of the folded span
	folded   map[string]int       // count of turns folded into summary
	plans    map[string][]*Plan   // every plan proposed, kept for pinning + blessing
	created  map[string][]Created // per-session record of issues a blessing filed
	blessed  map[string]bool      // sessionID+"\x00"+planID -> already handed off
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[string][]Turn),
		summary:  make(map[string]string),
		folded:   make(map[string]int),
		plans:    make(map[string][]*Plan),
		created:  make(map[string][]Created),
		blessed:  make(map[string]bool),
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

// Append records a turn in the unfolded tail. A turn's proposed plan is also kept
// in the durable plan list, so folding the turn away later does not lose the plan.
func (s *SessionStore) Append(sessionID string, t Turn) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = append(s.sessions[sessionID], t)
	if t.Plan != nil {
		s.plans[sessionID] = append(s.plans[sessionID], t.Plan)
	}
}

// History returns a copy of a session's UNFOLDED turns (the recent tail).
func (s *SessionStore) History(sessionID string) []Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.sessions[sessionID]
	out := make([]Turn, len(src))
	copy(out, src)
	return out
}

// FoldFunc condenses newly-aged-out turns into a running summary given the prior
// summary. Returns the updated summary.
type FoldFunc func(prior string, older []Turn) (string, error)

// FoldOldest folds every turn beyond keepRecent into the running summary and drops
// them from the tail, returning how many were folded. It is the incremental heart
// of endurance: coverage moves into the summary instead of being lost, and the
// tail stays bounded. On a fold error nothing is dropped (no silent loss) and the
// error is returned. A no-op (tail already within keepRecent) returns 0.
func (s *SessionStore) FoldOldest(sessionID string, keepRecent int, fold FoldFunc) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	turns := s.sessions[sessionID]
	if keepRecent < 0 || len(turns) <= keepRecent {
		return 0, nil
	}
	cut := len(turns) - keepRecent
	older := turns[:cut]
	newSummary, err := fold(s.summary[sessionID], older)
	if err != nil {
		return 0, err // keep the turns; do not drop coverage on a failed fold
	}
	// An empty summary is a failed fold too (e.g. a reasoning model that spent its
	// whole budget thinking and returned no content). Dropping turns here would
	// lose coverage silently — so keep them until a real summary comes back.
	if strings.TrimSpace(newSummary) == "" {
		return 0, nil
	}
	s.summary[sessionID] = newSummary
	s.folded[sessionID] += len(older)
	// Retain the tail in a fresh slice so the folded prefix can be GC'd.
	s.sessions[sessionID] = append([]Turn(nil), turns[cut:]...)
	return len(older), nil
}

// Summary returns the running summary of a session's folded span.
func (s *SessionStore) Summary(sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.summary[sessionID]
}

// FoldedCount returns how many turns are folded into the summary.
func (s *SessionStore) FoldedCount(sessionID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.folded[sessionID]
}

// ActivePlan returns the most recently proposed plan (pinned across folding), or
// nil. A revised plan supersedes an earlier one.
func (s *SessionStore) ActivePlan(sessionID string) *Plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps := s.plans[sessionID]
	if len(ps) == 0 {
		return nil
	}
	return ps[len(ps)-1]
}

// PlanByID returns a plan the session proposed, by id (latest wins), or nil. Kept
// durably so a blessing can act on it even after its turn was folded away.
func (s *SessionStore) PlanByID(sessionID, planID string) *Plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps := s.plans[sessionID]
	for i := len(ps) - 1; i >= 0; i-- {
		if ps[i].ID == planID {
			return ps[i]
		}
	}
	return nil
}
