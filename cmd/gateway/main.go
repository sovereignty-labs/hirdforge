package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	taskspkg "github.com/kitporath/project_valhalla/pkg/tasks"
)

var thinkTagRE = regexp.MustCompile(`(?s)<think>.*?</think>`)

const agentRequestTimeout = 120 * time.Second

var streamTimeout = 600 * time.Second

//go:embed index.html
var dashboardHTML string

type Agent struct {
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	Healthy        bool     `json:"healthy"`
	Model          string   `json:"model"`
	Tools          []string `json:"tools"`
	UptimeSeconds  int      `json:"uptime_seconds"`
	RequestsServed int64    `json:"requests_served"`
	ToolCallsMade  int64    `json:"tool_calls_made"`
}

type Event struct {
	Time    string `json:"time"`
	Type    string `json:"type"`
	Agent   string `json:"agent"`
	Summary string `json:"summary"`
}

type taskSocketEvent struct {
	Type        string `json:"type"`
	Agent       string `json:"agent"`
	DelegatedBy string `json:"delegated_by"`
	TaskID      string `json:"task_id"`
	Result      string `json:"result,omitempty"`
	Error       string `json:"error,omitempty"`
	Timestamp   string `json:"timestamp"`
}

type Notification struct {
	From      string `json:"from"`
	TaskID    string `json:"task_id"`
	Agent     string `json:"agent"`
	State     string `json:"state"`
	Result    string `json:"result"`
	Timestamp string `json:"timestamp"`
}

type approvalQueueItem struct {
	QueueID  string                 `json:"queue_id"`
	HuntID   string                 `json:"hunt_id"`
	Service  string                 `json:"service"`
	Action   string                 `json:"action"`
	Params   map[string]interface{} `json:"params"`
	Status   string                 `json:"status"`
	QueuedAt string                 `json:"queued_at"`
}

type approvalsResponse struct {
	Status string              `json:"status"`
	Queues []approvalQueueItem `json:"queues"`
}

type PodInfo struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Status    string `json:"status"`
	Node      string `json:"node"`
	Restarts  int64  `json:"restarts"`
	Age       string `json:"age"`
	Ready     bool   `json:"ready"`
	Image     string `json:"image"`
}

type NodeInfo struct {
	Name              string   `json:"name"`
	Status            string   `json:"status"`
	Roles             []string `json:"roles"`
	KubeletVersion    string   `json:"kubelet_version"`
	OS                string   `json:"os"`
	Architecture      string   `json:"architecture"`
	AllocatableCPU    string   `json:"allocatable_cpu"`
	AllocatableMemory string   `json:"allocatable_memory"`
	CPUPercent        float64  `json:"cpu_percent"`
	MemoryPercent     float64  `json:"memory_percent"`
}

type podLogsResponse struct {
	Pod  string `json:"pod"`
	Logs string `json:"logs"`
}

type gitopsDeploymentInfo struct {
	Image      string `json:"image"`
	SHA        string `json:"sha"`
	DeployedAt string `json:"deployed_at"`
}

type gitopsStatusResponse struct {
	App               string                 `json:"app"`
	SyncStatus        string                 `json:"sync_status"`
	HealthStatus      string                 `json:"health_status"`
	LastSync          string                 `json:"last_sync"`
	DeployedSHA       string                 `json:"deployed_sha"`
	GitHeadSHA        string                 `json:"git_head_sha"`
	Drift             bool                   `json:"drift"`
	SelfHeal          bool                   `json:"self_heal"`
	RecentDeployments []gitopsDeploymentInfo `json:"recent_deployments"`
}

type historyTaskItem struct {
	ID       int64    `json:"id"`
	Title    string   `json:"title"`
	Agents   []string `json:"agents"`
	Outcome  string   `json:"outcome"`
	PRNumber int64    `json:"pr_number"`
	ClosedAt string   `json:"closed_at"`
}

type historyTasksResponse struct {
	Tasks []historyTaskItem `json:"tasks"`
}

type historyDeploymentItem struct {
	SHA         string `json:"sha"`
	Message     string `json:"message"`
	DeployedAt  string `json:"deployed_at"`
	TriggeredBy string `json:"triggered_by"`
}

type historyDeploymentsResponse struct {
	Deployments []historyDeploymentItem `json:"deployments"`
}

type certExpiryInfo struct {
	Name          string `json:"name"`
	Expiry        string `json:"expiry"`
	DaysRemaining int64  `json:"days_remaining"`
}

type certsResponse struct {
	Certs []certExpiryInfo `json:"certs"`
}

type K8sEventInfo struct {
	Type    string `json:"type"`
	Reason  string `json:"reason"`
	Object  string `json:"object"`
	Message string `json:"message"`
	Time    string `json:"time"`
	Count   int64  `json:"count"`
}

type PodResourceInfo struct {
	Pod        string `json:"pod"`
	CPURequest string `json:"cpu_request"`
	CPULimit   string `json:"cpu_limit"`
	MemRequest string `json:"mem_request"`
	MemLimit   string `json:"mem_limit"`
}

type giteaCommit struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}

type giteaRepoInfo struct {
	Name          string `json:"name"`
	Stars         int64  `json:"stars"`
	Forks         int64  `json:"forks"`
	OpenIssues    int64  `json:"open_issues"`
	Size          int64  `json:"size"`
	DefaultBranch string `json:"default_branch"`
}

type giteaPRInfo struct {
	Number    int64    `json:"number"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	User      string   `json:"user"`
	Repo      string   `json:"repo"`
	Base      string   `json:"base"`
	Head      string   `json:"head"`
	Body      string   `json:"body"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	HTMLURL   string   `json:"html_url"`
	Labels    []string `json:"labels"`
	Approvals int64    `json:"approvals"`
	Mergeable bool     `json:"mergeable"`
}

type giteaRepoListItem struct {
	Name        string `json:"name"`
	Owner       string `json:"owner"`
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	Language    string `json:"language"`
	OpenPRs     int64  `json:"open_prs"`
	Stars       int64  `json:"stars"`
	HTMLURL     string `json:"html_url"`
}

type agentConfigureRequest struct {
	Model        string      `json:"model"`
	InferenceURL string      `json:"inference_url"`
	Tools        interface{} `json:"tools"`
	Peers        interface{} `json:"peers"`
}

type giteaContentResponse struct {
	Type     string `json:"type"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
	SHA      string `json:"sha"`
	Path     string `json:"path"`
}

type messageReq struct {
	Agent     string `json:"agent"`
	Content   string `json:"content"`
	SessionID string `json:"session_id"`
}

type dispatchReq struct {
	Agent   string `json:"agent"`
	Content string `json:"content"`
	From    string `json:"from"`
}

type taskSendRequest struct {
	Content string `json:"content"`
	From    string `json:"from"`
}

type dispatchResp struct {
	Agent  string `json:"agent"`
	TaskID string `json:"task_id"`
	Error  string `json:"error,omitempty"`
}

type agentHealthResponse struct {
	Status         string   `json:"status"`
	Model          string   `json:"model"`
	Tools          []string `json:"tools"`
	UptimeSeconds  int      `json:"uptime_seconds"`
	RequestsServed int64    `json:"requests_served"`
	ToolCallsMade  int64    `json:"tool_calls_made"`
}

type gateway struct {
	mu                sync.RWMutex
	agents            map[string]*Agent
	order             []string
	prReviewMu        sync.RWMutex
	prReviewState     map[string]prReviewState
	eventMu           sync.Mutex
	events            []Event
	eventCap          int
	k8s               *k8sState
	wsMu              sync.Mutex
	wsConns           []*wsClient
	sessionStore      *sessionStore
	settings          *settingsStore
	notifMu           sync.Mutex
	notifications     []Notification
	notifCap          int
	lastSessionMu     sync.RWMutex
	lastSession       map[string]string
	arMu              sync.RWMutex
	activeRequests    map[string]*ActiveRequest
	injectionMu       sync.Mutex
	injections        map[string][]InjectionMessage // keyed by agent name
	pausedAgents      map[string]bool
	syncMu           sync.Mutex
	webhookSecret     string
	reviewAgent       string
	taskRepo          string
	giteaURL          string
	giteaToken        string
	seidrURL          string
	discordWebhookURL string
}

type settingsStore struct {
	mu   sync.RWMutex
	data map[string]interface{}
}

func newSettingsStore() *settingsStore {
	return &settingsStore{
		data: map[string]interface{}{
			"callbacks.enabled":          true,
			"callbacks.session_routing":  "active",
			"delegation.timeout_seconds": 120,
			"ui.theme":                   "dark",
		},
	}
}

func (s *settingsStore) All() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]interface{}, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

func (s *settingsStore) SetBulk(in map[string]interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range in {
		s.data[k] = v
	}
}

func (s *settingsStore) Set(key string, value interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
}

func (s *settingsStore) GetBool(key string, fallback bool) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[key]
	if !ok {
		return fallback
	}
	b, ok := v.(bool)
	if !ok {
		return fallback
	}
	return b
}

func (s *settingsStore) GetString(key, fallback string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[key]
	if !ok {
		return fallback
	}
	str, ok := v.(string)
	if !ok || strings.TrimSpace(str) == "" {
		return fallback
	}
	return str
}

var (
	taskCompleteRE = regexp.MustCompile(`^completed task (task-[a-f0-9]+) \(from ([^)]+)\):\s*(.*)$`)
	taskFailedRE   = regexp.MustCompile(`^failed task (task-[a-f0-9]+) \(from ([^)]+)\):\s*(.*)$`)
)

type wsClient struct {
	conn net.Conn
	r    io.Reader
	mu   sync.Mutex
}

type k8sState struct {
	enabled bool
	client  *http.Client
	token   string
	podNS   string
	mu      sync.RWMutex
	pods    []PodInfo
	nodes   []NodeInfo
}

func die(msg string, err error) { log.Fatalf("%s: %v", msg, err) }

func parseAgents(raw string) (map[string]*Agent, []string, error) {
	out := map[string]*Agent{}
	order := []string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, url, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(url) == "" {
			return nil, nil, fmt.Errorf("invalid agent entry %q", part)
		}
		name, url = strings.TrimSpace(name), strings.TrimRight(strings.TrimSpace(url), "/")
		if _, exists := out[name]; exists {
			return nil, nil, fmt.Errorf("duplicate agent name %q", name)
		}
		out[name] = &Agent{Name: name, URL: url}
		order = append(order, name)
	}
	if len(out) == 0 {
		return nil, nil, fmt.Errorf("no agents configured")
	}
	sort.Strings(order)
	return out, order, nil
}

func (g *gateway) addEvent(eventType, agent, summary string) {
	e := Event{Time: time.Now().Format(time.RFC3339), Type: eventType, Agent: agent, Summary: summary}
	g.eventMu.Lock()
	if len(g.events) == g.eventCap {
		copy(g.events, g.events[1:])
		g.events[len(g.events)-1] = e
	} else {
		g.events = append(g.events, e)
	}
	g.eventMu.Unlock()
	g.broadcastEvent(e)
	if structured, ok := parseTaskSocketEvent(e); ok {
		g.broadcastPayload(structured)
		go g.notifyDelegatingAgent(structured)
	}
}

func writeWSFrame(conn net.Conn, opcode byte, payload []byte) error {
	header := []byte{0x80 | (opcode & 0x0f)}
	n := len(payload)
	switch {
	case n < 126:
		header = append(header, byte(n))
	case n < 65536:
		header = append(header, 126, byte(n>>8), byte(n))
	default:
		header = append(header, 127, 0, 0, 0, 0, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	if _, err := conn.Write(header); err != nil {
		return err
	}
	_, err := conn.Write(payload)
	return err
}

func (c *wsClient) writeFrame(opcode byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeWSFrame(c.conn, opcode, payload)
}

func (g *gateway) removeWSConn(target *wsClient) {
	g.wsMu.Lock()
	defer g.wsMu.Unlock()
	out := g.wsConns[:0]
	for _, c := range g.wsConns {
		if c != target {
			out = append(out, c)
		}
	}
	g.wsConns = out
}

func (g *gateway) broadcastEvent(e Event) {
	g.broadcastPayload(e)
}

func (g *gateway) broadcastPayload(v interface{}) {
	payload, err := json.Marshal(v)
	if err != nil {
		return
	}
	g.wsMu.Lock()
	conns := append([]*wsClient(nil), g.wsConns...)
	g.wsMu.Unlock()
	for _, c := range conns {
		if err := c.writeFrame(0x1, payload); err != nil {
			_ = c.conn.Close()
			g.removeWSConn(c)
		}
	}
}

func parseTaskSocketEvent(e Event) (taskSocketEvent, bool) {
	if e.Type != "task" {
		return taskSocketEvent{}, false
	}
	if matches := taskCompleteRE.FindStringSubmatch(e.Summary); len(matches) == 4 {
		return taskSocketEvent{
			Type:        "task_complete",
			Agent:       e.Agent,
			DelegatedBy: strings.TrimSpace(matches[2]),
			TaskID:      matches[1],
			Result:      strings.TrimSpace(matches[3]),
			Timestamp:   e.Time,
		}, true
	}
	if matches := taskFailedRE.FindStringSubmatch(e.Summary); len(matches) == 4 {
		return taskSocketEvent{
			Type:        "task_failed",
			Agent:       e.Agent,
			DelegatedBy: strings.TrimSpace(matches[2]),
			TaskID:      matches[1],
			Error:       strings.TrimSpace(matches[3]),
			Timestamp:   e.Time,
		}, true
	}
	return taskSocketEvent{}, false
}

func (g *gateway) notifyDelegatingAgent(evt taskSocketEvent) {
	if g.settings != nil && !g.settings.GetBool("callbacks.enabled", true) {
		return
	}
	delegatedBy := strings.TrimSpace(evt.DelegatedBy)
	if delegatedBy == "" {
		return
	}
	agent, ok := g.getAgent(delegatedBy)
	if !ok {
		log.Printf("task callback: unknown delegating agent %q for task %s", delegatedBy, evt.TaskID)
		return
	}
	content := ""
	switch evt.Type {
	case "task_complete":
		content = fmt.Sprintf("[Task Complete] %s finished task %s: %s", evt.Agent, evt.TaskID, evt.Result)
	case "task_failed":
		content = fmt.Sprintf("[Task Failed] %s failed task %s: %s", evt.Agent, evt.TaskID, evt.Error)
	default:
		return
	}
	sessionID := "task-callbacks"
	if g.settings == nil || g.settings.GetString("callbacks.session_routing", "active") == "active" {
		g.lastSessionMu.RLock()
		if last := strings.TrimSpace(g.lastSession[delegatedBy]); last != "" {
			sessionID = last
		}
		g.lastSessionMu.RUnlock()
	}
	body, err := json.Marshal(map[string]string{
		"content":    content,
		"session_id": sessionID,
	})
	if err != nil {
		return
	}
	client := &http.Client{Timeout: agentRequestTimeout}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(agent.URL, "/")+"/message", bytes.NewReader(body))
	if err != nil {
		log.Printf("task callback: build request failed for %s: %v", delegatedBy, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("task callback: post to %s failed: %v", delegatedBy, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("task callback: post to %s returned %d: %s", delegatedBy, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
}

func readWSFrame(r io.Reader) (opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	opcode = hdr[0] & 0x0f
	masked := (hdr[1] & 0x80) != 0
	payloadLen := int64(hdr[1] & 0x7f)
	switch payloadLen {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		payloadLen = int64(ext[0])<<8 | int64(ext[1])
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		payloadLen = int64(ext[0])<<56 | int64(ext[1])<<48 | int64(ext[2])<<40 | int64(ext[3])<<32 |
			int64(ext[4])<<24 | int64(ext[5])<<16 | int64(ext[6])<<8 | int64(ext[7])
	}
	if payloadLen < 0 || payloadLen > 1<<20 {
		return 0, nil, fmt.Errorf("websocket payload too large: %d", payloadLen)
	}
	var maskKey [4]byte
	if masked {
		if _, err = io.ReadFull(r, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}
	payload = make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err = io.ReadFull(r, payload); err != nil {
			return 0, nil, err
		}
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return opcode, payload, nil
}

func wsAccept(key string) string {
	h := sha1.New()
	_, _ = h.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func (g *gateway) eventsNewest() []Event {
	g.eventMu.Lock()
	defer g.eventMu.Unlock()
	out := make([]Event, len(g.events))
	for i := range g.events {
		out[i] = g.events[len(g.events)-1-i]
	}
	return out
}

func (g *gateway) addNotification(n Notification) {
	g.notifMu.Lock()
	if len(g.notifications) == g.notifCap {
		copy(g.notifications, g.notifications[1:])
		g.notifications[len(g.notifications)-1] = n
	} else {
		g.notifications = append(g.notifications, n)
	}
	g.notifMu.Unlock()
	g.broadcastPayload(n)
}

func (g *gateway) notificationsNewest() []Notification {
	g.notifMu.Lock()
	defer g.notifMu.Unlock()
	out := make([]Notification, len(g.notifications))
	for i := range g.notifications {
		out[i] = g.notifications[len(g.notifications)-1-i]
	}
	return out
}

func (g *gateway) snapshotAgents() []Agent {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Agent, 0, len(g.order))
	for _, n := range g.order {
		if a := g.agents[n]; a != nil {
			out = append(out, *a)
		}
	}
	return out
}

func (g *gateway) getAgent(name string) (*Agent, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	a, ok := g.agents[name]
	if !ok {
		return nil, false
	}
	cp := *a
	return &cp, true
}

func (g *gateway) updateAgent(updated Agent) {
	g.mu.Lock()
	old := g.agents[updated.Name]
	if old == nil {
		g.mu.Unlock()
		return
	}
	changed := old.Healthy != updated.Healthy
	*old = updated
	g.mu.Unlock()
	if changed {
		state := "unhealthy"
		if updated.Healthy {
			state = "healthy"
		}
		msg := fmt.Sprintf("%s became %s", updated.Name, state)
		log.Printf("agent health changed: %s", msg)
		g.addEvent("health_change", updated.Name, msg)
	}
}

func (g *gateway) refreshAgentHealth() {
	client := &http.Client{Timeout: 3 * time.Second}
	g.mu.RLock()
	names := append([]string(nil), g.order...)
	urls := make(map[string]string, len(g.order))
	for _, n := range g.order {
		urls[n] = g.agents[n].URL
	}
	g.mu.RUnlock()

	for _, name := range names {
		updated := Agent{Name: name, URL: urls[name], Tools: []string{}}
		h, err := queryAgentHealth(client, urls[name])
		if err == nil {
			updated.Healthy = true
			updated.Model = h.Model
			updated.Tools = h.Tools
			updated.UptimeSeconds = h.UptimeSeconds
			updated.RequestsServed = h.RequestsServed
			updated.ToolCallsMade = h.ToolCallsMade
		} else {
			updated.Healthy = false
			updated.Model = ""
			updated.Tools = []string{}
			updated.UptimeSeconds = 0
			updated.RequestsServed = 0
			updated.ToolCallsMade = 0
		}
		g.updateAgent(updated)
	}
}

func queryAgentHealth(client *http.Client, baseURL string) (agentHealthResponse, error) {
	var h agentHealthResponse
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/health")
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return h, fmt.Errorf("status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return h, err
	}
	return h, nil
}

func initK8s() *k8sState {
	tokenPath := "/var/run/secrets/kubernetes.io/serviceaccount/token"
	caPath := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	nsPath := "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	tok, tokErr := os.ReadFile(tokenPath)
	ca, caErr := os.ReadFile(caPath)
	ns, nsErr := os.ReadFile(nsPath)
	if tokErr != nil || caErr != nil || nsErr != nil {
		log.Printf("k8s integration disabled (serviceaccount files not found)")
		return &k8sState{enabled: false, podNS: "valhalla"}
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		log.Printf("k8s integration disabled (invalid CA cert)")
		return &k8sState{enabled: false, podNS: "valhalla"}
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	client := &http.Client{Timeout: 5 * time.Second, Transport: tr}
	namespace := strings.TrimSpace(string(ns))
	if namespace == "" {
		namespace = "valhalla"
	}
	return &k8sState{enabled: true, client: client, token: strings.TrimSpace(string(tok)), podNS: namespace}
}

func (k *k8sState) setPods(p []PodInfo) {
	k.mu.Lock()
	k.pods = p
	k.mu.Unlock()
}

func (k *k8sState) setNodes(n []NodeInfo) {
	k.mu.Lock()
	k.nodes = n
	k.mu.Unlock()
}

func (k *k8sState) snapshotPods() []PodInfo {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]PodInfo, len(k.pods))
	copy(out, k.pods)
	return out
}

func (k *k8sState) snapshotNodes() []NodeInfo {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]NodeInfo, len(k.nodes))
	copy(out, k.nodes)
	return out
}

func (k *k8sState) do(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, "https://kubernetes.default.svc"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return k.client.Do(req)
}

func (k *k8sState) get(path string) (map[string]interface{}, error) {
	resp, err := k.do(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (k *k8sState) getBytes(path string) (int, []byte, error) {
	resp, err := k.do(http.MethodGet, path, nil)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, body, nil
}

func (k *k8sState) getJSONWithStatus(path string) (int, map[string]interface{}, []byte, error) {
	status, body, err := k.getBytes(path)
	if err != nil {
		return 0, nil, nil, err
	}
	if status < 200 || status >= 300 {
		return status, nil, body, nil
	}
	var out map[string]interface{}
	if len(body) == 0 {
		return status, map[string]interface{}{}, body, nil
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return status, nil, body, err
	}
	return status, out, body, nil
}

func (k *k8sState) patchJSON(path string, payload interface{}) (int, []byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequest(http.MethodPatch, "https://kubernetes.default.svc"+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Content-Type", "application/merge-patch+json")
	resp, err := k.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, respBody, nil
}

func asMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}
func asSlice(v interface{}) []interface{} {
	s, _ := v.([]interface{})
	return s
}
func asString(v interface{}) string {
	s, _ := v.(string)
	return s
}
func asInt64(v interface{}) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	default:
		return 0
	}
}

func asInt(v string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(v))
	return n
}

func asBool(v interface{}) bool {
	b, _ := v.(bool)
	return b
}

func ageFrom(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

func parsePods(body map[string]interface{}) []PodInfo {
	items := asSlice(body["items"])
	pods := make([]PodInfo, 0, len(items))
	for _, item := range items {
		im := asMap(item)
		meta := asMap(im["metadata"])
		spec := asMap(im["spec"])
		status := asMap(im["status"])
		ready := false
		for _, c := range asSlice(status["conditions"]) {
			cm := asMap(c)
			if asString(cm["type"]) == "Ready" && asString(cm["status"]) == "True" {
				ready = true
				break
			}
		}
		restarts := int64(0)
		image := ""
		cs := asSlice(status["containerStatuses"])
		if len(cs) > 0 {
			first := asMap(cs[0])
			restarts = asInt64(first["restartCount"])
			image = asString(first["image"])
		}
		if image == "" {
			containers := asSlice(spec["containers"])
			if len(containers) > 0 {
				image = asString(asMap(containers[0])["image"])
			}
		}
		pods = append(pods, PodInfo{
			Name:      asString(meta["name"]),
			Namespace: asString(meta["namespace"]),
			Status:    asString(status["phase"]),
			Node:      asString(spec["nodeName"]),
			Restarts:  restarts,
			Age:       ageFrom(asString(meta["creationTimestamp"])),
			Ready:     ready,
			Image:     image,
		})
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	return pods
}

func parseNodes(body map[string]interface{}) []NodeInfo {
	items := asSlice(body["items"])
	nodes := make([]NodeInfo, 0, len(items))
	for _, item := range items {
		im := asMap(item)
		meta := asMap(im["metadata"])
		status := asMap(im["status"])
		labels := asMap(meta["labels"])
		roles := []string{}
		for k := range labels {
			if strings.HasPrefix(k, "node-role.kubernetes.io/") {
				r := strings.TrimPrefix(k, "node-role.kubernetes.io/")
				if r == "" {
					r = "control-plane"
				}
				roles = append(roles, r)
			}
		}
		if len(roles) == 0 {
			roles = []string{"worker"}
		}
		sort.Strings(roles)
		nodeStatus := "Unknown"
		for _, c := range asSlice(status["conditions"]) {
			cm := asMap(c)
			if asString(cm["type"]) == "Ready" {
				if asString(cm["status"]) == "True" {
					nodeStatus = "Ready"
				} else {
					nodeStatus = "NotReady"
				}
				break
			}
		}
		nodeInfo := asMap(status["nodeInfo"])
		alloc := asMap(status["allocatable"])
		nodes = append(nodes, NodeInfo{
			Name:              asString(meta["name"]),
			Status:            nodeStatus,
			Roles:             roles,
			KubeletVersion:    asString(nodeInfo["kubeletVersion"]),
			OS:                asString(nodeInfo["osImage"]),
			Architecture:      asString(nodeInfo["architecture"]),
			AllocatableCPU:    asString(alloc["cpu"]),
			AllocatableMemory: asString(alloc["memory"]),
		})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes
}

type nodeUsage struct {
	cpuMilli int64
	memKi    int64
}

func parseCPUUsageNanoToMilli(q string) (int64, bool) {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0, false
	}
	if !strings.HasSuffix(q, "n") {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSuffix(q, "n"), 10, 64)
	if err != nil {
		return 0, false
	}
	return v / 1_000_000, true
}

func parseCPUAllocMilli(q string) (int64, bool) {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0, false
	}
	if strings.HasSuffix(q, "m") {
		v, err := strconv.ParseInt(strings.TrimSuffix(q, "m"), 10, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	// Fallback for plain core values (e.g., "4") if present.
	v, err := strconv.ParseInt(q, 10, 64)
	if err != nil {
		return 0, false
	}
	return v * 1000, true
}

func parseMemoryKi(q string) (int64, bool) {
	q = strings.TrimSpace(q)
	if q == "" || !strings.HasSuffix(q, "Ki") {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSuffix(q, "Ki"), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func parseNodeUsage(body map[string]interface{}) map[string]nodeUsage {
	items := asSlice(body["items"])
	out := make(map[string]nodeUsage, len(items))
	for _, item := range items {
		im := asMap(item)
		name := asString(asMap(im["metadata"])["name"])
		usage := asMap(im["usage"])
		cpuMilli, okCPU := parseCPUUsageNanoToMilli(asString(usage["cpu"]))
		memKi, okMem := parseMemoryKi(asString(usage["memory"]))
		if name == "" || !okCPU || !okMem {
			continue
		}
		out[name] = nodeUsage{cpuMilli: cpuMilli, memKi: memKi}
	}
	return out
}

func enrichNodesWithUsage(nodes []NodeInfo, usage map[string]nodeUsage) []NodeInfo {
	for i := range nodes {
		u, ok := usage[nodes[i].Name]
		if !ok {
			continue
		}
		if allocMilli, ok := parseCPUAllocMilli(nodes[i].AllocatableCPU); ok && allocMilli > 0 {
			nodes[i].CPUPercent = float64((u.cpuMilli * 100) / allocMilli)
		}
		if allocMemKi, ok := parseMemoryKi(nodes[i].AllocatableMemory); ok && allocMemKi > 0 {
			nodes[i].MemoryPercent = float64((u.memKi * 100) / allocMemKi)
		}
	}
	return nodes
}

func parseK8sEvents(body map[string]interface{}) []K8sEventInfo {
	items := asSlice(body["items"])
	events := make([]K8sEventInfo, 0, len(items))
	for _, item := range items {
		im := asMap(item)
		inv := asMap(im["involvedObject"])
		t := asString(im["lastTimestamp"])
		if t == "" {
			t = asString(im["eventTime"])
		}
		if t == "" {
			t = asString(im["firstTimestamp"])
		}
		events = append(events, K8sEventInfo{
			Type:    asString(im["type"]),
			Reason:  asString(im["reason"]),
			Object:  strings.ToLower(asString(inv["kind"])) + "/" + asString(inv["name"]),
			Message: asString(im["message"]),
			Time:    t,
			Count:   asInt64(im["count"]),
		})
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Time > events[j].Time })
	if len(events) > 50 {
		events = events[:50]
	}
	return events
}

func parsePodResources(body map[string]interface{}) []PodResourceInfo {
	items := asSlice(body["items"])
	out := make([]PodResourceInfo, 0, len(items))
	for _, item := range items {
		im := asMap(item)
		meta := asMap(im["metadata"])
		spec := asMap(im["spec"])
		info := PodResourceInfo{Pod: asString(meta["name"])}
		containers := asSlice(spec["containers"])
		for _, c := range containers {
			res := asMap(asMap(c)["resources"])
			req := asMap(res["requests"])
			lim := asMap(res["limits"])
			if info.CPURequest == "" {
				info.CPURequest = asString(req["cpu"])
			}
			if info.MemRequest == "" {
				info.MemRequest = asString(req["memory"])
			}
			if info.CPULimit == "" {
				info.CPULimit = asString(lim["cpu"])
			}
			if info.MemLimit == "" {
				info.MemLimit = asString(lim["memory"])
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pod < out[j].Pod })
	return out
}

func fetchGiteaJSON(client *http.Client, baseURL, token, path string, out interface{}) error {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func giteaRequest(client *http.Client, method, baseURL, token, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, strings.TrimRight(baseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	return client.Do(req)
}

func giteaGetJSONWithStatus(client *http.Client, baseURL, token, path string, out interface{}) (int, []byte, error) {
	resp, err := giteaRequest(client, http.MethodGet, baseURL, token, path, nil)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, body, nil
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, body, err
		}
	}
	return resp.StatusCode, body, nil
}

func resolveGatewayGiteaToken(flagToken string) string {
	flagToken = strings.TrimSpace(flagToken)
	if flagToken != "" {
		return flagToken
	}
	if data, err := os.ReadFile("/vault/secrets/gitea-token"); err == nil {
		if token := strings.TrimSpace(string(data)); token != "" {
			return token
		}
	}
	return strings.TrimSpace(os.Getenv("GITEA_TOKEN"))
}

func fetchGitOpsStatus(k8s *k8sState) gitopsStatusResponse {
	out := gitopsStatusResponse{App: "asgard", RecentDeployments: []gitopsDeploymentInfo{}}
	if k8s == nil || !k8s.enabled {
		return out
	}
	status, body, _, err := k8s.getJSONWithStatus("/apis/argoproj.io/v1alpha1/namespaces/argocd/applications/asgard")
	if err != nil || status == http.StatusNotFound {
		return out
	}
	if status < 200 || status >= 300 {
		return out
	}
	spec := asMap(body["spec"])
	appStatus := asMap(body["status"])
	sync := asMap(appStatus["sync"])
	health := asMap(appStatus["health"])
	operationState := asMap(appStatus["operationState"])
	syncPolicy := asMap(spec["syncPolicy"])
	automated := asMap(syncPolicy["automated"])
	history := asSlice(appStatus["history"])

	out.SyncStatus = asString(sync["status"])
	out.HealthStatus = asString(health["status"])
	out.DeployedSHA = shortSHA(asString(sync["revision"]))
	out.GitHeadSHA = shortSHA(asString(asMap(spec["source"])["targetRevision"]))
	out.Drift = strings.TrimSpace(out.SyncStatus) != "" && out.SyncStatus != "Synced"
	out.SelfHeal = asMap(syncPolicy["automated"]) != nil && (automated["selfHeal"] == true)
	if finished := asString(operationState["finishedAt"]); finished != "" {
		out.LastSync = finished
	} else if len(history) > 0 {
		out.LastSync = asString(asMap(history[0])["deployedAt"])
	}
	for _, item := range history {
		im := asMap(item)
		out.RecentDeployments = append(out.RecentDeployments, gitopsDeploymentInfo{
			Image:      "argocd",
			SHA:        shortSHA(asString(im["revision"])),
			DeployedAt: asString(im["deployedAt"]),
		})
		if len(out.RecentDeployments) == 5 {
			break
		}
	}
	return out
}

func fetchTaskHistory(client *http.Client, baseURL, token, repoFull, agentFilter, outcomeFilter string, limit int) historyTasksResponse {
	out := historyTasksResponse{Tasks: []historyTaskItem{}}
	if strings.TrimSpace(baseURL) == "" {
		return out
	}
	q := url.Values{}
	q.Set("state", "closed")
	q.Set("sort", "updated")
	q.Set("limit", strconv.Itoa(clampInt(limit, 50, 100)))
	path := fmt.Sprintf("/api/v1/repos/%s/issues?%s", repoFull, q.Encode())
	var issues []map[string]interface{}
	status, body, err := giteaGetJSONWithStatus(client, baseURL, token, path, &issues)
	if err != nil || status < 200 || status >= 300 {
		return out
	}
	_ = body
	for _, issue := range issues {
		labels := collectLabelNames(asSlice(issue["labels"]))
		outcome := ""
		switch {
		case slices.Contains(labels, "status/done"):
			outcome = "completed"
		case slices.Contains(labels, "status/failed"):
			outcome = "failed"
		default:
			continue
		}
		if outcomeFilter != "" && outcomeFilter != outcome {
			continue
		}
		agents := make([]string, 0, 4)
		for _, label := range labels {
			if strings.HasPrefix(label, "agent/") {
				agents = append(agents, strings.TrimPrefix(label, "agent/"))
			}
		}
		sort.Strings(agents)
		if agentFilter != "" && !slices.Contains(agents, agentFilter) {
			continue
		}
		prNum := extractPRNumber(asString(issue["body"]))
		out.Tasks = append(out.Tasks, historyTaskItem{
			ID:       asInt64(issue["number"]),
			Title:    asString(issue["title"]),
			Agents:   agents,
			Outcome:  outcome,
			PRNumber: prNum,
			ClosedAt: asString(issue["closed_at"]),
		})
		if len(out.Tasks) == clampInt(limit, 50, 100) {
			break
		}
	}
	return out
}

func fetchDeploymentHistory(client *http.Client, baseURL, token string, limit int) historyDeploymentsResponse {
	out := historyDeploymentsResponse{Deployments: []historyDeploymentItem{}}
	if strings.TrimSpace(baseURL) == "" {
		return out
	}
	path := fmt.Sprintf("/api/v1/repos/kit/valhalla-infra/commits?sha=main&limit=%d", clampInt(limit, 20, 100))
	var commits []map[string]interface{}
	status, _, err := giteaGetJSONWithStatus(client, baseURL, token, path, &commits)
	if err != nil || status < 200 || status >= 300 {
		return out
	}
	for _, commit := range commits {
		sha := asString(commit["sha"])
		include := false
		detailPath := fmt.Sprintf("/api/v1/repos/kit/valhalla-infra/git/commits/%s", url.PathEscape(sha))
		var detail map[string]interface{}
		if detailStatus, _, detailErr := giteaGetJSONWithStatus(client, baseURL, token, detailPath, &detail); detailErr == nil && detailStatus >= 200 && detailStatus < 300 {
			for _, file := range asSlice(detail["files"]) {
				filePath := asString(asMap(file)["filename"])
				if strings.Contains(filePath, "deployment-") && (strings.HasSuffix(filePath, ".yaml") || strings.HasSuffix(filePath, ".yml")) {
					include = true
					break
				}
			}
		}
		if !include {
			msg := asString(asMap(commit["commit"])["message"])
			if strings.Contains(strings.ToLower(msg), "deployment") || strings.Contains(strings.ToLower(msg), "image") {
				include = true
			}
		}
		if !include {
			continue
		}
		commitMeta := asMap(commit["commit"])
		author := asMap(commitMeta["author"])
		message := asString(commitMeta["message"])
		if i := strings.Index(message, "\n"); i >= 0 {
			message = message[:i]
		}
		out.Deployments = append(out.Deployments, historyDeploymentItem{
			SHA:         shortSHA(sha),
			Message:     message,
			DeployedAt:  asString(author["date"]),
			TriggeredBy: asString(author["name"]),
		})
		if len(out.Deployments) == clampInt(limit, 20, 100) {
			break
		}
	}
	return out
}

func listCertExpiries(k8s *k8sState, namespace string) certsResponse {
	out := certsResponse{Certs: []certExpiryInfo{}}
	if k8s == nil || !k8s.enabled {
		return out
	}
	status, body, _, err := k8s.getJSONWithStatus(fmt.Sprintf("/apis/cert-manager.io/v1/namespaces/%s/certificates", url.PathEscape(namespace)))
	if err != nil || status == http.StatusNotFound || status < 200 || status >= 300 {
		return out
	}
	for _, item := range asSlice(body["items"]) {
		im := asMap(item)
		meta := asMap(im["metadata"])
		statusMap := asMap(im["status"])
		expiry := asString(statusMap["notAfter"])
		daysRemaining := int64(0)
		if expiry != "" {
			if t, err := time.Parse(time.RFC3339, expiry); err == nil {
				daysRemaining = int64(time.Until(t).Hours() / 24)
			}
		}
		out.Certs = append(out.Certs, certExpiryInfo{
			Name:          asString(meta["name"]),
			Expiry:        expiry,
			DaysRemaining: daysRemaining,
		})
	}
	sort.Slice(out.Certs, func(i, j int) bool { return out.Certs[i].Name < out.Certs[j].Name })
	return out
}

func updateAgentModel(k8s *k8sState, agentName, model string) error {
	if k8s == nil || !k8s.enabled {
		return fmt.Errorf("kubernetes integration disabled")
	}
	deployPath := fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments/%s", url.PathEscape(k8s.podNS), url.PathEscape(agentName))
	status, deploy, _, err := k8s.getJSONWithStatus(deployPath)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return os.ErrNotExist
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("deployment lookup returned %d", status)
	}
	updatedViaConfigMap := false
	if cmStatus, cmBody, _, err := k8s.getJSONWithStatus(fmt.Sprintf("/api/v1/namespaces/%s/configmaps", url.PathEscape(k8s.podNS))); err == nil && cmStatus >= 200 && cmStatus < 300 {
		for _, item := range asSlice(cmBody["items"]) {
			im := asMap(item)
			meta := asMap(im["metadata"])
			name := asString(meta["name"])
			labels := asMap(meta["labels"])
			if name == "" {
				continue
			}
			if labels["valhalla.io/agent"] != agentName && !strings.Contains(name, agentName) {
				continue
			}
			data := asMap(im["data"])
			patchData := map[string]string{}
			switch {
			case asString(data["model"]) != "":
				patchData["model"] = model
			case asString(data["MODEL"]) != "":
				patchData["MODEL"] = model
			case asString(data["args"]) != "":
				patchData["args"] = regexp.MustCompile(`--model=[^\s"]+`).ReplaceAllString(asString(data["args"]), "--model="+model)
			}
			if len(patchData) == 0 {
				continue
			}
			cmPath := fmt.Sprintf("/api/v1/namespaces/%s/configmaps/%s", url.PathEscape(k8s.podNS), url.PathEscape(name))
			patch := map[string]interface{}{"data": patchData}
			if patchStatus, patchBody, err := k8s.patchJSON(cmPath, patch); err != nil {
				return err
			} else if patchStatus < 200 || patchStatus >= 300 {
				return fmt.Errorf("configmap patch returned %d: %s", patchStatus, strings.TrimSpace(string(patchBody)))
			}
			updatedViaConfigMap = true
			break
		}
	}
	if !updatedViaConfigMap {
		// Fall back to patching the deployment args directly when no agent config map is present.
	}
	spec := asMap(deploy["spec"])
	template := asMap(spec["template"])
	podSpec := asMap(template["spec"])
	containers := asSlice(podSpec["containers"])
	if len(containers) == 0 {
		return fmt.Errorf("deployment has no containers")
	}
	container := asMap(containers[0])
	args := asSlice(container["args"])
	if len(args) > 0 {
		arg0 := asString(args[0])
		if arg0 != "" && strings.Contains(arg0, "--model=") {
			arg0 = regexp.MustCompile(`--model=[^\s"]+`).ReplaceAllString(arg0, "--model="+model)
			patch := map[string]interface{}{
				"spec": map[string]interface{}{
					"template": map[string]interface{}{
						"spec": map[string]interface{}{
							"containers": []map[string]interface{}{{
								"name": container["name"],
								"args": []string{arg0},
							}},
						},
					},
				},
			}
			if patchStatus, patchBody, err := k8s.patchJSON(deployPath, patch); err != nil {
				return err
			} else if patchStatus < 200 || patchStatus >= 300 {
				return fmt.Errorf("deployment patch returned %d: %s", patchStatus, strings.TrimSpace(string(patchBody)))
			}
		}
	}
	podName := ""
	if podStatus, podBody, _, err := k8s.getJSONWithStatus(fmt.Sprintf("/api/v1/namespaces/%s/pods?labelSelector=%s", url.PathEscape(k8s.podNS), url.QueryEscape("valhalla.io/agent="+agentName))); err == nil && podStatus >= 200 && podStatus < 300 {
		items := asSlice(podBody["items"])
		if len(items) > 0 {
			podName = asString(asMap(asMap(items[0])["metadata"])["name"])
		}
	}
	if podName == "" {
		if podStatus, podBody, _, err := k8s.getJSONWithStatus(fmt.Sprintf("/api/v1/namespaces/%s/pods", url.PathEscape(k8s.podNS))); err == nil && podStatus >= 200 && podStatus < 300 {
			for _, item := range asSlice(podBody["items"]) {
				name := asString(asMap(asMap(item)["metadata"])["name"])
				if strings.HasPrefix(name, agentName+"-") || name == agentName {
					podName = name
					break
				}
			}
		}
	}
	if podName != "" {
		resp, err := k8s.do(http.MethodDelete, fmt.Sprintf("/api/v1/namespaces/%s/pods/%s", url.PathEscape(k8s.podNS), url.PathEscape(podName)), nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			return fmt.Errorf("pod delete returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
	}
	return nil
}

func shellQuoteSingle(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func normalizeAgentConfigValue(v interface{}) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return strings.TrimSpace(x), strings.TrimSpace(x) != ""
	case []interface{}:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ","), len(parts) > 0
	default:
		s := strings.TrimSpace(fmt.Sprint(x))
		return s, s != ""
	}
}

func updateAgentDeploymentArgs(content string, req agentConfigureRequest) (string, []string, error) {
	fields := []string{}
	updated := content
	apply := func(flagName, value, label string) error {
		pattern := regexp.MustCompile(regexp.QuoteMeta(flagName) + `(?:=|\s+)(?:"[^"]*"|'[^']*'|[^\s"']+)`)
		replacement := flagName + "=" + shellQuoteSingle(value)
		if !pattern.MatchString(updated) {
			return fmt.Errorf("flag %s not found in deployment args", flagName)
		}
		updated = pattern.ReplaceAllString(updated, replacement)
		fields = append(fields, label)
		return nil
	}
	if value := strings.TrimSpace(req.Model); value != "" {
		if err := apply("--model", value, "model"); err != nil {
			return "", nil, err
		}
	}
	if value := strings.TrimSpace(req.InferenceURL); value != "" {
		if err := apply("--inference-url", value, "inference_url"); err != nil {
			return "", nil, err
		}
	}
	if value, ok := normalizeAgentConfigValue(req.Tools); ok {
		if err := apply("--tools", value, "tools"); err != nil {
			return "", nil, err
		}
	}
	if value, ok := normalizeAgentConfigValue(req.Peers); ok {
		if err := apply("--peers", value, "peers"); err != nil {
			return "", nil, err
		}
	}
	if len(fields) == 0 {
		return "", nil, fmt.Errorf("no supported fields provided")
	}
	return updated, fields, nil
}

func createGitOpsAgentConfigPR(client *http.Client, baseURL, token, agentName string, req agentConfigureRequest) (string, error) {
	const repoFullName = "kit/valhalla-infra"
	manifestPath := fmt.Sprintf("infrastructure/valhalla/deployment-%s.yaml", agentName)

	var contentResp giteaContentResponse
	status, body, err := giteaGetJSONWithStatus(client, baseURL, token, fmt.Sprintf("/api/v1/repos/%s/contents/%s?ref=main", repoFullName, url.PathEscape(manifestPath)), &contentResp)
	if err != nil {
		return "", fmt.Errorf("fetch manifest: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("fetch manifest: %s", strings.TrimSpace(string(body)))
	}
	if contentResp.Encoding != "base64" {
		return "", fmt.Errorf("unsupported content encoding: %s", contentResp.Encoding)
	}
	rawContent, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(contentResp.Content, "\n", ""))
	if err != nil {
		return "", fmt.Errorf("decode manifest: %w", err)
	}
	updatedContent, fields, err := updateAgentDeploymentArgs(string(rawContent), req)
	if err != nil {
		return "", err
	}

	branch := fmt.Sprintf("settings/%s-%d", agentName, time.Now().Unix())
	branchBody, _ := json.Marshal(map[string]string{
		"new_branch_name": branch,
		"old_ref_name":    "main",
	})
	resp, err := giteaRequest(client, http.MethodPost, baseURL, token, fmt.Sprintf("/api/v1/repos/%s/branches", repoFullName), bytes.NewReader(branchBody))
	if err != nil {
		return "", fmt.Errorf("create branch: %w", err)
	}
	branchRespBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("create branch: %s", strings.TrimSpace(string(branchRespBody)))
	}

	updateBody, _ := json.Marshal(map[string]interface{}{
		"branch":  branch,
		"content": base64.StdEncoding.EncodeToString([]byte(updatedContent)),
		"message": fmt.Sprintf("settings: update %s config (%s)", agentName, strings.Join(fields, ", ")),
		"sha":     contentResp.SHA,
		"author": map[string]string{
			"name":  "Hirdforge Gateway",
			"email": "gateway@valhalla.local",
		},
		"committer": map[string]string{
			"name":  "Hirdforge Gateway",
			"email": "gateway@valhalla.local",
		},
	})
	resp, err = giteaRequest(client, http.MethodPut, baseURL, token, fmt.Sprintf("/api/v1/repos/%s/contents/%s", repoFullName, url.PathEscape(manifestPath)), bytes.NewReader(updateBody))
	if err != nil {
		return "", fmt.Errorf("commit manifest: %w", err)
	}
	updateRespBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("commit manifest: %s", strings.TrimSpace(string(updateRespBody)))
	}

	prBody, _ := json.Marshal(map[string]string{
		"base":  "main",
		"head":  branch,
		"title": fmt.Sprintf("settings: update %s config (%s)", agentName, strings.Join(fields, ", ")),
		"body":  fmt.Sprintf("Automated agent configuration update for `%s`.\n\nChanged fields: %s", agentName, strings.Join(fields, ", ")),
	})
	resp, err = giteaRequest(client, http.MethodPost, baseURL, token, fmt.Sprintf("/api/v1/repos/%s/pulls", repoFullName), bytes.NewReader(prBody))
	if err != nil {
		return "", fmt.Errorf("create PR: %w", err)
	}
	prRespBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("create PR: %s", strings.TrimSpace(string(prRespBody)))
	}
	var pr struct {
		HTMLURL string `json:"html_url"`
		URL     string `json:"url"`
	}
	if err := json.Unmarshal(prRespBody, &pr); err != nil {
		return "", fmt.Errorf("parse PR response: %w", err)
	}
	if strings.TrimSpace(pr.HTMLURL) != "" {
		return pr.HTMLURL, nil
	}
	if strings.TrimSpace(pr.URL) != "" {
		return pr.URL, nil
	}
	return "", fmt.Errorf("create PR: missing PR URL in response")
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func (k *k8sState) refreshPods() error {
	path := "/api/v1/namespaces/valhalla/pods"
	body, err := k.get(path)
	if err != nil {
		k.setPods(nil)
		return err
	}
	k.setPods(parsePods(body))
	return nil
}

func (k *k8sState) refreshNodes() error {
	body, err := k.get("/api/v1/nodes")
	if err != nil {
		k.setNodes(nil)
		return err
	}
	nodes := parseNodes(body)
	metricsBody, err := k.get("/apis/metrics.k8s.io/v1beta1/nodes")
	if err == nil {
		nodes = enrichNodesWithUsage(nodes, parseNodeUsage(metricsBody))
	}
	k.setNodes(nodes)
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func podNameFromPath(path, action string) (string, bool) {
	const prefix = "/api/v1/cluster/pods/"
	suffix := "/" + action
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	name = strings.Trim(name, "/")
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}

func namespacedPodPath(path string) (namespace, pod, action string, ok bool) {
	const prefix = "/api/v1/cluster/pods/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", "", false
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(path, prefix), "/"), "/")
	switch len(parts) {
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return "", "", "", false
		}
		return parts[0], parts[1], "", true
	case 3:
		if parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return "", "", "", false
		}
		return parts[0], parts[1], parts[2], true
	default:
		return "", "", "", false
	}
}

func clampInt(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

func extractPRNumber(text string) int64 {
	matches := regexp.MustCompile(`/pulls/(\d+)`).FindStringSubmatch(text)
	if len(matches) == 2 {
		n, _ := strconv.ParseInt(matches[1], 10, 64)
		return n
	}
	return 0
}

func collectLabelNames(raw []interface{}) []string {
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		name := asString(asMap(item)["name"])
		if name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func firstLabelWithPrefix(labels []string, prefix string) string {
	for _, label := range labels {
		if strings.HasPrefix(label, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(label, prefix))
		}
	}
	return ""
}

func shortSHA(in string) string {
	in = strings.TrimSpace(in)
	if len(in) > 7 {
		return in[:7]
	}
	return in
}

func summarizeApprovalParams(params map[string]interface{}) string {
	if len(params) == 0 {
		return "(no params)"
	}
	b, err := json.Marshal(params)
	if err != nil {
		return "(unserializable params)"
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 280 {
		return s[:280] + "..."
	}
	return s
}

func approvalAgentName(item approvalQueueItem) string {
	if item.Params == nil {
		return "unknown"
	}
	for _, key := range []string{"agent", "from", "requested_by"} {
		if raw, ok := item.Params[key]; ok {
			if s := strings.TrimSpace(fmt.Sprint(raw)); s != "" {
				return s
			}
		}
	}
	return "unknown"
}

func sendDiscordApprovalWebhook(webhookURL string, item approvalQueueItem) error {
	webhookURL = strings.TrimSpace(webhookURL)
	if webhookURL == "" {
		return nil
	}
	agent := approvalAgentName(item)
	action := strings.TrimSpace(item.Action)
	if action == "" {
		action = "unknown_action"
	}
	payload := map[string]interface{}{
		"embeds": []map[string]interface{}{{
			"title":       "Pending Lockbox Approval",
			"description": fmt.Sprintf("New write approval queued for `%s` by `%s`.", action, agent),
			"color":       16096779, // amber
			"fields": []map[string]interface{}{
				{"name": "Action", "value": action, "inline": true},
				{"name": "Agent", "value": agent, "inline": true},
				{"name": "Queue ID", "value": item.QueueID, "inline": false},
				{"name": "Details", "value": summarizeApprovalParams(item.Params), "inline": false},
			},
			"footer": map[string]string{
				"text": "Valhalla Gateway • Approval required",
			},
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	log.Printf("approval notifier: sending webhook for %s", item.QueueID)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("discord webhook returned %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	log.Printf("approval notifier: webhook sent for %s", item.QueueID)
	return nil
}

func main() {
	port := flag.String("port", "8080", "HTTP port")
	agentsFlag := flag.String("agents", "", "comma-separated name=url agent list")
	giteaURL := flag.String("gitea-url", "", "Gitea base URL")
	giteaToken := flag.String("gitea-token", "", "Gitea API token (optional)")
	giteaRepo := flag.String("gitea-repo", "gitea_admin/project_valhalla", "Gitea repo in owner/name format")
	lockboxURL := flag.String("lockbox-url", "", "Lockbox base URL for approval queue proxy")
	discordWebhookURL := flag.String("discord-webhook-url", "", "Discord webhook URL for new approval notifications")
	webhookSecret := flag.String("webhook-secret", "", "HMAC secret for Gitea webhook validation (optional)")
	reviewAgentFlag := flag.String("review-agent", "freya", "Agent to dispatch for PR reviews")
	taskRepoFlag := flag.String("task-repo", "kit/hirdforge-tasks", "Gitea repo for task board issues")
	seidrURLFlag := flag.String("seidr-url", "http://seidr.valhalla.svc:8082", "Seidr memory service URL")
	flag.Parse()
	if raw := strings.TrimSpace(os.Getenv("GATEWAY_STREAM_TIMEOUT_SECONDS")); raw != "" {
		if n, err := strconv.Atoi(raw); err != nil {
			log.Printf("invalid GATEWAY_STREAM_TIMEOUT_SECONDS=%q: %v", raw, err)
		} else if n > 0 {
			streamTimeout = time.Duration(n) * time.Second
		} else {
			log.Printf("ignoring non-positive GATEWAY_STREAM_TIMEOUT_SECONDS=%q", raw)
		}
	}
	log.Printf("gateway stream timeout configured to %s", streamTimeout)
	if strings.TrimSpace(*agentsFlag) == "" {
		die("missing --agents", fmt.Errorf("required"))
	}

	agents, order, err := parseAgents(*agentsFlag)
	if err != nil {
		die("failed to parse --agents", err)
	}
	gw := &gateway{
		agents:            agents,
		order:             order,
		events:            make([]Event, 0, 200),
		eventCap:          200,
		k8s:               initK8s(),
		sessionStore:      newSessionStore(),
		settings:          newSettingsStore(),
		notifications:     make([]Notification, 0, 100),
		notifCap:          100,
		lastSession:       map[string]string{},
		prReviewState:     map[string]prReviewState{},
		activeRequests:    map[string]*ActiveRequest{},
		injections:        map[string][]InjectionMessage{},
		pausedAgents:      map[string]bool{},
		webhookSecret:     strings.TrimSpace(*webhookSecret),
		reviewAgent:       strings.TrimSpace(*reviewAgentFlag),
		taskRepo:          strings.TrimSpace(*taskRepoFlag),
		giteaURL:          strings.TrimSpace(*giteaURL),
		giteaToken:        resolveGatewayGiteaToken(*giteaToken),
		seidrURL:          strings.TrimSpace(*seidrURLFlag),
		discordWebhookURL: strings.TrimSpace(*discordWebhookURL),
	}
	if err := gw.loadPipelineState(); err != nil {
		log.Printf("webhook: failed to load pipeline state: %v", err)
	}
	gw.addEvent("agent_start", "gateway", fmt.Sprintf("Gateway started with %d agents", len(order)))
	gw.refreshAgentHealth()

	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			gw.refreshAgentHealth()
		}
	}()

	if gw.k8s.enabled {
		gw.addEvent("k8s_event", "cluster", "Kubernetes integration enabled")
		if err := gw.k8s.refreshPods(); err != nil {
			gw.addEvent("k8s_event", "cluster", "pod refresh error: "+err.Error())
		}
		if err := gw.k8s.refreshNodes(); err != nil {
			gw.addEvent("k8s_event", "cluster", "node refresh error: "+err.Error())
		}
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for range t.C {
				if err := gw.k8s.refreshPods(); err != nil {
					gw.addEvent("k8s_event", "cluster", "pod refresh error: "+err.Error())
				}
			}
		}()
		go func() {
			t := time.NewTicker(60 * time.Second)
			defer t.Stop()
			for range t.C {
				if err := gw.k8s.refreshNodes(); err != nil {
					gw.addEvent("k8s_event", "cluster", "node refresh error: "+err.Error())
				}
			}
		}()
	} else {
		gw.addEvent("k8s_event", "cluster", "Kubernetes integration disabled")
	}

	proxyClient := &http.Client{Timeout: agentRequestTimeout}
	streamClient := &http.Client{Timeout: streamTimeout}
	giteaClient := &http.Client{Timeout: 10 * time.Second}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, dashboardHTML)
	})
	mux.HandleFunc("/ws/events", func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "upgrade required", http.StatusUpgradeRequired)
			return
		}
		key := r.Header.Get("Sec-WebSocket-Key")
		log.Printf("ws: key=%q accept=%q", key, wsAccept(key))
		if key == "" {
			http.Error(w, "missing websocket key", http.StatusBadRequest)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "websocket unsupported", http.StatusInternalServerError)
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		resp := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + wsAccept(key) + "\r\n\r\n"
		if _, err := rw.WriteString(resp); err != nil {
			_ = conn.Close()
			return
		}
		if err := rw.Flush(); err != nil {
			_ = conn.Close()
			return
		}
		log.Printf("ws: client connected from %s", conn.RemoteAddr())
		client := &wsClient{conn: conn, r: rw.Reader}
		gw.wsMu.Lock()
		gw.wsConns = append(gw.wsConns, client)
		gw.wsMu.Unlock()
		defer func() {
			log.Printf("ws: client disconnected: %s", client.conn.RemoteAddr())
			_ = client.conn.Close()
			gw.removeWSConn(client)
		}()
		const (
			pingInterval = 30 * time.Second
			pongWait     = 60 * time.Second
		)
		_ = client.conn.SetReadDeadline(time.Now().Add(pongWait))
		log.Printf("ws: entering read loop")
		stopPing := make(chan struct{})
		go func() {
			t := time.NewTicker(pingInterval)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					if err := client.writeFrame(0x9, nil); err != nil {
						log.Printf("ws: ping write error: %v", err)
						_ = client.conn.Close()
						return
					}
				case <-stopPing:
					return
				}
			}
		}()
		defer close(stopPing)
		for {
			opcode, payload, err := readWSFrame(client.r)
			if err != nil {
				log.Printf("ws: read error: %v", err)
				return
			}
			switch opcode {
			case 0x8: // close
				log.Printf("ws: client sent close frame")
				return
			case 0x9: // ping
				if err := client.writeFrame(0xA, payload); err != nil {
					return
				}
			case 0xA: // pong
				_ = client.conn.SetReadDeadline(time.Now().Add(pongWait))
			default:
				// Ignore non-control frames from client.
			}
		}
	})
	mux.HandleFunc("/api/v1/agents/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/agents/")
		switch {
		case strings.HasSuffix(path, "/inject"):
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/inject")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			if _, ok := gw.getAgent(name); !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
				return
			}
			var req struct {
				Content   string `json:"content"`
				SessionID string `json:"session_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Content) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content is required"})
				return
			}
			gw.injectionMu.Lock()
			gw.injections[name] = append(gw.injections[name], InjectionMessage{
				Agent:     name,
				Content:   req.Content,
				SessionID: req.SessionID,
				QueuedAt:  time.Now().Unix(),
			})
			gw.injectionMu.Unlock()
			gw.addEvent("injection_queued", name, fmt.Sprintf("Sovereign queued injection for %s", name))
			writeJSON(w, http.StatusOK, map[string]string{"status": "queued", "agent": name})
		case strings.HasSuffix(path, "/pause"):
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/pause")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			if _, ok := gw.getAgent(name); !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
				return
			}
			gw.injectionMu.Lock()
			gw.pausedAgents[name] = true
			gw.injectionMu.Unlock()
			gw.addEvent("agent_paused", name, fmt.Sprintf("%s paused by Sovereign", name))
			writeJSON(w, http.StatusOK, map[string]string{"status": "paused", "agent": name})
		case strings.HasSuffix(path, "/resume"):
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/resume")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			if _, ok := gw.getAgent(name); !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
				return
			}
			gw.injectionMu.Lock()
			delete(gw.pausedAgents, name)
			gw.injectionMu.Unlock()
			gw.addEvent("agent_resumed", name, fmt.Sprintf("%s resumed by Sovereign", name))
			writeJSON(w, http.StatusOK, map[string]string{"status": "resumed", "agent": name})
		case strings.HasSuffix(path, "/state"):
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/state")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			if _, ok := gw.getAgent(name); !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
				return
			}
			state := AgentState{Name: name}
			gw.injectionMu.Lock()
			state.Paused = gw.pausedAgents[name]
			gw.injectionMu.Unlock()
			gw.arMu.RLock()
			if ar, ok := gw.activeRequests[name]; ok {
				state.Active = true
				state.SessionID = ar.SessionID
				state.Source = detectSessionSource(ar.SessionID)
				state.Since = ar.StartedAt.Unix()
				if sess, ok := gw.sessionStore.get(ar.SessionID); ok {
					state.TaskRef = sess.TaskRef
				}
			}
			gw.arMu.RUnlock()
			writeJSON(w, http.StatusOK, state)
		case strings.HasSuffix(path, "/configure"):
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/configure")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			if _, ok := gw.getAgent(name); !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
				return
			}
			var in agentConfigureRequest
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
				return
			}
			if strings.TrimSpace(*giteaURL) == "" {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "missing Gitea URL"})
				return
			}
			token := resolveGatewayGiteaToken(*giteaToken)
			if token == "" {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "missing Gitea token"})
				return
			}
			prURL, err := createGitOpsAgentConfigPR(giteaClient, *giteaURL, token, name, in)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "pr_url": prURL})
		case strings.HasSuffix(path, "/files"):
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/files")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			agent, ok := gw.getAgent(name)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
				return
			}
			uReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, strings.TrimRight(agent.URL, "/")+"/api/v1/files", nil)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to create upstream request"})
				return
			}
			uResp, err := proxyClient.Do(uReq)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream request failed"})
				return
			}
			defer uResp.Body.Close()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(uResp.StatusCode)
			_, _ = io.Copy(w, io.LimitReader(uResp.Body, 4<<20))
		case strings.HasSuffix(path, "/stop"):
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimSuffix(path, "/stop")
			name = strings.Trim(name, "/")
			if name == "" || strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
			stopped := gw.stopAgent(name)
			writeJSON(w, http.StatusOK, map[string]interface{}{"status": "stopped", "agent": name, "active": stopped})
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, gw.snapshotAgents())
	})
	mux.HandleFunc("/api/v1/agents/stop-all", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		n := gw.stopAllAgents()
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "stopped", "count": n})
	})
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		agent := strings.TrimSpace(r.URL.Query().Get("agent"))
		source := strings.TrimSpace(r.URL.Query().Get("source"))
		writeJSON(w, http.StatusOK, gw.sessionStore.list(agent, source))
	})
	mux.HandleFunc("/api/v1/fleet/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		states := make([]AgentState, 0, len(gw.order))
		paused := make(map[string]bool, len(gw.order))
		gw.injectionMu.Lock()
		for _, name := range gw.order {
			paused[name] = gw.pausedAgents[name]
		}
		gw.injectionMu.Unlock()
		gw.arMu.RLock()
		for _, name := range gw.order {
			state := AgentState{Name: name, Paused: paused[name]}
			if ar, ok := gw.activeRequests[name]; ok {
				state.Active = true
				state.SessionID = ar.SessionID
				state.Source = detectSessionSource(ar.SessionID)
				state.Since = ar.StartedAt.Unix()
				if sess, ok := gw.sessionStore.get(ar.SessionID); ok {
					state.TaskRef = sess.TaskRef
				}
			}
			states = append(states, state)
		}
		gw.arMu.RUnlock()
		writeJSON(w, http.StatusOK, states)
	})
	mux.HandleFunc("/api/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
		if path == "" || strings.Contains(path, "//") {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(path, "/messages") {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			sessionID := strings.TrimSuffix(path, "/messages")
			sessionID = strings.Trim(sessionID, "/")
			if sessionID == "" || strings.Contains(sessionID, "/") {
				http.NotFound(w, r)
				return
			}
			var in ChatMessage
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
				return
			}
			if strings.TrimSpace(in.Role) == "" || strings.TrimSpace(in.Agent) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "role and agent are required"})
				return
			}
			id := gw.sessionStore.appendMessage(sessionID, in.Agent, in)
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "id": id})
			return
		}
		sessionID := strings.Trim(path, "/")
		if sessionID == "" || strings.Contains(sessionID, "/") {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			sess, ok := gw.sessionStore.get(sessionID)
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
				return
			}
			writeJSON(w, http.StatusOK, sess)
		case http.MethodDelete:
			if !gw.sessionStore.delete(sessionID) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": sessionID})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, gw.eventsNewest())
		case http.MethodPost:
			var in struct {
				Type    string `json:"type"`
				Agent   string `json:"agent"`
				Message string `json:"message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
				return
			}
			if in.Type == "" {
				in.Type = "task"
			}
			gw.addEvent(in.Type, in.Agent, in.Message)
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/notify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in Notification
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		in.From = strings.TrimSpace(in.From)
		in.TaskID = strings.TrimSpace(in.TaskID)
		in.Agent = strings.TrimSpace(in.Agent)
		in.State = strings.TrimSpace(in.State)
		in.Result = strings.TrimSpace(in.Result)
		if in.From == "" || in.TaskID == "" || in.Agent == "" || in.State == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from, task_id, agent, and state are required"})
			return
		}
		if strings.TrimSpace(in.Timestamp) == "" {
			in.Timestamp = time.Now().UTC().Format(time.RFC3339)
		}
		gw.addNotification(in)
		writeJSON(w, http.StatusOK, in)
	})
	mux.HandleFunc("/api/v1/notifications", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, gw.notificationsNewest())
	})
	proxyLockbox := func(w http.ResponseWriter, r *http.Request, method, path string, body []byte) {
		base := strings.TrimSpace(*lockboxURL)
		if base == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "lockbox not configured"})
			return
		}
		var bodyReader io.Reader
		if body != nil {
			bodyReader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(r.Context(), method, strings.TrimRight(base, "/")+path, bodyReader)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		if contentType := strings.TrimSpace(resp.Header.Get("Content-Type")); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 4<<20))
	}
	mux.HandleFunc("/api/v1/approvals", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		proxyLockbox(w, r, http.MethodGet, "/queues", nil)
	})
	mux.HandleFunc("/api/v1/approvals/approve", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			QueueID string `json:"queue_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		in.QueueID = strings.TrimSpace(in.QueueID)
		if in.QueueID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "queue_id is required"})
			return
		}
		payload, _ := json.Marshal(map[string]interface{}{
			"hunt_id":  "mcp",
			"queue_id": in.QueueID,
			"approved": true,
		})
		proxyLockbox(w, r, http.MethodPost, "/approve-write", payload)
	})
	mux.HandleFunc("/api/v1/approvals/reject", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			QueueID string `json:"queue_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		in.QueueID = strings.TrimSpace(in.QueueID)
		if in.QueueID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "queue_id is required"})
			return
		}
		payload, _ := json.Marshal(map[string]interface{}{
			"hunt_id":  "mcp",
			"queue_id": in.QueueID,
			"approved": false,
		})
		proxyLockbox(w, r, http.MethodPost, "/approve-write", payload)
	})
	mux.HandleFunc("/api/v1/settings/agents/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/settings/agents/"), "/")
		parts := strings.Split(path, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] != "model" {
			http.NotFound(w, r)
			return
		}
		var in struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Model) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model is required"})
			return
		}
		if err := updateAgentModel(gw.k8s, parts[0], strings.TrimSpace(in.Model)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"agent":  parts[0],
			"model":  strings.TrimSpace(in.Model),
			"status": "updated",
		})
	})
	mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, gw.settings.All())
		case http.MethodPut:
			var in map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
				return
			}
			gw.settings.SetBulk(in)
			writeJSON(w, http.StatusOK, gw.settings.All())
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/settings/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		key := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/settings/"), "/")
		if key == "" || strings.Contains(key, "/") {
			http.NotFound(w, r)
			return
		}
		var raw interface{}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		if wrapped, ok := raw.(map[string]interface{}); ok {
			if value, exists := wrapped["value"]; exists && len(wrapped) == 1 {
				raw = value
			}
		}
		gw.settings.Set(key, raw)
		writeJSON(w, http.StatusOK, gw.settings.All())
	})
	mux.HandleFunc("/api/v1/cluster/pods", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if gw.k8s == nil || !gw.k8s.enabled {
			writeJSON(w, http.StatusOK, []PodInfo{})
			return
		}
		writeJSON(w, http.StatusOK, gw.k8s.snapshotPods())
	})
	mux.HandleFunc("/api/v1/cluster/pods/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			namespace, podName, action, ok := namespacedPodPath(r.URL.Path)
			if ok && action == "logs" {
				if gw.k8s == nil || !gw.k8s.enabled {
					writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes integration disabled"})
					return
				}
				tail := 50
				if raw := strings.TrimSpace(r.URL.Query().Get("tail")); raw != "" {
					v, err := strconv.Atoi(raw)
					if err != nil {
						writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tail parameter"})
						return
					}
					tail = clampInt(v, 50, 500)
				}
				status, body, err := gw.k8s.getBytes(fmt.Sprintf(
					"/api/v1/namespaces/%s/pods/%s/log?tailLines=%d&timestamps=true",
					url.PathEscape(namespace),
					url.PathEscape(podName),
					tail,
				))
				if err != nil {
					writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes request failed"})
					return
				}
				if status == http.StatusNotFound {
					writeJSON(w, http.StatusNotFound, map[string]string{"error": "pod not found"})
					return
				}
				if status < 200 || status >= 300 {
					msg := strings.TrimSpace(string(body))
					if strings.Contains(msg, "PodInitializing") || strings.Contains(msg, "ContainerCreating") {
						writeJSON(w, http.StatusOK, podLogsResponse{Pod: podName, Logs: ""})
						return
					}
					writeJSON(w, http.StatusBadGateway, map[string]string{"error": msg})
					return
				}
				writeJSON(w, http.StatusOK, podLogsResponse{Pod: podName, Logs: string(body)})
				return
			}
		case strings.HasSuffix(r.URL.Path, "/logs") && strings.HasPrefix(r.URL.Path, "/api/v1/cluster/pods/"):
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if gw.k8s == nil || !gw.k8s.enabled {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes integration disabled"})
				return
			}
			podName, ok := podNameFromPath(r.URL.Path, "logs")
			if !ok {
				http.NotFound(w, r)
				return
			}
			lines := 100
			if raw := strings.TrimSpace(r.URL.Query().Get("lines")); raw != "" {
				v, err := strconv.Atoi(raw)
				if err != nil || v <= 0 {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid lines parameter"})
					return
				}
				lines = v
			}
			namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
			if namespace == "" {
				namespace = "valhalla"
			}
			path := fmt.Sprintf(
				"/api/v1/namespaces/%s/pods/%s/log?tailLines=%d&timestamps=true",
				url.PathEscape(namespace),
				url.PathEscape(podName),
				lines,
			)
			resp, err := gw.k8s.do(http.MethodGet, path, nil)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes request failed"})
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "pod not found"})
				return
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(b))})
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			_, _ = io.Copy(w, resp.Body)
			return

		case r.Method == http.MethodDelete:
			namespace, podName, action, ok := namespacedPodPath(r.URL.Path)
			if !ok || action != "" {
				http.NotFound(w, r)
				return
			}
			if gw.k8s == nil || !gw.k8s.enabled {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes integration disabled"})
				return
			}
			resp, err := gw.k8s.do(http.MethodDelete, fmt.Sprintf("/api/v1/namespaces/%s/pods/%s", url.PathEscape(namespace), url.PathEscape(podName)), nil)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes request failed"})
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "pod not found"})
				return
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(b))})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"pod": podName, "status": "deleted"})
			return

		case strings.HasSuffix(r.URL.Path, "/restart") && strings.HasPrefix(r.URL.Path, "/api/v1/cluster/pods/"):
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if gw.k8s == nil || !gw.k8s.enabled {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes integration disabled"})
				return
			}
			podName, ok := podNameFromPath(r.URL.Path, "restart")
			if !ok {
				http.NotFound(w, r)
				return
			}
			path := fmt.Sprintf("/api/v1/namespaces/valhalla/pods/%s", url.PathEscape(podName))
			resp, err := gw.k8s.do(http.MethodDelete, path, nil)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes request failed"})
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "pod not found"})
				return
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(b))})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{
				"status":  "ok",
				"message": fmt.Sprintf("Pod %s deleted, deployment will recreate", podName),
			})
			return
		default:
			http.NotFound(w, r)
			return
		}
	})
	mux.HandleFunc("/api/v1/cluster/nodes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if gw.k8s == nil || !gw.k8s.enabled {
			writeJSON(w, http.StatusOK, []NodeInfo{})
			return
		}
		writeJSON(w, http.StatusOK, gw.k8s.snapshotNodes())
	})
	mux.HandleFunc("/api/v1/cluster/certs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, listCertExpiries(gw.k8s, "valhalla"))
	})
	mux.HandleFunc("/api/v1/gitops/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, fetchGitOpsStatus(gw.k8s))
	})
	mux.HandleFunc("/api/v1/gitops/sync", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if gw.k8s == nil || !gw.k8s.enabled {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes integration disabled"})
			return
		}
		status, body, err := gw.k8s.patchJSON("/apis/argoproj.io/v1alpha1/namespaces/argocd/applications/asgard", map[string]interface{}{
			"metadata": map[string]interface{}{
				"annotations": map[string]string{
					"argocd.argoproj.io/refresh": "hard",
				},
			},
		})
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		if status == http.StatusNotFound {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "ArgoCD application CRD not found"})
			return
		}
		if status < 200 || status >= 300 {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(body))})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "sync triggered"})
	})
	mux.HandleFunc("/api/v1/k8s/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if gw.k8s == nil || !gw.k8s.enabled {
			writeJSON(w, http.StatusOK, []K8sEventInfo{})
			return
		}
		body, err := gw.k8s.get("/api/v1/namespaces/valhalla/events")
		if err != nil {
			writeJSON(w, http.StatusOK, []K8sEventInfo{})
			return
		}
		writeJSON(w, http.StatusOK, parseK8sEvents(body))
	})
	mux.HandleFunc("/api/v1/k8s/resources", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if gw.k8s == nil || !gw.k8s.enabled {
			writeJSON(w, http.StatusOK, []PodResourceInfo{})
			return
		}
		body, err := gw.k8s.get("/api/v1/namespaces/valhalla/pods")
		if err != nil {
			writeJSON(w, http.StatusOK, []PodResourceInfo{})
			return
		}
		writeJSON(w, http.StatusOK, parsePodResources(body))
	})
	mux.HandleFunc("/api/v1/gitea/commits", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(*giteaURL) == "" {
			writeJSON(w, http.StatusOK, []giteaCommit{})
			return
		}
		var resp []map[string]interface{}
		err := fetchGiteaJSON(giteaClient, *giteaURL, *giteaToken, "/api/v1/repos/"+*giteaRepo+"/commits?limit=10", &resp)
		if err != nil {
			writeJSON(w, http.StatusOK, []giteaCommit{})
			return
		}
		out := make([]giteaCommit, 0, len(resp))
		for _, c := range resp {
			commit := asMap(c["commit"])
			author := asMap(commit["author"])
			sha := asString(c["sha"])
			msg := asString(commit["message"])
			if i := strings.Index(msg, "\n"); i >= 0 {
				msg = msg[:i]
			}
			out = append(out, giteaCommit{
				SHA:     sha,
				Message: msg,
				Author:  asString(author["name"]),
				Date:    asString(author["date"]),
			})
		}
		if len(out) > 10 {
			out = out[:10]
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("/api/v1/history/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit := clampInt(asInt(r.URL.Query().Get("limit")), 50, 100)
		agent := strings.TrimSpace(r.URL.Query().Get("agent"))
		outcome := strings.TrimSpace(r.URL.Query().Get("outcome"))
		writeJSON(w, http.StatusOK, fetchTaskHistory(giteaClient, *giteaURL, *giteaToken, "kit/hirdforge-tasks", agent, outcome, limit))
	})
	mux.HandleFunc("/api/v1/history/deployments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit := clampInt(asInt(r.URL.Query().Get("limit")), 20, 100)
		writeJSON(w, http.StatusOK, fetchDeploymentHistory(giteaClient, *giteaURL, *giteaToken, limit))
	})
	mux.HandleFunc("/api/v1/whoami", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		user := strings.TrimSpace(r.Header.Get("X-Forwarded-User"))
		email := strings.TrimSpace(r.Header.Get("X-Forwarded-Email"))
		if user == "" {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"user":          "Sovereign",
				"role":          "sovereign",
				"authenticated": false,
			})
			return
		}
		resp := map[string]interface{}{
			"user":          user,
			"role":          "sovereign",
			"authenticated": true,
		}
		if email != "" {
			resp["email"] = email
		}
		writeJSON(w, http.StatusOK, resp)
	})
	mux.HandleFunc("/api/v1/gitea/prs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(*giteaURL) == "" {
			writeJSON(w, http.StatusOK, []giteaPRInfo{})
			return
		}
		repos := []string{"gitea_admin/project_valhalla", "kit/valhalla-infra", "kit/hirdforge-personas", "kit/hirdforge-tasks"}
		type query struct {
			repo  string
			state string
			limit int
		}
		queries := []query{
			{repo: repos[0], state: "open", limit: 20},
			{repo: repos[1], state: "open", limit: 20},
			{repo: repos[2], state: "open", limit: 20},
			{repo: repos[3], state: "open", limit: 20},
			{repo: repos[0], state: "closed", limit: 10},
			{repo: repos[1], state: "closed", limit: 10},
			{repo: repos[2], state: "closed", limit: 10},
			{repo: repos[3], state: "closed", limit: 10},
		}
		out := make([]giteaPRInfo, 0, 60)
		for _, q := range queries {
			path := fmt.Sprintf("/api/v1/repos/%s/pulls?state=%s&sort=newest&limit=%d", q.repo, q.state, q.limit)
			var prs []map[string]interface{}
			status, body, err := giteaGetJSONWithStatus(giteaClient, *giteaURL, *giteaToken, path, &prs)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Gitea unreachable"})
				return
			}
			if status < 200 || status >= 300 {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(body))})
				return
			}
			for _, pr := range prs {
				number := asInt64(pr["number"])
				labelsRaw := asSlice(pr["labels"])
				labels := make([]string, 0, len(labelsRaw))
				for _, l := range labelsRaw {
					name := asString(asMap(l)["name"])
					if name != "" {
						labels = append(labels, name)
					}
				}
				approvals := int64(0)
				if q.state == "open" && number > 0 {
					var reviews []map[string]interface{}
					reviewPath := fmt.Sprintf("/api/v1/repos/%s/pulls/%d/reviews", q.repo, number)
					if reviewStatus, _, err := giteaGetJSONWithStatus(giteaClient, *giteaURL, *giteaToken, reviewPath, &reviews); err == nil && reviewStatus >= 200 && reviewStatus < 300 {
						latestByReviewer := map[string]string{}
						for _, review := range reviews {
							user := asString(asMap(review["user"])["login"])
							state := strings.ToUpper(asString(review["state"]))
							if user == "" || state == "" {
								continue
							}
							latestByReviewer[user] = state
						}
						for _, state := range latestByReviewer {
							if state == "APPROVED" {
								approvals++
							}
						}
					}
				}
				out = append(out, giteaPRInfo{
					Number:    number,
					Title:     asString(pr["title"]),
					State:     asString(pr["state"]),
					User:      asString(asMap(pr["user"])["login"]),
					Repo:      q.repo,
					Base:      asString(asMap(pr["base"])["ref"]),
					Head:      asString(asMap(pr["head"])["ref"]),
					Body:      truncateRunes(asString(pr["body"]), 200),
					CreatedAt: asString(pr["created_at"]),
					UpdatedAt: asString(pr["updated_at"]),
					HTMLURL:   asString(pr["html_url"]),
					Labels:    labels,
					Approvals: approvals,
					Mergeable: asBool(pr["mergeable"]),
				})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("/api/v1/gitea/prs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(gw.giteaURL) == "" {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Gitea unreachable"})
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/gitea/prs/")
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) != 4 {
			http.NotFound(w, r)
			return
		}
		owner, repo, idxRaw, action := parts[0], parts[1], parts[2], parts[3]
		if owner == "" || repo == "" || idxRaw == "" {
			http.NotFound(w, r)
			return
		}
		if _, err := strconv.Atoi(idxRaw); err != nil {
			http.NotFound(w, r)
			return
		}
		var (
			resp *http.Response
			err  error
		)
		switch action {
		case "merge":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			body, _ := json.Marshal(map[string]string{
				"Do":                  "merge",
				"merge_message_field": "Merged via Hirdforge UI",
			})
			uPath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%s/merge", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(idxRaw))
			resp, err = giteaRequest(giteaClient, http.MethodPost, gw.giteaURL, gw.giteaToken, uPath, bytes.NewReader(body))
		case "close":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			body, _ := json.Marshal(map[string]string{"state": "closed"})
			uPath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%s", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(idxRaw))
			resp, err = giteaRequest(giteaClient, http.MethodPatch, gw.giteaURL, gw.giteaToken, uPath, bytes.NewReader(body))
		case "approve":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			body, _ := json.Marshal(map[string]string{"event": "APPROVED", "body": "Approved via Hirdforge UI"})
			uPath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%s/reviews", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(idxRaw))
			resp, err = giteaRequest(giteaClient, http.MethodPost, gw.giteaURL, gw.giteaToken, uPath, bytes.NewReader(body))
		case "status":
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			prPath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%s", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(idxRaw))
			prResp, prErr := giteaRequest(giteaClient, http.MethodGet, gw.giteaURL, gw.giteaToken, prPath, nil)
			if prErr != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Gitea unreachable"})
				return
			}
			prBody, _ := io.ReadAll(io.LimitReader(prResp.Body, 2<<20))
			prResp.Body.Close()
			if prResp.StatusCode < 200 || prResp.StatusCode >= 300 {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(prBody))})
				return
			}
			var pr map[string]interface{}
			if err := json.Unmarshal(prBody, &pr); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "invalid Gitea PR response"})
				return
			}
			headSHA := asString(asMap(pr["head"])["sha"])
			if strings.TrimSpace(headSHA) == "" {
				writeJSON(w, http.StatusOK, map[string]string{"status": "unknown"})
				return
			}
			statusPath := fmt.Sprintf("/api/v1/repos/%s/%s/commits/%s/status", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(headSHA))
			statusResp, statusErr := giteaRequest(giteaClient, http.MethodGet, gw.giteaURL, gw.giteaToken, statusPath, nil)
			if statusErr != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Gitea unreachable"})
				return
			}
			statusBody, _ := io.ReadAll(io.LimitReader(statusResp.Body, 2<<20))
			statusResp.Body.Close()
			if statusResp.StatusCode < 200 || statusResp.StatusCode >= 300 {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(statusBody))})
				return
			}
			var statusPayload map[string]interface{}
			if err := json.Unmarshal(statusBody, &statusPayload); err != nil {
				writeJSON(w, http.StatusOK, map[string]string{"status": "unknown"})
				return
			}
			overall := strings.TrimSpace(asString(statusPayload["state"]))
			if overall == "" {
				overall = "unknown"
			}
			result := map[string]string{"status": overall}
			statuses := asSlice(statusPayload["statuses"])
			if len(statuses) > 0 {
				first := asMap(statuses[0])
				if workflow := strings.TrimSpace(asString(first["context"])); workflow != "" {
					result["workflow"] = workflow
				}
				started := strings.TrimSpace(asString(first["created_at"]))
				finished := strings.TrimSpace(asString(first["updated_at"]))
				if started != "" && finished != "" {
					if startTime, err := time.Parse(time.RFC3339, started); err == nil {
						if endTime, err := time.Parse(time.RFC3339, finished); err == nil && !endTime.Before(startTime) {
							result["duration"] = endTime.Sub(startTime).String()
						}
					}
				}
			}
			writeJSON(w, http.StatusOK, result)
			return
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Gitea unreachable"})
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		for k, vv := range resp.Header {
			if strings.EqualFold(k, "Content-Type") && len(vv) > 0 {
				w.Header().Set("Content-Type", vv[0])
				break
			}
		}
		w.WriteHeader(resp.StatusCode)
		if len(respBody) > 0 {
			_, _ = w.Write(respBody)
		}
	})
	mux.HandleFunc("/api/v1/seidr/memories", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(gw.seidrURL) == "" {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Seidr unavailable"})
			return
		}
		agent := strings.TrimSpace(r.URL.Query().Get("agent"))
		query := strings.TrimSpace(r.URL.Query().Get("query"))
		limit := clampInt(asInt(r.URL.Query().Get("limit")), 20, 100)
		payload, _ := json.Marshal(map[string]interface{}{
			"query_text": query,
			"agent_name": agent,
			"n_results":  limit,
		})
		client := &http.Client{Timeout: 10 * time.Second}
		req, err := http.NewRequest(http.MethodPost, strings.TrimRight(gw.seidrURL, "/")+"/query", bytes.NewReader(payload))
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		for k, vv := range resp.Header {
			if strings.EqualFold(k, "Content-Type") && len(vv) > 0 {
				w.Header().Set("Content-Type", vv[0])
				break
			}
		}
		w.WriteHeader(resp.StatusCode)
		if len(respBody) > 0 {
			_, _ = w.Write(respBody)
		}
	})
	mux.HandleFunc("/api/v1/seidr/memories/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(gw.seidrURL) == "" {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Seidr unavailable"})
			return
		}
		id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/seidr/memories/"), "/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		agent := strings.TrimSpace(r.URL.Query().Get("agent"))
		client := &http.Client{Timeout: 10 * time.Second}
		u := strings.TrimRight(gw.seidrURL, "/") + "/memories/" + url.PathEscape(id)
		if agent != "" {
			u += "?agent_name=" + url.QueryEscape(agent)
		}
		req, err := http.NewRequest(http.MethodDelete, u, nil)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		for k, vv := range resp.Header {
			if strings.EqualFold(k, "Content-Type") && len(vv) > 0 {
				w.Header().Set("Content-Type", vv[0])
				break
			}
		}
		w.WriteHeader(resp.StatusCode)
		if len(respBody) > 0 {
			_, _ = w.Write(respBody)
		}
	})
	mux.HandleFunc("/api/v1/gitea/repos", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(*giteaURL) == "" {
			writeJSON(w, http.StatusOK, []giteaRepoListItem{})
			return
		}
		all := make([]map[string]interface{}, 0, 40)
		{
			var repos []map[string]interface{}
			status, body, err := giteaGetJSONWithStatus(giteaClient, *giteaURL, *giteaToken, "/api/v1/user/repos?limit=20", &repos)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Gitea unreachable"})
				return
			}
			if status < 200 || status >= 300 {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(body))})
				return
			}
			all = append(all, repos...)
		}
		{
			var repos []map[string]interface{}
			status, _, err := giteaGetJSONWithStatus(giteaClient, *giteaURL, *giteaToken, "/api/v1/orgs/gitea_admin/repos?limit=20", &repos)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Gitea unreachable"})
				return
			}
			if status >= 200 && status < 300 {
				all = append(all, repos...)
			}
		}
		seen := map[string]giteaRepoListItem{}
		for _, repo := range all {
			full := asString(repo["full_name"])
			if full == "" {
				continue
			}
			seen[full] = giteaRepoListItem{
				Name:        asString(repo["name"]),
				Owner:       asString(asMap(repo["owner"])["login"]),
				FullName:    full,
				Description: asString(repo["description"]),
				Language:    asString(repo["language"]),
				OpenPRs:     asInt64(repo["open_pr_counter"]),
				Stars:       asInt64(repo["stars_count"]),
				HTMLURL:     asString(repo["html_url"]),
			}
		}
		out := make([]giteaRepoListItem, 0, len(seen))
		for _, r := range seen {
			out = append(out, r)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].FullName < out[j].FullName })
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("/api/v1/gitea/repo", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(*giteaURL) == "" {
			writeJSON(w, http.StatusOK, map[string]interface{}{})
			return
		}
		var repo map[string]interface{}
		err := fetchGiteaJSON(giteaClient, *giteaURL, *giteaToken, "/api/v1/repos/"+*giteaRepo, &repo)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{})
			return
		}
		out := giteaRepoInfo{
			Name:          asString(repo["name"]),
			Stars:         asInt64(repo["stars_count"]),
			Forks:         asInt64(repo["forks_count"]),
			OpenIssues:    asInt64(repo["open_issues_count"]),
			Size:          asInt64(repo["size"]),
			DefaultBranch: asString(repo["default_branch"]),
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("/api/v1/dispatch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in []dispatchReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		out := make([]dispatchResp, len(in))
		var wg sync.WaitGroup
		for i := range in {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				reqItem := in[idx]
				out[idx].Agent = reqItem.Agent
				agent, ok := gw.getAgent(reqItem.Agent)
				if !ok {
					out[idx].Error = "unknown agent"
					return
				}
				body, _ := json.Marshal(taskSendRequest{Content: reqItem.Content, From: reqItem.From})
				req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(agent.URL, "/")+"/tasks/send", bytes.NewReader(body))
				if err != nil {
					out[idx].Error = "failed to create upstream request"
					return
				}
				req.Header.Set("Content-Type", "application/json")
				resp, err := proxyClient.Do(req)
				if err != nil {
					out[idx].Error = "upstream request failed"
					return
				}
				defer resp.Body.Close()
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
					out[idx].Error = strings.TrimSpace(string(b))
					return
				}
				var tr struct {
					ID string `json:"id"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
					out[idx].Error = "invalid upstream response"
					return
				}
				out[idx].TaskID = tr.ID
			}(i)
		}
		wg.Wait()
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("/api/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		from := strings.TrimSpace(r.URL.Query().Get("from"))
		agentFilter := strings.TrimSpace(r.URL.Query().Get("agent"))
		agentsToQuery := gw.snapshotAgents()
		tasksOut := make([]taskspkg.Task, 0)
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, a := range agentsToQuery {
			if agentFilter != "" && a.Name != agentFilter {
				continue
			}
			wg.Add(1)
			go func(agent Agent) {
				defer wg.Done()
				u := strings.TrimRight(agent.URL, "/") + "/tasks"
				params := url.Values{}
				if status != "" {
					params.Set("status", status)
				}
				if agentFilter != "" {
					params.Set("agent", agentFilter)
				}
				if q := params.Encode(); q != "" {
					u += "?" + q
				}
				req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
				if err != nil {
					return
				}
				resp, err := proxyClient.Do(req)
				if err != nil {
					return
				}
				defer resp.Body.Close()
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					return
				}
				var tasks []taskspkg.Task
				if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
					return
				}
				if from != "" {
					filtered := tasks[:0]
					for _, t := range tasks {
						if t.From == from {
							filtered = append(filtered, t)
						}
					}
					tasks = filtered
				}
				mu.Lock()
				tasksOut = append(tasksOut, tasks...)
				mu.Unlock()
			}(a)
		}
		wg.Wait()
		sort.Slice(tasksOut, func(i, j int) bool { return tasksOut[i].CreatedAt.After(tasksOut[j].CreatedAt) })
		writeJSON(w, http.StatusOK, tasksOut)
	})
	mux.HandleFunc("/api/v1/tasks/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		taskID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/"), "/")
		if taskID == "" || strings.Contains(taskID, "/") {
			http.NotFound(w, r)
			return
		}
		for _, agent := range gw.snapshotAgents() {
			req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, strings.TrimRight(agent.URL, "/")+"/tasks/"+url.PathEscape(taskID), nil)
			if err != nil {
				continue
			}
			resp, err := proxyClient.Do(req)
			if err != nil {
				continue
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				continue
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(body)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
	})
	mux.HandleFunc("/api/v1/message", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Async mode: accept message and process in background goroutine
		if r.URL.Query().Get("async") == "true" {
			var in messageReq
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
				return
			}
			agent, ok := gw.getAgent(in.Agent)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
				return
			}
			sessionID := strings.TrimSpace(in.SessionID)
			if sessionID == "" {
				sessionID = fmt.Sprintf("hirdforge-%s-%d", in.Agent, time.Now().UnixNano())
			}
			gw.lastSessionMu.Lock()
			gw.lastSession[in.Agent] = sessionID
			gw.lastSessionMu.Unlock()
			gw.addEvent("message", in.Agent, fmt.Sprintf("Message sent to %s", in.Agent))

			// Launch goroutine to send message asynchronously
			go func(agentName string, content string, sessionID string) {
				agentCtx, agentCancel := context.WithTimeout(context.Background(), streamTimeout)
				defer agentCancel()
				gw.setActiveRequest(agentName, sessionID, agentCancel)
				defer gw.clearActiveRequest(agentName, agentCancel)

				body, _ := json.Marshal(map[string]string{"content": content, "session_id": sessionID})
				uReq, err := http.NewRequestWithContext(agentCtx, http.MethodPost, strings.TrimRight(agent.URL, "/")+"/message", bytes.NewReader(body))
				if err != nil {
					log.Printf("async message: failed to create request for %s: %v", agentName, err)
					return
				}
				uReq.Header.Set("Content-Type", "application/json")
				uResp, err := streamClient.Do(uReq)
				if err != nil {
					log.Printf("async message: upstream request failed for %s: %v", agentName, err)
					return
				}
				defer uResp.Body.Close()
				// Drain and discard the response body
				io.Copy(io.Discard, uResp.Body)
			}(in.Agent, in.Content, sessionID)

			writeJSON(w, http.StatusOK, map[string]string{"status": "queued", "session_id": sessionID, "agent": in.Agent})
			return
		}

		var in messageReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		agent, ok := gw.getAgent(in.Agent)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
			return
		}
		sessionID := strings.TrimSpace(in.SessionID)
		gw.injectionMu.Lock()
		paused := gw.pausedAgents[in.Agent]
		gw.injectionMu.Unlock()
		if detectSessionSource(sessionID) == "cronjob" && paused {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"content\":\"Agent %s is paused by Sovereign. Skipping task.\"}\n\n", in.Agent)
			fmt.Fprintf(w, "data: {\"done\":true}\n\n")
			gw.addEvent("agent_paused_skip", in.Agent, fmt.Sprintf("CronJob poll skipped — %s is paused", in.Agent))
			return
		}
		if !agent.Healthy {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent is unhealthy"})
			return
		}
		if sessionID == "" {
			sessionID = fmt.Sprintf("hirdforge-%s-%d", in.Agent, time.Now().UnixNano())
		}
		gw.lastSessionMu.Lock()
		gw.lastSession[in.Agent] = sessionID
		gw.lastSessionMu.Unlock()
		gw.addEvent("message", in.Agent, fmt.Sprintf("Message sent to %s", in.Agent))

		agentCtx, agentCancel := context.WithTimeout(context.Background(), streamTimeout)
		defer agentCancel()
		gw.setActiveRequest(in.Agent, sessionID, agentCancel)
		defer gw.clearActiveRequest(in.Agent, agentCancel)

		body, _ := json.Marshal(map[string]string{"content": in.Content, "session_id": sessionID})
		uReq, err := http.NewRequestWithContext(agentCtx, http.MethodPost, strings.TrimRight(agent.URL, "/")+"/message", bytes.NewReader(body))
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to create upstream request"})
			return
		}
		uReq.Header.Set("Content-Type", "application/json")
		uResp, err := streamClient.Do(uReq)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream request failed"})
			return
		}
		if uResp.StatusCode == 429 {
			b, _ := io.ReadAll(io.LimitReader(uResp.Body, 4096))
			uResp.Body.Close()
			log.Printf("rate limit: upstream agent returned 429 for %s", in.Agent)
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate_limited", "detail": strings.TrimSpace(string(b))})
			return
		}
		if uResp.StatusCode < 200 || uResp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(uResp.Body, 4096))
			uResp.Body.Close()
			writeJSON(w, uResp.StatusCode, map[string]string{"error": strings.TrimSpace(string(b))})
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			uResp.Body.Close()
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		// SSE keepalive - prevents browser/proxy from dropping idle connections
		keepaliveDone := make(chan struct{})
		defer close(keepaliveDone)
		go func() {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					fmt.Fprintf(w, ": keepalive\n\n")
					flusher.Flush()
				case <-keepaliveDone:
					return
				case <-agentCtx.Done():
					return
				}
			}
		}()

		reader := bufio.NewReader(uResp.Body)
		var contentBuf strings.Builder
		var doneEvt map[string]interface{}
		var saveOnce sync.Once
		processInjectionQueue := func(baseSessionID string) {
			gw.injectionMu.Lock()
			pending := gw.injections[in.Agent]
			if len(pending) == 0 {
				gw.injectionMu.Unlock()
				return
			}
			next := pending[0]
			gw.injections[in.Agent] = pending[1:]
			gw.injectionMu.Unlock()
			injSessionID := strings.TrimSpace(next.SessionID)
			if injSessionID == "" {
				injSessionID = baseSessionID
			}
			go func() {
				injBody, _ := json.Marshal(map[string]string{
					"content":    next.Content,
					"session_id": injSessionID,
				})
				injReq, err := http.NewRequest(http.MethodPost, strings.TrimRight(agent.URL, "/")+"/message", bytes.NewReader(injBody))
				if err != nil {
					log.Printf("injection failed for %s: %v", in.Agent, err)
					return
				}
				injReq.Header.Set("Content-Type", "application/json")
				gw.addEvent("injection_sent", in.Agent, fmt.Sprintf("Sovereign injection delivered to %s", in.Agent))
				resp, err := http.DefaultClient.Do(injReq)
				if err != nil {
					log.Printf("injection request failed for %s: %v", in.Agent, err)
					return
				}
				defer resp.Body.Close()

				var fullResp strings.Builder
				scanner := bufio.NewScanner(resp.Body)
				for scanner.Scan() {
					line := scanner.Text()
					if strings.HasPrefix(line, "data: ") {
						chunk := strings.TrimPrefix(line, "data: ")
						var obj map[string]interface{}
						if json.Unmarshal([]byte(chunk), &obj) == nil {
							if c, ok := obj["content"].(string); ok {
								fullResp.WriteString(c)
							}
						}
					}
				}
				cleaned := thinkTagRE.ReplaceAllString(fullResp.String(), "")
				gw.sessionStore.appendConversation(injSessionID, in.Agent, next.Content, cleaned)
				gw.addEvent("injection_complete", in.Agent, fmt.Sprintf("Sovereign injection response from %s", in.Agent))
			}()
		}
		saveConversation := func() {
			saveOnce.Do(func() {
				raw := contentBuf.String()
				cleaned := thinkTagRE.ReplaceAllString(raw, "")
				gw.sessionStore.appendConversation(sessionID, in.Agent, in.Content, cleaned)
			})
		}
		drainAgentResponse := func() {
			go func() {
				defer uResp.Body.Close()
				defer agentCancel()
				for {
					line, err := reader.ReadBytes('\n')
					if err != nil {
						break
					}
					_ = line
				}
				saveConversation()
			}()
		}
		forward := func(evt map[string]interface{}) bool {
			b, err := json.Marshal(evt)
			if err != nil {
				return true
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}
		forwardReplaceIfNeeded := func() bool {
			raw := contentBuf.String()
			cleaned := thinkTagRE.ReplaceAllString(raw, "")
			if cleaned != raw && cleaned != "" {
				if !forward(map[string]interface{}{"type": "replace", "content": cleaned, "done": false}) {
					return false
				}
			}
			return true
		}
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				trim := strings.TrimSpace(string(line))
				if strings.HasPrefix(trim, "data:") {
					payload := strings.TrimSpace(strings.TrimPrefix(trim, "data:"))
					var evt map[string]interface{}
					if json.Unmarshal([]byte(payload), &evt) == nil {
						typ, _ := evt["type"].(string)
						if typ == "content" {
							if content, _ := evt["content"].(string); content != "" {
								contentBuf.WriteString(content)
							}
							if !forward(evt) {
								// Client disconnected - drain agent response in background
								drainAgentResponse()
								return
							}
							goto lineDone
						}
						if typ, _ := evt["type"].(string); typ == "tool_call" {
							gw.addEvent("tool_call", in.Agent, "Tool call observed")
							evtBytes, _ := json.Marshal(evt)
							evtBlob := strings.ToLower(string(evtBytes))
							toolName := strings.ToLower(strings.TrimSpace(fmt.Sprint(evt["name"])))
							if toolName == "" {
								if tc, ok := evt["tool_call"].(map[string]interface{}); ok {
									toolName = strings.ToLower(strings.TrimSpace(fmt.Sprint(tc["name"])))
									if toolName == "" {
										if fn, ok := tc["function"].(map[string]interface{}); ok {
											toolName = strings.ToLower(strings.TrimSpace(fmt.Sprint(fn["name"])))
										}
									}
								}
							}
							switch {
							case toolName == "exec" && strings.Contains(evtBlob, "cat /tmp/valhalla-personas"):
								gw.addEvent("skill_loaded", in.Agent, "Loaded skill file")
							case toolName == "git-clone":
								gw.addEvent("recon_started", in.Agent, "Cloning repository")
							case toolName == "gitea" && strings.Contains(evtBlob, "create-pr"):
								gw.addEvent("pr_created", in.Agent, "Pull request created")
							case toolName == "read" && strings.Contains(strings.ToUpper(string(evtBytes)), "ARCHITECTURE"):
								gw.addEvent("recon_reading", in.Agent, "Reading ARCHITECTURE.md")
							}
						}
						if typ == "done" {
							doneEvt = evt
							if _, ok := doneEvt["session_id"]; !ok {
								doneEvt["session_id"] = sessionID
							}
							if !forwardReplaceIfNeeded() {
								// Client disconnected - drain agent response in background
								drainAgentResponse()
								return
							}
							saveConversation()
							processInjectionQueue(sessionID)
							if !forward(doneEvt) {
								// Client disconnected - drain agent response in background
								drainAgentResponse()
								return
							}
							uResp.Body.Close()
							return
						}
						if !forward(evt) {
							// Client disconnected - drain agent response in background
							drainAgentResponse()
							return
						}
					}
				}
			lineDone:
			}
			if err == io.EOF {
				if !forwardReplaceIfNeeded() {
					uResp.Body.Close()
					return
				}
				saveConversation()
				processInjectionQueue(sessionID)
				if doneEvt != nil {
					_ = forward(doneEvt)
				} else {
					_ = forward(map[string]interface{}{"type": "done", "done": true, "session_id": sessionID})
				}
				uResp.Body.Close()
				return
			}
			if err != nil {
				uResp.Body.Close()
				return
			}
		}
	})
	gw.registerHealthEndpoints(mux)
	registerGatewayMCP(mux)
	gw.registerWebhookHandlers(mux)
	go func() {
		time.Sleep(10 * time.Second)
		gw.ensureGiteaWebhooks()
	}()
	mux.HandleFunc("/api/v1/ping", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "agent": "gateway", "timestamp": time.Now().Unix()})
	})

	mux.HandleFunc("/api/v1/metrics/fleet", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		agents := gw.snapshotAgents()
		type agentMetrics struct {
			Name         string `json:"name"`
			Reachable    bool   `json:"reachable"`
			MetricsBytes int    `json:"metrics_bytes"`
		}
		metricsList := make([]agentMetrics, 0, len(agents))
		reachable := 0
		for _, a := range agents {
			m := agentMetrics{Name: a.Name, Reachable: false, MetricsBytes: 0}
			url := strings.TrimRight(a.URL, "/") + "/metrics"
			req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
			if err == nil {
				client := &http.Client{Timeout: 2 * time.Second}
				resp, err := client.Do(req)
				if err == nil {
					if resp.StatusCode == 200 {
						body, _ := io.ReadAll(io.LimitReader(resp.Body, 1 << 20))
						m.Reachable = true
						m.MetricsBytes = len(body)
						reachable++
					}
					_ = resp.Body.Close()
				}
			}
			metricsList = append(metricsList, m)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"agents":        metricsList,
			"timestamp":     time.Now().UTC().Format(time.RFC3339),
			"total_agents":  len(agents),
			"reachable":     reachable,
		})
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		agents := gw.snapshotAgents()
		healthy := 0
		for _, a := range agents {
			if a.Healthy {
				healthy++
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ready", "agents": len(agents), "healthy": healthy})
	})

	log.Printf("approval notifier: discord-webhook-url=%q lockbox-url=%q", *discordWebhookURL, *lockboxURL)
	if strings.TrimSpace(*discordWebhookURL) != "" {
		go func() {
			log.Printf("approval notifier: goroutine started")
			seen := map[string]struct{}{}
			poll := func() {
				approvalsURL := fmt.Sprintf("http://127.0.0.1:%s/api/v1/approvals", strings.TrimSpace(*port))
				req, err := http.NewRequest(http.MethodGet, approvalsURL, nil)
				if err != nil {
					log.Printf("approval notifier: create request failed: %v", err)
					return
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					log.Printf("approval notifier: poll failed: %v", err)
					return
				}
				defer resp.Body.Close()
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
					log.Printf("approval notifier: poll returned %s: %s", resp.Status, strings.TrimSpace(string(b)))
					return
				}
				var out approvalsResponse
				if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
					log.Printf("approval notifier: decode failed: %v", err)
					return
				}
				for _, item := range out.Queues {
					if strings.TrimSpace(item.Status) != "pending" {
						continue
					}
					qid := strings.TrimSpace(item.QueueID)
					if qid == "" {
						continue
					}
					if _, exists := seen[qid]; exists {
						continue
					}
					seen[qid] = struct{}{}
					if err := sendDiscordApprovalWebhook(*discordWebhookURL, item); err != nil {
						log.Printf("approval notifier: webhook send failed for %s: %v", qid, err)
					}
				}
			}
			time.Sleep(5 * time.Second)
			log.Printf("approval notifier: starting first poll")
			poll()
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				poll()
			}
		}()
	}

	addr := ":" + *port
	log.Printf("Valhalla Gateway listening on %s", addr)
	log.Printf("Agents: %s", strings.Join(order, ","))
	die("gateway failed", http.ListenAndServe(addr, mux))
}
