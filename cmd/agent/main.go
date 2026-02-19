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
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

type messageRequest struct {
	Content   string `json:"content"`
	SessionID string `json:"session_id"`
}

type sseChunk struct {
	Content   string `json:"content"`
	Done      bool   `json:"done"`
	SessionID string `json:"session_id,omitempty"`
}

type sessionSummary struct {
	SessionID string `json:"session_id"`
	Messages  int    `json:"messages"`
}

var (
	sessionsMu sync.Mutex
	sessions   = map[string][]message{}
)

func die(msg string, err error) {
	fmt.Fprintln(os.Stderr, msg+":", err)
	os.Exit(1)
}

func streamOllama(messages []message, inferenceURL, model string) <-chan string {
	chunks := make(chan string)
	go func() {
		defer close(chunks)

		endpoint := strings.TrimRight(inferenceURL, "/") + "/v1/chat/completions"
		body, err := json.Marshal(chatRequest{Model: model, Messages: messages, Stream: true})
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
			if len(chunk.Choices) > 0 {
				if chunk.Choices[0].Delta.Content != "" {
					chunks <- chunk.Choices[0].Delta.Content
				}
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
	flag.Parse()

	soulBytes, err := os.ReadFile(*soulPath)
	if err != nil {
		die("failed to read soul file", err)
	}
	soul := string(soulBytes)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "ready",
			"agent":  "valhalla-agent",
			"model":  *model,
		})
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
		out := make([]sessionSummary, 0, len(ids))
		for _, id := range ids {
			out = append(out, sessionSummary{SessionID: id, Messages: len(sessions[id])})
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

		messages := make([]message, 0, len(history)+2)
		messages = append(messages, message{Role: "system", Content: soul})
		messages = append(messages, history...)
		messages = append(messages, message{Role: "user", Content: req.Content})

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		var full strings.Builder
		for chunk := range streamOllama(messages, *inferenceURL, *model) {
			full.WriteString(chunk)
			writeSSE(w, sseChunk{Content: chunk, Done: false})
			flusher.Flush()
		}

		sessionsMu.Lock()
		sessions[sessionID] = append(sessions[sessionID],
			message{Role: "user", Content: req.Content},
			message{Role: "assistant", Content: full.String()},
		)
		sessionsMu.Unlock()

		writeSSE(w, sseChunk{Content: "", Done: true, SessionID: sessionID})
		flusher.Flush()
	})

	addr := ":" + *port
	fmt.Printf("Valhalla Agent listening on %s\n", addr)
	fmt.Printf("SOUL loaded: %s (%d bytes)\n", *soulPath, len(soulBytes))
	fmt.Printf("Inference: %s model=%s\n", *inferenceURL, *model)
	die("server failed", http.ListenAndServe(addr, mux))
}
