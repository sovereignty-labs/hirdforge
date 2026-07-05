package workbench

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// LockboxApprovalRequest is a human approval boundary around a Builder change
// proposal. It is a Lockbox-lite record only: approving or rejecting a request
// records the decision but never applies the proposed files. It never carries
// the provider API key.
type LockboxApprovalRequest struct {
	ID             string                `json:"id"`
	TS             time.Time             `json:"ts"`
	ProposalID     string                `json:"proposal_id"`
	Goal           string                `json:"goal"`
	Status         string                `json:"status"`
	Summary        string                `json:"summary"`
	Files          []BuilderProposedFile `json:"files"`
	DecisionTS     *time.Time            `json:"decision_ts"`
	DecisionReason string                `json:"decision_reason"`
}

const (
	lockboxStatusPending  = "pending"
	lockboxStatusApproved = "approved"
	lockboxStatusRejected = "rejected"
)

var (
	errLockboxNotFound   = errors.New("approval request not found")
	errLockboxNotPending = errors.New("approval request is not pending")
)

// cloneLockboxRequest deep-copies the mutable parts of a request (the files
// slice and the decision timestamp pointer) so callers can never mutate the
// store's internal state through a returned value.
func cloneLockboxRequest(in LockboxApprovalRequest) LockboxApprovalRequest {
	out := in
	if in.Files != nil {
		out.Files = make([]BuilderProposedFile, len(in.Files))
		copy(out.Files, in.Files)
	}
	if in.DecisionTS != nil {
		ts := *in.DecisionTS
		out.DecisionTS = &ts
	}
	return out
}

// lockboxStore is the in-memory record of Lockbox approval requests. Every
// accessor returns a deep copy, never a pointer into the stored slice.
type lockboxStore struct {
	mu       sync.Mutex
	nextID   int64
	requests []LockboxApprovalRequest
}

func newLockboxStore() *lockboxStore {
	return &lockboxStore{}
}

// Append assigns an id and timestamp to in, stores it, and returns a copy of
// the stored value.
func (s *lockboxStore) Append(in LockboxApprovalRequest) LockboxApprovalRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	in.ID = strconv.FormatInt(s.nextID, 10)
	in.TS = time.Now().UTC()
	s.requests = append(s.requests, in)
	return cloneLockboxRequest(in)
}

// List returns deep copies of all requests in insertion order (oldest first,
// newest last).
func (s *lockboxStore) List() []LockboxApprovalRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LockboxApprovalRequest, len(s.requests))
	for i := range s.requests {
		out[i] = cloneLockboxRequest(s.requests[i])
	}
	return out
}

// Current returns a deep copy of the most recent request, or nil if there are
// none.
func (s *lockboxStore) Current() *LockboxApprovalRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return nil
	}
	c := cloneLockboxRequest(s.requests[len(s.requests)-1])
	return &c
}

// Approve transitions the pending request with the given id to approved.
func (s *lockboxStore) Approve(id, reason string) (*LockboxApprovalRequest, error) {
	return s.decide(id, lockboxStatusApproved, reason)
}

// Reject transitions the pending request with the given id to rejected.
func (s *lockboxStore) Reject(id, reason string) (*LockboxApprovalRequest, error) {
	return s.decide(id, lockboxStatusRejected, reason)
}

// decide records a decision on a pending request, returning a deep copy of the
// updated request. It returns errLockboxNotFound or errLockboxNotPending when
// the request is missing or already decided.
func (s *lockboxStore) decide(id, status, reason string) (*LockboxApprovalRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.requests {
		if s.requests[i].ID != id {
			continue
		}
		if s.requests[i].Status != lockboxStatusPending {
			return nil, errLockboxNotPending
		}
		now := time.Now().UTC()
		s.requests[i].Status = status
		s.requests[i].DecisionTS = &now
		s.requests[i].DecisionReason = reason
		c := cloneLockboxRequest(s.requests[i])
		return &c, nil
	}
	return nil, errLockboxNotFound
}

// handleLockboxRequest serves GET (current approval request) and POST (create a
// new approval request from a proposed change proposal).
func (wb *Server) handleLockboxRequest(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		request := wb.lockbox.Current()
		if request == nil {
			http.Error(w, "no approval request", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, *request)
	case http.MethodPost:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var in struct {
			ProposalID string `json:"proposal_id"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}

		var proposal *BuilderChangeProposal
		if in.ProposalID == "" {
			proposal = wb.proposals.Current()
			if proposal == nil {
				http.Error(w, "no proposal to request approval for", http.StatusConflict)
				return
			}
		} else {
			proposal = wb.proposals.Find(in.ProposalID)
			if proposal == nil {
				http.Error(w, "proposal not found", http.StatusNotFound)
				return
			}
		}
		if proposal.Status != builderProposalStatusProposed {
			http.Error(w, "proposal is not in proposed status", http.StatusConflict)
			return
		}

		// Copy the proposed files so the request owns an independent slice.
		files := make([]BuilderProposedFile, len(proposal.Files))
		copy(files, proposal.Files)

		request := wb.lockbox.Append(LockboxApprovalRequest{
			ProposalID: proposal.ID,
			Goal:       proposal.Goal,
			Status:     lockboxStatusPending,
			Summary:    proposal.Summary,
			Files:      files,
		})
		data, err := json.Marshal(request)
		if err != nil {
			http.Error(w, "marshal request: "+err.Error(), http.StatusInternalServerError)
			return
		}
		wb.store.Append("lockbox.request.created", "Created Lockbox approval request", data)
		writeJSON(w, http.StatusCreated, request)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (wb *Server) handleLockboxRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, wb.lockbox.List())
}

func (wb *Server) handleLockboxApprove(w http.ResponseWriter, r *http.Request) {
	wb.handleLockboxDecision(w, r, lockboxStatusApproved)
}

func (wb *Server) handleLockboxReject(w http.ResponseWriter, r *http.Request) {
	wb.handleLockboxDecision(w, r, lockboxStatusRejected)
}

// handleLockboxDecision approves or rejects a pending request by id. It records
// the decision only; no proposed files are ever applied.
func (wb *Server) handleLockboxDecision(w http.ResponseWriter, r *http.Request, decision string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var in struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if in.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	var (
		request   *LockboxApprovalRequest
		decideErr error
		eventType string
		eventMsg  string
	)
	if decision == lockboxStatusApproved {
		request, decideErr = wb.lockbox.Approve(in.ID, in.Reason)
		eventType, eventMsg = "lockbox.request.approved", "Approved Lockbox request"
	} else {
		request, decideErr = wb.lockbox.Reject(in.ID, in.Reason)
		eventType, eventMsg = "lockbox.request.rejected", "Rejected Lockbox request"
	}
	if decideErr != nil {
		switch {
		case errors.Is(decideErr, errLockboxNotFound):
			http.Error(w, "approval request not found", http.StatusNotFound)
		case errors.Is(decideErr, errLockboxNotPending):
			http.Error(w, "approval request is not pending", http.StatusConflict)
		default:
			http.Error(w, decideErr.Error(), http.StatusInternalServerError)
		}
		return
	}

	data, err := json.Marshal(request)
	if err != nil {
		http.Error(w, "marshal request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	wb.store.Append(eventType, eventMsg, data)
	writeJSON(w, http.StatusOK, *request)
}
