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
	"time"

	toolpkg "github.com/kitporath/project_valhalla/pkg/tools"
)

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

func die(msg string, err error) {
	fmt.Fprintln(os.Stderr, msg+":", err)
	os.Exit(1)
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

func callOllamaNonStreaming(messages []message, defs []toolDef, inferenceURL, model string) (chatResponse, error) {
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

func streamOllama(messages []message, defs []toolDef, inferenceURL, model string) <-chan string {
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

func main() {
	rand.Seed(time.Now().UnixNano())
	soulPath := flag.String("soul", "./soul.md", "path to SOUL.md")
	port := flag.String("port", "8081", "HTTP port")
	inferenceURL := flag.String("inference-url", "http://localhost:11434", "inference base URL")
	model := flag.String("model", "qwen3:30b", "model name")
	workspace := flag.String("workspace", "./workspace", "tool workspace directory")
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
	toolDefs := buildToolDefs(reg)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ready", "agent": "valhalla-agent", "model": *model})
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

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		for i := 0; i < 10; i++ {
			resp, err := callOllamaNonStreaming(messages, toolDefs, *inferenceURL, *model)
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
				args := map[string]interface{}{}
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					args = map[string]interface{}{"_raw": tc.Function.Arguments}
				}
				writeSSE(w, sseChunk{Type: "tool_call", Tool: tc.Function.Name, Args: args, Done: false})
				flusher.Flush()
				result := toolpkg.ToolResult{Error: "unknown tool: " + tc.Function.Name}
				if t, ok := reg.Get(tc.Function.Name); ok {
					result = t.Execute(args)
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
		for chunk := range streamOllama(messages, nil, *inferenceURL, *model) {
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
