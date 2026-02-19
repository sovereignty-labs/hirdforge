package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
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

func die(msg string, err error) {
	fmt.Fprintln(os.Stderr, msg+":", err)
	os.Exit(1)
}

func main() {
	soulPath := flag.String("soul", "./soul.md", "path to SOUL.md")
	userMessage := flag.String("message", "", "user message")
	inferenceURL := flag.String("inference-url", "http://localhost:11434", "inference base URL")
	model := flag.String("model", "qwen3:30b", "model name")
	flag.Parse()

	soul, err := os.ReadFile(*soulPath)
	if err != nil {
		die("failed to read soul file", err)
	}

	endpoint := *inferenceURL
	if len(endpoint) > 0 && endpoint[len(endpoint)-1] == '/' {
		endpoint = endpoint[:len(endpoint)-1]
	}
	endpoint += "/v1/chat/completions"

	body, err := json.Marshal(chatRequest{
		Model: *model,
		Messages: []message{
			{Role: "system", Content: string(soul)},
			{Role: "user", Content: *userMessage},
		},
		Stream: true,
	})
	if err != nil {
		die("failed to marshal request", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		die("failed to create request", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		die("request failed", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			die("failed reading stream", err)
		}
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
		}
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(line) < 6 || line[:6] != "data: " {
			continue
		}
		payload := line[6:]
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			die("failed to parse chunk", err)
		}
		if len(chunk.Choices) > 0 {
			fmt.Print(chunk.Choices[0].Delta.Content)
		}
	}
	fmt.Println()
}
