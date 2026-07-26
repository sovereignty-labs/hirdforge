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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tasklifepkg "git.hirdforge.com/kit/hirdforge/pkg/tasklife"
	taskspkg "git.hirdforge.com/kit/hirdforge/pkg/tasks"
	toolpkg "git.hirdforge.com/kit/hirdforge/pkg/tools"
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
		Content   string `json:"content"`
		From      string `json:"from"`
		SessionID string `json:"session_id,omitempty"`
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
	prRef struct {
		Owner string `json:"owner"`
		Repo  string `json:"repo"`
		Index int    `json:"index"`
	}
	agentWorkspaceState struct {
		CurrentPR         *prRef `json:"current_pr,omitempty"`
		CurrentReviewFile string `json:"current_review_file,omitempty"`
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
	// Hermes/Qwen function-call format: <tool_call><function=NAME><parameter=KEY>VALUE</parameter>…</function></tool_call>.
	// Qwen models (qwen27b-worker, qwen-reserved) emit this; without a parser it leaks
	// as raw text into the reply (observed live in the cockpit 2026-07-26).
	qwenToolCallRE  = regexp.MustCompile(`(?s)<tool_call>(.*?)</tool_call>`)
	qwenFunctionRE  = regexp.MustCompile(`<function=([^>\s]+)\s*>`)
	qwenParameterRE = regexp.MustCompile(`(?s)<parameter=([^>\s]+)\s*>(.*?)</parameter>`)

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

var (
	portValue       string
	grpcPortValue   string
	giteaURLValue   string
	giteaTokenValue string
	gatewayURLValue string
	memoryURLValue  string
)

// logWriter is the sink for structured logs. It defaults to stdout; tests swap
// it to capture and assert on emitted log events (see captureLogs in the tests).
var logWriter io.Writer = os.Stdout

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
	_, _ = logWriter.Write(b)
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

// parseQwenToolCalls extracts Hermes/Qwen-format tool calls (<tool_call><function=
// NAME><parameter=KEY>VALUE</parameter>…</function></tool_call>) that the OpenAI
// tool_calls field did not carry. Without this the raw XML leaks into the reply —
// the interlocutor looked broken in the cockpit until this landed.
func parseQwenToolCalls(content string) ([]toolCall, string) {
	blocks := qwenToolCallRE.FindAllStringSubmatch(content, -1)
	if len(blocks) == 0 {
		return nil, content
	}
	calls := make([]toolCall, 0, len(blocks))
	for i, m := range blocks {
		block := m[1]
		fn := qwenFunctionRE.FindStringSubmatch(block)
		if len(fn) < 2 {
			continue
		}
		name := strings.TrimSpace(fn[1])
		if name == "" {
			continue
		}
		args := map[string]string{}
		for _, pm := range qwenParameterRE.FindAllStringSubmatch(block, -1) {
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
			ID:       fmt.Sprintf("qw_%d", i),
			Type:     "function",
			Function: toolCallFunction{Name: name, Arguments: string(b)},
		})
	}
	if len(calls) == 0 {
		return nil, content
	}
	return calls, qwenToolCallRE.ReplaceAllString(content, "")
}

// hasFinalToolCallXML reports whether content carries a minimax OR Hermes/Qwen
// tool-call block — the guard for the final-content handler, kept as a helper so
// callers stay single-condition.
func hasFinalToolCallXML(content string) bool {
	return strings.Contains(content, "<minimax:tool_call>") || strings.Contains(content, "<tool_call>")
}

// extractAndExecuteXMLToolCalls executes any XML tool calls in content — both the
// minimax (<minimax:tool_call><invoke>) and Hermes/Qwen (<tool_call><function=…>)
// formats — and returns the content with EVERY such block stripped, so raw
// tool-call XML can never leak to the operator, even a block that would not parse
// into a call (the #149 leak). A malformed match is skipped, not fatal.
func extractAndExecuteXMLToolCalls(content string, execute func(toolCall) ToolResult) (cleanedContent string, toolResults []ToolResult) {
	var calls []toolCall
	for i, m := range xmlToolCallRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 3 {
			continue
		}
		toolName := strings.TrimSpace(m[1])
		if toolName == "" {
			continue
		}
		args := map[string]string{}
		for _, pm := range xmlParamRe.FindAllStringSubmatch(m[2], -1) {
			if len(pm) >= 3 {
				if key := strings.TrimSpace(pm[1]); key != "" {
					args[key] = strings.TrimSpace(pm[2])
				}
			}
		}
		argBytes, err := json.Marshal(args)
		if err != nil {
			continue
		}
		calls = append(calls, toolCall{
			ID:       fmt.Sprintf("xml_%d", i),
			Type:     "function",
			Function: toolCallFunction{Name: toolName, Arguments: string(argBytes)},
		})
	}
	qwCalls, _ := parseQwenToolCalls(content)
	calls = append(calls, qwCalls...)
	cleaned := strings.TrimSpace(qwenToolCallRE.ReplaceAllString(xmlToolCallRe.ReplaceAllString(content, ""), ""))
	if len(calls) == 0 {
		if cleaned == strings.TrimSpace(content) {
			return content, nil
		}
		return cleaned, nil
	}
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

// validPeerRoles mirrors the gateway's validAgentRoles set so that --peers and
// --agents accept the same name=url[:role][:warband] entries.
var validPeerRoles = map[string]bool{
	"builder":     true,
	"reviewer":    true,
	"coordinator": true,
	"assistant":   true,
	"specialist":  true,
	"architect":   true,
}

// parsePeers parses a comma-separated --peers value into a URL map and a role
// map (keyed by peer name). Entries are name=url[:role][:warband]; role and
// warband segments are optional and only recognized when role matches
// validPeerRoles. URLs may contain their own colons (scheme/port), so role
// detection works right-to-left and bails out as soon as the candidate isn't
// a known role.
func parsePeers(raw string) (map[string]string, map[string]string, error) {
	peers := map[string]string{}
	roles := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return peers, roles, nil
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, rest, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(rest) == "" {
			return nil, nil, fmt.Errorf("invalid peer entry %q", part)
		}
		name = strings.TrimSpace(name)
		rest = strings.TrimSpace(rest)
		role := ""
		segments := strings.Split(rest, ":")
		if len(segments) >= 3 {
			candidate := strings.TrimSpace(segments[len(segments)-2])
			if validPeerRoles[candidate] {
				role = candidate
				rest = strings.TrimSpace(strings.Join(segments[:len(segments)-2], ":"))
			}
		}
		if role == "" && len(segments) >= 2 {
			candidate := strings.TrimSpace(segments[len(segments)-1])
			if validPeerRoles[candidate] {
				role = candidate
				rest = strings.TrimSpace(strings.Join(segments[:len(segments)-1], ":"))
			}
		}
		if _, exists := peers[name]; exists {
			return nil, nil, fmt.Errorf("duplicate peer name %q", name)
		}
		peers[name] = strings.TrimRight(rest, "/")
		if role != "" {
			roles[name] = role
		}
	}
	return peers, roles, nil
}

func useResponsesAPI(model string) bool {
	return strings.Contains(strings.ToLower(model), "codex")
}

// useAnthropicAPI returns true for model names containing "claude".
func useAnthropicAPI(model string) bool {
	return strings.Contains(strings.ToLower(model), "claude")
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

func streamOllamaWithContext(ctx context.Context, messages []message, defs []toolDef, inferenceURL, model, apiKey string) <-chan inferenceStreamEvent {
	if useAnthropicAPI(model) {
		return streamAnthropicWithContext(ctx, messages, defs, inferenceURL, model, apiKey)
	}
	if useResponsesAPI(model) {
		return streamResponsesWithContext(ctx, messages, defs, inferenceURL, model, apiKey)
	}
	return streamChatCompletionsWithContext(ctx, messages, defs, inferenceURL, model, apiKey)
}

const (
	repetitionBufferSize    = 500
	repetitionCheckInterval = 50
	repetitionWindowSize    = 100
	repetitionThreshold     = 3
)

// repetitionDetector flags streaming output that is stuck in a loop by looking
// for the same window of trailing bytes repeating multiple times across an
// assembled rolling buffer. It works on assembled text instead of per-chunk
// equality, so the detection survives the inference server splitting a
// repeated token across arbitrary chunk boundaries.
type repetitionDetector struct {
	buf            []byte
	sinceLastCheck int
}

func newRepetitionDetector() *repetitionDetector {
	return &repetitionDetector{buf: make([]byte, 0, repetitionBufferSize)}
}

func (d *repetitionDetector) observe(content string) bool {
	if content == "" {
		return false
	}
	d.buf = append(d.buf, content...)
	if len(d.buf) > repetitionBufferSize {
		d.buf = d.buf[len(d.buf)-repetitionBufferSize:]
	}
	d.sinceLastCheck += len(content)
	if d.sinceLastCheck < repetitionCheckInterval {
		return false
	}
	d.sinceLastCheck = 0
	if len(d.buf) < repetitionWindowSize {
		return false
	}
	window := d.buf[len(d.buf)-repetitionWindowSize:]
	return bytes.Count(d.buf, window) >= repetitionThreshold
}

// errStreamCanceled is a sentinel returned from streaming-helper closures when
// the producer goroutine should exit because the context was canceled. It is
// never surfaced to the consumer.
var errStreamCanceled = fmt.Errorf("stream canceled")

func streamChatCompletionsWithContext(ctx context.Context, messages []message, defs []toolDef, inferenceURL, model, apiKey string) <-chan inferenceStreamEvent {
	chunks := make(chan inferenceStreamEvent)
	go func() {
		defer close(chunks)
		send := func(evt inferenceStreamEvent) bool {
			select {
			case chunks <- evt:
				return true
			case <-ctx.Done():
				return false
			}
		}
		endpoint := strings.TrimRight(inferenceURL, "/") + "/v1/chat/completions"
		body, err := json.Marshal(chatRequest{Model: model, Messages: messages, Stream: true, Tools: defs})
		if err != nil {
			send(inferenceStreamEvent{Err: fmt.Errorf("failed to marshal request: %w", err)})
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			send(inferenceStreamEvent{Err: fmt.Errorf("failed to create request: %w", err)})
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			send(inferenceStreamEvent{Err: fmt.Errorf("request failed: %w", err)})
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			send(inferenceStreamEvent{Err: fmt.Errorf("inference returned %s: %s", resp.Status, strings.TrimSpace(string(b)))})
			return
		}
		reader := bufio.NewReader(resp.Body)
		detector := newRepetitionDetector()
		for {
			line, err := reader.ReadString('\n')
			if err == io.EOF {
				break
			}
			if err != nil {
				send(inferenceStreamEvent{Err: fmt.Errorf("failed reading stream: %w", err)})
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
				send(inferenceStreamEvent{Err: fmt.Errorf("failed to parse chunk: %w", err)})
				return
			}
			if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
				content := chunk.Choices[0].Delta.Content
				if detector.observe(content) {
					logJSON("warn", "repetition loop detected", map[string]interface{}{"event": "repetition_detected", "action": "truncating_response"})
					return
				}
				if !send(inferenceStreamEvent{Content: content}) {
					return
				}
			}
		}
	}()
	return chunks
}

func streamResponsesWithContext(ctx context.Context, messages []message, defs []toolDef, inferenceURL, model, apiKey string) <-chan inferenceStreamEvent {
	chunks := make(chan inferenceStreamEvent)
	go func() {
		defer close(chunks)
		send := func(evt inferenceStreamEvent) bool {
			select {
			case chunks <- evt:
				return true
			case <-ctx.Done():
				return false
			}
		}
		endpoint := strings.TrimRight(inferenceURL, "/") + "/v1/responses"
		body, err := json.Marshal(responsesRequest{Model: model, Input: convertMessagesForResponses(messages), Stream: true, Tools: convertToolDefsForResponses(defs)})
		if err != nil {
			send(inferenceStreamEvent{Err: fmt.Errorf("failed to marshal request: %w", err)})
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			send(inferenceStreamEvent{Err: fmt.Errorf("failed to create request: %w", err)})
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			send(inferenceStreamEvent{Err: fmt.Errorf("request failed: %w", err)})
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			send(inferenceStreamEvent{Err: fmt.Errorf("inference returned %s: %s", resp.Status, strings.TrimSpace(string(b)))})
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
				if !send(inferenceStreamEvent{ToolCalls: []toolCall{{
					ID:   p.id,
					Type: "function",
					Function: toolCallFunction{
						Name:      p.name,
						Arguments: argText,
					},
				}}}) {
					return
				}
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
					if !send(inferenceStreamEvent{Content: payload.Delta}) {
						return errStreamCanceled
					}
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
				send(inferenceStreamEvent{Err: fmt.Errorf("failed reading stream: %w", err)})
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if err := flush(); err != nil {
					if err == errStreamCanceled {
						return
					}
					send(inferenceStreamEvent{Err: fmt.Errorf("failed to parse chunk: %w", err)})
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
		send := func(evt inferenceStreamEvent) bool {
			select {
			case chunks <- evt:
				return true
			case <-ctx.Done():
				return false
			}
		}
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
			send(inferenceStreamEvent{Err: fmt.Errorf("failed to marshal request: %w", err)})
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			send(inferenceStreamEvent{Err: fmt.Errorf("failed to create request: %w", err)})
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			send(inferenceStreamEvent{Err: fmt.Errorf("request failed: %w", err)})
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			send(inferenceStreamEvent{Err: fmt.Errorf("inference returned %s: %s", resp.Status, strings.TrimSpace(string(b)))})
			return
		}

		type pendingToolCall struct {
			id   string
			name string
			args strings.Builder
		}
		pendingByIndex := map[int]*pendingToolCall{}
		finalize := func(index int) bool {
			pc := pendingByIndex[index]
			if pc == nil {
				return true
			}
			argText := strings.TrimSpace(pc.args.String())
			if argText == "" {
				argText = "{}"
			}
			if !send(inferenceStreamEvent{ToolCalls: []toolCall{{
				ID:   pc.id,
				Type: "function",
				Function: toolCallFunction{
					Name:      pc.name,
					Arguments: argText,
				},
			}}}) {
				return false
			}
			delete(pendingByIndex, index)
			return true
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
						if !send(inferenceStreamEvent{Content: evt.Delta.Text}) {
							return errStreamCanceled
						}
					} else if evt.Delta.Type == "input_json_delta" && evt.Delta.PartialJSON != "" {
						if pc := pendingByIndex[evt.Index]; pc != nil {
							pc.args.WriteString(evt.Delta.PartialJSON)
						}
					}
				}
			case "content_block_stop":
				if !finalize(evt.Index) {
					return errStreamCanceled
				}
			case "message_stop":
				indexes := make([]int, 0, len(pendingByIndex))
				for idx := range pendingByIndex {
					indexes = append(indexes, idx)
				}
				sort.Ints(indexes)
				for _, idx := range indexes {
					if !finalize(idx) {
						return errStreamCanceled
					}
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
				send(inferenceStreamEvent{Err: fmt.Errorf("failed reading stream: %w", err)})
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if err := flush(); err != nil {
					if err == errStreamCanceled {
						return
					}
					send(inferenceStreamEvent{Err: fmt.Errorf("failed to parse chunk: %w", err)})
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
	scriptStyleRE := regexp.MustCompile(`(?is)<(?:script|style)[^>]*>.*?</(?:script|style)>`)
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

func writeSSE(w http.ResponseWriter, payload interface{}) {
	b, _ := json.Marshal(payload)
	fmt.Fprintf(w, "data: %s\n\n", b)
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
	soulPath := flag.String("soul", "./soul.md", "path to SOUL.md")
	port := flag.String("port", "8081", "HTTP port")
	grpcPort := flag.String("grpc-port", "8082", "gRPC port")
	inferenceURL := flag.String("inference-url", "http://localhost:11434", "inference base URL")
	model := flag.String("model", "qwen3:30b", "model name")
	apiKey := flag.String("api-key", "", "API key for inference backend (optional)")
	maxContext := flag.Int("max-context", 20, "max number of user/assistant message pairs to keep (0 disables trimming)")
	hunterMode := flag.Bool("hunter-mode", false, "Run as ephemeral hunter: execute task, write to memory, exit")
	oneShotFlag := flag.Bool("one-shot", false, "Run one sandbox task from an envelope and exit (P1.4)")
	envelopeFlag := flag.String("envelope", "/task/envelope.json", "envelope path for --one-shot")
	intuitionFlag := flag.Bool("intuition", false, "Enable Seidr intuitive recall (injects stale context; off by default")
	episodicFlag := flag.Bool("episodic", false, "Enable episodic state loading/injection/persist")
	bootstrapFlag := flag.Bool("bootstrap", false, "Enable session bootstrap and reflection context injection")
	hunterTask := flag.String("hunter-task", "", "JSON string with fetch instructions for hunter mode")
	hunterQuarantine := flag.String("hunter-quarantine-prefix", "", "Seidr collection prefix for quarantine writes")
	requirePRPattern := flag.String("require-pr-pattern", "", "Regex pattern required in async task completion responses; empty disables the gate")
	completionMaxNudges := flag.Int("completion-max-nudges", 3, "Maximum completion gate nudges before failing a task")
	maxDelegationTokens := flag.Int("max-delegation-tokens", 500, "Maximum delegation message token budget before trimming optional sections")
	sovereignNotifyURL := flag.String("sovereign-notify-url", "", "Gateway URL for sovereign task notifications; empty disables sovereign reporting")
	sovereignNotifyOn := flag.String("sovereign-notify-on", "completed,failed,nudged", "Comma-separated sovereign notification states")
	workspace := flag.String("workspace", "./workspace", "tool workspace directory")
	agentsFlag := flag.String("agents", "", "comma-separated name=url peer agents (alias for --peers)")
	peersFlag := flag.String("peers", "", "comma-separated name=url peer agents")
	legacyDelegate := flag.Bool("legacy-delegate", false, "Use the legacy synchronous /tasks/send delegation path")
	memoryURL := flag.String("memory-url", "", "Seidr memory service URL")
	memoryToolsFlag := flag.Bool("memory-tools", false, "Enable recall, remember, and memory-edit tools (requires --memory-url)")
	gatewayURL := flag.String("gateway-url", "", "Gateway URL for event notifications (optional)")
	reviewContextIdleTimeout := flag.Duration("review-context-idle-timeout", 30*time.Minute, "Idle timeout before clearing PR review context")
	agentNameFlag := flag.String("agent-name", "", "agent name override (defaults to soul filename)")
	personaRepoFlag := flag.String("persona-repo", "", "git URL of persona repository")
	modelTemplateFlag := flag.String("model-template", "", "Override model template auto-detection. One of qwen|claude|gpt|gemini|default, or any string containing one of those substrings. Empty means auto-detect from --model.")
	toolsFile := flag.String("tools-file", "/etc/valhalla/tools.md", "path to tools context file")
	playbookFile := flag.String("playbook-file", "/etc/valhalla/playbook.md", "path to playbook context file")
	toolsFlag := flag.String("tools", "exec,read,write,edit", "comma-separated enabled tools")
	procedureFlag := flag.String("procedure", "", "operating procedure to render: builder | reviewer | none (O-PROFILE; empty ⇒ builder)")
	completionFlag := flag.String("completion", "", "completion mode: pr | review | none (O-PROFILE completion.requires; empty ⇒ pr semantics)")
	skillsFlag := flag.String("skills", "", "comma-separated bundle skill names resolved to skills/<name>.md in the skills repo (O-SKILL-BUNDLE)")
	skillsRepoFlag := flag.String("skills-repo", "", "git URL of the skills/personas repo for --skills (tokenless; auth via the primed credential helper)")
	memoryScopesFlag := flag.String("memory-scopes", "", "comma-separated Seidr collection scopes for recall/remember (O-SKILL-BUNDLE)")
	maxToolRetries := flag.Int("max-tool-retries", 2, "max retry attempts per tool call (0 disables retries)")
	maxToolRounds := flag.Int("max-tool-rounds", 30, "maximum LLM inference rounds in the tool-calling loop")
	inferenceTimeout := flag.Int("inference-timeout", 120, "timeout in seconds for each inference call")
	debugIOFlag := flag.Bool("debug-io", false, "log tool commands, tool output, and final model content (diagnostic; verbose; off by default)")
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
	peers := map[string]string{}
	peerRoles := map[string]string{}
	for _, rawPeers := range []string{*peersFlag, *agentsFlag} {
		parsedPeers, parsedRoles, parseErr := parsePeers(rawPeers)
		if parseErr != nil {
			die("failed to parse peers", parseErr)
		}
		for name, value := range parsedPeers {
			if existing, ok := peers[name]; ok && existing != value {
				die("failed to parse peers", fmt.Errorf("duplicate peer %q with conflicting URLs", name))
			}
			peers[name] = value
		}
		for name, role := range parsedRoles {
			if existing, ok := peerRoles[name]; ok && existing != role {
				die("failed to parse peers", fmt.Errorf("duplicate peer %q with conflicting roles", name))
			}
			peerRoles[name] = role
		}
	}
	peerNames := make([]string, 0, len(peers))
	for name := range peers {
		peerNames = append(peerNames, name)
	}
	sort.Strings(peerNames)
	agentName := strings.TrimSpace(*agentNameFlag)
	var err error
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
	reviewTracker := newReviewContextTracker(agentName, *gatewayURL, *reviewContextIdleTimeout)
	defer reviewTracker.Stop()
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
	enabled := map[string]bool{}
	for _, name := range strings.Split(*toolsFlag, ",") {
		if name = strings.TrimSpace(name); name != "" {
			enabled[name] = true
		}
	}
	if allowedTools := parseSoulAllowedTools(soul); len(allowedTools) > 0 {
		allowed := map[string]bool{}
		for _, t := range allowedTools {
			allowed[t] = true
		}
		for name := range enabled {
			if !allowed[name] {
				delete(enabled, name)
				logJSON("info", "tool filtered by soul allowed_tools", map[string]interface{}{"tool": name, "agent": agentName})
			}
		}
	}
	if trimmed := strings.TrimSpace(soul); strings.HasPrefix(trimmed, "---") {
		if parts := strings.SplitN(trimmed, "---", 3); len(parts) >= 3 {
			soul = strings.TrimSpace(parts[2])
		}
	}
	delegationGates := []string{}
	if strings.TrimSpace(*requirePRPattern) != "" {
		delegationGates = append(delegationGates, "require_pr_pattern="+strings.TrimSpace(*requirePRPattern))
	}
	reg := toolpkg.NewRegistry()
	if enabled["plan"] {
		reg.Register(toolpkg.NewPlanTool())
	}
	if enabled["plan-step-complete"] {
		reg.Register(toolpkg.NewPlanStepCompleteTool())
	}
	giteaTool, toolDefs := configureToolRegistry(reg, toolSetupDeps{
		workspace:           *workspace,
		giteaURL:            *giteaURL,
		giteaToken:          giteaToken,
		agentName:           agentName,
		peers:               peers,
		peerRoles:           peerRoles,
		gatewayURL:          *gatewayURL,
		maxDelegationTokens: *maxDelegationTokens,
		memoryURL:           *memoryURL,
		memoryScopes:        parseCSVList(*memoryScopesFlag),
		memoryToolsEnabled:  *memoryToolsFlag,
		mcpServers:          *mcpServers,
		reviewTracker:       reviewTracker,
		enabled:             enabled,
		delegationGates:     delegationGates,
		legacyDelegate:      *legacyDelegate,
	})
	enabledTools = reg.List()
	modelName = *model
	portValue = *port
	grpcPortValue = *grpcPort
	giteaURLValue = *giteaURL
	giteaTokenValue = giteaToken
	gatewayURLValue = *gatewayURL
	memoryURLValue = *memoryURL

	personaRoot := ""
	if persona != nil {
		personaRoot = persona.Root
	}
	templateName, templateContent, templateSource := resolveModelTemplate(*model, *modelTemplateFlag, personaRoot)

	// O-SKILL-BUNDLE: resolve the task's bundle skills to prompt content. An
	// unresolved skill is a LOUD, fatal dispatch failure — never a silent skip
	// that runs the agent believing it had knowledge it lacked.
	skillsContent := ""
	if names := parseCSVList(*skillsFlag); len(names) > 0 {
		skillsRoot := strings.TrimSpace(*skillsRepoFlag)
		if skillsRoot == "" {
			log.Fatalf("--skills set (%v) but --skills-repo is empty; cannot resolve skills", names)
		}
		dst := filepath.Join(os.TempDir(), "valhalla-skills")
		if err := syncPersonaRepo(skillsRoot, dst); err != nil {
			log.Fatalf("skills repo clone failed: %v", err)
		}
		content, loaded, err := resolveBundleSkills(dst, names)
		if err != nil {
			log.Fatalf("skill resolution failed: %v", err)
		}
		skillsContent = content
		logJSON("info", "skills_loaded", map[string]interface{}{"skills": strings.Join(loaded, ","), "count": len(loaded)})
	}

	processConversation := newConversationProcessor(conversationDeps{
		workspace:        *workspace,
		inferenceTimeout: *inferenceTimeout,
		memoryURL:        *memoryURL,
		bootstrap:        *bootstrapFlag,
		intuition:        *intuitionFlag,
		episodic:         *episodicFlag,
		debugIO:          *debugIOFlag,
		maxContext:       *maxContext,
		model:            *model,
		apiKey:           *apiKey,
		maxToolRetries:   *maxToolRetries,
		maxToolRounds:    *maxToolRounds,
		gatewayURL:       *gatewayURL,
		giteaURL:         *giteaURL,
		inferenceURL:     *inferenceURL,
		soulMaxLines:     *soulMaxLines,
		toolsFile:        *toolsFile,
		playbookFile:     *playbookFile,
		agentName:        agentName,
		soul:             soul,
		procedure:        *procedureFlag,
		completion:       *completionFlag,
		skillsContent:    skillsContent,
		modelTemplate:    templateContent,
		peers:            peers,
		peerRoles:        peerRoles,
		persona:          persona,
		reg:              reg,
		toolDefs:         toolDefs,
		giteaTool:        giteaTool,
		reviewTracker:    reviewTracker,
	})
	if *oneShotFlag {
		os.Exit(runOneShot(processConversation, *envelopeFlag))
	}

	a2aRuntime := newA2ARuntime(agentName, *gatewayURL, processConversation)

	mux := http.NewServeMux()
	registerRoutes(mux, serverDeps{
		episodic:            *episodicFlag,
		workspace:           *workspace,
		webhookSecret:       *webhookSecret,
		agentName:           agentName,
		peerNames:           peerNames,
		processConversation: processConversation,
		taskStore:           taskStore,
		taskTracker:         taskTracker,
		sovereignStates:     sovereignStates,
		sovereignReporter:   sovereignReporter,
		taskCancelMu:        &taskCancelMu,
		taskCancels:         taskCancels,
		requirePRPattern:    *requirePRPattern,
		completionMaxNudges: *completionMaxNudges,
		reviewTracker:       reviewTracker,
		agentDescription:    agentDescriptionFromSoul(soul),
		a2aRuntime:          a2aRuntime,
	})

	go func() {
		log.Printf("a2a grpc: listening on :%s", *grpcPort)
		die("a2a grpc server failed", startA2AGRPCServer(*grpcPort, a2aRuntime))
	}()

	addr := ":" + *port
	logJSON("info", "agent started", map[string]interface{}{
		"port":            *port,
		"grpc_port":       *grpcPort,
		"model":           *model,
		"template":        templateName,
		"template_source": templateSource,
		"tools":           enabledTools,
		"peers":           peerNames,
	})
	die("server failed", http.ListenAndServe(addr, mux))
}
