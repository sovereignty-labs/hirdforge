package tasklife

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type SovereignReporter struct {
	gatewayURL string
	agentName  string
	enabled    bool
	client     *http.Client
}

func NewSovereignReporter(gatewayURL, agentName string, enabled bool) *SovereignReporter {
	return &SovereignReporter{
		gatewayURL: strings.TrimRight(strings.TrimSpace(gatewayURL), "/"),
		agentName:  strings.TrimSpace(agentName),
		enabled:    enabled,
		client:     &http.Client{Timeout: 5 * time.Second},
	}
}

func (r *SovereignReporter) Report(event TaskEvent) {
	if r == nil || !r.enabled || r.gatewayURL == "" {
		return
	}
	payload := map[string]string{
		"from":      firstNonEmpty(event.From, r.agentName),
		"task_id":   strings.TrimSpace(event.TaskID),
		"agent":     firstNonEmpty(event.Agent, r.agentName),
		"state":     string(event.State),
		"result":    formatSovereignMessage(event),
		"timestamp": reportTimestamp(event.Timestamp),
	}
	go func() {
		body, err := json.Marshal(payload)
		if err != nil {
			return
		}
		req, err := http.NewRequest(http.MethodPost, r.gatewayURL+"/api/v1/notify", bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := r.client.Do(req)
		if err != nil {
			return
		}
		_ = resp.Body.Close()
	}()
}

func formatSovereignMessage(event TaskEvent) string {
	preview := truncatePreview(event.Result, 200)
	switch event.State {
	case StateCompleted:
		if preview == "" {
			return "✅ completed"
		}
		return "✅ completed: " + preview
	case StateFailedNoPR:
		if preview == "" {
			return "❌ failed"
		}
		return "❌ failed: " + preview
	case StateNudged:
		if preview == "" {
			return "⚠️ nudged"
		}
		return "⚠️ nudged: " + preview
	default:
		if preview == "" {
			return string(event.State)
		}
		return string(event.State) + ": " + preview
	}
}

func truncatePreview(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit] + "..."
}

func reportTimestamp(ts time.Time) string {
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return ts.UTC().Format(time.RFC3339)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
