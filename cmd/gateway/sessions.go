package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ChatMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp"`
	Agent     string `json:"agent"`
}

type Session struct {
	ID        string        `json:"id"`
	Agent     string        `json:"agent"`
	Messages  []ChatMessage `json:"messages"`
	CreatedAt int64         `json:"created_at"`
	UpdatedAt int64         `json:"updated_at"`
}

type SessionSummary struct {
	ID           string `json:"id"`
	Agent        string `json:"agent"`
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
		return sess
	}
	sess := &Session{
		ID:        id,
		Agent:     agent,
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
		ID:        sess.ID,
		Agent:     sess.Agent,
		Messages:  append([]ChatMessage(nil), sess.Messages...),
		CreatedAt: sess.CreatedAt,
		UpdatedAt: sess.UpdatedAt,
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

func (s *sessionStore) list(agent string) []SessionSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SessionSummary, 0, len(s.sessions))
	for _, sess := range s.sessions {
		if agent != "" && sess.Agent != agent {
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
}

func (g *gateway) setActiveRequest(agent, sessionID string, cancel context.CancelFunc) {
	g.arMu.Lock()
	defer g.arMu.Unlock()
	if existing, ok := g.activeRequests[agent]; ok && existing.Cancel != nil {
		existing.Cancel()
	}
	g.activeRequests[agent] = &ActiveRequest{
		Agent:     agent,
		SessionID: sessionID,
		Cancel:    cancel,
		StartedAt: time.Now(),
	}
}

func (g *gateway) clearActiveRequest(agent string, cancel context.CancelFunc) {
	g.arMu.Lock()
	defer g.arMu.Unlock()
	if _, ok := g.activeRequests[agent]; ok {
		delete(g.activeRequests, agent)
	}
}

func (g *gateway) stopAgent(name string) bool {
	g.arMu.Lock()
	defer g.arMu.Unlock()
	ar, ok := g.activeRequests[name]
	if !ok {
		return false
	}
	ar.Cancel()
	delete(g.activeRequests, name)
	return true
}

func (g *gateway) stopAllAgents() int {
	g.arMu.Lock()
	defer g.arMu.Unlock()
	count := 0
	for name, ar := range g.activeRequests {
		ar.Cancel()
		delete(g.activeRequests, name)
		count++
	}
	return count
}
