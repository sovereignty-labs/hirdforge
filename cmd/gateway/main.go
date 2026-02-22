package main

import (
	"bufio"
	_ "embed"
	"bytes"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var thinkTagRE = regexp.MustCompile(`(?s)<think>.*?</think>`)

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

type messageReq struct {
	Agent     string `json:"agent"`
	Content   string `json:"content"`
	SessionID string `json:"session_id"`
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
	mu       sync.RWMutex
	agents   map[string]*Agent
	order    []string
	eventMu  sync.Mutex
	events   []Event
	eventCap int
	k8s      *k8sState
	wsMu     sync.Mutex
	wsConns  []*wsClient
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
	payload, err := json.Marshal(e)
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
	client := &http.Client{Timeout: 2 * time.Second}
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

func (k *k8sState) get(path string) (map[string]interface{}, error) {
	req, err := http.NewRequest(http.MethodGet, "https://kubernetes.default.svc"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	resp, err := k.client.Do(req)
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
	cpuMilli float64
	memBytes float64
}

func parseCPUToMilli(q string) (float64, bool) {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0, false
	}
	if strings.HasSuffix(q, "m") {
		v, err := strconv.ParseFloat(strings.TrimSuffix(q, "m"), 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	v, err := strconv.ParseFloat(q, 64)
	if err != nil {
		return 0, false
	}
	return v * 1000, true
}

func parseBytesQuantity(q string) (float64, bool) {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0, false
	}
	units := map[string]float64{
		"Ki": 1024,
		"Mi": 1024 * 1024,
		"Gi": 1024 * 1024 * 1024,
		"Ti": 1024 * 1024 * 1024 * 1024,
		"Pi": 1024 * 1024 * 1024 * 1024 * 1024,
		"Ei": 1024 * 1024 * 1024 * 1024 * 1024 * 1024,
		"K":  1000,
		"M":  1000 * 1000,
		"G":  1000 * 1000 * 1000,
		"T":  1000 * 1000 * 1000 * 1000,
		"P":  1000 * 1000 * 1000 * 1000 * 1000,
		"E":  1000 * 1000 * 1000 * 1000 * 1000 * 1000,
	}
	for suffix, scale := range units {
		if strings.HasSuffix(q, suffix) {
			v, err := strconv.ParseFloat(strings.TrimSuffix(q, suffix), 64)
			if err != nil {
				return 0, false
			}
			return v * scale, true
		}
	}
	v, err := strconv.ParseFloat(q, 64)
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
		cpuMilli, okCPU := parseCPUToMilli(asString(usage["cpu"]))
		memBytes, okMem := parseBytesQuantity(asString(usage["memory"]))
		if name == "" || !okCPU || !okMem {
			continue
		}
		out[name] = nodeUsage{cpuMilli: cpuMilli, memBytes: memBytes}
	}
	return out
}

func enrichNodesWithUsage(nodes []NodeInfo, usage map[string]nodeUsage) []NodeInfo {
	for i := range nodes {
		u, ok := usage[nodes[i].Name]
		if !ok {
			continue
		}
		if allocMilli, ok := parseCPUToMilli(nodes[i].AllocatableCPU); ok && allocMilli > 0 {
			nodes[i].CPUPercent = math.Round((u.cpuMilli/allocMilli)*1000) / 10
		}
		if allocMem, ok := parseBytesQuantity(nodes[i].AllocatableMemory); ok && allocMem > 0 {
			nodes[i].MemoryPercent = math.Round((u.memBytes/allocMem)*1000) / 10
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

func main() {
	port := flag.String("port", "8080", "HTTP port")
	agentsFlag := flag.String("agents", "", "comma-separated name=url agent list")
	giteaURL := flag.String("gitea-url", "", "Gitea base URL")
	giteaToken := flag.String("gitea-token", "", "Gitea API token (optional)")
	giteaRepo := flag.String("gitea-repo", "gitea_admin/project_valhalla", "Gitea repo in owner/name format")
	flag.Parse()
	if strings.TrimSpace(*agentsFlag) == "" {
		die("missing --agents", fmt.Errorf("required"))
	}

	agents, order, err := parseAgents(*agentsFlag)
	if err != nil {
		die("failed to parse --agents", err)
	}
	gw := &gateway{agents: agents, order: order, events: make([]Event, 0, 200), eventCap: 200, k8s: initK8s()}
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

	proxyClient := &http.Client{}
	giteaClient := &http.Client{Timeout: 5 * time.Second}
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
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, gw.snapshotAgents())
	})
	mux.HandleFunc("/api/v1/agents/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/agents/")
		if !strings.HasSuffix(path, "/files") {
			http.NotFound(w, r)
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
	})
	mux.HandleFunc("/api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, gw.eventsNewest())
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
	mux.HandleFunc("/api/v1/message", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
		if !agent.Healthy {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent is unhealthy"})
			return
		}
		gw.addEvent("message", in.Agent, fmt.Sprintf("Message sent to %s", in.Agent))

		body, _ := json.Marshal(map[string]string{"content": in.Content, "session_id": in.SessionID})
		uReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(agent.URL, "/")+"/message", bytes.NewReader(body))
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to create upstream request"})
			return
		}
		uReq.Header.Set("Content-Type", "application/json")
		uResp, err := proxyClient.Do(uReq)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream request failed"})
			return
		}
		defer uResp.Body.Close()
		if uResp.StatusCode < 200 || uResp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(uResp.Body, 4096))
			writeJSON(w, uResp.StatusCode, map[string]string{"error": strings.TrimSpace(string(b))})
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		reader := bufio.NewReader(uResp.Body)
		var contentBuf strings.Builder
		var doneEvt map[string]interface{}
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
		flushContent := func() bool {
			clean := thinkTagRE.ReplaceAllString(contentBuf.String(), "")
			if clean != "" {
				if !forward(map[string]interface{}{"type": "content", "content": clean, "done": false}) {
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
							goto lineDone
						}
						if typ, _ := evt["type"].(string); typ == "tool_call" {
							gw.addEvent("tool_call", in.Agent, "Tool call observed")
						}
						if typ == "done" {
							doneEvt = evt
							if !flushContent() {
								return
							}
							if !forward(doneEvt) {
								return
							}
							return
						}
						if !forward(evt) {
							return
						}
					}
				}
			lineDone:
			}
			if err == io.EOF {
				if !flushContent() {
					return
				}
				if doneEvt != nil {
					_ = forward(doneEvt)
				} else {
					_ = forward(map[string]interface{}{"type": "done", "done": true})
				}
				return
			}
			if err != nil {
				return
			}
		}
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

	addr := ":" + *port
	log.Printf("Valhalla Gateway listening on %s", addr)
	log.Printf("Agents: %s", strings.Join(order, ","))
	die("gateway failed", http.ListenAndServe(addr, mux))
}
