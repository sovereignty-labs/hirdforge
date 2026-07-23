package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type HuntScope struct {
	HuntID       string
	Permissions  map[string]map[string]struct{} // service -> allowed actions
	MaxRequests  int
	UsedRequests int
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

type AuditEntry struct {
	Timestamp time.Time              `json:"timestamp"`
	HuntID    string                 `json:"hunt_id"`
	Service   string                 `json:"service"`
	Action    string                 `json:"action"`
	Params    map[string]interface{} `json:"params,omitempty"`
	Status    string                 `json:"status"`
	Note      string                 `json:"note,omitempty"`
	Error     string                 `json:"error,omitempty"`
}

type Trigger struct {
	Type       string                 `json:"type"`
	Summary    string                 `json:"summary"`
	OccurredAt time.Time              `json:"occurred_at"`
	Source     map[string]interface{} `json:"source,omitempty"`
}

type WriteQueue struct {
	QueueID   string                 `json:"queue_id"`
	HuntID    string                 `json:"hunt_id"`
	Service   string                 `json:"service"`
	Action    string                 `json:"action"`
	AgentName string                 `json:"agent_name,omitempty"`
	Params    map[string]interface{} `json:"params"`
	Status    string                 `json:"status"`
	Error     string                 `json:"error,omitempty"`
	Note      string                 `json:"note,omitempty"`
	Trigger   *Trigger               `json:"trigger,omitempty"`
	QueuedAt  time.Time              `json:"queued_at"`
}

type ServiceCredential struct {
	Type  string            `json:"type"`
	Token string            `json:"token"`
	Extra map[string]string `json:"extra"`
}

type ServiceHandler interface {
	Name() string
	SupportedActions() []string
	Execute(action string, params map[string]interface{}, cred ServiceCredential) (interface{}, error)
	IsWriteAction(action string) bool
}

type lockboxState struct {
	mu       sync.RWMutex
	hunts    map[string]*HuntScope
	audits   map[string][]AuditEntry
	queues   map[string]*WriteQueue
	services map[string]ServiceHandler
	tools    map[string]ToolHandler
	queueSeq uint64

	upstreamMCPURL    string
	upstreamSessionID string
	upstreamTools     map[string]upstreamTool
	upstreamRPCSeq    uint64
	upstreamInitMu    sync.Mutex
	googleUserEmail   string
}

type ToolHandler struct {
	Name        string
	Description string
	InputSchema map[string]interface{}
	WriteTier   string
	Handler     func(params map[string]interface{}) (interface{}, error)
}

type permissionSpec struct {
	Service string   `json:"service"`
	Actions []string `json:"actions"`
}

type registerReq struct {
	HuntID      string           `json:"hunt_id"`
	Permissions []permissionSpec `json:"permissions"`
	MaxRequests int              `json:"max_requests"`
	TTLSeconds  int              `json:"ttl_seconds"`
}

type actionReq struct {
	HuntID    string                 `json:"hunt_id"`
	Service   string                 `json:"service"`
	Action    string                 `json:"action"`
	AgentName string                 `json:"agent_name,omitempty"`
	Params    map[string]interface{} `json:"params"`
	Trigger   *Trigger               `json:"trigger,omitempty"`
}

type approveWriteReq struct {
	HuntID   string `json:"hunt_id"`
	QueueID  string `json:"queue_id"`
	Approved *bool  `json:"approved"`
}

type reviseWriteReq struct {
	HuntID  string `json:"hunt_id"`
	QueueID string `json:"queue_id"`
	Note    string `json:"note"`
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func decodeJSONStrict(r *http.Request, dst interface{}) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("unexpected trailing data")
	}
	return nil
}

func cloneParams(in map[string]interface{}) map[string]interface{} {
	if in == nil {
		return map[string]interface{}{}
	}
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneTrigger(in *Trigger) *Trigger {
	if in == nil {
		return nil
	}
	out := *in
	if out.OccurredAt.IsZero() {
		out.OccurredAt = time.Now().UTC()
	}
	return &out
}

func parseTriggerTime(raw interface{}) (time.Time, bool) {
	switch v := raw.(type) {
	case time.Time:
		return v.UTC(), true
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC(), true
			}
		}
	}
	return time.Time{}, false
}

func parseTriggerMap(raw map[string]interface{}) *Trigger {
	if raw == nil {
		return nil
	}
	trigger := &Trigger{
		Type:    strings.TrimSpace(fmt.Sprint(raw["type"])),
		Summary: strings.TrimSpace(fmt.Sprint(raw["summary"])),
	}
	explicitOccurredAt := false
	if t, ok := parseTriggerTime(raw["occurred_at"]); ok {
		trigger.OccurredAt = t
		explicitOccurredAt = true
	}
	if source, ok := raw["source"].(map[string]interface{}); ok && len(source) > 0 {
		trigger.Source = source
	}
	if trigger.Type == "" && trigger.Summary == "" && trigger.Source == nil && !explicitOccurredAt {
		return nil
	}
	if trigger.OccurredAt.IsZero() {
		trigger.OccurredAt = time.Now().UTC()
	}
	return trigger
}

func parseTriggerValue(raw interface{}) *Trigger {
	switch v := raw.(type) {
	case nil:
		return nil
	case *Trigger:
		return cloneTrigger(v)
	case Trigger:
		return cloneTrigger(&v)
	case map[string]interface{}:
		return parseTriggerMap(v)
	case json.RawMessage:
		if len(v) == 0 {
			return nil
		}
		var out Trigger
		if err := json.Unmarshal(v, &out); err == nil {
			if out.OccurredAt.IsZero() {
				out.OccurredAt = time.Now().UTC()
			}
			return &out
		}
		var generic map[string]interface{}
		if err := json.Unmarshal(v, &generic); err == nil {
			return parseTriggerMap(generic)
		}
		return nil
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil
		}
		var out Trigger
		if err := json.Unmarshal([]byte(s), &out); err == nil {
			if out.OccurredAt.IsZero() {
				out.OccurredAt = time.Now().UTC()
			}
			return &out
		}
		var generic map[string]interface{}
		if err := json.Unmarshal([]byte(s), &generic); err == nil {
			return parseTriggerMap(generic)
		}
		return &Trigger{
			Summary:    s,
			OccurredAt: time.Now().UTC(),
		}
	default:
		return nil
	}
}

func extractTrigger(args map[string]interface{}) *Trigger {
	if args == nil {
		return nil
	}
	raw, ok := args["_trigger"]
	if !ok {
		return nil
	}
	delete(args, "_trigger")
	return parseTriggerValue(raw)
}

func extractAgentName(args map[string]interface{}) string {
	if args == nil {
		return ""
	}
	raw, ok := args["_agent_name"]
	if !ok {
		return ""
	}
	delete(args, "_agent_name")
	if raw == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(raw))
}

func actionSupported(svc ServiceHandler, action string) bool {
	for _, a := range svc.SupportedActions() {
		if a == action {
			return true
		}
	}
	return false
}

func newLockboxState() *lockboxState {
	return &lockboxState{
		hunts:         map[string]*HuntScope{},
		audits:        map[string][]AuditEntry{},
		queues:        map[string]*WriteQueue{},
		services:      map[string]ServiceHandler{},
		tools:         map[string]ToolHandler{},
		upstreamTools: map[string]upstreamTool{},
	}
}

func registerRoutes(mux *http.ServeMux, state *lockboxState) {
	mux.HandleFunc("/register", state.registerHunt)
	mux.HandleFunc("/action", state.action)
	mux.HandleFunc("/approve-write", state.approveWrite)
	mux.HandleFunc("/revise", state.reviseWrite)
	mux.HandleFunc("/audit/", state.audit)
	mux.HandleFunc("/queues", state.listQueues)
	mux.HandleFunc("/health", state.health)
	mux.HandleFunc("/mcp", state.mcp)
}

func main() {
	port := flag.Int("port", 8083, "HTTP port")
	googleClientID := flag.String("google-client-id", "", "Google OAuth client ID")
	googleClientSecret := flag.String("google-client-secret", "", "Google OAuth client secret")
	googleUserEmail := flag.String("google-user-email", "", "Google user email to inject into tool params")
	googleTokenFile := flag.String("google-token-file", "/vault/secrets/google-token.json", "Path to stored Google OAuth token")
	upstreamMCP := flag.String("upstream-mcp", "", "Upstream MCP server URL")
	flag.Parse()

	state := newLockboxState()
	state.upstreamMCPURL = strings.TrimSpace(*upstreamMCP)
	state.googleUserEmail = strings.TrimSpace(*googleUserEmail)
	state.services["mock"] = &MockService{}
	if hf := newHirdforgeServiceFromEnv(); hf != nil {
		state.services["hirdforge"] = hf
		log.Printf("lockbox: hirdforge merge service enabled (gateway %s)", hf.GatewayURL)
	} else {
		log.Printf("lockbox: hirdforge merge service disabled (no HIRDFORGE_GATEWAY_URL)")
	}

	if strings.TrimSpace(*googleClientID) != "" || strings.TrimSpace(*googleClientSecret) != "" {
		clients, err := setupGoogleClients(context.Background(), *googleClientID, *googleClientSecret, *googleTokenFile)
		if err != nil {
			log.Fatalf("google setup failed: %v", err)
		}
		registerGoogleTools(state, clients)
	}
	if state.upstreamMCPURL != "" {
		if err := state.discoverUpstreamTools(); err != nil {
			log.Printf("warning: upstream MCP unavailable: %v", err)
		} else {
			state.mu.RLock()
			n := len(state.upstreamTools)
			state.mu.RUnlock()
			log.Printf("upstream MCP connected: discovered %d tools", n)
		}
	}

	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for range t.C {
			state.pruneExpired()
		}
	}()

	mux := http.NewServeMux()
	registerRoutes(mux, state)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("valhalla-lockbox listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
