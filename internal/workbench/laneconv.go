package workbench

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LaneConversationMessage is one turn in a lane-scoped conversation. These
// messages are the transcript and never carry the provider API key.
type LaneConversationMessage struct {
	ID      string    `json:"id"`
	TS      time.Time `json:"ts"`
	Role    string    `json:"role"`
	Content string    `json:"content"`
}

// LaneConversationContext scopes a conversation to a lane and/or artifact. A
// conversation is never global chat: it always targets a lane, an artifact, or
// an approval/apply context.
type LaneConversationContext struct {
	Kind         string `json:"kind"`
	TaskID       string `json:"task_id"`
	LaneID       string `json:"lane_id"`
	LaneRole     string `json:"lane_role"`
	ArtifactID   string `json:"artifact_id"`
	ArtifactType string `json:"artifact_type"`
}

// LaneConversation is a generic lane-scoped conversation. Architect, Builder,
// Reviewer, Validator, and Lockbox/Apply conversations all share this base
// model; the context kind selects which artifacts inform the provider prompt.
type LaneConversation struct {
	ID       string                    `json:"id"`
	TS       time.Time                 `json:"ts"`
	Status   string                    `json:"status"`
	Context  LaneConversationContext   `json:"context"`
	Messages []LaneConversationMessage `json:"messages"`
}

const (
	laneConvRoleUser      = "user"
	laneConvRoleAssistant = "assistant"
	laneConvRoleSystem    = "system"

	laneConvStatusActive = "active"
	laneConvStatusClosed = "closed"

	laneConvKindArchitect = "architect"
	laneConvKindBuilder   = "builder"
	laneConvKindReviewer  = "reviewer"
	laneConvKindValidator = "validator"
	laneConvKindLockbox   = "lockbox"
	laneConvKindApply     = "apply"

	laneConvSystemPrompt = "You are a Hirdforge lane assistant. Help the user with the selected lane or artifact: planning, proposals, reviews, validation, or approval/apply safety. Do not write files, run commands, or claim any change was made."
)

// validLaneConvKind reports whether kind is a recognized conversation kind.
func validLaneConvKind(kind string) bool {
	switch kind {
	case laneConvKindArchitect, laneConvKindBuilder, laneConvKindReviewer, laneConvKindValidator, laneConvKindLockbox, laneConvKindApply:
		return true
	}
	return false
}

// laneConvLaneRequired reports whether a conversation kind must be scoped to a
// lane. Builder, Reviewer, and Validator conversations are lane work; Architect
// and Lockbox/Apply conversations may stand alone.
func laneConvLaneRequired(kind string) bool {
	switch kind {
	case laneConvKindBuilder, laneConvKindReviewer, laneConvKindValidator:
		return true
	}
	return false
}

// laneConvRoleEvent returns the role-specific event type and message for a
// conversation kind, alongside the generic lane.message.created event. Lockbox
// and Apply conversations share the lockbox approval/apply family.
func laneConvRoleEvent(kind string) (evType, message string) {
	switch kind {
	case laneConvKindArchitect:
		return "architect.message.created", "Created Architect message"
	case laneConvKindBuilder:
		return "builder.message.created", "Created Builder message"
	case laneConvKindReviewer:
		return "reviewer.message.created", "Created Reviewer message"
	case laneConvKindValidator:
		return "validator.message.created", "Created Validator message"
	case laneConvKindLockbox, laneConvKindApply:
		return "lockbox.message.created", "Created Lockbox message"
	}
	return "", ""
}

// cloneLaneConversation deep-copies the message slice so callers can never
// mutate the store's internal state through a returned value. The context has no
// reference fields, so a struct copy suffices for it.
func cloneLaneConversation(in LaneConversation) LaneConversation {
	out := in
	if in.Messages != nil {
		out.Messages = make([]LaneConversationMessage, len(in.Messages))
		copy(out.Messages, in.Messages)
	}
	return out
}

// laneConversationStore is the in-memory record of lane conversations. Every
// accessor returns a deep copy, never a pointer into the stored slice.
type laneConversationStore struct {
	mu            sync.Mutex
	nextID        int64
	conversations []LaneConversation
}

func newLaneConversationStore() *laneConversationStore {
	return &laneConversationStore{}
}

// Append assigns an id and timestamp to in, stores it, and returns a copy.
func (s *laneConversationStore) Append(in LaneConversation) LaneConversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	in.ID = strconv.FormatInt(s.nextID, 10)
	in.TS = time.Now().UTC()
	stored := cloneLaneConversation(in)
	s.conversations = append(s.conversations, stored)
	return cloneLaneConversation(stored)
}

// Current returns a deep copy of the most recent conversation, or nil if none.
func (s *laneConversationStore) Current() *LaneConversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.conversations) == 0 {
		return nil
	}
	c := cloneLaneConversation(s.conversations[len(s.conversations)-1])
	return &c
}

// Find returns a deep copy of the conversation with the given id, or nil.
func (s *laneConversationStore) Find(id string) *LaneConversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.conversations {
		if s.conversations[i].ID == id {
			c := cloneLaneConversation(s.conversations[i])
			return &c
		}
	}
	return nil
}

// List returns deep copies of all conversations in insertion order.
func (s *laneConversationStore) List() []LaneConversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LaneConversation, len(s.conversations))
	for i := range s.conversations {
		out[i] = cloneLaneConversation(s.conversations[i])
	}
	return out
}

// ListByTask returns deep copies of conversations for the given task id.
func (s *laneConversationStore) ListByTask(taskID string) []LaneConversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []LaneConversation{}
	for i := range s.conversations {
		if s.conversations[i].Context.TaskID == taskID {
			out = append(out, cloneLaneConversation(s.conversations[i]))
		}
	}
	return out
}

// ListByLane returns deep copies of conversations for the given lane id.
func (s *laneConversationStore) ListByLane(laneID string) []LaneConversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []LaneConversation{}
	for i := range s.conversations {
		if s.conversations[i].Context.LaneID == laneID {
			out = append(out, cloneLaneConversation(s.conversations[i]))
		}
	}
	return out
}

// ListByKind returns deep copies of conversations for the given context kind.
func (s *laneConversationStore) ListByKind(kind string) []LaneConversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []LaneConversation{}
	for i := range s.conversations {
		if s.conversations[i].Context.Kind == kind {
			out = append(out, cloneLaneConversation(s.conversations[i]))
		}
	}
	return out
}

// Update replaces the stored conversation that shares in's id and returns a deep
// copy of the stored value, or nil if there is no such conversation.
func (s *laneConversationStore) Update(in LaneConversation) *LaneConversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.conversations {
		if s.conversations[i].ID == in.ID {
			s.conversations[i] = cloneLaneConversation(in)
			c := cloneLaneConversation(s.conversations[i])
			return &c
		}
	}
	return nil
}

// appendLaneConversationMessage appends a message to conv, assigning it a stable
// per-conversation id and a timestamp.
func appendLaneConversationMessage(conv *LaneConversation, role, content string) {
	conv.Messages = append(conv.Messages, LaneConversationMessage{
		ID:      strconv.Itoa(len(conv.Messages) + 1),
		TS:      time.Now().UTC(),
		Role:    role,
		Content: content,
	})
}

// appendLaneEvent marshals payload as the event data, falling back to nil data
// when marshaling fails.
func (wb *Server) appendLaneEvent(evType, message string, payload any) {
	if data, err := json.Marshal(payload); err == nil {
		wb.store.Append(evType, message, data)
	} else {
		wb.store.Append(evType, message, nil)
	}
}

// parseLaneMessageContent extracts the assistant message from a provider
// response. The response may be plain text or a JSON object with a "message"
// field; an empty response is an error.
func parseLaneMessageContent(content string) (string, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", errors.New("provider returned an empty message")
	}
	var parsed struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil && strings.TrimSpace(parsed.Message) != "" {
		return parsed.Message, nil
	}
	return trimmed, nil
}

// requestProviderLaneMessage calls the configured provider for one lane
// conversation turn and returns the assistant message, or an error covering any
// failure mode (non-2xx status, transport error, or an empty response).
func requestProviderLaneMessage(cfg *providerConfig, systemPrompt, userPrompt string) (string, error) {
	endpoint := strings.TrimRight(cfg.baseURL, "/") + "/chat/completions"
	reqBody, err := json.Marshal(map[string]any{
		"model": cfg.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": 0,
	})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(reqBody)))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.apiKey)

	client := &http.Client{Timeout: buildPlanTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBody))
		if msg == "" {
			msg = resp.Status
		}
		return "", fmt.Errorf("provider returned status %d: %s", resp.StatusCode, msg)
	}

	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return "", fmt.Errorf("decode provider response: %w", err)
	}
	if len(envelope.Choices) == 0 {
		return "", errors.New("provider response contained no choices")
	}
	return parseLaneMessageContent(envelope.Choices[0].Message.Content)
}

// laneArtifactContext renders the artifact context relevant to a conversation
// kind: the latest matching planning/proposal/review/validation/approval record.
// It is read-only and never includes the provider API key.
func (wb *Server) laneArtifactContext(ctx LaneConversationContext) string {
	var b strings.Builder
	switch ctx.Kind {
	case laneConvKindArchitect:
		if s := wb.architect.Current(); s != nil {
			b.WriteString("Architect session " + s.ID + " (status " + s.Status + ")\n")
			b.WriteString("Spec goal: " + s.Spec.Goal + "\n")
			if len(s.Spec.Constraints) > 0 {
				b.WriteString("Constraints: " + strings.Join(s.Spec.Constraints, "; ") + "\n")
			}
			if len(s.Spec.OpenQuestions) > 0 {
				b.WriteString("Open questions: " + strings.Join(s.Spec.OpenQuestions, "; ") + "\n")
			}
		} else {
			b.WriteString("(no architect spec yet)\n")
		}
	case laneConvKindBuilder:
		if p := wb.laneProposalForContext(ctx); p != nil {
			b.WriteString("Lane proposal " + p.ID + " (status " + p.Status + ")\n")
			b.WriteString("Summary: " + p.Summary + "\n")
			b.WriteString("Files: " + strconv.Itoa(len(p.Files)) + "\n")
		} else {
			b.WriteString("(no builder lane proposal yet)\n")
		}
	case laneConvKindReviewer:
		if rv := wb.reviewForContext(ctx); rv != nil {
			b.WriteString("Aggregate review " + rv.ID + " (status " + rv.Status + ", verdict " + rv.Verdict + ")\n")
			b.WriteString("Summary: " + rv.Summary + "\n")
		} else {
			b.WriteString("(no reviewer review yet)\n")
		}
	case laneConvKindValidator:
		if v := wb.cortexValidations.Current(); v != nil {
			b.WriteString("Apply validation " + v.ID + " (status " + v.Status + ", exit " + strconv.Itoa(v.ExitCode) + ")\n")
			b.WriteString("Command: " + v.Command + "\n")
		} else {
			b.WriteString("(no apply validation yet)\n")
		}
	case laneConvKindLockbox, laneConvKindApply:
		if req := wb.lockbox.Current(); req != nil {
			b.WriteString("Lockbox request " + req.ID + " (status " + req.Status + ")\n")
			b.WriteString("Summary: " + req.Summary + "\n")
		} else {
			b.WriteString("(no lockbox request yet)\n")
		}
		if pv := wb.cortexApplyPreviews.Current(); pv != nil {
			b.WriteString("Apply preview " + pv.ID + " (status " + pv.Status + ")\n")
		}
		if ar := wb.cortexApplies.Current(); ar != nil {
			b.WriteString("Apply result " + ar.ID + " (status " + ar.Status + ")\n")
		}
		if v := wb.cortexValidations.Current(); v != nil {
			b.WriteString("Apply validation " + v.ID + " (status " + v.Status + ")\n")
		}
	}
	return b.String()
}

// laneProposalForContext returns the lane proposal most relevant to ctx: the
// latest for the scoped lane, else the current proposal, else nil.
func (wb *Server) laneProposalForContext(ctx LaneConversationContext) *CortexLaneProposal {
	if ctx.LaneID != "" {
		if list := wb.cortexLaneProposals.ListByLane(ctx.LaneID); len(list) > 0 {
			p := list[len(list)-1]
			return &p
		}
	}
	return wb.cortexLaneProposals.Current()
}

// reviewForContext returns the aggregate review most relevant to ctx: the latest
// for the scoped lane, else the current review, else nil.
func (wb *Server) reviewForContext(ctx LaneConversationContext) *CortexAggregateReview {
	if ctx.LaneID != "" {
		if list := wb.cortexReviews.ListByLane(ctx.LaneID); len(list) > 0 {
			rv := list[len(list)-1]
			return &rv
		}
	}
	return wb.cortexReviews.Current()
}

// laneConversationUserPrompt renders the provider user message for one lane
// conversation turn: the conversation context, the selected lane, the relevant
// artifact context, the prior conversation, and the new user message.
func (wb *Server) laneConversationUserPrompt(ctx LaneConversationContext, prior []LaneConversationMessage, newMessage string) string {
	var b strings.Builder
	b.WriteString("Hirdforge lane conversation.\n")
	b.WriteString("Conversation kind: " + ctx.Kind + "\n")
	if ctx.TaskID != "" {
		b.WriteString("Task id: " + ctx.TaskID + "\n")
	}
	if ctx.LaneID != "" {
		b.WriteString("Lane id: " + ctx.LaneID + "\n")
		b.WriteString("Lane role: " + ctx.LaneRole + "\n")
	}
	if ctx.ArtifactID != "" {
		b.WriteString("Artifact id: " + ctx.ArtifactID + "\n")
	}
	if ctx.ArtifactType != "" {
		b.WriteString("Artifact type: " + ctx.ArtifactType + "\n")
	}
	b.WriteString("\n")

	if project := wb.project.Get(); project != nil {
		b.WriteString("Project: " + project.Name + " (" + project.Path + ")\n\n")
	}

	// Selected lane details, when the conversation is lane-scoped.
	if ctx.LaneID != "" {
		var task *CortexTask
		if ctx.TaskID == "" {
			task = wb.cortex.Current()
		} else {
			task = wb.cortex.Find(ctx.TaskID)
		}
		if task != nil {
			for _, l := range task.Lanes {
				if l.ID == ctx.LaneID {
					b.WriteString("Selected lane: " + l.Task + " (status " + l.Status + ")\n\n")
					break
				}
			}
		}
	}

	b.WriteString("Artifact context:\n")
	b.WriteString(wb.laneArtifactContext(ctx))
	b.WriteString("\n")

	b.WriteString("Conversation so far:\n")
	if len(prior) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, m := range prior {
			b.WriteString("- " + m.Role + ": " + m.Content + "\n")
		}
	}
	b.WriteString("\n")

	b.WriteString("New user message: " + newMessage + "\n\n")
	b.WriteString("Reply to the user about this lane or artifact. Do not write files, run commands, or claim any change was made. ")
	b.WriteString(`Respond with plain text, or JSON {"message":"..."}.`)
	return b.String()
}

// handleLaneConversation serves GET (current conversation) and POST (create a
// lane-scoped conversation). It never calls the provider.
func (wb *Server) handleLaneConversation(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		conv := wb.laneConversations.Current()
		if conv == nil {
			http.Error(w, "no lane conversation", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, *conv)
	case http.MethodPost:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var in struct {
			Kind         string `json:"kind"`
			TaskID       string `json:"task_id"`
			LaneID       string `json:"lane_id"`
			ArtifactID   string `json:"artifact_id"`
			ArtifactType string `json:"artifact_type"`
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &in); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
		}

		if in.Kind == "" {
			http.Error(w, "kind is required", http.StatusBadRequest)
			return
		}
		if !validLaneConvKind(in.Kind) {
			http.Error(w, "kind must be one of architect, builder, reviewer, validator, lockbox, apply", http.StatusBadRequest)
			return
		}

		ctx := LaneConversationContext{
			Kind:         in.Kind,
			TaskID:       in.TaskID,
			LaneID:       in.LaneID,
			ArtifactID:   in.ArtifactID,
			ArtifactType: in.ArtifactType,
		}

		if in.LaneID != "" {
			var task *CortexTask
			if in.TaskID == "" {
				task = wb.cortex.Current()
			} else {
				task = wb.cortex.Find(in.TaskID)
			}
			if task == nil {
				http.Error(w, "cortex task not found", http.StatusNotFound)
				return
			}
			var lane *CortexLane
			for i := range task.Lanes {
				if task.Lanes[i].ID == in.LaneID {
					l := task.Lanes[i]
					lane = &l
					break
				}
			}
			if lane == nil {
				http.Error(w, "lane not found", http.StatusNotFound)
				return
			}
			ctx.TaskID = task.ID
			ctx.LaneRole = lane.Role
		} else if laneConvLaneRequired(in.Kind) {
			http.Error(w, "lane_id is required for builder, reviewer, and validator conversations", http.StatusBadRequest)
			return
		}

		conv := LaneConversation{
			Status:   laneConvStatusActive,
			Context:  ctx,
			Messages: []LaneConversationMessage{},
		}
		stored := wb.laneConversations.Append(conv)
		wb.appendLaneEvent("lane.conversation.created", "Created lane conversation", stored)
		writeJSON(w, http.StatusCreated, stored)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (wb *Server) handleLaneConversations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	taskID := q.Get("task_id")
	laneID := q.Get("lane_id")
	kind := q.Get("kind")

	var out []LaneConversation
	switch {
	case taskID != "":
		out = wb.laneConversations.ListByTask(taskID)
	case laneID != "":
		out = wb.laneConversations.ListByLane(laneID)
	case kind != "":
		out = wb.laneConversations.ListByKind(kind)
	default:
		out = wb.laneConversations.List()
	}
	writeJSON(w, http.StatusOK, out)
}

// handleLaneConversationMessage appends a user turn, asks the provider for a
// reply scoped to the conversation's lane/artifact, and records the assistant
// reply. It never writes files and never exposes the provider API key.
func (wb *Server) handleLaneConversationMessage(w http.ResponseWriter, r *http.Request) {
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
		ConversationID string `json:"conversation_id"`
		Message        string `json:"message"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	var conv *LaneConversation
	if in.ConversationID == "" {
		conv = wb.laneConversations.Current()
	} else {
		conv = wb.laneConversations.Find(in.ConversationID)
	}
	if conv == nil {
		http.Error(w, "lane conversation not found", http.StatusNotFound)
		return
	}
	if conv.Status == laneConvStatusClosed {
		http.Error(w, "conversation is closed", http.StatusConflict)
		return
	}

	cfg := wb.provider.Config()
	if cfg == nil {
		http.Error(w, "provider must be configured", http.StatusConflict)
		return
	}
	if in.Message == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	// Prior turns drive the prompt; the new user message is appended now and
	// shown to the provider separately.
	prior := make([]LaneConversationMessage, len(conv.Messages))
	copy(prior, conv.Messages)
	appendLaneConversationMessage(conv, laneConvRoleUser, in.Message)

	userPrompt := wb.laneConversationUserPrompt(conv.Context, prior, in.Message)
	content, msgErr := requestProviderLaneMessage(cfg, laneConvSystemPrompt, userPrompt)
	if msgErr != nil {
		appendLaneConversationMessage(conv, laneConvRoleSystem, truncateString("Lane request failed: "+msgErr.Error(), 1000))
		stored := wb.laneConversations.Update(*conv)
		wb.appendLaneEvent("lane.message.failed", "Failed lane message", stored)
		writeJSON(w, http.StatusBadGateway, *stored)
		return
	}

	appendLaneConversationMessage(conv, laneConvRoleAssistant, content)
	stored := wb.laneConversations.Update(*conv)
	wb.appendLaneEvent("lane.message.created", "Created lane message", stored)
	if evType, evMsg := laneConvRoleEvent(conv.Context.Kind); evType != "" {
		wb.appendLaneEvent(evType, evMsg, stored)
	}
	writeJSON(w, http.StatusOK, *stored)
}

// handleLaneConversationClose marks a conversation closed.
func (wb *Server) handleLaneConversationClose(w http.ResponseWriter, r *http.Request) {
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
		ConversationID string `json:"conversation_id"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	var conv *LaneConversation
	if in.ConversationID == "" {
		conv = wb.laneConversations.Current()
	} else {
		conv = wb.laneConversations.Find(in.ConversationID)
	}
	if conv == nil {
		http.Error(w, "lane conversation not found", http.StatusNotFound)
		return
	}
	if conv.Status == laneConvStatusClosed {
		http.Error(w, "conversation is already closed", http.StatusConflict)
		return
	}

	conv.Status = laneConvStatusClosed
	stored := wb.laneConversations.Update(*conv)
	wb.appendLaneEvent("lane.conversation.closed", "Closed lane conversation", stored)
	writeJSON(w, http.StatusOK, *stored)
}
