package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
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

	workspacepkg "git.hirdforge.com/kit/hirdforge/pkg/workspace"
)

const agentRequestTimeout = 120 * time.Second

var streamTimeout = 600 * time.Second

//go:embed ui.html
var uiHTML string

type Agent struct {
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	Role           string   `json:"role"`
	Fleet        string   `json:"warband"`
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

type delegateResult struct {
	Agent     string    `json:"agent"`
	SessionID string    `json:"session_id"`
	Content   string    `json:"content"`
	Done      bool      `json:"done"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// delegationTimelineEvent is a typed event stored in the delegation timeline,
// indexed by session_id so callers can retrieve the full event chain for a session.
type delegationTimelineEvent struct {
	Type      string                 `json:"type"`
	Agent     string                 `json:"agent"`
	Metadata  map[string]interface{} `json:"metadata"`
	Timestamp string                 `json:"timestamp"`
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
	QueueID   string                 `json:"queue_id"`
	HuntID    string                 `json:"hunt_id"`
	Service   string                 `json:"service"`
	Action    string                 `json:"action"`
	AgentName string                 `json:"agent_name,omitempty"`
	Params    map[string]interface{} `json:"params"`
	Status    string                 `json:"status"`
	QueuedAt  string                 `json:"queued_at"`
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
	Namespace     string `json:"namespace"`
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
	Source    string `json:"source"`
}

type agentMessageRequest struct {
	Content   string `json:"content"`
	SessionID string `json:"session_id,omitempty"`
}

type dispatchReq struct {
	Agent   string `json:"agent"`
	Content string `json:"content"`
	From    string `json:"from"`
}

type taskSendRequest struct {
	Content   string `json:"content"`
	From      string `json:"from"`
	SessionID string `json:"session_id,omitempty"`
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
	a2aStore          *A2ATaskStore
	reposMu           sync.RWMutex
	repos             []string
	eventMu           sync.Mutex
	events            []Event
	eventCap          int
	k8s               *k8sState
	wsMu              sync.Mutex
	wsConns           []*wsClient
	sessionStore      *sessionStore
	projector         *workspacepkg.Projector
	settings          *settingsStore
	notifMu           sync.Mutex
	notifications     []Notification
	notifCap          int
	lastSessionMu     sync.RWMutex
	lastSession       map[string]string
	arMu              sync.RWMutex
	activeRequests    map[string]*ActiveRequest
	arEpoch           uint64
	webhookDedup      sync.Map
	delegateResults   sync.Map
	injectionMu       sync.Mutex
	injections        map[string][]InjectionMessage // keyed by agent name
	pausedAgents      map[string]bool
	webhookSecret     string
	taskRepo          string
	giteaURL          string
	giteaToken        string
	seidrURL          string
	defaultFleet    string
	discordWebhookURL string

	// delegationTimelines stores typed events indexed by session_id.
	// Access is protected by dtlMu.
	dtlMu               sync.RWMutex
	delegationTimelines map[string][]delegationTimelineEvent
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

var validAgentRoles = map[string]bool{
	"builder":     true,
	"reviewer":    true,
	"coordinator": true,
	"assistant":   true,
	"specialist":  true,
	"architect":   true,
}

func normalizeFleetName(fleet string) string {
	fleet = strings.TrimSpace(fleet)
	if fleet == "" {
		return "default"
	}
	return fleet
}

func parseAgents(raw string, defaultFleet string) (map[string]*Agent, []string, error) {
	defaultFleet = normalizeFleetName(defaultFleet)
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
		name, url = strings.TrimSpace(name), strings.TrimSpace(url)
		role := ""
		fleet := defaultFleet
		segments := strings.Split(url, ":")
		if len(segments) >= 3 {
			candidateRole := strings.TrimSpace(segments[len(segments)-2])
			if validAgentRoles[candidateRole] {
				role = candidateRole
				fleet = normalizeFleetName(strings.TrimSpace(segments[len(segments)-1]))
				url = strings.TrimSpace(strings.Join(segments[:len(segments)-2], ":"))
			}
		}
		if role == "" && len(segments) >= 2 {
			candidateRole := strings.TrimSpace(segments[len(segments)-1])
			if validAgentRoles[candidateRole] {
				role = candidateRole
				url = strings.TrimSpace(strings.Join(segments[:len(segments)-1], ":"))
			}
		}
		url = strings.TrimRight(url, "/")
		if _, exists := out[name]; exists {
			return nil, nil, fmt.Errorf("duplicate agent name %q", name)
		}
		out[name] = &Agent{Name: name, URL: url, Role: role, Fleet: fleet}
		order = append(order, name)
	}
	if len(out) == 0 {
		return nil, nil, fmt.Errorf("no agents configured")
	}
	sort.Strings(order)
	return out, order, nil
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

// envOrDefault returns the value of an environment variable, or a default if unset/empty.
func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
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
	path := fmt.Sprintf("/api/v1/repos/kit/asgard-infra/commits?sha=main&limit=%d", clampInt(limit, 20, 100))
	var commits []map[string]interface{}
	status, _, err := giteaGetJSONWithStatus(client, baseURL, token, path, &commits)
	if err != nil || status < 200 || status >= 300 {
		return out
	}
	for _, commit := range commits {
		sha := asString(commit["sha"])
		include := false
		detailPath := fmt.Sprintf("/api/v1/repos/kit/asgard-infra/git/commits/%s", url.PathEscape(sha))
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

func createGitOpsAgentConfigPR(client *http.Client, baseURL, token, namespace, agentName string, req agentConfigureRequest) (string, error) {
	const repoFullName = "kit/asgard-infra"
	manifestPath := fmt.Sprintf("infrastructure/%s/deployment-%s.yaml", namespace, agentName)

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
			"email": "gateway@asgard.local",
		},
		"committer": map[string]string{
			"name":  "Hirdforge Gateway",
			"email": "gateway@asgard.local",
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

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
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

func shortSHA(in string) string {
	in = strings.TrimSpace(in)
	if len(in) > 7 {
		return in[:7]
	}
	return in
}

// refreshRepos queries the Gitea API to discover all visible repositories
// and stores the canonical list in g.repos. It is called once at gateway startup
// and then on a 5-minute ticker to pick up new repositories or access changes.
//
// The function hits three endpoints in sequence:
//
//   - /api/v1/user/repos?limit=50         — repositories owned by the authenticated user
//   - /api/v1/users/warband/repos?limit=50 — repositories owned by the warband user
//   - /api/v1/users/kit/repos?limit=50     — repositories owned by the kit user
//
// Results are deduplicated by full_name, sorted alphabetically, and assigned to
// g.repos under g.reposMu. An empty or unset giteaURL / giteaToken causes the
// function to return immediately without any API call.
//
// Logging behaviour (as added in PR #155): the function now logs only when the
// repository list actually changes — it records the new full list at INFO level.
// On startup or a periodic tick where the list is unchanged, no log entry is
// emitted, keeping noise low in high-frequency ticker loops.
func (g *gateway) refreshRepos() {
	if strings.TrimSpace(g.giteaURL) == "" || strings.TrimSpace(g.giteaToken) == "" {
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	all := make([]map[string]interface{}, 0, 100)
	for _, path := range []string{
		"/api/v1/user/repos?limit=50",
		"/api/v1/users/warband/repos?limit=50",
		"/api/v1/users/kit/repos?limit=50",
	} {
		var repos []map[string]interface{}
		status, body, err := giteaGetJSONWithStatus(client, g.giteaURL, g.giteaToken, path, &repos)
		if err != nil {
			log.Printf("gitea repos: discovery failed for %s: %v", path, err)
			continue
		}
		if status < 200 || status >= 300 {
			log.Printf("gitea repos: discovery returned %d for %s: %s", status, path, strings.TrimSpace(string(body)))
			continue
		}
		all = append(all, repos...)
	}
	if len(all) == 0 {
		return
	}

	seen := make(map[string]struct{}, len(all))
	repos := make([]string, 0, len(all))
	for _, repo := range all {
		fullName := strings.TrimSpace(asString(repo["full_name"]))
		if fullName == "" {
			continue
		}
		if _, ok := seen[fullName]; ok {
			continue
		}
		seen[fullName] = struct{}{}
		repos = append(repos, fullName)
	}
	sort.Strings(repos)

	changed := len(g.repos) != len(repos)
	if !changed {
		for i := range g.repos {
			if g.repos[i] != repos[i] {
				changed = true
				break
			}
		}
	}

	g.reposMu.Lock()
	g.repos = repos
	g.reposMu.Unlock()
	if changed {
		log.Printf("gitea repos: discovered %v", repos)
	}
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
	if s := strings.TrimSpace(item.AgentName); s != "" {
		return s
	}
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

func writeUpstreamResponse(w http.ResponseWriter, resp *http.Response, limit int64) {
	if resp == nil {
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
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
}

func extractFileFromUnifiedDiff(rawDiff string, filename string) string {
	target := strings.TrimSpace(filename)
	if target == "" || rawDiff == "" {
		return ""
	}
	lines := strings.Split(rawDiff, "\n")
	sectionStart := -1
	sectionPath := ""
	for i, line := range lines {
		if !strings.HasPrefix(line, "diff --git ") {
			continue
		}
		if sectionStart >= 0 && sectionPath == target {
			return strings.Join(lines[sectionStart:i], "\n")
		}
		sectionStart = i
		sectionPath = ""
		parts := strings.Fields(line)
		if len(parts) >= 4 {
			sectionPath = strings.TrimPrefix(parts[3], "b/")
		}
	}
	if sectionStart >= 0 && sectionPath == target {
		return strings.Join(lines[sectionStart:], "\n")
	}
	return ""
}

func registerGiteaPRRoutes(mux *http.ServeMux, gw *gateway, giteaClient *http.Client) {
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
		case "files":
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			uPath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%s/files", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(idxRaw))
			resp, err = giteaRequest(giteaClient, http.MethodGet, gw.giteaURL, gw.giteaToken, uPath, nil)
		case "diff":
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			targetFile := strings.TrimSpace(r.URL.Query().Get("file"))
			if targetFile == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file query parameter is required"})
				return
			}
			uPath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%s/files", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(idxRaw))
			filesResp, reqErr := giteaRequest(giteaClient, http.MethodGet, gw.giteaURL, gw.giteaToken, uPath, nil)
			if reqErr != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Gitea unreachable"})
				return
			}
			filesBody, _ := io.ReadAll(io.LimitReader(filesResp.Body, 2<<20))
			filesResp.Body.Close()
			if filesResp.StatusCode < 200 || filesResp.StatusCode >= 300 {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(filesBody))})
				return
			}
			var files []map[string]interface{}
			if err := json.Unmarshal(filesBody, &files); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "invalid Gitea files response"})
				return
			}
			var matchedFile map[string]interface{}
			for _, file := range files {
				if asString(file["filename"]) == targetFile {
					matchedFile = file
					break
				}
			}
			if matchedFile == nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found in pull request"})
				return
			}
			diffPath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%s.diff", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(idxRaw))
			diffResp, diffReqErr := giteaRequest(giteaClient, http.MethodGet, gw.giteaURL, gw.giteaToken, diffPath, nil)
			if diffReqErr != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Gitea unreachable"})
				return
			}
			diffBody, _ := io.ReadAll(io.LimitReader(diffResp.Body, 8<<20))
			diffResp.Body.Close()
			if diffResp.StatusCode < 200 || diffResp.StatusCode >= 300 {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(diffBody))})
				return
			}
			out := make(map[string]interface{}, len(matchedFile)+1)
			for k, v := range matchedFile {
				out[k] = v
			}
			out["patch"] = extractFileFromUnifiedDiff(string(diffBody), targetFile)
			writeJSON(w, http.StatusOK, out)
			return
		case "comments":
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			reviewsPath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%s/reviews", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(idxRaw))
			reviewsResp, reqErr := giteaRequest(giteaClient, http.MethodGet, gw.giteaURL, gw.giteaToken, reviewsPath, nil)
			if reqErr != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Gitea unreachable"})
				return
			}
			reviewsBody, _ := io.ReadAll(io.LimitReader(reviewsResp.Body, 2<<20))
			reviewsResp.Body.Close()
			if reviewsResp.StatusCode < 200 || reviewsResp.StatusCode >= 300 {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(reviewsBody))})
				return
			}
			var reviews []map[string]interface{}
			if err := json.Unmarshal(reviewsBody, &reviews); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "invalid Gitea reviews response"})
				return
			}
			comments := make([]map[string]interface{}, 0)
			for _, review := range reviews {
				reviewID := asInt64(review["id"])
				if reviewID <= 0 {
					continue
				}
				commentsPath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%s/reviews/%d/comments", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(idxRaw), reviewID)
				commentsResp, commentsErr := giteaRequest(giteaClient, http.MethodGet, gw.giteaURL, gw.giteaToken, commentsPath, nil)
				if commentsErr != nil {
					writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Gitea unreachable"})
					return
				}
				commentBody, _ := io.ReadAll(io.LimitReader(commentsResp.Body, 2<<20))
				commentsResp.Body.Close()
				if commentsResp.StatusCode < 200 || commentsResp.StatusCode >= 300 {
					writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(commentBody))})
					return
				}
				var reviewComments []map[string]interface{}
				if err := json.Unmarshal(commentBody, &reviewComments); err != nil {
					writeJSON(w, http.StatusBadGateway, map[string]string{"error": "invalid Gitea review comments response"})
					return
				}
				for _, comment := range reviewComments {
					comments = append(comments, map[string]interface{}{
						"path":          asString(comment["path"]),
						"line":          asInt64(comment["line"]),
						"original_line": asInt64(comment["original_line"]),
						"body":          asString(comment["body"]),
						"user":          asMap(comment["user"]),
						"created_at":    asString(comment["created_at"]),
						"review_id":     reviewID,
					})
				}
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"comments": comments})
			return
		case "review":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			body, readErr := io.ReadAll(io.LimitReader(r.Body, 2<<20))
			if readErr != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid review body"})
				return
			}
			var payload map[string]interface{}
			if err := json.Unmarshal(body, &payload); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
				return
			}
			event := strings.ToUpper(strings.TrimSpace(asString(payload["event"])))
			if event != "APPROVED" && event != "REQUEST_CHANGES" && event != "COMMENT" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "event must be one of APPROVED, REQUEST_CHANGES, COMMENT"})
				return
			}
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
		writeUpstreamResponse(w, resp, 2<<20)
	})
}

func resolveApprovalHuntID(ctx context.Context, lockboxURL, queueID string) (string, error) {
	base := strings.TrimSpace(lockboxURL)
	if base == "" {
		return "", fmt.Errorf("lockbox not configured")
	}
	queueID = strings.TrimSpace(queueID)
	if queueID == "" {
		return "", fmt.Errorf("queue_id is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/queues", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("lockbox /queues returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out approvalsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	for _, item := range out.Queues {
		if strings.TrimSpace(item.QueueID) == queueID {
			huntID := strings.TrimSpace(item.HuntID)
			if huntID == "" {
				return "", fmt.Errorf("queue %s missing hunt_id", queueID)
			}
			return huntID, nil
		}
	}
	return "", fmt.Errorf("queue item not found")
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
				"text": "Hirdforge Gateway • Approval required",
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
	agentsFlag := flag.String("agents", "", "comma-separated name=url[:role][:warband] agent list")
	defaultFleetFlag := flag.String("default-warband", "default", "fallback warband for agents without one")
	giteaURL := flag.String("gitea-url", "", "Gitea base URL")
	giteaToken := flag.String("gitea-token", "", "Gitea API token (optional)")
	giteaRepo := flag.String("gitea-repo", "kit/hirdforge", "Gitea repo in owner/name format")
	lockboxURL := flag.String("lockbox-url", envOrDefault("LOCKBOX_URL", ""), "Lockbox base URL for approval queue proxy")
	discordWebhookURL := flag.String("discord-webhook-url", envOrDefault("DISCORD_WEBHOOK_URL", ""), "Discord webhook URL for new approval notifications")
	webhookSecret := flag.String("webhook-secret", "", "HMAC secret for Gitea webhook validation (optional)")
	taskRepoFlag := flag.String("task-repo", "kit/hirdforge-tasks", "Gitea repo for task board issues")
	seidrURLFlag := flag.String("seidr-url", "http://seidr.asgard.svc:8082", "Seidr memory service URL")
	a2aDBURL := flag.String("a2a-db-url", "", "PostgreSQL URL for A2A task store")
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

	defaultFleet := normalizeFleetName(*defaultFleetFlag)
	agents, order, err := parseAgents(*agentsFlag, defaultFleet)
	if err != nil {
		die("failed to parse --agents", err)
	}
	gw := &gateway{
		agents:              agents,
		order:               order,
		repos:               []string{},
		events:              make([]Event, 0, 200),
		eventCap:            200,
		k8s:                 initK8s(),
		sessionStore:        newSessionStore(),
		projector:           workspacepkg.NewProjector(),
		settings:            newSettingsStore(),
		notifications:       make([]Notification, 0, 100),
		notifCap:            100,
		lastSession:         map[string]string{},
		activeRequests:      map[string]*ActiveRequest{},
		injections:          map[string][]InjectionMessage{},
		pausedAgents:        map[string]bool{},
		webhookSecret:       strings.TrimSpace(*webhookSecret),
		taskRepo:            strings.TrimSpace(*taskRepoFlag),
		giteaURL:            strings.TrimSpace(*giteaURL),
		giteaToken:          resolveGatewayGiteaToken(*giteaToken),
		seidrURL:            strings.TrimSpace(*seidrURLFlag),
		defaultFleet:      defaultFleet,
		discordWebhookURL:   strings.TrimSpace(*discordWebhookURL),
		delegationTimelines: make(map[string][]delegationTimelineEvent),
	}
	if strings.TrimSpace(*a2aDBURL) != "" {
		store := &A2ATaskStore{}
		if err := store.Init(*a2aDBURL); err != nil {
			die("failed to initialize a2a task store", err)
		}
		gw.a2aStore = store
		log.Printf("a2a: task store initialized")
	} else {
		log.Printf("a2a: task store disabled (no --a2a-db-url)")
	}
	gw.addEvent("agent_start", "gateway", fmt.Sprintf("Gateway started with %d agents", len(order)))
	gw.refreshRepos()
	gw.refreshAgentHealth()

	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			gw.refreshAgentHealth()
		}
	}()
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			gw.refreshRepos()
		}
	}()
	go gw.runWebhookDedupCleanup()
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			cutoff := time.Now().Add(-1 * time.Hour)
			gw.delegateResults.Range(func(key, value interface{}) bool {
				if r, ok := value.(*delegateResult); ok && r.CreatedAt.Before(cutoff) {
					gw.delegateResults.Delete(key)
				}
				return true
			})
		}
	}()

	if gw.k8s.enabled {
		gw.addEvent("k8s_event", "cluster", "Kubernetes integration enabled")
		if err := gw.k8s.refreshPods(); err != nil {
			log.Printf("k8s: pod refresh error: %v", err)
			gw.addEvent("k8s_event", "cluster", "pod refresh error: "+err.Error())
		}
		if err := gw.k8s.refreshNodes(); err != nil {
			log.Printf("k8s: node refresh error: %v", err)
			gw.addEvent("k8s_event", "cluster", "node refresh error: "+err.Error())
		}
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for range t.C {
				if err := gw.k8s.refreshPods(); err != nil {
					log.Printf("k8s: pod refresh error: %v", err)
					gw.addEvent("k8s_event", "cluster", "pod refresh error: "+err.Error())
				}
			}
		}()
		go func() {
			t := time.NewTicker(60 * time.Second)
			defer t.Stop()
			for range t.C {
				if err := gw.k8s.refreshNodes(); err != nil {
					log.Printf("k8s: node refresh error: %v", err)
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
	registerRoutes(mux, gw, routeDeps{
		proxyClient:  proxyClient,
		streamClient: streamClient,
		giteaClient:  giteaClient,
		lockboxURL:   strings.TrimSpace(*lockboxURL),
		giteaRepo:    strings.TrimSpace(*giteaRepo),
	})
	gw.registerA2ARoutes(mux)
	gw.registerHealthEndpoints(mux)
	registerGatewayMCP(mux, gw)
	gw.registerWebhookHandlers(mux)
	go func() {
		time.Sleep(10 * time.Second)
		gw.ensureGiteaWebhooks()
	}()

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
	log.Printf("Hirdforge Gateway listening on %s", addr)
	log.Printf("Agents: %s", strings.Join(order, ","))
	die("gateway failed", http.ListenAndServe(addr, withRequestBodyLimit(mux, maxRequestBodyBytes)))
}

// maxRequestBodyBytes caps any single HTTP request body. LLM passthrough is
// the largest legitimate payload — long conversation histories or attached
// files — and 32 MiB is well above the inference servers' own per-request
// limits, so this cuts off pathological inputs without restricting normal use.
const maxRequestBodyBytes = 32 << 20

// withRequestBodyLimit wraps each request's r.Body in an http.MaxBytesReader
// so json.NewDecoder(r.Body).Decode(...) (and any other read of r.Body) can't
// be made to allocate unbounded memory by a hostile or buggy client.
func withRequestBodyLimit(next http.Handler, limit int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.ContentLength != 0 {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}
