package main

// Build: force rebuild for git-clone credential fix

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mcppkg "github.com/kitporath/project_valhalla/pkg/mcp"
	tasklifepkg "github.com/kitporath/project_valhalla/pkg/tasklife"
	taskspkg "github.com/kitporath/project_valhalla/pkg/tasks"
	toolpkg "github.com/kitporath/project_valhalla/pkg/tools"
)

const dashboardHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Valhalla Dashboard</title>
  <style>
    :root { color-scheme: dark; }
    * { box-sizing: border-box; }
    body {
      margin: 0; min-height: 100vh; background: #1a1a2e; color: #e0e0e0;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      display: flex; justify-content: center;
    }
    .app { width: 100%; max-width: 800px; height: 100vh; display: flex; flex-direction: column; padding: 16px; gap: 12px; }
    .header {
      background: #22223b; border: 1px solid #33344d; border-radius: 12px;
      padding: 12px 14px; display: flex; align-items: center; justify-content: space-between;
    }
    .title { font-size: 20px; font-weight: 700; letter-spacing: 0.2px; }
    .status { display: flex; align-items: center; gap: 8px; color: #b7ffd0; font-size: 14px; }
    .dot { width: 10px; height: 10px; border-radius: 50%; background: #2bd576; box-shadow: 0 0 10px #2bd576; }
    .messages {
      flex: 1; overflow-y: auto; background: #1f1f33; border: 1px solid #31324a;
      border-radius: 12px; padding: 14px; display: flex; flex-direction: column; gap: 10px;
    }
    .msg { max-width: 85%; padding: 10px 12px; border-radius: 12px; white-space: pre-wrap; line-height: 1.4; }
    .msg.user { align-self: flex-end; background: #0d6efd; color: #fff; border-bottom-right-radius: 6px; }
    .msg.assistant { align-self: flex-start; background: #2d2d44; color: #e0e0e0; border-bottom-left-radius: 6px; }
    .tools { display: flex; flex-direction: column; gap: 8px; margin-top: 6px; }
    details.toolbox {
      background: #1a1b2c; border: 1px solid #363856; border-radius: 10px; padding: 6px 10px;
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    }
    details.toolbox summary { cursor: pointer; color: #cfd6ff; outline: none; }
    details.toolbox pre {
      margin: 8px 0 0; background: #10111f; border: 1px solid #2b2d45; border-radius: 8px;
      padding: 10px; overflow-x: auto; color: #cdd2ef;
    }
    .inputbar {
      display: flex; gap: 10px; background: #22223b; border: 1px solid #33344d;
      border-radius: 12px; padding: 10px;
    }
    input[type="text"] {
      flex: 1; border: 1px solid #474a70; border-radius: 10px; background: #141526; color: #e0e0e0;
      padding: 12px 14px; font-size: 15px;
    }
    button {
      border: 0; border-radius: 10px; background: #0d6efd; color: #fff; padding: 12px 16px;
      font-weight: 600; cursor: pointer;
    }
    button:disabled { opacity: 0.55; cursor: not-allowed; }
  </style>
</head>
<body>
  <div class="app">
    <div class="header">
      <div class="title">⚔️ Valhalla</div>
      <div class="status"><span class="dot"></span><span>connected</span></div>
    </div>
    <div id="messages" class="messages"></div>
    <div class="inputbar">
      <input id="input" type="text" placeholder="Ask Valhalla..." />
      <button id="send">Send</button>
    </div>
  </div>
  <script>
    const messagesEl = document.getElementById("messages");
    const inputEl = document.getElementById("input");
    const sendEl = document.getElementById("send");
    const sessionId = Math.floor(Math.random() * Number.MAX_SAFE_INTEGER).toString(16);
    let streaming = false;
    let currentAssistant = null;
    const pendingTools = {};

    function scrollBottom() { messagesEl.scrollTop = messagesEl.scrollHeight; }
    function appendBubble(text, role) {
      const el = document.createElement("div");
      el.className = "msg " + role;
      el.textContent = text || "";
      messagesEl.appendChild(el);
      scrollBottom();
      return el;
    }
    function parseSSEBlock(block) {
      const lines = block.split("\n");
      let data = "";
      for (const line of lines) {
        if (line.startsWith("data:")) data += line.slice(5).trimStart();
      }
      if (!data) return null;
      try { return JSON.parse(data); } catch { return null; }
    }
    function toolHeader(tool, args) {
      let preview = "";
      if (args && typeof args === "object" && "command" in args) preview = String(args.command);
      else if (args !== undefined) preview = JSON.stringify(args);
      return "🔨 " + tool + (preview ? ": " + preview : "");
    }
    function addToolCall(tool, args) {
      if (!currentAssistant) currentAssistant = appendBubble("", "assistant");
      let wrap = currentAssistant.querySelector(".tools");
      if (!wrap) {
        wrap = document.createElement("div");
        wrap.className = "tools";
        currentAssistant.appendChild(wrap);
      }
      const details = document.createElement("details");
      details.className = "toolbox";
      details.open = true;
      const summary = document.createElement("summary");
      summary.textContent = toolHeader(tool, args);
      const pre = document.createElement("pre");
      pre.textContent = "running...";
      details.append(summary, pre);
      wrap.appendChild(details);
      if (!pendingTools[tool]) pendingTools[tool] = [];
      pendingTools[tool].push(pre);
      scrollBottom();
    }
    function setToolResult(tool, result) {
      const queue = pendingTools[tool] || [];
      const pre = queue.shift();
      if (!pre) return;
      const out = result && result.output ? String(result.output) : "";
      const err = result && result.error ? String(result.error) : "";
      pre.textContent = err ? (out ? (err + "\n" + out) : err) : out;
      scrollBottom();
    }

    async function sendMessage() {
      const text = inputEl.value.trim();
      if (!text || streaming) return;
      streaming = true;
      sendEl.disabled = true;
      inputEl.disabled = true;
      appendBubble(text, "user");
      currentAssistant = appendBubble("", "assistant");
      inputEl.value = "";

      try {
        const resp = await fetch("/message", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ content: text, session_id: sessionId })
        });
        if (!resp.ok || !resp.body) throw new Error("Request failed");
        const reader = resp.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        while (true) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          for (;;) {
            const idx = buffer.indexOf("\n\n");
            if (idx < 0) break;
            const block = buffer.slice(0, idx);
            buffer = buffer.slice(idx + 2);
            const evt = parseSSEBlock(block);
            if (!evt) continue;
            if (evt.type === "content" && evt.content !== undefined) {
              currentAssistant.textContent += evt.content;
            } else if (evt.type === "tool_call") {
              addToolCall(evt.tool || "tool", evt.args);
            } else if (evt.type === "tool_result") {
              setToolResult(evt.tool || "tool", evt.result || {});
            } else if (evt.type === "done") {
              streaming = false;
              sendEl.disabled = false;
              inputEl.disabled = false;
              inputEl.focus();
            }
            scrollBottom();
          }
        }
      } catch (err) {
        appendBubble("Error: " + (err && err.message ? err.message : String(err)), "assistant");
      } finally {
        streaming = false;
        sendEl.disabled = false;
        inputEl.disabled = false;
        inputEl.focus();
      }
    }

    sendEl.addEventListener("click", sendMessage);
    inputEl.addEventListener("keydown", (e) => {
      if (e.key === "Enter") { e.preventDefault(); sendMessage(); }
    });
    inputEl.focus();
  </script>
</body>
</html>`

type (
	ToolResult       = toolpkg.ToolResult
	toolCallFunction struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	toolCall struct {
		ID       string           `json:"id"`
		Type     string           `json:"type"`
		Function toolCallFunction `json:"function"`
	}
	message struct {
		Role       string     `json:"role"`
		Content    string     `json:"content"`
		ToolCalls  []toolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
	}
	toolProperty struct {
		Type        string `json:"type"`
		Description string `json:"description"`
	}
	toolParameters struct {
		Type       string                  `json:"type"`
		Properties map[string]toolProperty `json:"properties"`
		Required   []string                `json:"required"`
	}
	toolFunctionDef struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  toolParameters `json:"parameters"`
	}
	toolDef struct {
		Type     string          `json:"type"`
		Function toolFunctionDef `json:"function"`
	}
	chatRequest struct {
		Model    string    `json:"model"`
		Messages []message `json:"messages"`
		Stream   bool      `json:"stream"`
		Tools    []toolDef `json:"tools,omitempty"`
	}
	responsesRequest struct {
		Model  string             `json:"model"`
		Input  []interface{}      `json:"input"`
		Stream bool               `json:"stream"`
		Tools  []responsesToolDef `json:"tools,omitempty"`
	}
	responsesToolDef struct {
		Type        string      `json:"type"`
		Name        string      `json:"name"`
		Description string      `json:"description"`
		Parameters  interface{} `json:"parameters,omitempty"`
	}
	anthropicRequest struct {
		Model     string             `json:"model"`
		MaxTokens int                `json:"max_tokens"`
		System    interface{}        `json:"system,omitempty"`
		Messages  []anthropicMessage `json:"messages"`
		Stream    bool               `json:"stream"`
		Tools     []anthropicTool    `json:"tools,omitempty"`
	}
	anthropicMessage struct {
		Role    string      `json:"role"`
		Content interface{} `json:"content"`
	}
	anthropicContentBlock struct {
		Type      string      `json:"type"`
		Text      string      `json:"text,omitempty"`
		ID        string      `json:"id,omitempty"`
		Name      string      `json:"name,omitempty"`
		Input     interface{} `json:"input,omitempty"`
		ToolUseID string      `json:"tool_use_id,omitempty"`
		Content   string      `json:"content,omitempty"`
	}
	anthropicTool struct {
		Name        string      `json:"name"`
		Description string      `json:"description"`
		InputSchema interface{} `json:"input_schema"`
	}
	anthropicResponse struct {
		Content    []anthropicResponseBlock `json:"content"`
		StopReason string                   `json:"stop_reason"`
	}
	anthropicResponseBlock struct {
		Type  string          `json:"type"`
		Text  string          `json:"text,omitempty"`
		ID    string          `json:"id,omitempty"`
		Name  string          `json:"name,omitempty"`
		Input json.RawMessage `json:"input,omitempty"`
	}
	anthropicStreamEvent struct {
		Type         string `json:"type"`
		Index        int    `json:"index"`
		ContentBlock *struct {
			Type  string          `json:"type"`
			ID    string          `json:"id,omitempty"`
			Name  string          `json:"name,omitempty"`
			Text  string          `json:"text,omitempty"`
			Input json.RawMessage `json:"input,omitempty"`
		} `json:"content_block,omitempty"`
		Delta *struct {
			Type        string `json:"type"`
			Text        string `json:"text,omitempty"`
			PartialJSON string `json:"partial_json,omitempty"`
			StopReason  string `json:"stop_reason,omitempty"`
		} `json:"delta,omitempty"`
	}
	streamChunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	inferenceStreamEvent struct {
		Content   string
		ToolCalls []toolCall
		Err       error
	}
	chatResponse struct {
		Choices []struct {
			Message struct {
				Role      string     `json:"role"`
				Content   string     `json:"content"`
				ToolCalls []toolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	responsesOutputText struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	responsesOutputItem struct {
		ID        string                `json:"id"`
		Type      string                `json:"type"`
		Role      string                `json:"role"`
		Name      string                `json:"name"`
		CallID    string                `json:"call_id"`
		Arguments string                `json:"arguments"`
		Content   []responsesOutputText `json:"content"`
	}
	responsesResponse struct {
		Output []responsesOutputItem `json:"output"`
	}
	messageRequest struct {
		Content   string `json:"content"`
		SessionID string `json:"session_id"`
	}
	taskSendRequest struct {
		Content string `json:"content"`
		From    string `json:"from"`
	}
	sseChunk struct {
		Type      string      `json:"type"`
		Content   string      `json:"content,omitempty"`
		Tool      string      `json:"tool,omitempty"`
		Args      interface{} `json:"args,omitempty"`
		Result    interface{} `json:"result,omitempty"`
		Done      bool        `json:"done"`
		SessionID string      `json:"session_id,omitempty"`
	}
)

var sessionsMu sync.Mutex

type trackedTask struct {
	IssueNumber  int
	IssueOwner   string
	IssueRepo    string
	PRCreated    bool
	PRUrl        string
	ToolRetries  int
	FilesWritten []string
	BranchName   string
}

var sessions = map[string][]message{}
var seenSessions = map[string]bool{}
var skillNudgeInjected = map[string]bool{}
var sessionContextMemoryIDs = map[string]map[string]struct{}{}
var sessionValidatedIDs = map[string]map[string]bool{}
var episodicStates = map[string]string{} // sessionID -> loaded narrative
var trackedTasks = map[string]*trackedTask{}
var (
	startTime         = time.Now()
	requestCount      int64
	toolCalls         int64
	modelName         string
	enabledTools      []string
	thinkTagRE        = regexp.MustCompile(`(?s)<think>.*?</think>`)
	orphanThinkRe     = regexp.MustCompile(`</?think>`)
	xmlToolCallRe     = regexp.MustCompile(`(?s)<minimax:tool_call>\s*<invoke\s*name="([^"]+)">(.*?)</invoke>\s*</minimax:tool_call>`)
	xmlParamRe        = regexp.MustCompile(`<parameter name="([^"]+)">([^<]*)</parameter>`)
	minimaxToolCallRE = regexp.MustCompile(`(?s)<minimax:tool_call>(.*?)</minimax:tool_call>`)

	metricsRequestsTotal      int64
	metricsToolCallsTotal     int64
	metricsErrorsTotal        int64
	metricsStallsTotal        int64
	metricsActiveRequests     int64
	metricsLastRequestDurBits uint64
	toolMetricMu              sync.Mutex
	metricsToolCallsByTool    = map[string]*int64{}
	selfImproveMu             sync.Mutex
	selfImproveLastRun        = map[string]time.Time{}
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
	resp, err := sendPeerAgentTask(agent, peerURL, t.agentName, formatted)
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

func sendPeerAgentTask(agentName, peerURL, from, task string) (string, error) {
	logJSON("info", "delegating", map[string]interface{}{"target_agent": agentName})
	body, _ := json.Marshal(taskSendRequest{
		Content: task,
		From:    from,
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
			resp, err := sendPeerAgentTask(peerName, t.peers[peerName], "broadcast", task)
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

func logJSON(level, msg string, fields map[string]interface{}) {
	entry := map[string]interface{}{
		"ts":    time.Now().UTC().Format(time.RFC3339),
		"level": level,
		"msg":   msg,
	}
	for k, v := range fields {
		entry[k] = v
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	b = append(b, '\n')
	_, _ = os.Stdout.Write(b)
}

func incError(msg string, err error, fields map[string]interface{}) {
	atomic.AddInt64(&metricsErrorsTotal, 1)
	if fields == nil {
		fields = map[string]interface{}{}
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	logJSON("error", msg, fields)
}

func stripThinkTags(s string) string {
	s = thinkTagRE.ReplaceAllString(s, "")
	s = orphanThinkRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

func parseMiniMaxToolCalls(content string) ([]toolCall, string) {
	matches := minimaxToolCallRE.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil, content
	}
	invokeRE := regexp.MustCompile(`(?s)<invoke\s*name="([^"]+)">`)
	paramRE := regexp.MustCompile(`(?s)<parameter\s+name="([^"]+)">(.*?)</parameter>`)
	calls := make([]toolCall, 0, len(matches))
	for i, m := range matches {
		block := m[1]
		invoke := invokeRE.FindStringSubmatch(block)
		if len(invoke) < 2 {
			continue
		}
		name := strings.TrimSpace(invoke[1])
		if name == "" {
			continue
		}
		args := map[string]string{}
		for _, pm := range paramRE.FindAllStringSubmatch(block, -1) {
			if len(pm) < 3 {
				continue
			}
			k := strings.TrimSpace(pm[1])
			if k == "" {
				continue
			}
			args[k] = strings.TrimSpace(pm[2])
		}
		b, err := json.Marshal(args)
		if err != nil {
			continue
		}
		calls = append(calls, toolCall{
			ID:   fmt.Sprintf("mm_%d", i),
			Type: "function",
			Function: toolCallFunction{
				Name:      name,
				Arguments: string(b),
			},
		})
	}
	if len(calls) == 0 {
		return nil, content
	}
	cleaned := minimaxToolCallRE.ReplaceAllString(content, "")
	return calls, cleaned
}

func extractAndExecuteXMLToolCalls(content string, execute func(toolCall) ToolResult) (cleanedContent string, toolResults []ToolResult) {
	matches := xmlToolCallRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return content, nil
	}
	calls := make([]toolCall, 0, len(matches))
	for i, m := range matches {
		if len(m) < 3 {
			return content, nil
		}
		toolName := strings.TrimSpace(m[1])
		if toolName == "" {
			return content, nil
		}
		rawParams := m[2]
		args := map[string]string{}
		for _, pm := range xmlParamRe.FindAllStringSubmatch(rawParams, -1) {
			if len(pm) < 3 {
				continue
			}
			key := strings.TrimSpace(pm[1])
			if key == "" {
				continue
			}
			args[key] = strings.TrimSpace(pm[2])
		}
		argBytes, err := json.Marshal(args)
		if err != nil {
			return content, nil
		}
		calls = append(calls, toolCall{
			ID:   fmt.Sprintf("xml_%d", i),
			Type: "function",
			Function: toolCallFunction{
				Name:      toolName,
				Arguments: string(argBytes),
			},
		})
	}
	cleaned := xmlToolCallRe.ReplaceAllString(content, "")
	results := make([]ToolResult, 0, len(calls))
	for _, tc := range calls {
		results = append(results, execute(tc))
	}
	return cleaned, results
}

func setLastDuration(d time.Duration) {
	atomic.StoreUint64(&metricsLastRequestDurBits, math.Float64bits(d.Seconds()))
}

func getLastDuration() float64 {
	return math.Float64frombits(atomic.LoadUint64(&metricsLastRequestDurBits))
}

func incToolMetric(tool string) {
	toolMetricMu.Lock()
	ptr, ok := metricsToolCallsByTool[tool]
	if !ok {
		ptr = new(int64)
		metricsToolCallsByTool[tool] = ptr
	}
	toolMetricMu.Unlock()
	atomic.AddInt64(ptr, 1)
}

func snapshotToolMetrics() map[string]int64 {
	toolMetricMu.Lock()
	defer toolMetricMu.Unlock()
	out := make(map[string]int64, len(metricsToolCallsByTool))
	for k, v := range metricsToolCallsByTool {
		out[k] = atomic.LoadInt64(v)
	}
	return out
}

type fileInfo struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

func die(msg string, err error) {
	incError(msg, err, nil)
	os.Exit(1)
}

func parsePeers(raw string) (map[string]string, error) {
	peers := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return peers, nil
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, url, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(url) == "" {
			return nil, fmt.Errorf("invalid peer entry %q", part)
		}
		name = strings.TrimSpace(name)
		if _, exists := peers[name]; exists {
			return nil, fmt.Errorf("duplicate peer name %q", name)
		}
		peers[name] = strings.TrimRight(strings.TrimSpace(url), "/")
	}
	return peers, nil
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

// useResponsesAPI currently only matches model names that contain "codex".
// That means models such as "o3" or "o4-mini" still bypass this logic even though
// they should also use the Responses API.
func useResponsesAPI(model string) bool {
	return strings.Contains(strings.ToLower(model), "codex")
}

// useAnthropicAPI returns true for model names containing "claude".
func useAnthropicAPI(model string) bool {
	return strings.Contains(strings.ToLower(model), "claude")
}

func callOllamaNonStreaming(messages []message, defs []toolDef, inferenceURL, model, apiKey string) (chatResponse, error) {
	return callOllamaNonStreamingWithContext(context.Background(), messages, defs, inferenceURL, model, apiKey)
}

func callOllamaNonStreamingWithContext(ctx context.Context, messages []message, defs []toolDef, inferenceURL, model, apiKey string) (chatResponse, error) {
	if useAnthropicAPI(model) {
		return callAnthropicNonStreamingWithContext(ctx, messages, defs, inferenceURL, model, apiKey)
	}
	if useResponsesAPI(model) {
		return callResponsesNonStreamingWithContext(ctx, messages, defs, inferenceURL, model, apiKey)
	}
	return callChatCompletionsNonStreamingWithContext(ctx, messages, defs, inferenceURL, model, apiKey)
}

func callChatCompletionsNonStreamingWithContext(ctx context.Context, messages []message, defs []toolDef, inferenceURL, model, apiKey string) (chatResponse, error) {
	endpoint := strings.TrimRight(inferenceURL, "/") + "/v1/chat/completions"
	body, err := json.Marshal(chatRequest{Model: model, Messages: messages, Stream: false, Tools: defs})
	if err != nil {
		return chatResponse{}, fmt.Errorf("failed to marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return chatResponse{}, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return chatResponse{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return chatResponse{}, fmt.Errorf("inference returned %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return chatResponse{}, fmt.Errorf("failed to parse response: %w", err)
	}
	if len(out.Choices) > 0 {
		out.Choices[0].Message.Content = stripThinkTags(out.Choices[0].Message.Content)
	}
	return out, nil
}

func callResponsesNonStreamingWithContext(ctx context.Context, messages []message, defs []toolDef, inferenceURL, model, apiKey string) (chatResponse, error) {
	endpoint := strings.TrimRight(inferenceURL, "/") + "/v1/responses"
	body, err := json.Marshal(responsesRequest{Model: model, Input: convertMessagesForResponses(messages), Stream: false, Tools: convertToolDefsForResponses(defs)})
	if err != nil {
		return chatResponse{}, fmt.Errorf("failed to marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return chatResponse{}, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return chatResponse{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return chatResponse{}, fmt.Errorf("inference returned %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out responsesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return chatResponse{}, fmt.Errorf("failed to parse response: %w", err)
	}
	var normalized chatResponse
	normalized.Choices = append(normalized.Choices, struct {
		Message struct {
			Role      string     `json:"role"`
			Content   string     `json:"content"`
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"message"`
	}{})
	msg := &normalized.Choices[0].Message
	msg.Role = "assistant"
	var content strings.Builder
	for i, item := range out.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" {
					content.WriteString(part.Text)
				}
			}
		case "function_call":
			callID := strings.TrimSpace(item.CallID)
			if callID == "" {
				callID = strings.TrimSpace(item.ID)
			}
			if callID == "" {
				callID = fmt.Sprintf("responses_%d", i)
			}
			msg.ToolCalls = append(msg.ToolCalls, toolCall{
				ID:   callID,
				Type: "function",
				Function: toolCallFunction{
					Name:      strings.TrimSpace(item.Name),
					Arguments: strings.TrimSpace(item.Arguments),
				},
			})
		}
	}
	msg.Content = stripThinkTags(content.String())
	return normalized, nil
}

func callAnthropicNonStreamingWithContext(ctx context.Context, messages []message, defs []toolDef, inferenceURL, model, apiKey string) (chatResponse, error) {
	endpoint := strings.TrimRight(inferenceURL, "/") + "/v1/messages"
	systemPrompt, anthropicMsgs := convertMessagesForAnthropic(messages)
	reqBody := anthropicRequest{
		Model:     model,
		MaxTokens: 16384,
		System:    systemPrompt,
		Messages:  anthropicMsgs,
		Stream:    false,
		Tools:     convertToolDefsForAnthropic(defs),
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return chatResponse{}, fmt.Errorf("failed to marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return chatResponse{}, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return chatResponse{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return chatResponse{}, fmt.Errorf("inference returned %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return chatResponse{}, fmt.Errorf("failed to parse response: %w", err)
	}
	var normalized chatResponse
	normalized.Choices = append(normalized.Choices, struct {
		Message struct {
			Role      string     `json:"role"`
			Content   string     `json:"content"`
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"message"`
	}{})
	msg := &normalized.Choices[0].Message
	msg.Role = "assistant"
	var contentBuf strings.Builder
	for i, block := range out.Content {
		switch block.Type {
		case "text":
			contentBuf.WriteString(block.Text)
		case "tool_use":
			argStr := "{}"
			if len(block.Input) > 0 {
				argStr = string(block.Input)
			}
			callID := strings.TrimSpace(block.ID)
			if callID == "" {
				callID = fmt.Sprintf("anthropic_%d", i)
			}
			msg.ToolCalls = append(msg.ToolCalls, toolCall{
				ID:   callID,
				Type: "function",
				Function: toolCallFunction{
					Name:      block.Name,
					Arguments: argStr,
				},
			})
		}
	}
	msg.Content = stripThinkTags(contentBuf.String())
	return normalized, nil
}

func streamOllama(messages []message, defs []toolDef, inferenceURL, model, apiKey string) <-chan inferenceStreamEvent {
	return streamOllamaWithContext(context.Background(), messages, defs, inferenceURL, model, apiKey)
}

func streamOllamaWithContext(ctx context.Context, messages []message, defs []toolDef, inferenceURL, model, apiKey string) <-chan inferenceStreamEvent {
	if useAnthropicAPI(model) {
		return streamAnthropicWithContext(ctx, messages, defs, inferenceURL, model, apiKey)
	}
	if useResponsesAPI(model) {
		return streamResponsesWithContext(ctx, messages, defs, inferenceURL, model, apiKey)
	}
	return streamChatCompletionsWithContext(ctx, messages, defs, inferenceURL, model, apiKey)
}

func streamChatCompletionsWithContext(ctx context.Context, messages []message, defs []toolDef, inferenceURL, model, apiKey string) <-chan inferenceStreamEvent {
	chunks := make(chan inferenceStreamEvent)
	go func() {
		defer close(chunks)
		endpoint := strings.TrimRight(inferenceURL, "/") + "/v1/chat/completions"
		body, err := json.Marshal(chatRequest{Model: model, Messages: messages, Stream: true, Tools: defs})
		if err != nil {
			chunks <- inferenceStreamEvent{Err: fmt.Errorf("failed to marshal request: %w", err)}
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			chunks <- inferenceStreamEvent{Err: fmt.Errorf("failed to create request: %w", err)}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			chunks <- inferenceStreamEvent{Err: fmt.Errorf("request failed: %w", err)}
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			chunks <- inferenceStreamEvent{Err: fmt.Errorf("inference returned %s: %s", resp.Status, strings.TrimSpace(string(b)))}
			return
		}
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err == io.EOF {
				break
			}
			if err != nil {
				chunks <- inferenceStreamEvent{Err: fmt.Errorf("failed reading stream: %w", err)}
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := line[6:]
			if payload == "[DONE]" {
				break
			}
			var chunk streamChunk
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				chunks <- inferenceStreamEvent{Err: fmt.Errorf("failed to parse chunk: %w", err)}
				return
			}
			if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
				chunks <- inferenceStreamEvent{Content: chunk.Choices[0].Delta.Content}
			}
		}
	}()
	return chunks
}

func streamResponsesWithContext(ctx context.Context, messages []message, defs []toolDef, inferenceURL, model, apiKey string) <-chan inferenceStreamEvent {
	chunks := make(chan inferenceStreamEvent)
	go func() {
		defer close(chunks)
		endpoint := strings.TrimRight(inferenceURL, "/") + "/v1/responses"
		body, err := json.Marshal(responsesRequest{Model: model, Input: convertMessagesForResponses(messages), Stream: true, Tools: convertToolDefsForResponses(defs)})
		if err != nil {
			chunks <- inferenceStreamEvent{Err: fmt.Errorf("failed to marshal request: %w", err)}
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			chunks <- inferenceStreamEvent{Err: fmt.Errorf("failed to create request: %w", err)}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			chunks <- inferenceStreamEvent{Err: fmt.Errorf("request failed: %w", err)}
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			chunks <- inferenceStreamEvent{Err: fmt.Errorf("inference returned %s: %s", resp.Status, strings.TrimSpace(string(b)))}
			return
		}
		type pendingCall struct {
			id   string
			name string
			args strings.Builder
		}
		pending := map[string]*pendingCall{}
		finalizePending := func() {
			keys := make([]string, 0, len(pending))
			for k := range pending {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				p := pending[k]
				if strings.TrimSpace(p.name) == "" {
					continue
				}
				argText := strings.TrimSpace(p.args.String())
				if argText == "" {
					argText = "{}"
				}
				chunks <- inferenceStreamEvent{ToolCalls: []toolCall{{
					ID:   p.id,
					Type: "function",
					Function: toolCallFunction{
						Name:      p.name,
						Arguments: argText,
					},
				}}}
			}
			clear(pending)
		}
		reader := bufio.NewReader(resp.Body)
		var eventName string
		var dataLines []string
		flush := func() error {
			if eventName == "" && len(dataLines) == 0 {
				return nil
			}
			data := strings.Join(dataLines, "\n")
			switch eventName {
			case "response.output_text.delta":
				var payload struct {
					Delta string `json:"delta"`
				}
				if err := json.Unmarshal([]byte(data), &payload); err != nil {
					return err
				}
				if payload.Delta != "" {
					chunks <- inferenceStreamEvent{Content: payload.Delta}
				}
			case "response.output_item.added", "response.output_item.done":
				var payload struct {
					Item responsesOutputItem `json:"item"`
				}
				if err := json.Unmarshal([]byte(data), &payload); err == nil {
					if payload.Item.Type == "function_call" {
						key := strings.TrimSpace(payload.Item.CallID)
						if key == "" {
							key = strings.TrimSpace(payload.Item.ID)
						}
						if key == "" {
							key = payload.Item.Name
						}
						pc := pending[key]
						if pc == nil {
							pc = &pendingCall{id: key}
							pending[key] = pc
						}
						if strings.TrimSpace(payload.Item.CallID) != "" {
							pc.id = strings.TrimSpace(payload.Item.CallID)
						}
						if strings.TrimSpace(payload.Item.Name) != "" {
							pc.name = strings.TrimSpace(payload.Item.Name)
						}
						if strings.TrimSpace(payload.Item.Arguments) != "" {
							pc.args.Reset()
							pc.args.WriteString(strings.TrimSpace(payload.Item.Arguments))
						}
					}
				}
			case "response.function_call_arguments.delta":
				var payload struct {
					Delta  string `json:"delta"`
					ItemID string `json:"item_id"`
					CallID string `json:"call_id"`
					Name   string `json:"name"`
				}
				if err := json.Unmarshal([]byte(data), &payload); err != nil {
					return err
				}
				key := strings.TrimSpace(payload.CallID)
				if key == "" {
					key = strings.TrimSpace(payload.ItemID)
				}
				if key == "" {
					key = strings.TrimSpace(payload.Name)
				}
				if key == "" {
					key = fmt.Sprintf("responses_%d", len(pending))
				}
				pc := pending[key]
				if pc == nil {
					pc = &pendingCall{id: key}
					pending[key] = pc
				}
				if strings.TrimSpace(payload.CallID) != "" {
					pc.id = strings.TrimSpace(payload.CallID)
				}
				if strings.TrimSpace(payload.Name) != "" {
					pc.name = strings.TrimSpace(payload.Name)
				}
				pc.args.WriteString(payload.Delta)
			case "response.function_call_arguments.done":
				finalizePending()
			case "response.completed":
				finalizePending()
			}
			eventName = ""
			dataLines = nil
			return nil
		}
		for {
			line, err := reader.ReadString('\n')
			if err == io.EOF {
				_ = flush()
				break
			}
			if err != nil {
				chunks <- inferenceStreamEvent{Err: fmt.Errorf("failed reading stream: %w", err)}
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if err := flush(); err != nil {
					chunks <- inferenceStreamEvent{Err: fmt.Errorf("failed to parse chunk: %w", err)}
					return
				}
				continue
			}
			if strings.HasPrefix(line, "event:") {
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
				continue
			}
			if strings.HasPrefix(line, "data:") {
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
	}()
	return chunks
}

func streamAnthropicWithContext(ctx context.Context, messages []message, defs []toolDef, inferenceURL, model, apiKey string) <-chan inferenceStreamEvent {
	chunks := make(chan inferenceStreamEvent)
	go func() {
		defer close(chunks)
		endpoint := strings.TrimRight(inferenceURL, "/") + "/v1/messages"
		systemPrompt, anthropicMsgs := convertMessagesForAnthropic(messages)
		reqBody := anthropicRequest{
			Model:     model,
			MaxTokens: 16384,
			System:    systemPrompt,
			Messages:  anthropicMsgs,
			Stream:    true,
			Tools:     convertToolDefsForAnthropic(defs),
		}
		body, err := json.Marshal(reqBody)
		if err != nil {
			chunks <- inferenceStreamEvent{Err: fmt.Errorf("failed to marshal request: %w", err)}
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			chunks <- inferenceStreamEvent{Err: fmt.Errorf("failed to create request: %w", err)}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			chunks <- inferenceStreamEvent{Err: fmt.Errorf("request failed: %w", err)}
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			chunks <- inferenceStreamEvent{Err: fmt.Errorf("inference returned %s: %s", resp.Status, strings.TrimSpace(string(b)))}
			return
		}

		type pendingToolCall struct {
			id   string
			name string
			args strings.Builder
		}
		pendingByIndex := map[int]*pendingToolCall{}
		finalize := func(index int) {
			pc := pendingByIndex[index]
			if pc == nil {
				return
			}
			argText := strings.TrimSpace(pc.args.String())
			if argText == "" {
				argText = "{}"
			}
			chunks <- inferenceStreamEvent{ToolCalls: []toolCall{{
				ID:   pc.id,
				Type: "function",
				Function: toolCallFunction{
					Name:      pc.name,
					Arguments: argText,
				},
			}}}
			delete(pendingByIndex, index)
		}

		reader := bufio.NewReader(resp.Body)
		var eventName string
		var dataLines []string
		flush := func() error {
			if eventName == "" && len(dataLines) == 0 {
				return nil
			}
			data := strings.Join(dataLines, "\n")
			var evt anthropicStreamEvent
			if err := json.Unmarshal([]byte(data), &evt); err != nil {
				eventName = ""
				dataLines = nil
				return nil
			}

			switch eventName {
			case "content_block_start":
				if evt.ContentBlock != nil && evt.ContentBlock.Type == "tool_use" {
					pc := &pendingToolCall{
						id:   strings.TrimSpace(evt.ContentBlock.ID),
						name: strings.TrimSpace(evt.ContentBlock.Name),
					}
					if len(evt.ContentBlock.Input) > 0 {
						pc.args.Write(evt.ContentBlock.Input)
					}
					pendingByIndex[evt.Index] = pc
				}
			case "content_block_delta":
				if evt.Delta != nil {
					if evt.Delta.Type == "text_delta" && evt.Delta.Text != "" {
						chunks <- inferenceStreamEvent{Content: evt.Delta.Text}
					} else if evt.Delta.Type == "input_json_delta" && evt.Delta.PartialJSON != "" {
						if pc := pendingByIndex[evt.Index]; pc != nil {
							pc.args.WriteString(evt.Delta.PartialJSON)
						}
					}
				}
			case "content_block_stop":
				finalize(evt.Index)
			case "message_stop":
				indexes := make([]int, 0, len(pendingByIndex))
				for idx := range pendingByIndex {
					indexes = append(indexes, idx)
				}
				sort.Ints(indexes)
				for _, idx := range indexes {
					finalize(idx)
				}
			}
			eventName = ""
			dataLines = nil
			return nil
		}

		for {
			line, err := reader.ReadString('\n')
			if err == io.EOF {
				_ = flush()
				break
			}
			if err != nil {
				chunks <- inferenceStreamEvent{Err: fmt.Errorf("failed reading stream: %w", err)}
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if err := flush(); err != nil {
					chunks <- inferenceStreamEvent{Err: fmt.Errorf("failed to parse chunk: %w", err)}
					return
				}
				continue
			}
			if strings.HasPrefix(line, "event:") {
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
				continue
			}
			if strings.HasPrefix(line, "data:") {
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
	}()
	return chunks
}

type hunterTaskRequest struct {
	ID   string `json:"id"`
	URLs []struct {
		URL   string `json:"url"`
		Label string `json:"label"`
	} `json:"urls"`
	MaxBytesPerURL int   `json:"max_bytes_per_url"`
	TimeoutSeconds int   `json:"timeout_seconds"`
	StripHTML      *bool `json:"strip_html"`
}

func stripHTML(rawBytes []byte) string {
	s := string(rawBytes)
	scriptStyleRE := regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</\1>`)
	tagRE := regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRE := regexp.MustCompile(`\s+`)
	s = scriptStyleRE.ReplaceAllString(s, " ")
	s = tagRE.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = spaceRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func runHunterMode(taskJSON, memoryURL, quarantinePrefix string) error {
	if strings.TrimSpace(taskJSON) == "" {
		return fmt.Errorf("--hunter-task is required when --hunter-mode is set")
	}
	if strings.TrimSpace(memoryURL) == "" {
		return fmt.Errorf("--memory-url is required when --hunter-mode is set")
	}
	if strings.TrimSpace(quarantinePrefix) == "" {
		return fmt.Errorf("--hunter-quarantine-prefix is required when --hunter-mode is set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var task hunterTaskRequest
	if err := json.Unmarshal([]byte(taskJSON), &task); err != nil {
		return fmt.Errorf("invalid --hunter-task JSON: %w", err)
	}
	if len(task.URLs) == 0 {
		return fmt.Errorf("hunter task requires at least one URL")
	}
	if strings.TrimSpace(task.ID) == "" {
		task.ID = fmt.Sprintf("hunt-%d", time.Now().Unix())
	}
	if task.MaxBytesPerURL <= 0 {
		task.MaxBytesPerURL = 50000
	}
	if task.TimeoutSeconds <= 0 {
		task.TimeoutSeconds = 30
	}
	strip := true
	if task.StripHTML != nil {
		strip = *task.StripHTML
	}
	perURLTimeout := time.Duration(task.TimeoutSeconds) * time.Second

	var report strings.Builder
	report.WriteString(fmt.Sprintf("HUNTER REPORT — %s\n", task.ID))
	client := &http.Client{}
	for i, u := range task.URLs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		rawURL := strings.TrimSpace(u.URL)
		label := strings.TrimSpace(u.Label)
		if label == "" {
			label = fmt.Sprintf("url-%d", i+1)
		}
		if rawURL == "" {
			report.WriteString(fmt.Sprintf("--- %s (%s) [ERROR] ---\n%s\n", label, rawURL, "missing url"))
			continue
		}
		parsed, err := url.ParseRequestURI(rawURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			report.WriteString(fmt.Sprintf("--- %s (%s) [ERROR] ---\n%s\n", label, rawURL, "invalid URL"))
			continue
		}
		reqCtx, reqCancel := context.WithTimeout(ctx, perURLTimeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
		if err != nil {
			reqCancel()
			report.WriteString(fmt.Sprintf("--- %s (%s) [ERROR] ---\n%s\n", label, rawURL, err.Error()))
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			reqCancel()
			report.WriteString(fmt.Sprintf("--- %s (%s) [ERROR] ---\n%s\n", label, rawURL, err.Error()))
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, int64(task.MaxBytesPerURL)+1))
		_ = resp.Body.Close()
		reqCancel()
		statusPart := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if readErr != nil {
			report.WriteString(fmt.Sprintf("--- %s (%s) [%s] ---\n%s\n", label, rawURL, statusPart, readErr.Error()))
			continue
		}
		body := string(data)
		if len(data) > task.MaxBytesPerURL {
			body = string(data[:task.MaxBytesPerURL]) + "\n[truncated]"
		}
		if strip {
			body = stripHTML([]byte(body))
		}
		report.WriteString(fmt.Sprintf("--- %s (%s) [%s] ---\n%s\n", label, rawURL, statusPart, body))
	}
	report.WriteString("END REPORT")

	rememberExec := &rememberTool{
		memoryURL:  memoryURL,
		agentName:  "hunter",
		collection: quarantinePrefix,
	}
	res := rememberExec.Execute(map[string]interface{}{
		"content": report.String(),
		"tags":    "hunter,quarantine,fetch",
	})
	if res.Error != "" {
		return fmt.Errorf("remember failed: %s", res.Error)
	}
	logJSON("info", "hunter completed", map[string]interface{}{
		"task_id":    task.ID,
		"collection": quarantinePrefix,
		"url_count":  len(task.URLs),
		"remembered": res.Error == "",
	})
	fmt.Printf("hunter success: fetched %d urls and wrote report to quarantine collection %q\n", len(task.URLs), quarantinePrefix)
	return nil
}

func writeSSE(w http.ResponseWriter, payload sseChunk) {
	b, _ := json.Marshal(payload)
	fmt.Fprintf(w, "data: %s\n\n", b)
}

func dropOldestPairs(msgs []message, maxPairs int) []message {
	if maxPairs == 0 || len(msgs) <= 1 {
		return msgs
	}
	maxNonSystem := maxPairs * 2
	nonSystemCount := len(msgs) - 1
	if nonSystemCount <= maxNonSystem {
		return msgs
	}
	start := len(msgs) - maxNonSystem
	trimmed := make([]message, 0, 1+maxNonSystem)
	trimmed = append(trimmed, msgs[0])
	trimmed = append(trimmed, msgs[start:]...)
	logJSON("info", "context trimmed", map[string]interface{}{"kept": len(trimmed), "total": len(msgs)})
	return trimmed
}

func dropOldestHistoryPairs(history []message, maxPairs int) []message {
	if len(history) == 0 || maxPairs == 0 {
		return history
	}
	msgs := append([]message{{Role: "system", Content: "_"}}, history...)
	trimmed := dropOldestPairs(msgs, maxPairs)
	if len(trimmed) <= 1 {
		return nil
	}
	return append([]message(nil), trimmed[1:]...)
}

func truncateSessionStateText(text string, maxChars int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if maxChars <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	return string(runes[:maxChars])
}

func identifyMessageGroups(history []message) [][]message {
	if len(history) == 0 {
		return nil
	}
	groups := make([][]message, 0, len(history))
	current := make([]message, 0, 4)
	currentHasUser := false
	for _, msg := range history {
		if msg.Role == "user" && currentHasUser && len(current) > 0 {
			groups = append(groups, current)
			current = make([]message, 0, 4)
			currentHasUser = false
		}
		current = append(current, msg)
		if msg.Role == "user" {
			currentHasUser = true
		}
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}

func extractRawSessionState(history []message, agentName string) string {
	if len(history) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Agent: ")
	b.WriteString(agentName)
	b.WriteString("\nMessages: ")
	b.WriteString(strconv.Itoa(len(history)))
	b.WriteString("\n\n")
	for _, msg := range history {
		switch msg.Role {
		case "system":
			continue
		case "user":
			content := truncateSessionStateText(msg.Content, 200)
			if content == "" {
				continue
			}
			b.WriteString("TASK: ")
			b.WriteString(content)
			b.WriteString("\n")
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					b.WriteString("CALLED: ")
					b.WriteString(strings.TrimSpace(tc.Function.Name))
					b.WriteString("(")
					b.WriteString(truncateSessionStateText(tc.Function.Arguments, 50))
					b.WriteString(")\n")
				}
				continue
			}
			content := truncateSessionStateText(msg.Content, 150)
			if content == "" {
				continue
			}
			b.WriteString("RESPONSE: ")
			b.WriteString(content)
			b.WriteString("\n")
		case "tool":
			content := truncateSessionStateText(msg.Content, 100)
			if content == "" {
				continue
			}
			b.WriteString("RESULT: ")
			b.WriteString(content)
			b.WriteString("\n")
		}
	}
	raw := b.String()
	if len(raw) > 2000 {
		raw = raw[:2000]
	}
	return raw
}

func persistEpisodicState(memoryURL, agent, sessionID string, history []message, agentName string) {
	if strings.TrimSpace(memoryURL) == "" {
		return
	}
	rawText := extractRawSessionState(history, agentName)
	if len(strings.TrimSpace(rawText)) < 50 {
		return
	}
	payload := map[string]interface{}{
		"agent":         agent,
		"content":       rawText,
		"session_id":    sessionID,
		"message_count": len(history),
	}
	body, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(memoryURL, "/")+"/session-state", bytes.NewReader(body))
	if err != nil {
		logJSON("warn", "episodic state persist failed", map[string]interface{}{"agent": agent, "session_id": sessionID, "error": err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		logJSON("warn", "episodic state persist failed", map[string]interface{}{"agent": agent, "session_id": sessionID, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		logJSON("warn", "episodic state persist failed", map[string]interface{}{
			"agent":      agent,
			"session_id": sessionID,
			"status":     resp.StatusCode,
			"error":      strings.TrimSpace(string(body)),
		})
	}
}

func loadEpisodicState(memoryURL, agent string) string {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(agent) == "" {
		return ""
	}
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(memoryURL, "/")+"/session-state?agent="+url.QueryEscape(agent), nil)
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
	var payload struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Content)
}

func progressiveTrim(history []message, maxMessages int) []message {
	if len(history) == 0 || maxMessages <= 0 {
		return history
	}
	groups := identifyMessageGroups(history)
	if len(groups) <= 3 {
		return history
	}
	trimmedGroups := groups[2:]
	trimmed := make([]message, 0, len(history))
	for _, group := range trimmedGroups {
		trimmed = append(trimmed, group...)
	}
	return trimmed
}

func addSessionContextMemoryIDs(sessionID string, ids []string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(ids) == 0 {
		return
	}
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	set, ok := sessionContextMemoryIDs[sessionID]
	if !ok {
		set = map[string]struct{}{}
		sessionContextMemoryIDs[sessionID] = set
	}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		set[id] = struct{}{}
	}
}

func snapshotSessionContextMemoryIDs(sessionID string) []string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	set := sessionContextMemoryIDs[sessionID]
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func markSessionMemoryValidated(sessionID, memoryID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	memoryID = strings.TrimSpace(memoryID)
	if sessionID == "" || memoryID == "" {
		return false
	}
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	set, ok := sessionValidatedIDs[sessionID]
	if !ok {
		set = map[string]bool{}
		sessionValidatedIDs[sessionID] = set
	}
	if set[memoryID] {
		return false
	}
	set[memoryID] = true
	return true
}

func buildSessionBootstrapContext(memoryURL, agentName, toolsFile, playbookFile, soulContent string, persona *personaRepo) (string, []string) {
	var blocks []string
	contextIDs := make([]string, 0, 12)
	if b, ids := fetchRecentMemoryBlocks(memoryURL, agentName); b != "" {
		blocks = append(blocks, b)
		contextIDs = append(contextIDs, ids...)
	}
	if b, ids := fetchRecentToolLessons(memoryURL, agentName, soulContent); b != "" {
		blocks = append(blocks, b)
		contextIDs = append(contextIDs, ids...)
	}
	if persona != nil {
		if b := loadPersonaSessionContext(persona); b != "" {
			blocks = append(blocks, b)
		}
	} else {
		if b := readContextFileBlock("## Tools Reference", toolsFile); b != "" {
			blocks = append(blocks, b)
		}
		if b := readContextFileBlock("## Playbook Reference", playbookFile); b != "" {
			blocks = append(blocks, b)
		}
	}
	if len(blocks) == 0 {
		return "", contextIDs
	}
	return "Use this reference context for this session.\n\n" + strings.Join(blocks, "\n\n"), contextIDs
}

func readContextFileBlock(title, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logJSON("warn", "failed reading context file", map[string]interface{}{"path": path, "error": err.Error()})
		}
		return ""
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return ""
	}
	return title + "\n" + content
}

func fetchMemoryCollectionBlock(memoryURL, collection, title string) (string, []string) {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(collection) == "" {
		return "", nil
	}
	payload := map[string]interface{}{
		"collection": collection,
		"query":      "recent task context summary",
		"n_results":  5,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(memoryURL, "/")+"/query", bytes.NewReader(body))
	if err != nil {
		return "", nil
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil
	}
	var out struct {
		Results []struct {
			ID         string  `json:"id"`
			Content    string  `json:"content"`
			Similarity float64 `json:"similarity"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", nil
	}
	lines := make([]string, 0, len(out.Results))
	ids := make([]string, 0, len(out.Results))
	for _, r := range out.Results {
		if r.Similarity <= 0.5 {
			continue
		}
		content := strings.TrimSpace(r.Content)
		if content == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- [%.2f] %s", r.Similarity, content))
		if id := strings.TrimSpace(r.ID); id != "" {
			ids = append(ids, id)
		}
	}
	if len(lines) == 0 {
		return "", ids
	}
	return title + "\n" + strings.Join(lines, "\n"), ids
}

func fetchRecentMemoryBlocks(memoryURL, agentName string) (string, []string) {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(agentName) == "" {
		return "", nil
	}
	blocks := make([]string, 0, 2)
	ids := make([]string, 0, 10)
	if b, found := fetchMemoryCollectionBlock(memoryURL, fmt.Sprintf("%s-memory", agentName), "## Recent Memory"); b != "" {
		blocks = append(blocks, b)
		ids = append(ids, found...)
	}
	if b, found := fetchMemoryCollectionBlock(memoryURL, "warband-context", "## Warband Context"); b != "" {
		blocks = append(blocks, b)
		ids = append(ids, found...)
	}
	return strings.Join(blocks, "\n\n"), ids
}

func soulHasLearnedTool(soulContent, toolName string) bool {
	if strings.TrimSpace(soulContent) == "" || strings.TrimSpace(toolName) == "" {
		return false
	}
	needle := strings.ToLower(toolName)
	for _, line := range strings.Split(soulContent, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "## Learned:") {
			continue
		}
		if strings.Contains(strings.ToLower(line), needle) {
			return true
		}
	}
	return false
}

func fetchRecentToolLessons(memoryURL, agentName, soulContent string) (string, []string) {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(agentName) == "" {
		return "", nil
	}

	body, _ := json.Marshal(map[string]interface{}{
		"agent": agentName,
		"query": "tool_failure",
		"limit": 5,
	})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(memoryURL, "/")+"/query", bytes.NewReader(body))
	if err != nil {
		return "", nil
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil
	}

	var out struct {
		Results []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
			Text    string `json:"text"`
		} `json:"results"`
		Memories []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
			Text    string `json:"text"`
		} `json:"memories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", nil
	}

	rawItems := out.Results
	if len(rawItems) == 0 {
		rawItems = out.Memories
	}
	if len(rawItems) == 0 {
		return "", nil
	}

	const maxLessonChars = 2000
	lessons := make([]string, 0, len(rawItems))
	ids := make([]string, 0, len(rawItems))
	totalChars := 0
	for _, item := range rawItems {
		text := strings.TrimSpace(item.Content)
		if text == "" {
			text = strings.TrimSpace(item.Text)
		}
		if text == "" {
			continue
		}
		if !strings.HasPrefix(text, "[FAILURE:") && !strings.HasPrefix(text, "[RECOVERY:") {
			continue
		}
		if toolName, _, _, ok := parseToolFailureMemory(text); ok && soulHasLearnedTool(soulContent, toolName) {
			continue
		}
		entryLen := len(text)
		if len(lessons) > 0 {
			entryLen++
		}
		if totalChars+entryLen > maxLessonChars {
			break
		}
		lessons = append(lessons, text)
		if id := strings.TrimSpace(item.ID); id != "" {
			ids = append(ids, id)
		}
		totalChars += entryLen
	}
	if len(lessons) == 0 {
		return "", ids
	}

	return "## Recent Tool Lessons\nThe following are recent tool failures and recoveries from your past sessions. Use these to avoid repeating mistakes:\n<lessons>\n" +
		strings.Join(lessons, "\n") + "\n</lessons>", ids
}

func truncateWords(s string, maxWords int) string {
	if maxWords <= 0 {
		return ""
	}
	parts := strings.Fields(strings.TrimSpace(s))
	if len(parts) <= maxWords {
		return strings.Join(parts, " ")
	}
	return strings.Join(parts[:maxWords], " ") + "..."
}

func fetchReflectionContext(memoryURL, agentName string) string {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(agentName) == "" {
		return ""
	}
	body, _ := json.Marshal(map[string]string{"agent": agentName})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(memoryURL, "/")+"/reflect", bytes.NewReader(body))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	var out struct {
		Clusters []struct {
			Size    int `json:"size"`
			Members []struct {
				Content string `json:"content"`
			} `json:"members"`
		} `json:"clusters"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Clusters) == 0 {
		return ""
	}
	maxClusters := len(out.Clusters)
	if maxClusters > 2 {
		maxClusters = 2
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Memory reflection: You have %d experience clusters ready for synthesis.\n", len(out.Clusters))
	for i := 0; i < maxClusters; i++ {
		c := out.Clusters[i]
		preview := ""
		if len(c.Members) > 0 {
			preview = truncateWords(c.Members[0].Content, 32)
		}
		fmt.Fprintf(&b, "Cluster %d (%d experiences): %s Consider synthesizing lessons from these patterns using the remember tool with type=lesson.\n", i+1, c.Size, preview)
	}
	return truncateWords(b.String(), 500)
}

func fetchIntuitiveContext(memoryURL, agentName, messageContent string) string {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(agentName) == "" || len(strings.TrimSpace(messageContent)) < 20 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	query := strings.TrimSpace(messageContent)
	if len(query) > 500 {
		query = query[:500]
	}
	body, err := json.Marshal(map[string]interface{}{"query": query, "agent": agentName, "n_results": 5})
	if err != nil {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(memoryURL, "/")+"/query", bytes.NewReader(body))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	var result struct {
		Results []struct {
			Content    string  `json:"content"`
			Similarity float64 `json:"similarity"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ""
	}
	var lines []string
	for _, r := range result.Results {
		if r.Similarity < 0.55 {
			continue
		}
		c := strings.TrimSpace(r.Content)
		if c == "" || len(c) < 10 {
			continue
		}
		if len(c) > 200 {
			c = c[:200] + "..."
		}
		lines = append(lines, "- "+c)
		if len(lines) >= 3 {
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "[INTUITION] Relevant past experience:\n" + strings.Join(lines, "\n")
}

func validateContextMemoriesAsync(memoryURL, sessionID, outcome string) {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	outcome = strings.TrimSpace(outcome)
	if outcome != "success" && outcome != "contradiction" {
		return
	}
	ids := snapshotSessionContextMemoryIDs(sessionID)
	if len(ids) == 0 {
		return
	}
	for _, memoryID := range ids {
		if !markSessionMemoryValidated(sessionID, memoryID) {
			continue
		}
		go func(mid string) {
			payload, _ := json.Marshal(map[string]string{
				"memory_id": mid,
				"outcome":   outcome,
			})
			req, err := http.NewRequest(http.MethodPost, strings.TrimRight(memoryURL, "/")+"/validate", bytes.NewReader(payload))
			if err != nil {
				logJSON("warn", "memory validation request build failed", map[string]interface{}{"error": err.Error(), "memory_id": mid})
				return
			}
			req.Header.Set("Content-Type", "application/json")
			client := &http.Client{Timeout: 3 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				logJSON("warn", "memory validation request failed", map[string]interface{}{"error": err.Error(), "memory_id": mid})
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
				logJSON("warn", "memory validation returned non-2xx", map[string]interface{}{"memory_id": mid, "status": resp.StatusCode, "body": strings.TrimSpace(string(b))})
			}
		}(memoryID)
	}
}

type recalledMemory struct {
	ID         string
	Text       string
	Similarity float64
	Metadata   map[string]interface{}
}

func recallMemories(memoryURL string, payload map[string]interface{}, timeout time.Duration) []recalledMemory {
	if strings.TrimSpace(memoryURL) == "" {
		return nil
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(memoryURL, "/")+"/query", bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}

	type recallItem struct {
		ID         string                 `json:"id"`
		Content    string                 `json:"content"`
		Text       string                 `json:"text"`
		Similarity float64                `json:"similarity"`
		Metadata   map[string]interface{} `json:"metadata"`
	}
	var out struct {
		Results  []recallItem `json:"results"`
		Memories []recallItem `json:"memories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil
	}

	items := out.Results
	if len(items) == 0 {
		items = out.Memories
	}
	memories := make([]recalledMemory, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(item.Content)
		if text == "" {
			text = strings.TrimSpace(item.Text)
		}
		if text == "" {
			continue
		}
		memories = append(memories, recalledMemory{
			ID:         strings.TrimSpace(item.ID),
			Text:       text,
			Similarity: item.Similarity,
			Metadata:   item.Metadata,
		})
	}
	return memories
}

func appendCloneMemoryContext(memoryURL, agentName, repoName string, result toolpkg.ToolResult) toolpkg.ToolResult {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(agentName) == "" || strings.TrimSpace(repoName) == "" || result.Error != "" {
		return result
	}
	memories := recallMemories(memoryURL, map[string]interface{}{
		"agent": agentName,
		"query": repoName,
		"limit": 3,
	}, 3*time.Second)
	if len(memories) == 0 {
		return result
	}
	var contextLines []string
	for _, memory := range memories {
		if memory.Similarity <= 0.5 {
			continue
		}
		content := strings.TrimSpace(memory.Text)
		if content == "" {
			continue
		}
		contextLines = append(contextLines, "- "+content)
	}
	if len(contextLines) == 0 {
		return result
	}
	result.Output += "\n\n[MEMORY CONTEXT for " + repoName + "]\n" + strings.Join(contextLines, "\n") + "\n"
	return result
}

func selfImprovementAllowed(agentName string, now time.Time) bool {
	if strings.TrimSpace(agentName) == "" {
		return false
	}
	selfImproveMu.Lock()
	defer selfImproveMu.Unlock()
	last := selfImproveLastRun[agentName]
	if !last.IsZero() && now.Sub(last) < time.Hour {
		return false
	}
	return true
}

func markSelfImprovementTriggered(agentName string, now time.Time) {
	if strings.TrimSpace(agentName) == "" {
		return
	}
	selfImproveMu.Lock()
	selfImproveLastRun[agentName] = now
	selfImproveMu.Unlock()
}

func parseToolFailureMemory(text string) (toolName, failureType, errText string, ok bool) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "[FAILURE:") {
		if end := strings.Index(text, "]"); end > len("[FAILURE:") {
			failureType = text[len("[FAILURE:"):end]
		}
	}
	toolMatch := regexp.MustCompile(`\btool=([^ ]+)`).FindStringSubmatch(text)
	if len(toolMatch) < 2 {
		return "", "", "", false
	}
	toolName = strings.TrimSpace(toolMatch[1])
	if toolName == "" {
		return "", "", "", false
	}
	if idx := strings.Index(text, " error="); idx >= 0 {
		errText = strings.TrimSpace(text[idx+len(" error="):])
	}
	return toolName, failureType, errText, true
}

func parseToolRecoveryMemory(text string) (toolName string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "[RECOVERY") {
		return "", false
	}
	toolMatch := regexp.MustCompile(`\btool=([^ ]+)`).FindStringSubmatch(text)
	if len(toolMatch) < 2 {
		return "", false
	}
	toolName = strings.TrimSpace(toolMatch[1])
	return toolName, toolName != ""
}

func mostCommonValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	counts := make(map[string]int, len(values))
	best := values[0]
	bestCount := 0
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		counts[value]++
		if counts[value] > bestCount {
			best = value
			bestCount = counts[value]
		}
	}
	return best
}

func recallTimestamp(meta map[string]interface{}) time.Time {
	if meta == nil {
		return time.Time{}
	}
	raw, _ := meta["timestamp"].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return ts.UTC()
}

type soulSection struct {
	Heading string
	Body    string
	Raw     string
	Learned bool
	Tool    string
}

func parseLearnedToolFromHeading(heading string) string {
	heading = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(heading), "## Learned:"))
	if heading == "" {
		return ""
	}
	fields := strings.Fields(heading)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func parseSoulSections(content string) []soulSection {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	sections := make([]soulSection, 0)
	current := make([]string, 0)
	flush := func() {
		if len(current) == 0 {
			return
		}
		raw := strings.TrimSpace(strings.Join(current, "\n"))
		if raw == "" {
			current = current[:0]
			return
		}
		heading := ""
		if idx := strings.IndexByte(raw, '\n'); idx >= 0 {
			heading = strings.TrimSpace(raw[:idx])
		} else {
			heading = strings.TrimSpace(raw)
		}
		body := ""
		if idx := strings.IndexByte(raw, '\n'); idx >= 0 {
			body = strings.TrimSpace(raw[idx+1:])
		}
		learned := strings.HasPrefix(heading, "## Learned:")
		sections = append(sections, soulSection{
			Heading: heading,
			Body:    body,
			Raw:     raw,
			Learned: learned,
			Tool:    parseLearnedToolFromHeading(heading),
		})
		current = current[:0]
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") && len(current) > 0 {
			flush()
		}
		current = append(current, line)
	}
	flush()
	return sections
}

func renderSoulSections(sections []soulSection) string {
	parts := make([]string, 0, len(sections))
	for _, sec := range sections {
		raw := strings.TrimSpace(sec.Raw)
		if raw == "" {
			continue
		}
		parts = append(parts, raw)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n") + "\n"
}

func countSoulLines(content string) int {
	trimmed := strings.TrimRight(content, "\n")
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

func compactSoul(agentName, content string, maxLines int) string {
	if maxLines <= 0 || countSoulLines(content) <= maxLines {
		return content
	}

	sections := parseSoulSections(content)
	if len(sections) == 0 {
		return content
	}

	type learnedGroup struct {
		latestIdx int
		heading   string
		bodies    []string
		titles    []string
	}
	groups := map[string]*learnedGroup{}
	skipIdx := map[int]bool{}
	for idx, sec := range sections {
		if !sec.Learned || sec.Tool == "" {
			continue
		}
		group, ok := groups[sec.Tool]
		if !ok {
			groups[sec.Tool] = &learnedGroup{
				latestIdx: idx,
				heading:   sec.Heading,
				bodies:    []string{sec.Body},
				titles:    []string{sec.Heading},
			}
			continue
		}
		log.Printf("[COMPACTION] agent=%s removed learned section: %s", agentName, sec.Heading)
		skipIdx[idx] = true
		group.latestIdx = idx
		group.heading = sec.Heading
		group.bodies = append(group.bodies, sec.Body)
		group.titles = append(group.titles, sec.Heading)
	}
	for tool, group := range groups {
		if len(group.bodies) < 2 {
			continue
		}
		latest := sections[group.latestIdx]
		combinedBodies := make([]string, 0, len(group.bodies))
		for _, body := range group.bodies {
			body = strings.TrimSpace(body)
			if body != "" {
				combinedBodies = append(combinedBodies, body)
			}
		}
		latest.Heading = group.heading
		latest.Body = strings.Join(combinedBodies, "\n")
		if latest.Body != "" {
			latest.Raw = latest.Heading + "\n" + latest.Body
		} else {
			latest.Raw = latest.Heading
		}
		sections[group.latestIdx] = latest
		_ = tool
	}

	compacted := make([]soulSection, 0, len(sections))
	for idx, sec := range sections {
		if skipIdx[idx] {
			continue
		}
		compacted = append(compacted, sec)
	}

	rendered := renderSoulSections(compacted)
	if countSoulLines(rendered) <= maxLines {
		return rendered
	}

	for countSoulLines(rendered) > maxLines {
		removeIdx := -1
		removeHeading := ""
		for idx, sec := range compacted {
			if sec.Learned {
				removeIdx = idx
				removeHeading = sec.Heading
				break
			}
		}
		if removeIdx < 0 {
			break
		}
		log.Printf("[COMPACTION] agent=%s removed learned section: %s", agentName, removeHeading)
		compacted = append(compacted[:removeIdx], compacted[removeIdx+1:]...)
		rendered = renderSoulSections(compacted)
	}

	return rendered
}

func executeRegistryTool(reg *toolpkg.Registry, name string, args map[string]interface{}) toolpkg.ToolResult {
	t, ok := reg.Get(name)
	if !ok {
		return toolpkg.ToolResult{Error: "tool not registered: " + name}
	}
	result := t.Execute(args)
	if verifyErr := reg.VerifyResult(name, args, result); verifyErr != nil {
		result = toolpkg.ToolResult{
			Output: result.Output,
			Error:  "verification failed: " + verifyErr.Error(),
		}
	}
	return result
}

func checkSelfImprovementTrigger(memoryURL, agentName string, reg *toolpkg.Registry, soulMaxLines int) {
	now := time.Now().UTC()
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(agentName) == "" || reg == nil {
		return
	}
	if !selfImprovementAllowed(agentName, now) {
		return
	}
	for _, toolName := range []string{"git-clone", "read", "write", "git-commit", "gitea"} {
		if _, ok := reg.Get(toolName); !ok {
			logJSON("warn", "self improvement skipped; required tool missing", map[string]interface{}{"tool": toolName, "agent": agentName})
			return
		}
	}

	triggerTool := ""
	triggerCount := 0
	failuresByTool := map[string][]string{}
	failureTextsByTool := map[string][]string{}
	recoveriesByTool := map[string][]string{}
	cutoff := now.Add(-7 * 24 * time.Hour)
	for _, toolName := range []string{"git-clone", "read", "write", "git-commit", "gitea"} {
		memories := recallMemories(memoryURL, map[string]interface{}{
			"agent":  agentName,
			"query":  toolName,
			"limit":  20,
			"filter": map[string]interface{}{"type": "tool_failure", "tool": toolName},
		}, 3*time.Second)
		if len(memories) == 0 {
			continue
		}
		recentCount := 0
		for _, memory := range memories {
			text := memory.Text
			ts := recallTimestamp(memory.Metadata)
			if ts.IsZero() || ts.Before(cutoff) {
				continue
			}
			failureTool, failureType, errText, ok := parseToolFailureMemory(text)
			if !ok || failureTool != toolName {
				continue
			}
			if failureType != "" && failureType != "retry_exhausted" {
				continue
			}
			recentCount++
			failuresByTool[toolName] = append(failuresByTool[toolName], errText)
			failureTextsByTool[toolName] = append(failureTextsByTool[toolName], text)
		}
		if recentCount >= 3 && recentCount > triggerCount {
			triggerTool = toolName
			triggerCount = recentCount
		}
	}
	if triggerTool == "" {
		return
	}
	for _, memory := range recallMemories(memoryURL, map[string]interface{}{
		"agent": agentName,
		"query": triggerTool,
		"limit": 20,
	}, 3*time.Second) {
		if toolName, ok := parseToolRecoveryMemory(memory.Text); ok && toolName == triggerTool {
			recoveriesByTool[toolName] = append(recoveriesByTool[toolName], memory.Text)
		}
	}
	if !selfImprovementAllowed(agentName, now) {
		return
	}
	markSelfImprovementTriggered(agentName, now)

	commonErr := mostCommonValue(failuresByTool[triggerTool])
	if commonErr == "" {
		commonErr = "repeated operational failure"
	}
	recoveryHint := ""
	if recoveries := recoveriesByTool[triggerTool]; len(recoveries) > 0 {
		recoveryHint = recoveries[0]
	}

	repoSlug := "kit/hirdforge-personas"
	repoDir := "hirdforge-personas"
	soulRelPath := filepath.ToSlash(filepath.Join(repoDir, agentName, "soul.md"))
	readRes := executeRegistryTool(reg, "git-clone", map[string]interface{}{"repo": repoSlug})
	if readRes.Error != "" {
		logJSON("warn", "self improvement clone failed", map[string]interface{}{"agent": agentName, "error": readRes.Error})
		return
	}
	soulRes := executeRegistryTool(reg, "read", map[string]interface{}{"path": soulRelPath})
	if soulRes.Error != "" {
		logJSON("warn", "self improvement read failed", map[string]interface{}{"agent": agentName, "path": soulRelPath, "error": soulRes.Error})
		return
	}

	shortDescription := fmt.Sprintf("%s failure guard for %s", triggerTool, agentName)
	heading := "## Learned: " + shortDescription
	if strings.Contains(soulRes.Output, heading) {
		return
	}

	amendmentLines := []string{
		heading,
		fmt.Sprintf("Repeated failures with `%s` were observed. Most common error: %s. Before declaring success, verify prerequisites and confirm the expected side effect.", triggerTool, commonErr),
	}
	if recoveryHint != "" {
		amendmentLines = append(amendmentLines, "Known recovery signal: "+recoveryHint)
	}
	amendment := strings.Join(amendmentLines, "\n")
	updatedSoul := strings.TrimRight(soulRes.Output, "\n") + "\n\n" + amendment + "\n"
	if countSoulLines(updatedSoul) > soulMaxLines {
		compactedSoul := compactSoul(agentName, updatedSoul, soulMaxLines)
		if compactedSoul != updatedSoul {
			updatedSoul = compactedSoul
		}
	}

	writeRes := executeRegistryTool(reg, "write", map[string]interface{}{
		"path":    soulRelPath,
		"content": updatedSoul,
	})
	if writeRes.Error != "" {
		logJSON("warn", "self improvement write failed", map[string]interface{}{"agent": agentName, "path": soulRelPath, "error": writeRes.Error})
		return
	}

	branch := fmt.Sprintf("%s/self-improvement-%s", agentName, now.Format("20060102-150405"))
	commitMsg := fmt.Sprintf("soul: %s learned %s", agentName, shortDescription)
	commitRes := executeRegistryTool(reg, "git-commit", map[string]interface{}{
		"repo":    repoDir,
		"message": commitMsg,
		"branch":  branch,
	})
	if commitRes.Error != "" {
		logJSON("warn", "self improvement commit failed", map[string]interface{}{"agent": agentName, "error": commitRes.Error})
		return
	}

	failures := failureTextsByTool[triggerTool]
	if len(failures) > 5 {
		failures = failures[:5]
	}
	prBody := "Triggered by repeated recent failures:\n\n- " + strings.Join(failures, "\n- ")
	if recoveryHint != "" {
		prBody += "\n\nRecovery observed:\n- " + recoveryHint
	}
	prRes := executeRegistryTool(reg, "gitea", map[string]interface{}{
		"action": "create-pr",
		"repo":   repoSlug,
		"title":  fmt.Sprintf("soul: %s learned — %s", agentName, shortDescription),
		"body":   prBody,
		"head":   branch,
		"base":   "main",
	})
	if prRes.Error != "" {
		logJSON("warn", "self improvement PR failed", map[string]interface{}{"agent": agentName, "error": prRes.Error})
		return
	}
	logJSON("info", "self improvement PR created", map[string]interface{}{"agent": agentName, "tool": triggerTool, "branch": branch})
}

func agentNameFromSoulPath(path string) string {
	base := filepath.Base(strings.TrimSpace(path))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.TrimSuffix(base, "-soul")
	base = strings.TrimSpace(base)
	if base == "" || strings.EqualFold(base, "soul") {
		return ""
	}
	return base
}

type personaRepo struct {
	URL       string
	Root      string
	AgentName string
}

func syncPersonaRepo(repoURL, dst string) error {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "-C", dst, "pull", "--ff-only")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git pull failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	_ = os.RemoveAll(dst)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", repoURL, dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func initPersonaRepo(repoURL, agentName string) (*personaRepo, error) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return nil, nil
	}
	if strings.TrimSpace(agentName) == "" {
		return nil, fmt.Errorf("--agent-name is required when --persona-repo is set")
	}
	dst := filepath.Join(os.TempDir(), "valhalla-personas")
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return nil, err
	}
	if err := syncPersonaRepo(repoURL, dst); err != nil {
		return nil, err
	}
	return &personaRepo{URL: repoURL, Root: dst, AgentName: agentName}, nil
}

func loadPersonaSoul(repo *personaRepo) (string, error) {
	if repo == nil {
		return "", fmt.Errorf("persona repo not configured")
	}
	path := filepath.Join(repo.Root, repo.AgentName, "soul.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", fmt.Errorf("empty soul at %s", path)
	}
	return content, nil
}

func collectSharedPersonaFiles(repo *personaRepo) []string {
	if repo == nil {
		return nil
	}
	candidates := []string{
		filepath.Join(repo.Root, "shared"),
		filepath.Join(repo.Root, repo.AgentName, "shared"),
	}
	var files []string
	seen := map[string]bool{}
	for _, root := range candidates {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(d.Name()))
			if ext != ".md" && ext != ".txt" {
				return nil
			}
			if seen[path] {
				return nil
			}
			seen[path] = true
			files = append(files, path)
			return nil
		})
	}
	sort.Strings(files)
	return files
}

func loadPersonaSessionContext(repo *personaRepo) string {
	if repo == nil {
		return ""
	}
	var blocks []string
	agentDir := filepath.Join(repo.Root, repo.AgentName)
	if b := readContextFileBlock("## Tools Reference", filepath.Join(agentDir, "tools.md")); b != "" {
		blocks = append(blocks, b)
	}
	if b := readContextFileBlock("## Playbook Reference", filepath.Join(agentDir, "playbook.md")); b != "" {
		blocks = append(blocks, b)
	}
	sharedFiles := collectSharedPersonaFiles(repo)
	if len(sharedFiles) > 0 {
		var sharedBlocks []string
		for _, p := range sharedFiles {
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				continue
			}
			rel := p
			if r, err := filepath.Rel(repo.Root, p); err == nil {
				rel = filepath.ToSlash(r)
			}
			sharedBlocks = append(sharedBlocks, "### "+rel+"\n"+content)
		}
		if len(sharedBlocks) > 0 {
			blocks = append(blocks, "## Shared Context\n"+strings.Join(sharedBlocks, "\n\n"))
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, "\n\n")
}

func maybeRememberAction(memoryURL, agentName, sessionID, toolName string, args map[string]interface{}, result toolpkg.ToolResult) {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(agentName) == "" || result.Error != "" {
		return
	}
	if toolName == "git-clone" {
		repo := truncateMemoryValue(fmt.Sprint(args["repo"]), 200)
		workspacePath := truncateMemoryValue(extractCloneWorkspacePath(result.Output), 300)
		if repo == "" || workspacePath == "" {
			return
		}
		rememberStructuredOutcome(memoryURL, agentName, sessionID, toolName, fmt.Sprintf(
			`CLONED: repo=%q to=%q`,
			repo,
			workspacePath,
		))
		return
	}
	if toolName != "git-commit" && toolName != "write" && toolName != "exec" {
		return
	}
	outLower := strings.ToLower(result.Output)
	keywords := []string{"pull request", "created", "pushed", "merged", "committed"}
	matched := false
	for _, kw := range keywords {
		if strings.Contains(outLower, kw) {
			matched = true
			break
		}
	}
	if !matched {
		return
	}
	repo := truncateMemoryValue(fmt.Sprint(args["repo"]), 200)
	snippet := truncateMemoryValue(result.Output, 100)
	content := fmt.Sprintf(
		`TOOL_SUCCESS: tool=%q repo=%q result=%q`,
		toolName,
		repo,
		snippet,
	)
	rememberStructuredOutcome(memoryURL, agentName, sessionID, toolName, content)
}

func truncateMemoryValue(raw string, limit int) string {
	cleaned := strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	if cleaned == "<nil>" {
		return ""
	}
	if limit <= 0 {
		return cleaned
	}
	runes := []rune(cleaned)
	if len(runes) <= limit {
		return cleaned
	}
	return string(runes[:limit])
}

func extractCloneWorkspacePath(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	if idx := strings.LastIndex(output, " to "); idx >= 0 {
		return strings.TrimSpace(output[idx+4:])
	}
	if idx := strings.LastIndex(output, " in "); idx >= 0 {
		return strings.TrimSpace(output[idx+4:])
	}
	lines := strings.Split(output, "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func extractFirstURL(output string) string {
	for _, field := range strings.Fields(output) {
		if strings.HasPrefix(field, "http://") || strings.HasPrefix(field, "https://") {
			return strings.TrimRight(field, ".,);]")
		}
	}
	return ""
}

func rememberStructuredOutcome(memoryURL, agentName, sessionID, toolName, content string) {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(agentName) == "" || strings.TrimSpace(content) == "" {
		return
	}
	go func() {
		payload := map[string]interface{}{
			"agent":      agentName,
			"collection": fmt.Sprintf("%s-memory", agentName),
			"content":    content,
			"metadata": map[string]interface{}{
				"agent":      agentName,
				"type":       "action_success",
				"tool":       toolName,
				"session_id": sessionID,
				"timestamp":  time.Now().UTC().Format(time.RFC3339),
			},
		}
		body, _ := json.Marshal(payload)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(memoryURL, "/")+"/remember", bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		_ = resp.Body.Close()
	}()
}

func summarizeToolFailureArgs(args map[string]interface{}) string {
	parts := make([]string, 0, 3)
	if repo, ok := args["repo"]; ok {
		parts = append(parts, fmt.Sprintf("repo=%v", repo))
	}
	if cmd, ok := args["command"]; ok {
		cmdStr := fmt.Sprintf("%v", cmd)
		if len(cmdStr) > 100 {
			cmdStr = cmdStr[:100] + "..."
		}
		parts = append(parts, fmt.Sprintf("cmd=%s", cmdStr))
	}
	if path, ok := args["path"]; ok {
		parts = append(parts, fmt.Sprintf("path=%v", path))
	}
	return strings.Join(parts, " ")
}

func rememberToolFailure(memoryURL, agentName, sessionID, toolName string, args map[string]interface{}, result toolpkg.ToolResult, failureType string) {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(agentName) == "" || strings.TrimSpace(result.Error) == "" {
		return
	}
	argSummary := summarizeToolFailureArgs(args)
	memory := fmt.Sprintf("[FAILURE:%s] tool=%s", failureType, toolName)
	if argSummary != "" {
		memory += " " + argSummary
	}
	memory += fmt.Sprintf(" error=%s", result.Error)
	if len(memory) > 600 {
		memory = memory[:600] + "..."
	}
	go func() {
		payload := map[string]interface{}{
			"agent":      agentName,
			"collection": fmt.Sprintf("%s-memory", agentName),
			"content":    memory,
			"metadata": map[string]string{
				"type":    "tool_failure",
				"tool":    toolName,
				"failure": failureType,
				"session": sessionID,
			},
		}
		body, _ := json.Marshal(payload)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(memoryURL, "/")+"/remember", bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		_ = resp.Body.Close()
	}()
}

func rememberToolRecovery(memoryURL, agentName, sessionID, toolName string, args map[string]interface{}, attempt int) {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(agentName) == "" || attempt <= 1 {
		return
	}
	argSummary := summarizeToolFailureArgs(args)
	memory := fmt.Sprintf("[RECOVERY] tool=%s", toolName)
	if argSummary != "" {
		memory += " " + argSummary
	}
	memory += fmt.Sprintf(" recovered_on_attempt=%d", attempt)
	go func() {
		payload := map[string]interface{}{
			"agent":      agentName,
			"collection": fmt.Sprintf("%s-memory", agentName),
			"content":    memory,
			"metadata": map[string]string{
				"type":    "tool_recovery",
				"tool":    toolName,
				"session": sessionID,
			},
		}
		body, _ := json.Marshal(payload)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(memoryURL, "/")+"/remember", bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		_ = resp.Body.Close()
	}()
}

func validateGiteaHMAC(secret string, body []byte, provided string) bool {
	secret = strings.TrimSpace(secret)
	provided = strings.TrimSpace(provided)
	if secret == "" || provided == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	expected := fmt.Sprintf("%x", mac.Sum(nil))
	return hmac.Equal([]byte(strings.ToLower(expected)), []byte(strings.ToLower(provided)))
}

func newTaskID() string {
	return fmt.Sprintf("task-%08x", rand.Uint32())
}

func resolveTokenFlagOrFile(flagValue, filePath, envKey string) string {
	if token := strings.TrimSpace(flagValue); token != "" {
		return token
	}
	if data, err := os.ReadFile(filePath); err == nil {
		if token := strings.TrimSpace(string(data)); token != "" {
			return token
		}
	}
	return strings.TrimSpace(os.Getenv(envKey))
}

func taskNudgeCount(record tasklifepkg.TaskRecord) int {
	count := 0
	for _, state := range record.History {
		if state == tasklifepkg.StateNudged {
			count++
		}
	}
	return count
}

func sovereignStateEnabled(enabled map[string]bool, state tasklifepkg.TaskState) bool {
	if len(enabled) == 0 {
		return false
	}
	switch state {
	case tasklifepkg.StateCompleted:
		return enabled["completed"]
	case tasklifepkg.StateNudged:
		return enabled["nudged"]
	case tasklifepkg.StateFailedNoPR:
		return enabled["failed"]
	default:
		return false
	}
}

func notifyGateway(gatewayURL, eventType, agentName, message string) {
	if gatewayURL == "" {
		return
	}
	go func() {
		body, _ := json.Marshal(map[string]string{
			"type":    eventType,
			"agent":   agentName,
			"message": message,
		})
		req, err := http.NewRequest(http.MethodPost, strings.TrimRight(gatewayURL, "/")+"/api/v1/events", bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: agentCommTimeout}
		resp, err := client.Do(req)
		if err != nil {
			logJSON("warn", "gateway notify failed", map[string]interface{}{"error": err.Error()})
			return
		}
		_ = resp.Body.Close()
	}()
}

func statusPayload() map[string]interface{} {
	return map[string]interface{}{
		"status":          "ready",
		"agent":           "valhalla-agent",
		"model":           modelName,
		"tools":           enabledTools,
		"uptime_seconds":  int(time.Since(startTime).Seconds()),
		"requests_served": atomic.LoadInt64(&requestCount),
		"tool_calls_made": atomic.LoadInt64(&toolCalls),
	}
}

func agentNameFromSoul(soul string) string {
	line := strings.TrimSpace(strings.SplitN(soul, "\n", 2)[0])
	line = strings.TrimPrefix(line, "# ")
	line = strings.TrimPrefix(line, "#")
	line = strings.TrimSpace(line)
	if line == "" {
		return "valhalla-agent"
	}
	return line
}

func listWorkspaceFiles(workspace string, limit int) ([]fileInfo, error) {
	files := make([]fileInfo, 0)
	root, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if len(files) >= limit {
			return io.EOF
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, fileInfo{
			Name:     filepath.ToSlash(rel),
			Size:     info.Size(),
			Modified: info.ModTime().UTC().Format(time.RFC3339),
		})
		return nil
	})
	if err == io.EOF {
		err = nil
	}
	return files, err
}

func metricsText() string {
	var b strings.Builder
	b.WriteString("# HELP valhalla_agent_requests_total Total messages processed\n")
	b.WriteString("# TYPE valhalla_agent_requests_total counter\n")
	b.WriteString(fmt.Sprintf("valhalla_agent_requests_total %d\n", atomic.LoadInt64(&metricsRequestsTotal)))
	b.WriteString("# HELP valhalla_agent_request_duration_seconds Last request duration in seconds\n")
	b.WriteString("# TYPE valhalla_agent_request_duration_seconds gauge\n")
	b.WriteString(fmt.Sprintf("valhalla_agent_request_duration_seconds %.6f\n", getLastDuration()))
	b.WriteString("# HELP valhalla_agent_tool_calls_total Total tool calls by tool name\n")
	b.WriteString("# TYPE valhalla_agent_tool_calls_total counter\n")
	toolMap := snapshotToolMetrics()
	toolNames := make([]string, 0, len(toolMap))
	for name := range toolMap {
		toolNames = append(toolNames, name)
	}
	sort.Strings(toolNames)
	for _, name := range toolNames {
		b.WriteString(fmt.Sprintf("valhalla_agent_tool_calls_total{tool=%q} %d\n", name, toolMap[name]))
	}
	b.WriteString("# HELP valhalla_agent_errors_total Total errors\n")
	b.WriteString("# TYPE valhalla_agent_errors_total counter\n")
	b.WriteString(fmt.Sprintf("valhalla_agent_errors_total %d\n", atomic.LoadInt64(&metricsErrorsTotal)))
	b.WriteString("# HELP valhalla_agent_stalls_total Total inference stalls detected\n")
	b.WriteString("# TYPE valhalla_agent_stalls_total counter\n")
	b.WriteString(fmt.Sprintf("valhalla_agent_stalls_total %d\n", atomic.LoadInt64(&metricsStallsTotal)))
	b.WriteString("# HELP valhalla_agent_uptime_seconds Agent uptime in seconds\n")
	b.WriteString("# TYPE valhalla_agent_uptime_seconds gauge\n")
	b.WriteString(fmt.Sprintf("valhalla_agent_uptime_seconds %d\n", int(time.Since(startTime).Seconds())))
	b.WriteString("# HELP valhalla_agent_active_requests Active in-flight requests\n")
	b.WriteString("# TYPE valhalla_agent_active_requests gauge\n")
	b.WriteString(fmt.Sprintf("valhalla_agent_active_requests %d\n", atomic.LoadInt64(&metricsActiveRequests)))
	return b.String()
}

func main() {
	rand.Seed(time.Now().UnixNano())
	soulPath := flag.String("soul", "./soul.md", "path to SOUL.md")
	port := flag.String("port", "8081", "HTTP port")
	inferenceURL := flag.String("inference-url", "http://localhost:11434", "inference base URL")
	model := flag.String("model", "qwen3:30b", "model name")
	apiKey := flag.String("api-key", "", "API key for inference backend (optional)")
	maxContext := flag.Int("max-context", 20, "max number of user/assistant message pairs to keep (0 disables trimming)")
	hunterMode := flag.Bool("hunter-mode", false, "Run as ephemeral hunter: execute task, write to memory, exit")
	hunterTask := flag.String("hunter-task", "", "JSON string with fetch instructions for hunter mode")
	hunterQuarantine := flag.String("hunter-quarantine-prefix", "", "Seidr collection prefix for quarantine writes")
	requirePRPattern := flag.String("require-pr-pattern", "", "Regex pattern required in async task completion responses; empty disables the gate")
	completionMaxNudges := flag.Int("completion-max-nudges", 3, "Maximum completion gate nudges before failing a task")
	maxDelegationTokens := flag.Int("max-delegation-tokens", 500, "Maximum delegation message token budget before trimming optional sections")
	sovereignNotifyURL := flag.String("sovereign-notify-url", "", "Gateway URL for sovereign task notifications; empty disables sovereign reporting")
	sovereignNotifyOn := flag.String("sovereign-notify-on", "completed,failed,nudged", "Comma-separated sovereign notification states")
	workspace := flag.String("workspace", "./workspace", "tool workspace directory")
	peersFlag := flag.String("peers", "", "comma-separated name=url peer agents")
	memoryURL := flag.String("memory-url", "", "Seidr memory service URL")
	gatewayURL := flag.String("gateway-url", "", "Gateway URL for event notifications (optional)")
	agentNameFlag := flag.String("agent-name", "", "agent name override (defaults to soul filename)")
	personaRepoFlag := flag.String("persona-repo", "", "git URL of persona repository")
	toolsFile := flag.String("tools-file", "/etc/valhalla/tools.md", "path to tools context file")
	playbookFile := flag.String("playbook-file", "/etc/valhalla/playbook.md", "path to playbook context file")
	toolsFlag := flag.String("tools", "exec,read,write,edit", "comma-separated enabled tools")
	maxToolRetries := flag.Int("max-tool-retries", 2, "max retry attempts per tool call (0 disables retries)")
	inferenceTimeout := flag.Int("inference-timeout", 120, "timeout in seconds for each inference call")
	soulMaxLines := flag.Int("soul-max-lines", 80, "maximum number of lines allowed in a SOUL file")
	giteaURL := flag.String("gitea-url", "", "Gitea server URL for git tools")
	giteaTokenFlag := flag.String("gitea-token", "", "Gitea API token (optional; falls back to /vault/secrets/gitea-token)")
	giteaReviewersTokenFlag := flag.String("gitea-reviewers-token", "", "Gitea reviewers token (optional; falls back to /vault/secrets/gitea-reviewers-token)")
	webhookSecret := flag.String("webhook-secret", "", "HMAC secret for /webhook/gitea")
	mcpServers := flag.String("mcp-servers", "", "Comma-separated MCP server URLs")
	flag.Parse()

	if *hunterMode {
		if err := runHunterMode(*hunterTask, *memoryURL, *hunterQuarantine); err != nil {
			logJSON("error", "hunter failed", map[string]interface{}{"error": err.Error()})
			fmt.Printf("hunter failed: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if strings.TrimSpace(*requirePRPattern) != "" {
		if _, err := regexp.Compile(*requirePRPattern); err != nil {
			die("invalid --require-pr-pattern", err)
		}
	}

	if err := os.MkdirAll(*workspace, 0755); err != nil {
		die("failed to create workspace", err)
	}
	peers, err := parsePeers(*peersFlag)
	if err != nil {
		die("failed to parse peers", err)
	}
	peerNames := make([]string, 0, len(peers))
	for name := range peers {
		peerNames = append(peerNames, name)
	}
	sort.Strings(peerNames)
	agentName := strings.TrimSpace(*agentNameFlag)
	if agentName == "" {
		agentName = agentNameFromSoulPath(*soulPath)
	}
	var persona *personaRepo
	if strings.TrimSpace(*personaRepoFlag) != "" {
		if strings.TrimSpace(agentName) == "" {
			die("persona repo setup failed", fmt.Errorf("agent name could not be derived; set --agent-name"))
		}
		persona, err = initPersonaRepo(*personaRepoFlag, agentName)
		if err != nil {
			die("persona repo setup failed", err)
		}
	}

	var soul string
	if persona != nil {
		soul, err = loadPersonaSoul(persona)
		if err != nil {
			die("failed to load persona soul", err)
		}
	} else {
		soulBytes, readErr := os.ReadFile(*soulPath)
		if readErr != nil {
			die("failed to read soul file", readErr)
		}
		soul = string(soulBytes)
		if agentName == "" {
			agentName = agentNameFromSoul(soul)
		}
	}
	if agentName == "" {
		agentName = "valhalla-agent"
	}
	giteaToken := resolveTokenFlagOrFile(*giteaTokenFlag, "/vault/secrets/gitea-token", "GITEA_TOKEN")
	giteaReviewersToken := resolveTokenFlagOrFile(*giteaReviewersTokenFlag, "/vault/secrets/gitea-reviewers-token", "GITEA_REVIEWERS_TOKEN")
	if giteaReviewersToken != "" {
		_ = os.Setenv("GITEA_REVIEWERS_TOKEN", giteaReviewersToken)
	}
	taskStore := taskspkg.NewStore()
	taskTracker := tasklifepkg.NewTaskTracker()
	sovereignStates := map[string]bool{}
	for _, state := range strings.Split(*sovereignNotifyOn, ",") {
		if state = strings.ToLower(strings.TrimSpace(state)); state != "" {
			sovereignStates[state] = true
		}
	}
	sovereignReporter := tasklifepkg.NewSovereignReporter(*sovereignNotifyURL, agentName, strings.TrimSpace(*sovereignNotifyURL) != "")
	var taskCancelMu sync.Mutex
	taskCancels := map[string]context.CancelFunc{}
	delegationGates := []string{}
	if strings.TrimSpace(*requirePRPattern) != "" {
		delegationGates = append(delegationGates, "require_pr_pattern="+strings.TrimSpace(*requirePRPattern))
	}

	reg := toolpkg.NewRegistry()
	var giteaTool *toolpkg.GiteaAPITool
	delegateExec := &delegateTool{
		peers:               peers,
		agentName:           agentName,
		giteaURL:            *giteaURL,
		maxDelegationTokens: *maxDelegationTokens,
		gates:               delegationGates,
	}
	broadcastExec := &broadcastTool{peers: peers}
	taskStatusExec := &taskStatusTool{peers: peers}
	recallExec := &recallTool{
		memoryURL: *memoryURL,
		agentName: agentName,
		onMemories: func(sessionID string, ids []string) {
			addSessionContextMemoryIDs(sessionID, ids)
		},
	}
	rememberExec := &rememberTool{memoryURL: *memoryURL, agentName: agentName}
	enabled := map[string]bool{}
	for _, name := range strings.Split(*toolsFlag, ",") {
		if name = strings.TrimSpace(name); name != "" {
			enabled[name] = true
		}
	}
	if enabled["exec"] {
		reg.Register(toolpkg.NewExecTool())
	}
	if enabled["http"] {
		reg.Register(toolpkg.NewHTTPTool())
	}
	if enabled["read"] {
		reg.Register(toolpkg.NewReadTool(*workspace))
	}
	if enabled["write"] {
		reg.Register(toolpkg.NewWriteTool(*workspace))
	}
	if enabled["edit"] {
		reg.Register(toolpkg.NewEditTool(*workspace))
	}
	if enabled["git-clone"] || enabled["git-commit"] || enabled["git-diff"] || enabled["gitea"] {
		if enabled["git-clone"] {
			reg.Register(toolpkg.NewGitCloneTool(*workspace, *giteaURL, giteaToken))
		}
		if enabled["git-commit"] {
			reg.Register(toolpkg.NewGitCommitTool(*workspace, *giteaURL, giteaToken))
		}
		if enabled["git-diff"] {
			reg.Register(toolpkg.NewGitDiffTool(*workspace))
		}
		if enabled["gitea"] {
			giteaTool = toolpkg.NewGiteaAPITool(*giteaURL, giteaToken)
			reg.Register(giteaTool)
		}
	}
	if enabled["delegate"] || len(peers) > 0 {
		reg.Register(delegateExec)
		reg.Register(taskStatusExec)
	}
	if enabled["broadcast"] || len(peers) > 0 {
		reg.Register(broadcastExec)
	}
	if strings.TrimSpace(*memoryURL) != "" {
		reg.Register(recallExec)
		reg.Register(rememberExec)
	}
	// MCP tool discovery
	if *mcpServers != "" {
		for _, serverURL := range strings.Split(*mcpServers, ",") {
			serverURL = strings.TrimSpace(serverURL)
			if serverURL == "" {
				continue
			}
			logJSON("info", "connecting to MCP server", map[string]interface{}{"url": serverURL})
			client := mcppkg.NewClient(serverURL)
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
				// Don't override native tools.
				if _, exists := reg.Get(td.Name); exists {
					logJSON("warn", "MCP tool name conflicts with native tool, skipping", map[string]interface{}{"tool": td.Name, "server": serverURL})
					continue
				}
				reg.Register(mcpTool)
				logJSON("info", "registered MCP tool", map[string]interface{}{"tool": td.Name, "server": serverURL})
			}
		}
	}
	toolDefs := buildToolDefs(reg)
	enabledTools = reg.List()
	modelName = *model

	emitNoop := func(sseChunk) bool { return true }
	processConversation := func(ctx context.Context, sessionID, content string, emit func(sseChunk) bool, logTool func(taskspkg.ToolLog)) (string, error) {
		if emit == nil {
			emit = emitNoop
		}
		withInferenceTimeout := func(parent context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(parent, time.Duration(*inferenceTimeout)*time.Second)
		}
		hadToolCalls := false
		defer func() {
			if !hadToolCalls {
				return
			}
			go checkSelfImprovementTrigger(*memoryURL, agentName, reg, *soulMaxLines)
		}()
		defer func() {
			sessionsMu.Lock()
			ttPtr := trackedTasks[sessionID]
			var tt trackedTask
			if ttPtr != nil {
				tt = *ttPtr
				tt.FilesWritten = append([]string(nil), ttPtr.FilesWritten...)
			}
			delete(trackedTasks, sessionID)
			sessionsMu.Unlock()
			if ttPtr == nil || tt.IssueNumber == 0 || tt.PRCreated {
				return
			}
			go func(owner, repoName string, issueNum int, tt trackedTask) {
				if giteaTool == nil {
					return
				}
				retryCount := 0
				comments, err := giteaTool.GetComments(owner, repoName, issueNum)
				if err == nil {
					for _, c := range comments {
						body, _ := c["body"].(string)
						if strings.Contains(body, "## Retry Context") {
							retryCount++
						}
					}
				}
				filesStr := "none"
				if len(tt.FilesWritten) > 0 {
					filesStr = strings.Join(tt.FilesWritten, ", ")
				}
				branchStr := "none"
				if tt.BranchName != "" {
					branchStr = tt.BranchName
				}
				comment := fmt.Sprintf(
					"## Retry Context (attempt %d)\n**Agent:** %s\n**Failed at:** session ended without PR\n**Branch:** %s\n**Files written:** %s\n**Suggested approach:** Check if branch exists, verify files, complete remaining steps (commit, push, create PR)",
					retryCount+1,
					agentName,
					branchStr,
					filesStr,
				)
				if err := giteaTool.PostComment(owner, repoName, issueNum, comment); err != nil {
					logJSON("warn", "dlq: comment failed", map[string]interface{}{"issue": issueNum, "error": err.Error()})
				}
				labels, err := giteaTool.GetIssueLabels(owner, repoName, issueNum)
				if err != nil {
					labels = []string{}
				}
				newLabels := make([]string, 0, len(labels)+1)
				for _, l := range labels {
					if !strings.HasPrefix(l, "status/") {
						newLabels = append(newLabels, l)
					}
				}
				if retryCount+1 >= 3 {
					newLabels = append(newLabels, "status/failed")
					logJSON("warn", "dlq: issue permanently failed after 3 attempts", map[string]interface{}{"issue": issueNum})
				} else {
					newLabels = append(newLabels, "status/retry")
					logJSON("info", "dlq: issue marked for retry", map[string]interface{}{"issue": issueNum, "attempt": retryCount + 1})
				}
				if err := giteaTool.ReplaceLabels(owner, repoName, issueNum, newLabels); err != nil {
					logJSON("warn", "dlq: label update failed", map[string]interface{}{"issue": issueNum, "error": err.Error()})
				}
			}(tt.IssueOwner, tt.IssueRepo, tt.IssueNumber, tt)
		}()

		sessionsMu.Lock()
		history := append([]message(nil), sessions[sessionID]...)
		firstMessage := !seenSessions[sessionID]
		if firstMessage {
			seenSessions[sessionID] = true
		}
		injectSkillNudge := false
		if firstMessage && strings.TrimSpace(*personaRepoFlag) != "" && !skillNudgeInjected[sessionID] {
			skillNudgeInjected[sessionID] = true
			injectSkillNudge = true
		}
		sessionsMu.Unlock()
		sanitizeHistory := func(history []message) []message {
			if len(history) == 0 {
				return history
			}
			openToolCalls := map[string]int{}
			assistantWithToolCalls := map[int]bool{}
			assistantMatchedToolResponse := map[int]bool{}
			removeIdx := map[int]bool{}
			for i, msg := range history {
				switch msg.Role {
				case "assistant":
					if len(msg.ToolCalls) == 0 {
						continue
					}
					assistantWithToolCalls[i] = true
					for _, tc := range msg.ToolCalls {
						tcID := strings.TrimSpace(tc.ID)
						if tcID == "" {
							continue
						}
						openToolCalls[tcID] = i
					}
				case "tool":
					tcID := strings.TrimSpace(msg.ToolCallID)
					if tcID == "" {
						removeIdx[i] = true
						continue
					}
					assistantIdx, ok := openToolCalls[tcID]
					if !ok {
						removeIdx[i] = true
						continue
					}
					delete(openToolCalls, tcID)
					assistantMatchedToolResponse[assistantIdx] = true
				}
			}
			for assistantIdx := range assistantWithToolCalls {
				if !assistantMatchedToolResponse[assistantIdx] {
					removeIdx[assistantIdx] = true
				}
			}
			if len(removeIdx) == 0 {
				return history
			}
			removed := 0
			out := make([]message, 0, len(history)-len(removeIdx))
			for i, msg := range history {
				if removeIdx[i] {
					removed++
					continue
				}
				out = append(out, msg)
			}
			logJSON("info", "sanitized orphaned tool messages", map[string]interface{}{"removed": removed, "session_id": sessionID})
			return out
		}
		history = sanitizeHistory(history)
		if firstMessage && strings.TrimSpace(*memoryURL) != "" {
			loaded := loadEpisodicState(*memoryURL, agentName)
			if loaded != "" {
				sessionsMu.Lock()
				episodicStates[sessionID] = loaded
				sessionsMu.Unlock()
			}
		}

		bootstrapContext := ""
		bootstrapMemoryIDs := []string{}
		reflectionContext := ""
		if firstMessage {
			if persona != nil {
				if err := syncPersonaRepo(persona.URL, persona.Root); err != nil {
					logJSON("warn", "persona repo refresh failed", map[string]interface{}{"error": err.Error()})
				}
			}
			bootstrapContext, bootstrapMemoryIDs = buildSessionBootstrapContext(*memoryURL, agentName, *toolsFile, *playbookFile, soul, persona)
			if len(bootstrapMemoryIDs) > 0 {
				addSessionContextMemoryIDs(sessionID, bootstrapMemoryIDs)
			}
			reflectionContext = fetchReflectionContext(*memoryURL, agentName)
		}
		systemContent := soul
		if strings.TrimSpace(bootstrapContext) != "" {
			systemContent = soul + "\n\n" + bootstrapContext
		}
		if strings.TrimSpace(reflectionContext) != "" {
			systemContent += "\n\n" + reflectionContext
		}
		messages := []message{{Role: "system", Content: systemContent}}
		sessionsMu.Lock()
		episodicNarrative := episodicStates[sessionID]
		sessionsMu.Unlock()
		if strings.TrimSpace(episodicNarrative) != "" {
			messages = append(messages, message{
				Role:    "user",
				Content: "Session context from your previous work session:\n" + episodicNarrative + "\n\nContinue from where you left off if relevant to the current task.",
			})
			messages = append(messages, message{
				Role:    "assistant",
				Content: "Understood, I have context from my previous session.",
			})
		}
		messages = append(messages, history...)
		if injectSkillNudge {
			messages = append(messages, message{
				Role:    "user",
				Content: "Before starting this task, check the Skills table in your SOUL and load any matching skill files using exec: cat /tmp/valhalla-personas/<path>. Do not skip this step.",
			})
		}
		if intuitionCtx := fetchIntuitiveContext(*memoryURL, agentName, content); intuitionCtx != "" {
			messages = append(messages, message{Role: "user", Content: intuitionCtx})
			messages = append(messages, message{Role: "assistant", Content: "Noted, I'll keep that context in mind."})
			logJSON("info", "intuitive recall injected", map[string]interface{}{"agent": agentName, "session_id": sessionID, "memories": strings.Count(intuitionCtx, "\n- ") + 1})
		}
		messages = append(messages, message{Role: "user", Content: content})

		shouldRetryTool := func(name string) bool {
			switch name {
			case "delegate", "broadcast", "recall", "remember", "task_status":
				return false
			default:
				return true
			}
		}

		executeOneToolCall := func(tc toolCall) toolpkg.ToolResult {
			atomic.AddInt64(&toolCalls, 1)
			atomic.AddInt64(&metricsToolCallsTotal, 1)
			incToolMetric(tc.Function.Name)
			hadToolCalls = true
			args := map[string]interface{}{}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				args = map[string]interface{}{"_raw": tc.Function.Arguments}
			}
			logJSON("info", "tool called", map[string]interface{}{"tool": tc.Function.Name})
			if !emit(sseChunk{Type: "tool_call", Tool: tc.Function.Name, Args: args, Done: false}) {
				return toolpkg.ToolResult{Error: "stream closed"}
			}

			runToolAttempt := func() toolpkg.ToolResult {
				result := toolpkg.ToolResult{Error: "unknown tool: " + tc.Function.Name}
				switch tc.Function.Name {
				case "delegate":
					args["_task_id"] = sessionID
					result = delegateExec.Execute(args)
				case "broadcast":
					result = broadcastExec.Execute(args)
				default:
					if tc.Function.Name == "recall" {
						args["_session_id"] = sessionID
					}
					if t, ok := reg.Get(tc.Function.Name); ok {
						result = t.Execute(args)
					}
				}
				if verifyErr := reg.VerifyResult(tc.Function.Name, args, result); verifyErr != nil {
					result = toolpkg.ToolResult{
						Output: result.Output,
						Error:  "verification failed: " + verifyErr.Error(),
					}
				}
				return result
			}

			result := runToolAttempt()
			firstAttemptVerificationFailed := strings.HasPrefix(result.Error, "verification failed: ")
			if firstAttemptVerificationFailed {
				rememberToolFailure(*memoryURL, agentName, sessionID, tc.Function.Name, args, result, "verification_failed")
			}
			if result.Error != "" && *maxToolRetries > 0 && shouldRetryTool(tc.Function.Name) {
				for attempt := 1; attempt <= *maxToolRetries; attempt++ {
					sessionsMu.Lock()
					if trackedTasks[sessionID] != nil {
						trackedTasks[sessionID].ToolRetries++
					}
					sessionsMu.Unlock()
					log.Printf("[RETRY] tool=%s attempt=%d err=%s", tc.Function.Name, attempt, result.Error)
					time.Sleep(2 * time.Second)
					result = runToolAttempt()
					if result.Error == "" {
						log.Printf("[RECOVERY] tool=%s recovered on attempt=%d", tc.Function.Name, attempt+1)
						rememberToolRecovery(*memoryURL, agentName, sessionID, tc.Function.Name, args, attempt+1)
						break
					}
				}
			}
			if result.Error == "" && tc.Function.Name == "git-clone" {
				repoName := strings.TrimSpace(fmt.Sprint(args["repo"]))
				if repoName != "" && repoName != "<nil>" {
					if strings.TrimSpace(*memoryURL) != "" {
						result = appendCloneMemoryContext(*memoryURL, agentName, repoName, result)
					}
					if cloneTool, ok := reg.Get(tc.Function.Name); ok {
						if awarenessTool, ok := cloneTool.(interface {
							AppendProjectAwareness(repo string, output string) string
						}); ok {
							result.Output = awarenessTool.AppendProjectAwareness(repoName, result.Output)
						}
					}
				}
			}
			if result.Error != "" {
				rememberToolFailure(*memoryURL, agentName, sessionID, tc.Function.Name, args, result, "retry_exhausted")
			}
			if result.Error != "" {
				logJSON("info", "tool result", map[string]interface{}{"tool": tc.Function.Name, "success": false, "error": result.Error, "output": result.Output})
				validateContextMemoriesAsync(*memoryURL, sessionID, "contradiction")
			} else {
				logJSON("info", "tool result", map[string]interface{}{"tool": tc.Function.Name, "success": true})
				validateContextMemoriesAsync(*memoryURL, sessionID, "success")
				if tc.Function.Name == "write" {
					path := strings.TrimSpace(fmt.Sprint(args["path"]))
					if path != "" && path != "<nil>" {
						sessionsMu.Lock()
						if trackedTasks[sessionID] != nil {
							trackedTasks[sessionID].FilesWritten = append(trackedTasks[sessionID].FilesWritten, path)
						}
						sessionsMu.Unlock()
					}
				}
				if tc.Function.Name == "gitea" {
					action := strings.TrimSpace(fmt.Sprint(args["action"]))
					repo := truncateMemoryValue(fmt.Sprint(args["repo"]), 200)
					switch action {
					case "create-pr":
						head := truncateMemoryValue(fmt.Sprint(args["head"]), 200)
						title := truncateMemoryValue(fmt.Sprint(args["title"]), 200)
						prURL := truncateMemoryValue(extractFirstURL(result.Output), 300)
						if repo != "" && head != "" && title != "" && prURL != "" {
							rememberStructuredOutcome(*memoryURL, agentName, sessionID, tc.Function.Name, fmt.Sprintf(
								`PR_CREATED: repo=%q branch=%q title=%q pr_url=%q`,
								repo,
								head,
								title,
								prURL,
							))
						}
						var issueOwner, issueRepoName string
						var issueNum int
						sessionsMu.Lock()
						if trackedTasks[sessionID] != nil {
							trackedTasks[sessionID].PRCreated = true
							trackedTasks[sessionID].PRUrl = prURL
							trackedTasks[sessionID].BranchName = head
							issueOwner = trackedTasks[sessionID].IssueOwner
							issueRepoName = trackedTasks[sessionID].IssueRepo
							issueNum = trackedTasks[sessionID].IssueNumber
						}
						sessionsMu.Unlock()
						if issueNum > 0 && giteaTool != nil {
							go func(owner, repoName string, issueNum int, prURL string) {
								labels, err := giteaTool.GetIssueLabels(owner, repoName, issueNum)
								if err != nil {
									logJSON("warn", "auto-complete: get labels failed", map[string]interface{}{"issue": issueNum, "error": err.Error()})
									labels = []string{}
								}
								newLabels := []string{"status/done"}
								for _, l := range labels {
									if !strings.HasPrefix(l, "status/") {
										newLabels = append(newLabels, l)
									}
								}
								if err := giteaTool.ReplaceLabels(owner, repoName, issueNum, newLabels); err != nil {
									logJSON("warn", "auto-complete: label update failed", map[string]interface{}{"issue": issueNum, "error": err.Error()})
								}
								if err := giteaTool.PostComment(owner, repoName, issueNum, fmt.Sprintf("PR delivered: %s", prURL)); err != nil {
									logJSON("warn", "auto-complete: comment failed", map[string]interface{}{"issue": issueNum, "error": err.Error()})
								}
								if err := giteaTool.CloseIssue(owner, repoName, issueNum); err != nil {
									logJSON("warn", "auto-complete: close failed", map[string]interface{}{"issue": issueNum, "error": err.Error()})
								} else {
									logJSON("info", "auto-complete: issue "+fmt.Sprint(issueNum)+" closed with PR", nil)
								}
							}(issueOwner, issueRepoName, issueNum, prURL)
						}
					case "close-issue":
						issueNum := truncateMemoryValue(fmt.Sprint(args["issue"]), 50)
						if issueNum == "" || issueNum == "<nil>" {
							issueNum = truncateMemoryValue(fmt.Sprint(args["index"]), 50)
						}
						if repo != "" && issueNum != "" && issueNum != "<nil>" {
							rememberStructuredOutcome(*memoryURL, agentName, sessionID, tc.Function.Name, fmt.Sprintf(
								`ISSUE_CLOSED: repo=%q issue=%q`,
								repo,
								issueNum,
							))
						}
					case "get-issue", "list-issues":
						issueIdx := 0
						if v, ok := args["index"]; ok {
							switch n := v.(type) {
							case float64:
								issueIdx = int(n)
							case string:
								fmt.Sscanf(strings.TrimPrefix(n, "#"), "%d", &issueIdx)
							}
						}
						if issueIdx == 0 {
							if v, ok := args["issue"]; ok {
								switch n := v.(type) {
								case float64:
									issueIdx = int(n)
								case string:
									fmt.Sscanf(strings.TrimPrefix(n, "#"), "%d", &issueIdx)
								}
							}
						}
						issueOwner := strings.TrimSpace(fmt.Sprint(args["owner"]))
						issueRepoName := strings.TrimSpace(fmt.Sprint(args["repo"]))
						if issueOwner == "" || issueOwner == "<nil>" {
							issueOwner = "kit"
						}
						if issueRepoName == "" || issueRepoName == "<nil>" {
							issueRepoName = "hirdforge-tasks"
						}
						if strings.Contains(issueRepoName, "/") {
							parts := strings.SplitN(issueRepoName, "/", 2)
							issueOwner = parts[0]
							issueRepoName = parts[1]
						}
						if issueIdx > 0 {
							sessionsMu.Lock()
							if trackedTasks[sessionID] == nil {
								trackedTasks[sessionID] = &trackedTask{}
							}
							trackedTasks[sessionID].IssueNumber = issueIdx
							trackedTasks[sessionID].IssueOwner = issueOwner
							trackedTasks[sessionID].IssueRepo = issueRepoName
							sessionsMu.Unlock()
							logJSON("info", "task tracking: issue detected", map[string]interface{}{"session_id": sessionID, "issue": issueIdx, "owner": issueOwner, "repo": issueRepoName})
						}
					}
				}
			}
			if logTool != nil {
				inputBytes, _ := json.Marshal(args)
				out := result.Output
				if result.Error != "" {
					if out != "" {
						out = result.Error + "\n" + out
					} else {
						out = result.Error
					}
				}
				logTool(taskspkg.ToolLog{Name: tc.Function.Name, Input: string(inputBytes), Output: out})
			}
			maybeRememberAction(*memoryURL, agentName, sessionID, tc.Function.Name, args, result)
			if !emit(sseChunk{Type: "tool_result", Tool: tc.Function.Name, Result: result, Done: false}) {
				return result
			}
			toolContent := result.Output
			if result.Error != "" {
				if toolContent != "" {
					toolContent = result.Error + "\n" + toolContent
				} else {
					toolContent = result.Error
				}
			}
			if !strings.HasPrefix(tc.ID, "xml_") && !strings.HasPrefix(tc.ID, "mm_") {
				messages = append(messages, message{Role: "tool", ToolCallID: tc.ID, Content: toolContent})
			}
			return result
		}

		executeToolCalls := func(calls []toolCall) {
			for _, tc := range calls {
				_ = executeOneToolCall(tc)
			}
		}

		for i := 0; i < 10; i++ {
			if *maxContext > 0 && len(messages)-1 > int(float64(*maxContext)*0.8) {
				trimmed := progressiveTrim(messages[1:], *maxContext)
				messages = append([]message{messages[0]}, trimmed...)
			}
			inferenceCtx, cancel := withInferenceTimeout(ctx)
			resp, err := callOllamaNonStreamingWithContext(inferenceCtx, messages, toolDefs, *inferenceURL, *model, *apiKey)
			cancel()
			if err != nil {
				return "", err
			}
			if len(resp.Choices) == 0 {
				break
			}
			assistant := resp.Choices[0].Message
			if len(assistant.ToolCalls) == 0 && strings.Contains(assistant.Content, "<minimax:tool_call>") {
				mmCalls, cleaned := parseMiniMaxToolCalls(assistant.Content)
				if len(mmCalls) > 0 {
					assistant.ToolCalls = mmCalls
					assistant.Content = cleaned
				}
			}
			if len(assistant.ToolCalls) == 0 {
				break
			}
			messages = append(messages, message{Role: assistant.Role, Content: assistant.Content, ToolCalls: assistant.ToolCalls})
			executeToolCalls(assistant.ToolCalls)
			if i == 19 {
				_ = emit(sseChunk{Type: "content", Content: "tool call limit reached", Done: false})
			}
		}

		var full strings.Builder
		hadXMLToolCalls := false
		streamIterationHadToolCalls := false
		var xmlToolResults []string
		insideThink := false
		streamCtx, cancelStream := withInferenceTimeout(ctx)
		if *maxContext > 0 && len(messages)-1 > int(float64(*maxContext)*0.8) {
			trimmed := progressiveTrim(messages[1:], *maxContext)
			messages = append([]message{messages[0]}, trimmed...)
		}
		for evt := range streamOllamaWithContext(streamCtx, messages, nil, *inferenceURL, *model, *apiKey) {
			if evt.Err != nil {
				errText := evt.Err.Error()
				full.WriteString(errText)
				if !emit(sseChunk{Type: "content", Content: errText, Done: false}) {
					cancelStream()
					return full.String(), context.Canceled
				}
				break
			}
			if len(evt.ToolCalls) > 0 {
				hadXMLToolCalls = true
				streamIterationHadToolCalls = true
				for _, tc := range evt.ToolCalls {
					result := executeOneToolCall(tc)
					out := result.Output
					if result.Error != "" {
						out = "ERROR: " + result.Error
					}
					if len(out) > 500 {
						out = out[:500] + "...[truncated]"
					}
					xmlToolResults = append(xmlToolResults, fmt.Sprintf("[%s]: %s", tc.Function.Name, out))
				}
				continue
			}
			chunk := evt.Content
			cleaned := ""
			combined := chunk
			if insideThink {
				if idx := strings.Index(combined, "</think>"); idx >= 0 {
					insideThink = false
					combined = combined[idx+len("</think>"):]
				} else {
					continue
				}
			}
			if idx := strings.Index(combined, "<think>"); idx >= 0 {
				cleaned = combined[:idx]
				insideThink = true
				if end := strings.Index(combined[idx:], "</think>"); end >= 0 {
					insideThink = false
					cleaned += combined[idx+end+len("</think>"):]
				}
			} else {
				cleaned = combined
			}
			cleaned = orphanThinkRe.ReplaceAllString(cleaned, "")
			if cleaned == "" {
				continue
			}
			cleanedChunk, xmlResults := extractAndExecuteXMLToolCalls(cleaned, func(tc toolCall) ToolResult {
				result := executeOneToolCall(tc)
				out := result.Output
				if result.Error != "" {
					out = "ERROR: " + result.Error
				}
				if len(out) > 500 {
					out = out[:500] + "...[truncated]"
				}
				xmlToolResults = append(xmlToolResults, fmt.Sprintf("[%s]: %s", tc.Function.Name, out))
				return result
			})
			if len(xmlResults) > 0 {
				hadXMLToolCalls = true
			}
			if cleanedChunk == "" {
				continue
			}
			full.WriteString(cleanedChunk)
			if !emit(sseChunk{Type: "content", Content: cleanedChunk, Done: false}) {
				cancelStream()
				return full.String(), context.Canceled
			}
		}
		cancelStream()
		finalContent := full.String()
		if strings.TrimSpace(finalContent) == "" && !streamIterationHadToolCalls {
			log.Printf("[STALL] agent=%s model=%s session=%s — no output produced", agentName, *model, sessionID)
			atomic.AddInt64(&metricsStallsTotal, 1)
			rememberToolFailure(*memoryURL, agentName, sessionID, "inference", map[string]interface{}{}, toolpkg.ToolResult{Error: "no output produced"}, "stall")
		}
		if strings.Contains(finalContent, "<minimax:tool_call>") {
			cleanedFinal, postResults := extractAndExecuteXMLToolCalls(finalContent, func(tc toolCall) ToolResult {
				result := executeOneToolCall(tc)
				out := result.Output
				if result.Error != "" {
					out = "ERROR: " + result.Error
				}
				if len(out) > 500 {
					out = out[:500] + "...[truncated]"
				}
				xmlToolResults = append(xmlToolResults, fmt.Sprintf("[%s]: %s", tc.Function.Name, out))
				return result
			})
			if len(postResults) > 0 {
				hadXMLToolCalls = true
				full.Reset()
				full.WriteString(cleanedFinal)
				if !emit(sseChunk{Type: "replace", Content: cleanedFinal, Done: false}) {
					return cleanedFinal, context.Canceled
				}
			}
		}
		cleaned := strings.TrimSpace(full.String())
		if cleaned != "" {
			messages = append(messages, message{Role: "assistant", Content: cleaned})
		}
		if hadXMLToolCalls && len(xmlToolResults) > 0 {
			resultMsg := "Tool execution results:\n\n" + strings.Join(xmlToolResults, "\n\n")
			messages = append(messages, message{Role: "user", Content: resultMsg})
			for i := 0; i < 3; i++ {
				if *maxContext > 0 && len(messages)-1 > int(float64(*maxContext)*0.8) {
					trimmed := progressiveTrim(messages[1:], *maxContext)
					messages = append([]message{messages[0]}, trimmed...)
				}
				inferenceCtx, cancel := withInferenceTimeout(ctx)
				resp, err := callOllamaNonStreamingWithContext(inferenceCtx, messages, toolDefs, *inferenceURL, *model, *apiKey)
				cancel()
				if err != nil {
					return cleaned, err
				}
				if len(resp.Choices) == 0 {
					break
				}
				assistant := resp.Choices[0].Message
				assistant.Content = stripThinkTags(assistant.Content)
				if len(assistant.ToolCalls) == 0 && strings.Contains(assistant.Content, "<minimax:tool_call>") {
					mmCalls, mmCleaned := parseMiniMaxToolCalls(assistant.Content)
					if len(mmCalls) > 0 {
						assistant.ToolCalls = mmCalls
						assistant.Content = mmCleaned
					}
				}
				if len(assistant.ToolCalls) > 0 {
					messages = append(messages, message{Role: assistant.Role, Content: assistant.Content, ToolCalls: assistant.ToolCalls})
					executeToolCalls(assistant.ToolCalls)
					continue
				}
				chunkContent, xmlResults := extractAndExecuteXMLToolCalls(assistant.Content, func(tc toolCall) ToolResult {
					result := executeOneToolCall(tc)
					out := result.Output
					if result.Error != "" {
						out = "ERROR: " + result.Error
					}
					if len(out) > 500 {
						out = out[:500] + "...[truncated]"
					}
					xmlToolResults = append(xmlToolResults, fmt.Sprintf("[%s]: %s", tc.Function.Name, out))
					return result
				})
				if chunkContent != "" {
					full.WriteString(chunkContent)
					if !emit(sseChunk{Type: "content", Content: chunkContent, Done: false}) {
						return full.String(), context.Canceled
					}
					messages = append(messages, message{Role: "assistant", Content: chunkContent})
				}
				if len(xmlResults) == 0 {
					break
				}
				resultMsg = "Tool execution results:\n\n" + strings.Join(xmlToolResults, "\n\n")
				messages = append(messages, message{Role: "user", Content: resultMsg})
			}
			cleaned = strings.TrimSpace(full.String())
		}

		sessionsMu.Lock()
		sessions[sessionID] = append(sessions[sessionID], message{Role: "user", Content: content}, message{Role: "assistant", Content: cleaned})
		sessionsMu.Unlock()
		if *memoryURL != "" && hadToolCalls {
			sessionsMu.Lock()
			currentHistory := append([]message(nil), sessions[sessionID]...)
			sessionsMu.Unlock()
			go persistEpisodicState(*memoryURL, agentName, sessionID, currentHistory, agentName)
		}
		return cleaned, nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, dashboardHTML)
	})
	mux.HandleFunc("/.well-known/agent.json", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		host, _ := os.Hostname()
		if host == "" {
			host = "localhost"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"name":        agentName,
			"description": "Valhalla AI agent",
			"url":         fmt.Sprintf("http://%s:%s", host, *port),
			"version":     "0.0.2",
			"capabilities": map[string]interface{}{
				"streaming":  true,
				"tools":      enabledTools,
				"delegation": len(peers) > 0,
			},
			"peers":              peerNames,
			"defaultInputModes":  []string{"text"},
			"defaultOutputModes": []string{"text"},
		})
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(statusPayload())
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(statusPayload())
	})
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
			if sessionID == "" {
				http.Error(w, "missing session_id", http.StatusBadRequest)
				return
			}
			sessionsMu.Lock()
			delete(sessions, sessionID)
			delete(seenSessions, sessionID)
			delete(skillNudgeInjected, sessionID)
			delete(sessionContextMemoryIDs, sessionID)
			delete(sessionValidatedIDs, sessionID)
			delete(episodicStates, sessionID)
			delete(trackedTasks, sessionID)
			sessionsMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"deleted":    true,
				"session_id": sessionID,
			})
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sessionsMu.Lock()
		ids := make([]string, 0, len(sessions))
		for id := range sessions {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		out := make([]map[string]interface{}, 0, len(ids))
		for _, id := range ids {
			out = append(out, map[string]interface{}{"session_id": id, "messages": len(sessions[id])})
		}
		sessionsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/api/v1/files", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		files, err := listWorkspaceFiles(*workspace, 1000)
		if err != nil {
			incError("workspace list failed", err, nil)
			http.Error(w, "failed listing files", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(files)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = io.WriteString(w, metricsText())
	})
	mux.HandleFunc("/webhook/gitea", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024))
		if err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(*webhookSecret) == "" {
			http.Error(w, "webhook secret not configured", http.StatusServiceUnavailable)
			return
		}
		sig := r.Header.Get("X-Gitea-Signature")
		if !validateGiteaHMAC(*webhookSecret, body, sig) {
			incError("webhook signature validation failed", fmt.Errorf("invalid signature"), map[string]interface{}{
				"event":    r.Header.Get("X-Gitea-Event"),
				"delivery": r.Header.Get("X-Gitea-Delivery"),
			})
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		logJSON("info", "gitea webhook received", map[string]interface{}{
			"event":       r.Header.Get("X-Gitea-Event"),
			"delivery":    r.Header.Get("X-Gitea-Delivery"),
			"payload_len": len(body),
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/message", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		startReq := time.Now()
		atomic.AddInt64(&requestCount, 1)
		atomic.AddInt64(&metricsRequestsTotal, 1)
		atomic.AddInt64(&metricsActiveRequests, 1)
		defer func() {
			atomic.AddInt64(&metricsActiveRequests, -1)
			setLastDuration(time.Since(startReq))
		}()
		logJSON("info", "request received", nil)
		var req messageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			incError("invalid request body", err, nil)
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		sessionID := req.SessionID
		if sessionID == "" {
			sessionID = fmt.Sprintf("%x", rand.Int63())
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		emit := func(chunk sseChunk) bool {
			writeSSE(w, chunk)
			flusher.Flush()
			return true
		}
		if _, err := processConversation(r.Context(), sessionID, req.Content, emit, nil); err != nil {
			incError("message processing failed", err, nil)
			writeSSE(w, sseChunk{Type: "content", Content: err.Error(), Done: false})
		}
		writeSSE(w, sseChunk{Type: "done", Done: true, SessionID: sessionID})
		flusher.Flush()
	})
	mux.HandleFunc("/tasks/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req taskSendRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Content) == "" {
			http.Error(w, "content is required", http.StatusBadRequest)
			return
		}
		task := taskStore.Create(taskspkg.Task{
			ID:      newTaskID(),
			Agent:   agentName,
			From:    strings.TrimSpace(req.From),
			Content: req.Content,
			Status:  "submitted",
		})
		ctx, cancel := context.WithCancel(context.Background())
		taskCancelMu.Lock()
		taskCancels[task.ID] = cancel
		taskCancelMu.Unlock()
		go func(taskID string, content string) {
			defer func() {
				taskCancelMu.Lock()
				delete(taskCancels, taskID)
				taskCancelMu.Unlock()
			}()
			current, ok := taskStore.Get(taskID)
			if !ok {
				return
			}
			trackerKey := agentName + ":" + current.ID
			if _, err := taskTracker.Dispatch(trackerKey, trackerKey); err != nil {
				current.Status = "failed"
				current.Error = "completion_tracker_init_failed"
				taskStore.Update(current)
				notifyGateway(*gatewayURL, "task", agentName, fmt.Sprintf("failed task %s (from %s): %s", current.ID, current.From, current.Error))
				return
			}
			current.Status = "working"
			current.Error = ""
			taskStore.Update(current)
			notifyGateway(*gatewayURL, "task", agentName, fmt.Sprintf("started task %s (from %s)", current.ID, current.From))
			reportTrackedState := func(record tasklifepkg.TaskRecord) {
				if !sovereignStateEnabled(sovereignStates, record.State) {
					return
				}
				resultText := record.Result
				if record.State == tasklifepkg.StateNudged {
					resultText = record.Nudge
				}
				sovereignReporter.Report(tasklifepkg.TaskEvent{
					From:      current.From,
					TaskID:    current.ID,
					Agent:     agentName,
					State:     record.State,
					Result:    resultText,
					Timestamp: record.UpdatedAt,
				})
			}
			appendToolLog := func(log taskspkg.ToolLog) {
				cur, ok := taskStore.Get(taskID)
				if !ok {
					return
				}
				cur.Tools = append(cur.Tools, log)
				taskStore.Update(cur)
			}
			pendingContent := content
			var result string
			var err error
			completionGates := []tasklifepkg.CompletionGate(nil)
			if strings.TrimSpace(*requirePRPattern) != "" {
				completionGates = []tasklifepkg.CompletionGate{{
					Name:    "pr_url",
					Pattern: *requirePRPattern,
					Nudge:   fmt.Sprintf("Your completion message must include a PR reference matching %q. Reply with an updated completion message that includes it.", *requirePRPattern),
				}}
			}
			for {
				result, err = processConversation(ctx, taskID, pendingContent, nil, appendToolLog)
				if err != nil || len(completionGates) == 0 {
					break
				}
				gateResult, gateErr := tasklifepkg.CheckCompletionGates(result, completionGates)
				if gateErr != nil {
					err = fmt.Errorf("completion gate check failed: %w", gateErr)
					break
				}
				if gateResult.Passed {
					if record, completeErr := taskTracker.Complete(trackerKey, result, true); completeErr == nil {
						reportTrackedState(record)
					}
					break
				}
				record, ok := taskTracker.Task(trackerKey)
				if !ok {
					err = fmt.Errorf("completion tracker missing for task %s", taskID)
					break
				}
				if taskNudgeCount(record) >= *completionMaxNudges {
					if failedRecord, completeErr := taskTracker.Complete(trackerKey, "no_pr_url", false); completeErr == nil {
						reportTrackedState(failedRecord)
					}
					err = fmt.Errorf("no_pr_url")
					break
				}
				nudge := "Completion requirements were not met."
				if len(gateResult.Nudges) > 0 {
					nudge = strings.Join(gateResult.Nudges, "\n")
				}
				if nudgedRecord, nudgeErr := taskTracker.Nudge(trackerKey, nudge); nudgeErr != nil {
					err = fmt.Errorf("completion tracker nudge failed: %w", nudgeErr)
					break
				} else {
					reportTrackedState(nudgedRecord)
				}
				pendingContent = nudge
			}
			cur, ok := taskStore.Get(taskID)
			if !ok {
				return
			}
			if ctx.Err() == context.Canceled {
				cur.Status = "failed"
				cur.Error = "cancelled"
				taskStore.Update(cur)
				notifyGateway(*gatewayURL, "task", agentName, fmt.Sprintf("cancelled task %s (from %s)", cur.ID, cur.From))
				return
			}
			if err != nil {
				cur.Status = "failed"
				cur.Error = err.Error()
				taskStore.Update(cur)
				notifyGateway(*gatewayURL, "task", agentName, fmt.Sprintf("failed task %s (from %s): %s", cur.ID, cur.From, cur.Error))
				return
			}
			if len(completionGates) == 0 {
				if record, completeErr := taskTracker.Complete(trackerKey, result, true); completeErr == nil {
					reportTrackedState(record)
				}
			}
			cur.Status = "completed"
			cur.Result = result
			cur.Error = ""
			taskStore.Update(cur)
			summary := cur.Result
			if len(summary) > 200 {
				summary = summary[:200] + "..."
			}
			notifyGateway(*gatewayURL, "task", agentName, fmt.Sprintf("completed task %s (from %s): %s", cur.ID, cur.From, summary))
		}(task.ID, req.Content)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": task.ID, "status": task.Status})
	})
	mux.HandleFunc("/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		agentFilter := strings.TrimSpace(r.URL.Query().Get("agent"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(taskStore.List(agentFilter, status))
	})
	mux.HandleFunc("/tasks/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/tasks/")
		if strings.HasSuffix(path, "/cancel") {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			taskID := strings.Trim(strings.TrimSuffix(path, "/cancel"), "/")
			task, ok := taskStore.Get(taskID)
			if !ok {
				http.NotFound(w, r)
				return
			}
			task.Status = "failed"
			task.Error = "cancelled"
			taskStore.Update(task)
			taskCancelMu.Lock()
			cancel := taskCancels[taskID]
			taskCancelMu.Unlock()
			if cancel != nil {
				cancel()
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"id": taskID, "status": "failed"})
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		taskID := strings.Trim(path, "/")
		task, ok := taskStore.Get(taskID)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(task)
	})

	addr := ":" + *port
	logJSON("info", "agent started", map[string]interface{}{
		"port":  *port,
		"model": *model,
		"tools": enabledTools,
		"peers": peerNames,
	})
	die("server failed", http.ListenAndServe(addr, mux))
}
