package workbench

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	startupType = "workbench.started"
	startupMsg  = "Hirdforge Workbench started"
)

// WorkbenchEvent is a single entry in the in-memory Cortex-lite event log.
type WorkbenchEvent struct {
	ID      string          `json:"id"`
	TS      time.Time       `json:"ts"`
	Type    string          `json:"type"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// eventStore is a small thread-safe append-only log used by the workbench.
type eventStore struct {
	mu     sync.Mutex
	nextID int64
	events []WorkbenchEvent
}

func newEventStore() *eventStore {
	return &eventStore{}
}

// Append records a new event and returns the stored entry, including the
// assigned id and timestamp.
func (s *eventStore) Append(evType, message string, data json.RawMessage) WorkbenchEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	ev := WorkbenchEvent{
		ID:      strconv.FormatInt(s.nextID, 10),
		TS:      time.Now().UTC(),
		Type:    evType,
		Message: message,
		Data:    data,
	}
	s.events = append(s.events, ev)
	return ev
}

// List returns a snapshot copy of the current event log.
func (s *eventStore) List() []WorkbenchEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]WorkbenchEvent, len(s.events))
	copy(out, s.events)
	return out
}

// handleEvents serves GET (list the event log) and POST (append an event).
func (wb *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, wb.store.List())
	case http.MethodPost:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var in struct {
			Type    string          `json:"type"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if in.Type == "" {
			http.Error(w, "type is required", http.StatusBadRequest)
			return
		}
		ev := wb.store.Append(in.Type, in.Message, in.Data)
		writeJSON(w, http.StatusCreated, ev)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
