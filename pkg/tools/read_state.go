package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// M4 read-before-edit + staleness (BUILDER_HARNESS §M4).
//
// The benchmark refactor is a sequence of edits to the same file; the classic
// cascade is edit #2 targeting text that edit #1 just moved, so its old_str no
// longer matches and the run derails. The fix is to force fresh ground truth: an
// edit is only allowed against a file the agent has READ this session and that
// has not changed since; a successful edit invalidates that read, so the next
// edit must read again. A write establishes ground truth too (the agent knows
// what it wrote), so it records rather than blocks.
//
// Keyed by session, so it is opt-in: with no session id (many unit tests, non-
// session callers) enforcement is skipped and behavior is unchanged.

type readStateStore struct {
	mu sync.Mutex
	// sessionID -> absPath -> content hash at the moment it was last read/written.
	hashes map[string]map[string]string
}

var sessionReads = &readStateStore{hashes: map[string]map[string]string{}}

// staleMarker is stored in place of a real content hash after an edit: it never
// equals a sha256, so status() reports the file as known-but-changed, yielding a
// "read it again" message rather than "you never read it".
const staleMarker = "stale-after-edit"

func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// record notes that the agent now knows the current content of absPath (via a
// read or a write).
func (s *readStateStore) record(sessionID, absPath, hash string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hashes[sessionID] == nil {
		s.hashes[sessionID] = map[string]string{}
	}
	s.hashes[sessionID][absPath] = hash
}

// status reports whether absPath was read/written this session (known) and, if
// so, whether the on-disk content still matches what was known then (current).
func (s *readStateStore) status(sessionID, absPath, currentHash string) (known, current bool) {
	if sessionID == "" {
		return true, true // enforcement disabled without a session
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.hashes[sessionID][absPath]
	if !ok {
		return false, false
	}
	return true, h == currentHash
}

// invalidate marks absPath stale after an edit, so the next edit must re-read
// first. It poisons rather than deletes the record so the coaching can say "read
// it again" instead of "you never read it".
func (s *readStateStore) invalidate(sessionID, absPath string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hashes[sessionID] == nil {
		s.hashes[sessionID] = map[string]string{}
	}
	s.hashes[sessionID][absPath] = staleMarker
}

// ClearReadState drops all read tracking for a session (called at session end so
// the store does not accumulate).
func ClearReadState(sessionID string) {
	if sessionID == "" {
		return
	}
	sessionReads.mu.Lock()
	defer sessionReads.mu.Unlock()
	delete(sessionReads.hashes, sessionID)
}
