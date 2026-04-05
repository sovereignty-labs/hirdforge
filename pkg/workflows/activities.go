package workflows

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"
)

// AgentActivities holds the activity implementations for agent communication
type AgentActivities struct{}

// agentMessageRequest matches the gateway's message format to agent pods
type agentMessageRequest struct {
	Message   string `json:"message"`
	SessionID string `json:"session_id,omitempty"`
}

// SendMessageToAgent posts a message to an agent's HTTP endpoint and collects
// the full SSE response. This is the core activity that bridges Temporal
// workflows to the existing agent runtime.
//
// The agent binary serves HTTP on port 8081. It returns SSE-formatted responses
// with data: prefixed JSON chunks. This activity collects all chunks into a
// single string response.
func (a *AgentActivities) SendMessageToAgent(ctx context.Context, agentName string, content string, sessionID string) (string, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Sending message to agent", "agent", agentName, "content_len", len(content))

	// Construct agent URL via in-cluster service discovery
	// Agent pods expose port 8081 with a /message endpoint
	agentURL := fmt.Sprintf("http://%s.valhalla.svc:8081/message", agentName)

	reqBody, err := json.Marshal(agentMessageRequest{
		Message:   content,
		SessionID: sessionID,
	})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", agentURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("send to agent %s: %w", agentName, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("agent %s returned %d: %s", agentName, resp.StatusCode, string(body))
	}

	// Heartbeat while collecting response to prevent activity timeout
	activity.RecordHeartbeat(ctx, "collecting response")

	// Collect SSE response chunks into full response
	// The agent streams data: {"content": "chunk"} lines
	var fullResponse strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 1024*1024) // 1MB buffer for large responses
	scanner.Buffer(buf, 1024*1024)

	chunkCount := 0
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")

			if data == "[DONE]" {
				break
			}

			// Try parsing as JSON with content field
			var chunk map[string]interface{}
			if err := json.Unmarshal([]byte(data), &chunk); err == nil {
				// Extract text content from various possible field names
				for _, field := range []string{"content", "text", "response"} {
					if v, ok := chunk[field]; ok {
						if s, ok := v.(string); ok {
							fullResponse.WriteString(s)
							chunkCount++
							break
						}
					}
				}
			} else {
				// If not JSON, treat the raw data as content
				fullResponse.WriteString(data)
				chunkCount++
			}

			// Heartbeat every 10 chunks
			if chunkCount%10 == 0 {
				activity.RecordHeartbeat(ctx, fmt.Sprintf("received %d chunks", chunkCount))
			}
		}
	}

	if err := scanner.Err(); err != nil {
		logger.Warn("Scanner error while reading SSE", "error", err)
	}

	result := fullResponse.String()
	logger.Info("Response collected", "agent", agentName, "chunks", chunkCount, "response_len", len(result))

	if result == "" {
		return "", fmt.Errorf("agent %s returned empty response after %d chunks", agentName, chunkCount)
	}

	return result, nil
}