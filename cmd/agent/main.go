package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	streamChunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
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
	messageRequest struct {
		Content   string `json:"content"`
		SessionID string `json:"session_id"`
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
var sessions = map[string][]message{}
var (
	startTime    = time.Now()
	requestCount int64
	toolCalls    int64
	modelName    string
	enabledTools []string
)

type delegateTool struct {
	peers map[string]string
}

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
	peerURL, ok := t.peers[agent]
	if !ok {
		names := make([]string, 0, len(t.peers))
		for name := range t.peers {
			names = append(names, name)
		}
		sort.Strings(names)
		return toolpkg.ToolResult{Error: fmt.Sprintf("unknown agent: %s. Available: %s", agent, strings.Join(names, ", "))}
	}

	body, _ := json.Marshal(map[string]string{
		"content":    task,
		"session_id": fmt.Sprintf("delegate-%x", rand.Int63()),
	})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(peerURL, "/")+"/message", bytes.NewReader(body))
	if err != nil {
		return toolpkg.ToolResult{Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return toolpkg.ToolResult{Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return toolpkg.ToolResult{Error: fmt.Sprintf("peer returned %s: %s", resp.Status, strings.TrimSpace(string(b)))}
	}

	var full strings.Builder
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			return toolpkg.ToolResult{Error: err.Error()}
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := line[6:]
		if payload == "[DONE]" {
			break
		}
		var evt struct {
			Type    string `json:"type"`
			Content string `json:"content"`
			Done    bool   `json:"done"`
		}
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			continue
		}
		if evt.Type == "content" {
			full.WriteString(evt.Content)
		}
		if evt.Done {
			break
		}
	}
	return toolpkg.ToolResult{Output: full.String()}
}

func die(msg string, err error) {
	fmt.Fprintln(os.Stderr, msg+":", err)
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

func callOllamaNonStreaming(messages []message, defs []toolDef, inferenceURL, model, apiKey string) (chatResponse, error) {
	endpoint := strings.TrimRight(inferenceURL, "/") + "/v1/chat/completions"
	body, err := json.Marshal(chatRequest{Model: model, Messages: messages, Stream: false, Tools: defs})
	if err != nil {
		return chatResponse{}, fmt.Errorf("failed to marshal request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
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
	return out, nil
}

func streamOllama(messages []message, defs []toolDef, inferenceURL, model, apiKey string) <-chan string {
	chunks := make(chan string)
	go func() {
		defer close(chunks)
		endpoint := strings.TrimRight(inferenceURL, "/") + "/v1/chat/completions"
		body, err := json.Marshal(chatRequest{Model: model, Messages: messages, Stream: true, Tools: defs})
		if err != nil {
			chunks <- "failed to marshal request: " + err.Error()
			return
		}
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			chunks <- "failed to create request: " + err.Error()
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			chunks <- "request failed: " + err.Error()
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			chunks <- fmt.Sprintf("inference returned %s: %s", resp.Status, strings.TrimSpace(string(b)))
			return
		}
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err == io.EOF {
				break
			}
			if err != nil {
				chunks <- "failed reading stream: " + err.Error()
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
				chunks <- "failed to parse chunk: " + err.Error()
				return
			}
			if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
				chunks <- chunk.Choices[0].Delta.Content
			}
		}
	}()
	return chunks
}

func writeSSE(w http.ResponseWriter, payload sseChunk) {
	b, _ := json.Marshal(payload)
	fmt.Fprintf(w, "data: %s\n\n", b)
}

func trimMessages(msgs []message, maxPairs int) []message {
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
	fmt.Printf("trimmed context: kept %d of %d messages\n", len(trimmed), len(msgs))
	return trimmed
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

func main() {
	rand.Seed(time.Now().UnixNano())
	soulPath := flag.String("soul", "./soul.md", "path to SOUL.md")
	port := flag.String("port", "8081", "HTTP port")
	inferenceURL := flag.String("inference-url", "http://localhost:11434", "inference base URL")
	model := flag.String("model", "qwen3:30b", "model name")
	apiKey := flag.String("api-key", "", "API key for inference backend (optional)")
	maxContext := flag.Int("max-context", 20, "max number of user/assistant message pairs to keep (0 disables trimming)")
	workspace := flag.String("workspace", "./workspace", "tool workspace directory")
	peersFlag := flag.String("peers", "", "comma-separated name=url peer agents")
	toolsFlag := flag.String("tools", "exec,read,write", "comma-separated enabled tools")
	flag.Parse()

	soulBytes, err := os.ReadFile(*soulPath)
	if err != nil {
		die("failed to read soul file", err)
	}
	soul := string(soulBytes)
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
	agentName := agentNameFromSoul(soul)

	reg := toolpkg.NewRegistry()
	enabled := map[string]bool{}
	for _, name := range strings.Split(*toolsFlag, ",") {
		if name = strings.TrimSpace(name); name != "" {
			enabled[name] = true
		}
	}
	if enabled["exec"] {
		reg.Register(toolpkg.NewExecTool())
	}
	if enabled["read"] {
		reg.Register(toolpkg.NewReadTool(*workspace))
	}
	if enabled["write"] {
		reg.Register(toolpkg.NewWriteTool(*workspace))
	}
	if enabled["delegate"] || len(peers) > 0 {
		reg.Register(&delegateTool{peers: peers})
	}
	toolDefs := buildToolDefs(reg)
	enabledTools = reg.List()
	modelName = *model

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
	mux.HandleFunc("/message", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		atomic.AddInt64(&requestCount, 1)
		var req messageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		sessionID := req.SessionID
		if sessionID == "" {
			sessionID = fmt.Sprintf("%x", rand.Int63())
		}

		sessionsMu.Lock()
		history := append([]message(nil), sessions[sessionID]...)
		sessionsMu.Unlock()
		messages := append(append([]message{{Role: "system", Content: soul}}, history...), message{Role: "user", Content: req.Content})
		messages = trimMessages(messages, *maxContext)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		for i := 0; i < 10; i++ {
			resp, err := callOllamaNonStreaming(messages, toolDefs, *inferenceURL, *model, *apiKey)
			if err != nil {
				writeSSE(w, sseChunk{Type: "content", Content: err.Error(), Done: false})
				writeSSE(w, sseChunk{Type: "done", Done: true, SessionID: sessionID})
				flusher.Flush()
				return
			}
			if len(resp.Choices) == 0 || len(resp.Choices[0].Message.ToolCalls) == 0 {
				break
			}
			assistant := resp.Choices[0].Message
			messages = append(messages, message{Role: assistant.Role, Content: assistant.Content, ToolCalls: assistant.ToolCalls})
			for _, tc := range assistant.ToolCalls {
				atomic.AddInt64(&toolCalls, 1)
				args := map[string]interface{}{}
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					args = map[string]interface{}{"_raw": tc.Function.Arguments}
				}
				writeSSE(w, sseChunk{Type: "tool_call", Tool: tc.Function.Name, Args: args, Done: false})
				flusher.Flush()
				result := toolpkg.ToolResult{Error: "unknown tool: " + tc.Function.Name}
				switch tc.Function.Name {
				case "delegate":
					result = (&delegateTool{peers: peers}).Execute(args)
				default:
					if t, ok := reg.Get(tc.Function.Name); ok {
						result = t.Execute(args)
					}
				}
				writeSSE(w, sseChunk{Type: "tool_result", Tool: tc.Function.Name, Result: result, Done: false})
				flusher.Flush()
				toolContent := result.Output
				if result.Error != "" {
					if toolContent != "" {
						toolContent = result.Error + "\n" + toolContent
					} else {
						toolContent = result.Error
					}
				}
				messages = append(messages, message{Role: "tool", ToolCallID: tc.ID, Content: toolContent})
			}
			if i == 9 {
				writeSSE(w, sseChunk{Type: "content", Content: "tool call limit reached", Done: false})
			}
		}

		var full strings.Builder
		for chunk := range streamOllama(messages, nil, *inferenceURL, *model, *apiKey) {
			full.WriteString(chunk)
			writeSSE(w, sseChunk{Type: "content", Content: chunk, Done: false})
			flusher.Flush()
		}
		sessionsMu.Lock()
		sessions[sessionID] = append(sessions[sessionID], message{Role: "user", Content: req.Content}, message{Role: "assistant", Content: full.String()})
		sessionsMu.Unlock()
		writeSSE(w, sseChunk{Type: "done", Done: true, SessionID: sessionID})
		flusher.Flush()
	})

	addr := ":" + *port
	fmt.Printf("Valhalla Agent listening on %s\n", addr)
	fmt.Printf("SOUL loaded: %s (%d bytes)\n", *soulPath, len(soulBytes))
	fmt.Printf("Inference: %s model=%s\n", *inferenceURL, *model)
	die("server failed", http.ListenAndServe(addr, mux))
}
