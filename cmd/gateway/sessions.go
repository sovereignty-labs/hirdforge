package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var objectiveSentenceRE = regexp.MustCompile(`(?s)^(.+?[.!?])(?:\s|$)`)

type ChatMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp"`
	Agent     string `json:"agent"`
}

type Session struct {
	ID          string        `json:"id"`
	Agent       string        `json:"agent"`
	Source      string        `json:"source"`       // "comms", "cronjob", "delegation"
	TaskRef     string        `json:"task_ref"`     // Gitea issue ref if detected, e.g. "#24"
	TaskSummary string        `json:"task_summary"` // First line of task content, truncated to ~60 chars
	Messages    []ChatMessage `json:"messages"`
	CreatedAt   int64         `json:"created_at"`
	UpdatedAt   int64         `json:"updated_at"`
}

type SessionSummary struct {
	ID           string `json:"id"`
	Agent        string `json:"agent"`
	Source       string `json:"source"`
	TaskRef      string `json:"task_ref"`
	TaskSummary  string `json:"task_summary"`
	MessageCount int    `json:"message_count"`
	UpdatedAt    int64  `json:"updated_at"`
	LastPreview  string `json:"last_preview"`
}

type sessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	seq      uint64
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: map[string]*Session{}}
}

func (s *sessionStore) newID() string {
	return fmt.Sprintf("sess-%d-%d", time.Now().Unix(), atomic.AddUint64(&s.seq, 1))
}

func detectSessionSource(sessionID string) string {
	id := strings.ToLower(strings.TrimSpace(sessionID))
	if strings.HasPrefix(id, "task-poll-") ||
		strings.HasPrefix(id, "task-queue-") ||
		strings.HasPrefix(id, "task-review-") ||
		strings.HasPrefix(id, "briefing-") ||
		strings.HasPrefix(id, "morning-") ||
		strings.HasPrefix(id, "jeeves-morning-") {
		return "cronjob"
	}
	if strings.HasPrefix(id, "delegation-") || strings.HasPrefix(id, "delegate-") {
		return "delegation"
	}
	return "comms"
}

func extractTaskRef(content string) string {
	// Match "#" followed by digits, common in task briefs.
	re := regexp.MustCompile(`#(\d+)`)
	if m := re.FindString(content); m != "" {
		return m
	}
	return ""
}

func extractTaskSummary(content string) string {
	if content == "" {
		return ""
	}
	// Get the first line, stripping leading/trailing whitespace
	lines := strings.SplitN(content, "\n", 2)
	firstLine := strings.TrimSpace(lines[0])
	// Truncate to ~60 chars with "..." if longer
	const maxLen = 60
	if len(firstLine) > maxLen {
		return firstLine[:maxLen] + "..."
	}
	return firstLine
}

func extractObjectiveSentence(content string) string {
	text := strings.TrimSpace(content)
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	candidates := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(line), "FROM:") ||
			strings.HasPrefix(strings.ToUpper(line), "TASK_ID:") ||
			strings.HasPrefix(strings.ToUpper(line), "GATES:") ||
			strings.HasPrefix(strings.ToUpper(line), "GIT_IDENTITY:") ||
			strings.HasPrefix(strings.ToUpper(line), "CLONE_URL:") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "TASK:"))
		line = strings.TrimSpace(strings.TrimPrefix(line, "Task:"))
		if line != "" {
			candidates = append(candidates, line)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	first := candidates[0]
	if match := objectiveSentenceRE.FindStringSubmatch(first); len(match) >= 2 {
		first = strings.TrimSpace(match[1])
	}
	if len(first) > 220 {
		first = first[:220] + "..."
	}
	return strings.TrimSpace(first)
}

func objectiveFromSession(sess *Session) string {
	if sess == nil {
		return ""
	}
	for _, msg := range sess.Messages {
		if msg.Role != "user" {
			continue
		}
		if objective := extractObjectiveSentence(msg.Content); objective != "" {
			return objective
		}
	}
	if objective := extractObjectiveSentence(sess.TaskSummary); objective != "" {
		return objective
	}
	return ""
}

func (s *sessionStore) ensureSession(id, agent string) *Session {
	now := time.Now().Unix()
	id = strings.TrimSpace(id)
	if id == "" {
		id = s.newID()
	}
	if sess, ok := s.sessions[id]; ok {
		if sess.Agent == "" {
			sess.Agent = agent
		}
		if sess.Source == "" {
			sess.Source = detectSessionSource(id)
		}
		return sess
	}
	sess := &Session{
		ID:        id,
		Agent:     agent,
		Source:    detectSessionSource(id),
		Messages:  []ChatMessage{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.sessions[id] = sess
	return sess
}

func (s *sessionStore) appendConversation(id, agent, userContent, assistantContent string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.ensureSession(id, agent)
	now := time.Now().Unix()
	sess.Messages = append(sess.Messages,
		ChatMessage{Role: "user", Content: userContent, Timestamp: now, Agent: agent},
		ChatMessage{Role: "assistant", Content: assistantContent, Timestamp: now, Agent: agent},
	)
	if sess.TaskRef == "" {
		sess.TaskRef = extractTaskRef(userContent)
	}
	if sess.TaskSummary == "" {
		sess.TaskSummary = extractTaskSummary(userContent)
	}
	sess.UpdatedAt = now
	return sess.ID
}

func (s *sessionStore) appendMessage(id, agent string, msg ChatMessage) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.ensureSession(id, agent)
	if msg.Timestamp == 0 {
		msg.Timestamp = time.Now().Unix()
	}
	if msg.Agent == "" {
		msg.Agent = agent
	}
	sess.Messages = append(sess.Messages, msg)
	sess.UpdatedAt = msg.Timestamp
	return sess.ID
}

func (s *sessionStore) get(id string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, false
	}
	out := &Session{
		ID:          sess.ID,
		Agent:       sess.Agent,
		Source:      sess.Source,
		TaskRef:     sess.TaskRef,
		TaskSummary: sess.TaskSummary,
		Messages:    append([]ChatMessage(nil), sess.Messages...),
		CreatedAt:   sess.CreatedAt,
		UpdatedAt:   sess.UpdatedAt,
	}
	return out, true
}

func (s *sessionStore) delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		return false
	}
	delete(s.sessions, id)
	return true
}

func (s *sessionStore) list(agent string, source string) []SessionSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SessionSummary, 0, len(s.sessions))
	for _, sess := range s.sessions {
		if agent != "" && sess.Agent != agent {
			continue
		}
		if source != "" && sess.Source != source {
			continue
		}
		preview := ""
		if n := len(sess.Messages); n > 0 {
			preview = sess.Messages[n-1].Content
			if len(preview) > 80 {
				preview = preview[:80]
			}
		}
		out = append(out, SessionSummary{
			ID:           sess.ID,
			Agent:        sess.Agent,
			Source:       sess.Source,
			TaskRef:      sess.TaskRef,
			TaskSummary:  sess.TaskSummary,
			MessageCount: len(sess.Messages),
			UpdatedAt:    sess.UpdatedAt,
			LastPreview:  preview,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out
}

type ActiveRequest struct {
	Agent     string
	SessionID string
	Cancel    context.CancelFunc
	StartedAt time.Time
	Epoch     uint64
}

type AgentState struct {
	Name      string `json:"name"`
	Paused    bool   `json:"paused"`
	Active    bool   `json:"active"`
	Fleet   string `json:"warband"`
	SessionID string `json:"session_id,omitempty"`
	TaskRef   string `json:"task_ref,omitempty"`
	Source    string `json:"source,omitempty"`
	Since     int64  `json:"since,omitempty"`
}

func (g *gateway) fleetState(fleetFilter string) []AgentState {
	fleetFilter = strings.TrimSpace(fleetFilter)
	states := make([]AgentState, 0, len(g.order))
	paused := make(map[string]bool, len(g.order))
	g.injectionMu.Lock()
	for _, name := range g.order {
		paused[name] = g.pausedAgents[name]
	}
	g.injectionMu.Unlock()
	g.arMu.RLock()
	for _, name := range g.order {
		state := AgentState{
			Name:    name,
			Paused:  paused[name],
			Fleet: g.agentFleet(name),
		}
		if fleetFilter != "" && state.Fleet != fleetFilter {
			continue
		}
		if ar, ok := g.activeRequests[name]; ok {
			state.Active = true
			state.SessionID = ar.SessionID
			state.Source = detectSessionSource(ar.SessionID)
			state.Since = ar.StartedAt.Unix()
			if sess, ok := g.sessionStore.get(ar.SessionID); ok {
				state.TaskRef = sess.TaskRef
			}
		}
		states = append(states, state)
	}
	g.arMu.RUnlock()
	return states
}

type InjectionMessage struct {
	Agent     string `json:"agent"`
	Content   string `json:"content"`
	SessionID string `json:"session_id,omitempty"`
	QueuedAt  int64  `json:"queued_at"`
}

func (g *gateway) setActiveRequest(agent, sessionID string, cancel context.CancelFunc) uint64 {
	g.arMu.Lock()
	defer g.arMu.Unlock()
	if existing, ok := g.activeRequests[agent]; ok && existing.Cancel != nil {
		existing.Cancel()
	}
	g.arEpoch++
	epoch := g.arEpoch
	g.activeRequests[agent] = &ActiveRequest{
		Agent:     agent,
		SessionID: sessionID,
		Cancel:    cancel,
		StartedAt: time.Now(),
		Epoch:     epoch,
	}
	return epoch
}

func (g *gateway) clearActiveRequest(agent string, cancel context.CancelFunc) {
	g.arMu.Lock()
	defer g.arMu.Unlock()
	delete(g.activeRequests, agent)
}

func (g *gateway) clearActiveRequestIfCurrent(agent string, epoch uint64) {
	g.arMu.Lock()
	defer g.arMu.Unlock()
	if ar, ok := g.activeRequests[agent]; ok && ar != nil && ar.Epoch == epoch {
		delete(g.activeRequests, agent)
	}
}

func (g *gateway) activeSessionID(agent string) string {
	g.arMu.RLock()
	defer g.arMu.RUnlock()
	ar, ok := g.activeRequests[agent]
	if !ok || ar == nil {
		return ""
	}
	return strings.TrimSpace(ar.SessionID)
}

func (g *gateway) stopAgent(name string) bool {
	g.arMu.Lock()
	defer g.arMu.Unlock()
	ar, ok := g.activeRequests[name]
	if !ok {
		return false
	}
	if ar.Cancel != nil {
		ar.Cancel()
	}
	delete(g.activeRequests, name)
	return true
}

func (g *gateway) stopAllAgents() int {
	g.arMu.Lock()
	defer g.arMu.Unlock()
	count := 0
	for name, ar := range g.activeRequests {
		if ar.Cancel != nil {
			ar.Cancel()
		}
		delete(g.activeRequests, name)
		count++
	}
	return count
}
