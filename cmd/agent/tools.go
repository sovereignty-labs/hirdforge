package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	mcppkg "github.com/kitporath/project_valhalla/pkg/mcp"
	tasklifepkg "github.com/kitporath/project_valhalla/pkg/tasklife"
	toolpkg "github.com/kitporath/project_valhalla/pkg/tools"
)

var (
	delegateExecValue  *delegateTool
	broadcastExecValue *broadcastTool
)

type delegateTool struct {
	peers               map[string]string
	agentName           string
	giteaURL            string
	maxDelegationTokens int
	gates               []string
}

type peerHealthResponse struct {
	Model string `json:"model"`
}

const agentCommTimeout = 120 * time.Second

func (t *delegateTool) Name() string { return "delegate" }
func (t *delegateTool) Description() string {
	return "Send a task to another agent and get their response. Use this to delegate work to specialists."
}
func (t *delegateTool) Parameters() map[string]string {
	return map[string]string{
		"agent": "Name of the agent to delegate to (e.g. chuck, val, ragnar)",
		"task":  "The task description to send to the agent",
	}
}
func (t *delegateTool) Execute(args map[string]interface{}) toolpkg.ToolResult {
	agent, _ := args["agent"].(string)
	task, _ := args["task"].(string)
	if strings.TrimSpace(agent) == "" || strings.TrimSpace(task) == "" {
		return toolpkg.ToolResult{Error: "agent and task are required"}
	}
	taskID, _ := args["_task_id"].(string)
	sessionID := strings.TrimSpace(fmt.Sprint(args["_session_id"]))
	if sessionID == "" {
		sessionID = strings.TrimSpace(taskID)
	}
	peerURL, ok := t.peers[agent]
	if !ok {
		names := make([]string, 0, len(t.peers))
		for name := range t.peers {
			names = append(names, name)
		}
		sort.Strings(names)
		return toolpkg.ToolResult{Error: fmt.Sprintf("unknown agent: %s. Available: %s", agent, strings.Join(names, ", "))}
	}
	formatted, err := t.prepareDelegation(peerURL, task, taskID)
	if err != nil {
		return toolpkg.ToolResult{Error: err.Error()}
	}
	resp, err := sendPeerAgentTask(agent, peerURL, t.agentName, formatted, sessionID)
	if err != nil {
		return toolpkg.ToolResult{Error: err.Error()}
	}
	return toolpkg.ToolResult{Output: resp}
}

func (t *delegateTool) prepareDelegation(peerURL, task, taskID string) (string, error) {
	parsed, err := tasklifepkg.ValidateDelegation(task)
	if err != nil {
		return "", fmt.Errorf("invalid delegation format: %w", err)
	}
	targetModel := fetchPeerModel(peerURL)
	formatted := tasklifepkg.OptimizeDelegation(targetModel, parsed)
	tier := tasklifepkg.ModelTiers[strings.ToLower(strings.TrimSpace(targetModel))]
	if t.maxDelegationTokens > 0 && tier == tasklifepkg.TierCommand {
		formatted = tasklifepkg.FormatDelegation(parsed, t.maxDelegationTokens)
	}
	if t.maxDelegationTokens > 0 && tier == tasklifepkg.TierStrike && t.maxDelegationTokens < 500 {
		formatted = tasklifepkg.FormatDelegation(tasklifepkg.DelegationFormat{
			Task:     parsed.Task,
			Steps:    parsed.Steps,
			DoneWhen: parsed.DoneWhen,
		}, t.maxDelegationTokens)
	}
	gates := "none"
	if len(t.gates) > 0 {
		gates = strings.Join(t.gates, ", ")
	}
	cloneURL := strings.TrimRight(t.giteaURL, "/")
	if cloneURL == "" {
		cloneURL = "unknown"
	}
	gitIdentity := fmt.Sprintf("%s <agent@valhalla.local>", t.agentName)
	footer := fmt.Sprintf(
		"\n\nFROM: %s\nTASK_ID: %s\nGATES: %s\nGIT_IDENTITY: %s\nCLONE_URL: %s",
		t.agentName,
		strings.TrimSpace(taskID),
		gates,
		gitIdentity,
		cloneURL,
	)
	return formatted + footer, nil
}

func fetchPeerModel(peerURL string) string {
	if strings.TrimSpace(peerURL) == "" {
		return ""
	}
	client := &http.Client{Timeout: agentCommTimeout}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(peerURL, "/")+"/health", nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	var out peerHealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ""
	}
	return strings.TrimSpace(out.Model)
}

func sendPeerAgentTask(agentName, peerURL, from, task, sessionID string) (string, error) {
	logJSON("info", "delegating", map[string]interface{}{"target_agent": agentName})
	body, _ := json.Marshal(taskSendRequest{
		Content:   task,
		From:      from,
		SessionID: strings.TrimSpace(sessionID),
	})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(peerURL, "/")+"/tasks/send", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: agentCommTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("peer returned %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("peer returned empty task id")
	}
	return waitForPeerAgentTask(agentName, peerURL, out.ID)
}

func waitForPeerAgentTask(agentName, peerURL, taskID string) (string, error) {
	deadline := time.Now().Add(agentCommTimeout)
	for {
		status, result, err := fetchPeerAgentTask(peerURL, taskID)
		if err != nil {
			return "", err
		}
		switch status {
		case "completed":
			if strings.TrimSpace(result) == "" {
				return "", fmt.Errorf("peer %s completed task %s with empty result", agentName, taskID)
			}
			return result, nil
		case "failed":
			if strings.TrimSpace(result) == "" {
				result = "delegated task failed"
			}
			return "", fmt.Errorf("peer %s task %s failed: %s", agentName, taskID, result)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for %s task %s", agentName, taskID)
		}
		time.Sleep(2 * time.Second)
	}
}

func fetchPeerAgentTask(peerURL, taskID string) (string, string, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(peerURL, "/")+"/tasks/"+url.PathEscape(taskID), nil)
	if err != nil {
		return "", "", err
	}
	client := &http.Client{Timeout: agentCommTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("peer returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var taskResp struct {
		Status string `json:"status"`
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(body, &taskResp); err != nil {
		return "", "", err
	}
	result := strings.TrimSpace(taskResp.Result)
	if result == "" && strings.TrimSpace(taskResp.Error) != "" {
		result = strings.TrimSpace(taskResp.Error)
	}
	return strings.TrimSpace(taskResp.Status), result, nil
}

type broadcastTool struct {
	peers map[string]string
}

func (t *broadcastTool) Name() string { return "broadcast" }
func (t *broadcastTool) Description() string {
	return "Send a task to ALL peer agents in parallel and collect their responses. Use for gathering information or coordinating across the entire team."
}
func (t *broadcastTool) Parameters() map[string]string {
	return map[string]string{
		"task": "The task to send to all agents",
	}
}
func (t *broadcastTool) Execute(args map[string]interface{}) toolpkg.ToolResult {
	task, _ := args["task"].(string)
	if strings.TrimSpace(task) == "" {
		return toolpkg.ToolResult{Error: "task is required"}
	}
	if len(t.peers) == 0 {
		return toolpkg.ToolResult{Error: "no peers configured"}
	}
	names := make([]string, 0, len(t.peers))
	for name := range t.peers {
		names = append(names, name)
	}
	sort.Strings(names)
	type out struct {
		name string
		resp string
		err  error
	}
	results := make(chan out, len(names))
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func(peerName string) {
			defer wg.Done()
			resp, err := sendPeerAgentTask(peerName, t.peers[peerName], "broadcast", task, "")
			results <- out{name: peerName, resp: resp, err: err}
		}(name)
	}
	wg.Wait()
	close(results)
	collected := map[string]out{}
	for r := range results {
		collected[r.name] = r
	}
	var b strings.Builder
	for _, name := range names {
		r := collected[name]
		b.WriteString("=== " + name + " ===\n")
		if r.err != nil {
			b.WriteString("ERROR: " + r.err.Error() + "\n\n")
		} else {
			b.WriteString(r.resp + "\n\n")
		}
	}
	return toolpkg.ToolResult{Output: b.String()}
}

type taskStatusTool struct {
	peers map[string]string
}

func (t *taskStatusTool) Name() string { return "task_status" }
func (t *taskStatusTool) Description() string {
	return "Check the status of an async delegated task on a peer agent."
}
func (t *taskStatusTool) Parameters() map[string]string {
	return map[string]string{
		"agent":   "Name of the agent running the task",
		"task_id": "Task ID returned by delegate",
	}
}
func (t *taskStatusTool) Execute(args map[string]interface{}) toolpkg.ToolResult {
	agent, _ := args["agent"].(string)
	taskID, _ := args["task_id"].(string)
	if strings.TrimSpace(agent) == "" || strings.TrimSpace(taskID) == "" {
		return toolpkg.ToolResult{Error: "agent and task_id are required"}
	}
	peerURL, ok := t.peers[agent]
	if !ok {
		return toolpkg.ToolResult{Error: "unknown agent: " + agent}
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(peerURL, "/")+"/tasks/"+url.PathEscape(taskID), nil)
	if err != nil {
		return toolpkg.ToolResult{Error: err.Error()}
	}
	client := &http.Client{Timeout: agentCommTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return toolpkg.ToolResult{Error: err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return toolpkg.ToolResult{Error: strings.TrimSpace(string(body))}
	}
	var taskResp struct {
		Status string `json:"status"`
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(body, &taskResp); err != nil {
		return toolpkg.ToolResult{Output: strings.TrimSpace(string(body))}
	}
	trimmed := map[string]string{
		"status": strings.TrimSpace(taskResp.Status),
		"result": strings.TrimSpace(taskResp.Result),
	}
	if trimmed["result"] == "" && strings.TrimSpace(taskResp.Error) != "" {
		trimmed["result"] = strings.TrimSpace(taskResp.Error)
	}
	out, err := json.Marshal(trimmed)
	if err != nil {
		return toolpkg.ToolResult{Output: strings.TrimSpace(string(body))}
	}
	return toolpkg.ToolResult{Output: string(out)}
}

type recallTool struct {
	memoryURL  string
	agentName  string
	onMemories func(sessionID string, ids []string)
}

func (t *recallTool) Name() string { return "recall" }
func (t *recallTool) Description() string {
	return "Search your memories and the knowledge base for relevant information. Use this before starting a task to check if you've done something similar before."
}
func (t *recallTool) Parameters() map[string]string {
	return map[string]string{"query": "What to search for in memories"}
}
func (t *recallTool) Execute(args map[string]interface{}) toolpkg.ToolResult {
	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return toolpkg.ToolResult{Output: "No relevant memories found."}
	}
	body, _ := json.Marshal(map[string]interface{}{
		"query": query,
		"agent": t.agentName,
		"limit": 5,
	})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(t.memoryURL, "/")+"/query", bytes.NewReader(body))
	if err != nil {
		return toolpkg.ToolResult{Output: "No relevant memories found."}
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return toolpkg.ToolResult{Output: "No relevant memories found."}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return toolpkg.ToolResult{Output: "No relevant memories found."}
	}
	var out struct {
		Results []struct {
			ID         string  `json:"id"`
			Content    string  `json:"content"`
			Similarity float64 `json:"similarity"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Results) == 0 {
		return toolpkg.ToolResult{Output: "No relevant memories found."}
	}
	memoryIDs := make([]string, 0, len(out.Results))
	for _, r := range out.Results {
		if id := strings.TrimSpace(r.ID); id != "" {
			memoryIDs = append(memoryIDs, id)
		}
	}
	sessionID, _ := args["_session_id"].(string)
	if t.onMemories != nil && strings.TrimSpace(sessionID) != "" && len(memoryIDs) > 0 {
		t.onMemories(sessionID, memoryIDs)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d relevant memories:\n\n", len(out.Results))
	for i, r := range out.Results {
		fmt.Fprintf(&b, "%d. [%.2f] %s\n", i+1, r.Similarity, strings.TrimSpace(r.Content))
	}
	return toolpkg.ToolResult{Output: b.String()}
}

type rememberTool struct {
	memoryURL  string
	agentName  string
	collection string
}

func (t *rememberTool) Name() string { return "remember" }
func (t *rememberTool) Description() string {
	return "Store important information for future reference. Use this to save decisions, code patterns, lessons learned, or task completions."
}
func (t *rememberTool) Parameters() map[string]string {
	return map[string]string{
		"content": "The information to remember",
		"tags":    "Comma-separated tags for categorization (optional)",
	}
}
func (t *rememberTool) Execute(args map[string]interface{}) toolpkg.ToolResult {
	content, _ := args["content"].(string)
	if strings.TrimSpace(content) == "" {
		return toolpkg.ToolResult{Error: "content is required"}
	}
	tagsRaw, _ := args["tags"].(string)
	tags := make([]string, 0)
	for _, tag := range strings.Split(tagsRaw, ",") {
		tag = strings.TrimSpace(tag)
		if tag != "" {
			tags = append(tags, tag)
		}
	}
	payload := map[string]interface{}{
		"agent":   t.agentName,
		"content": content,
		"tags":    tags,
	}
	if strings.TrimSpace(t.collection) != "" {
		payload["collection"] = t.collection
		payload["metadata"] = map[string]interface{}{
			"source":     "hunter",
			"quarantine": true,
			"hunter_id":  t.collection,
		}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(t.memoryURL, "/")+"/remember", bytes.NewReader(body))
	if err != nil {
		return toolpkg.ToolResult{Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return toolpkg.ToolResult{Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return toolpkg.ToolResult{Error: strings.TrimSpace(string(b))}
	}
	return toolpkg.ToolResult{Output: "Remembered."}
}

type memoryEditTool struct {
	memoryURL string
}

func (t *memoryEditTool) Name() string { return "memory-edit" }
func (t *memoryEditTool) Description() string {
	return "Edit your Seidr memories. Actions: update, delete, promote, reclassify."
}
func (t *memoryEditTool) Parameters() map[string]string {
	return map[string]string{
		"action":     "Memory action: update, delete, promote, or reclassify",
		"memory_id":  "Memory ID to modify",
		"content":    "Updated memory content for action=update (optional)",
		"type":       "Memory type for action=update or action=reclassify (general, failure, recovery, lesson, fact, observation)",
		"importance": "Importance value 0.0-1.0 for action=update (optional)",
	}
}
func (t *memoryEditTool) Execute(args map[string]interface{}) toolpkg.ToolResult {
	action := strings.TrimSpace(fmt.Sprint(args["action"]))
	memoryID := strings.TrimSpace(fmt.Sprint(args["memory_id"]))
	if action == "" {
		return toolpkg.ToolResult{Output: t.usage()}
	}
	switch action {
	case "update":
		if memoryID == "" || memoryID == "<nil>" {
			return toolpkg.ToolResult{Output: t.usage()}
		}
		payload := map[string]interface{}{}
		if content := strings.TrimSpace(fmt.Sprint(args["content"])); content != "" && content != "<nil>" {
			payload["content"] = content
		}
		if memoryType := strings.TrimSpace(fmt.Sprint(args["type"])); memoryType != "" && memoryType != "<nil>" {
			payload["type"] = memoryType
		}
		if importanceRaw := strings.TrimSpace(fmt.Sprint(args["importance"])); importanceRaw != "" && importanceRaw != "<nil>" {
			importance, err := strconv.ParseFloat(importanceRaw, 64)
			if err != nil {
				return toolpkg.ToolResult{Error: "importance must be a float between 0.0 and 1.0"}
			}
			payload["importance"] = importance
		}
		if len(payload) == 0 {
			return toolpkg.ToolResult{Output: t.usage()}
		}
		return t.patchMemory(memoryID, payload, "Memory updated.")
	case "delete":
		if memoryID == "" || memoryID == "<nil>" {
			return toolpkg.ToolResult{Output: t.usage()}
		}
		req, err := http.NewRequest(http.MethodDelete, strings.TrimRight(t.memoryURL, "/")+"/memories/"+url.PathEscape(memoryID), nil)
		if err != nil {
			return toolpkg.ToolResult{Error: err.Error()}
		}
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return toolpkg.ToolResult{Error: err.Error()}
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			return toolpkg.ToolResult{Error: strings.TrimSpace(string(b))}
		}
		return toolpkg.ToolResult{Output: "Memory deleted: " + memoryID}
	case "promote":
		if memoryID == "" || memoryID == "<nil>" {
			return toolpkg.ToolResult{Output: t.usage()}
		}
		return t.patchMemory(memoryID, map[string]interface{}{"layer": "soul_candidate"}, "Memory promoted to soul_candidate.")
	case "reclassify":
		if memoryID == "" || memoryID == "<nil>" {
			return toolpkg.ToolResult{Output: t.usage()}
		}
		memoryType := strings.TrimSpace(fmt.Sprint(args["type"]))
		if memoryType == "" || memoryType == "<nil>" {
			return toolpkg.ToolResult{Output: t.usage()}
		}
		return t.patchMemory(memoryID, map[string]interface{}{"type": memoryType}, "Memory reclassified.")
	default:
		return toolpkg.ToolResult{Output: t.usage()}
	}
}

func (t *memoryEditTool) patchMemory(memoryID string, payload map[string]interface{}, successPrefix string) toolpkg.ToolResult {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPatch, strings.TrimRight(t.memoryURL, "/")+"/memories/"+url.PathEscape(memoryID), bytes.NewReader(body))
	if err != nil {
		return toolpkg.ToolResult{Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return toolpkg.ToolResult{Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return toolpkg.ToolResult{Error: strings.TrimSpace(string(b))}
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if strings.TrimSpace(string(respBody)) == "" {
		return toolpkg.ToolResult{Output: successPrefix + " " + memoryID}
	}
	return toolpkg.ToolResult{Output: successPrefix + "\n" + strings.TrimSpace(string(respBody))}
}

func (t *memoryEditTool) usage() string {
	return strings.TrimSpace(`memory-edit usage:
- update: action=update memory_id=<id> [content="..."] [type=general|failure|recovery|lesson|fact|observation] [importance=0.0-1.0]
- delete: action=delete memory_id=<id>
- promote: action=promote memory_id=<id>
- reclassify: action=reclassify memory_id=<id> type=general|failure|recovery|lesson|fact|observation`)
}

func buildToolDefs(reg *toolpkg.Registry) []toolDef {
	names := reg.List()
	defs := make([]toolDef, 0, len(names))
	for _, name := range names {
		t, ok := reg.Get(name)
		if !ok {
			continue
		}
		params := t.Parameters()
		required := make([]string, 0, len(params))
		for k := range params {
			if t.Name() == "remember" && k == "tags" {
				continue
			}
			required = append(required, k)
		}
		sort.Strings(required)
		props := make(map[string]toolProperty, len(required))
		for _, k := range required {
			props[k] = toolProperty{Type: "string", Description: params[k]}
		}
		defs = append(defs, toolDef{Type: "function", Function: toolFunctionDef{
			Name: t.Name(), Description: t.Description(),
			Parameters: toolParameters{Type: "object", Properties: props, Required: required},
		}})
	}
	return defs
}

func convertToolDefsForResponses(defs []toolDef) []responsesToolDef {
	out := make([]responsesToolDef, len(defs))
	for i, d := range defs {
		out[i] = responsesToolDef{
			Type:        d.Type,
			Name:        d.Function.Name,
			Description: d.Function.Description,
			Parameters:  d.Function.Parameters,
		}
	}
	return out
}

func convertMessagesForResponses(msgs []message) []interface{} {
	out := make([]interface{}, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			if strings.TrimSpace(m.Content) != "" {
				out = append(out, map[string]string{"role": "assistant", "content": m.Content})
			}
			for _, tc := range m.ToolCalls {
				out = append(out, map[string]string{
					"type":      "function_call",
					"call_id":   tc.ID,
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				})
			}
			continue
		}
		if m.Role == "tool" {
			out = append(out, map[string]string{
				"type":    "function_call_output",
				"call_id": m.ToolCallID,
				"output":  m.Content,
			})
			continue
		}
		out = append(out, map[string]string{"role": m.Role, "content": m.Content})
	}
	return out
}

func convertMessagesForAnthropic(msgs []message) (systemPrompt interface{}, converted []anthropicMessage) {
	systemParts := []string{}
	out := make([]anthropicMessage, 0, len(msgs))
	appendToolResult := func(m message) {
		block := anthropicContentBlock{
			Type:      "tool_result",
			ToolUseID: strings.TrimSpace(m.ToolCallID),
			Content:   m.Content,
		}
		if len(out) > 0 && out[len(out)-1].Role == "user" {
			if blocks, ok := out[len(out)-1].Content.([]anthropicContentBlock); ok {
				out[len(out)-1].Content = append(blocks, block)
				return
			}
		}
		out = append(out, anthropicMessage{
			Role:    "user",
			Content: []anthropicContentBlock{block},
		})
	}

	for _, m := range msgs {
		switch m.Role {
		case "system":
			if text := strings.TrimSpace(m.Content); text != "" {
				systemParts = append(systemParts, text)
			}
		case "user":
			out = append(out, anthropicMessage{Role: "user", Content: m.Content})
		case "assistant":
			if len(m.ToolCalls) == 0 {
				out = append(out, anthropicMessage{Role: "assistant", Content: m.Content})
				continue
			}
			blocks := []anthropicContentBlock{}
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				argText := strings.TrimSpace(tc.Function.Arguments)
				argRaw := json.RawMessage("{}")
				if argText != "" && json.Valid([]byte(argText)) {
					argRaw = json.RawMessage(argText)
				}
				blocks = append(blocks, anthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: argRaw,
				})
			}
			out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
		case "tool":
			appendToolResult(m)
		}
	}
	if len(out) == 0 {
		out = append(out, anthropicMessage{Role: "user", Content: "Hello"})
	}
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role != "user" {
			continue
		}
		if text, ok := out[i].Content.(string); ok {
			out[i].Content = []map[string]interface{}{{
				"type": "text",
				"text": text,
				"cache_control": map[string]string{
					"type": "ephemeral",
				},
			}}
		}
		break
	}
	systemText := strings.Join(systemParts, "\n")
	if strings.TrimSpace(systemText) != "" {
		systemPrompt = []map[string]interface{}{{
			"type": "text",
			"text": systemText,
			"cache_control": map[string]string{
				"type": "ephemeral",
			},
		}}
	}
	return systemPrompt, out
}

func convertToolDefsForAnthropic(defs []toolDef) []anthropicTool {
	out := make([]anthropicTool, len(defs))
	for i, d := range defs {
		out[i] = anthropicTool{
			Name:        d.Function.Name,
			Description: d.Function.Description,
			InputSchema: d.Function.Parameters,
		}
	}
	return out
}

type toolSetupDeps struct {
	workspace           string
	giteaURL            string
	giteaToken          string
	agentName           string
	peers               map[string]string
	maxDelegationTokens int
	memoryURL           string
	memoryToolsEnabled  bool
	mcpServers          string
	reviewTracker       *reviewContextTracker
	enabled             map[string]bool
	delegationGates     []string
}

func configureToolRegistry(reg *toolpkg.Registry, deps toolSetupDeps) (*toolpkg.GiteaAPITool, []toolDef) {
	var giteaTool *toolpkg.GiteaAPITool
	delegateExec := &delegateTool{
		peers:               deps.peers,
		agentName:           deps.agentName,
		giteaURL:            deps.giteaURL,
		maxDelegationTokens: deps.maxDelegationTokens,
		gates:               deps.delegationGates,
	}
	broadcastExec := &broadcastTool{peers: deps.peers}
	taskStatusExec := &taskStatusTool{peers: deps.peers}
	delegateExecValue = delegateExec
	broadcastExecValue = broadcastExec
	recallExec := &recallTool{
		memoryURL: deps.memoryURL,
		agentName: deps.agentName,
		onMemories: func(sessionID string, ids []string) {
			addSessionContextMemoryIDs(sessionID, ids)
		},
	}
	rememberExec := &rememberTool{memoryURL: deps.memoryURL, agentName: deps.agentName}
	memoryEditExec := &memoryEditTool{memoryURL: deps.memoryURL}
	if deps.enabled["exec"] {
		reg.Register(toolpkg.NewExecTool())
	}
	if deps.enabled["http"] {
		reg.Register(toolpkg.NewHTTPTool())
	}
	if deps.enabled["read"] {
		reg.Register(toolpkg.NewReadTool(deps.workspace))
	}
	if deps.enabled["write"] {
		reg.Register(toolpkg.NewWriteTool(deps.workspace))
	}
	if deps.enabled["edit"] {
		reg.Register(toolpkg.NewEditTool(deps.workspace))
	}
	if deps.enabled["git-clone"] || deps.enabled["git-commit"] || deps.enabled["git-diff"] || deps.enabled["gitea"] || deps.enabled["parallel-build"] {
		if deps.enabled["git-clone"] {
			reg.Register(toolpkg.NewGitCloneTool(deps.workspace, deps.giteaURL, deps.giteaToken, deps.agentName))
		}
		if deps.enabled["git-commit"] {
			reg.Register(toolpkg.NewGitCommitTool(deps.workspace, deps.giteaURL, deps.giteaToken, deps.agentName))
		}
		if deps.enabled["git-diff"] {
			reg.Register(toolpkg.NewGitDiffTool(deps.workspace))
		}
		if deps.enabled["gitea"] {
			giteaTool = toolpkg.NewGiteaAPITool(deps.giteaURL, deps.giteaToken)
			deps.reviewTracker.SetPRLookup(func(ref *prRef) bool {
				if ref == nil || giteaTool == nil {
					return false
				}
				req, err := http.NewRequest(http.MethodGet, strings.TrimRight(giteaTool.GiteaURL, "/")+"/api/v1/repos/"+url.PathEscape(ref.Owner)+"/"+url.PathEscape(ref.Repo)+"/pulls/"+strconv.Itoa(ref.Index), nil)
				if err != nil {
					return false
				}
				if strings.TrimSpace(giteaTool.Token) != "" {
					req.Header.Set("Authorization", "token "+strings.TrimSpace(giteaTool.Token))
				}
				resp, err := giteaTool.Client.Do(req)
				if err != nil {
					return false
				}
				defer resp.Body.Close()
				return resp.StatusCode == http.StatusOK
			})
			reg.Register(toolpkg.NewCreateIssueTool(giteaTool))
			reg.Register(toolpkg.NewCreatePRTool(giteaTool))
			reg.Register(toolpkg.NewListIssuesTool(giteaTool))
			reg.Register(toolpkg.NewCloseIssueTool(giteaTool))
			reg.Register(toolpkg.NewCommentTool(giteaTool))
			reg.Register(toolpkg.NewCreateReviewTool(giteaTool))
			reg.Register(toolpkg.NewMergePRTool(giteaTool))
			reg.Register(toolpkg.NewListPRFilesTool(giteaTool))
			reg.Register(toolpkg.NewUpdateLabelsTool(giteaTool))
			reg.Register(toolpkg.NewGetIssueTool(giteaTool))
			reg.Register(toolpkg.NewListBranchesTool(giteaTool))
		}
		if deps.enabled["parallel-build"] {
			reg.Register(toolpkg.NewParallelBuildTool(deps.workspace))
		}
	}
	if deps.enabled["delegate"] || len(deps.peers) > 0 {
		reg.Register(delegateExec)
		reg.Register(taskStatusExec)
	}
	if deps.enabled["broadcast"] || len(deps.peers) > 0 {
		reg.Register(broadcastExec)
	}
	if deps.memoryToolsEnabled && strings.TrimSpace(deps.memoryURL) != "" {
		reg.Register(recallExec)
		reg.Register(rememberExec)
		reg.Register(memoryEditExec)
	}
	if deps.mcpServers != "" {
		for _, serverURL := range strings.Split(deps.mcpServers, ",") {
			serverURL = strings.TrimSpace(serverURL)
			if serverURL == "" {
				continue
			}
			logJSON("info", "connecting to MCP server", map[string]interface{}{"url": serverURL})
			client := mcppkg.NewClientWithAgent(serverURL, deps.agentName)
			if err := client.Initialize(); err != nil {
				logJSON("warn", "MCP server unreachable, skipping", map[string]interface{}{"url": serverURL, "error": err.Error()})
				continue
			}
			tools, err := client.ListTools()
			if err != nil {
				logJSON("warn", "MCP tool discovery failed, skipping", map[string]interface{}{"url": serverURL, "error": err.Error()})
				continue
			}
			for _, td := range tools {
				mcpTool := mcppkg.NewMCPTool(client, td)
				if _, exists := reg.Get(td.Name); exists {
					logJSON("warn", "MCP tool name conflicts with native tool, skipping", map[string]interface{}{"tool": td.Name, "server": serverURL})
					continue
				}
				reg.Register(mcpTool)
				logJSON("info", "registered MCP tool", map[string]interface{}{"tool": td.Name, "server": serverURL})
			}
		}
	}
	return giteaTool, buildToolDefs(reg)
}
