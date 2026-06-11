package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// This file is the first deterministic stub-inference harness for agent runtime
// tests. It stands up an OpenAI/Ollama-compatible HTTP server that returns
// scripted responses in order, so agent inference behavior (content, tool calls,
// repeated tool calls, empty output, context-length errors) can be driven
// without a live model, GPU, or network. It is test-only (compiled solely under
// `go test`) and adds no production code paths.

// stubResponse is one scripted reply from the deterministic inference stub.
// Each agent inference call consumes the next queued response in order.
type stubResponse struct {
	// status is the HTTP status to return; 0 means 200 OK.
	status int
	// errorBody is returned verbatim when status is non-2xx, so the agent
	// surfaces it as an inference error (used for context-length style failures).
	errorBody string
	// content and toolCalls populate the assistant message of a 200 response.
	content   string
	toolCalls []toolCall
	// noChoices returns a 200 with an empty choices array (the "model returned
	// no choices" case).
	noChoices bool
}

// stubContent scripts a normal assistant message with text content.
func stubContent(content string) stubResponse { return stubResponse{content: content} }

// stubEmpty scripts a 200 response whose assistant content is empty (the
// "no actionable output" case).
func stubEmpty() stubResponse { return stubResponse{content: ""} }

// stubNoChoices scripts a 200 response with no choices at all.
func stubNoChoices() stubResponse { return stubResponse{noChoices: true} }

// stubToolCall scripts a single assistant tool call with the given name and raw
// JSON arguments.
func stubToolCall(name, arguments string) stubResponse {
	return stubResponse{toolCalls: []toolCall{{
		ID:       "call_" + name,
		Type:     "function",
		Function: toolCallFunction{Name: name, Arguments: arguments},
	}}}
}

// stubHTTPError scripts a non-2xx HTTP response with a raw error body.
func stubHTTPError(status int, body string) stubResponse {
	return stubResponse{status: status, errorBody: body}
}

// stubContextLengthError scripts the kind of 400 an OpenAI-compatible backend
// returns when the prompt exceeds the model's context window.
func stubContextLengthError() stubResponse {
	return stubHTTPError(http.StatusBadRequest,
		`{"error":{"message":"This model's maximum context length is 8192 tokens. Please reduce the length of the messages.","type":"invalid_request_error","code":"context_length_exceeded"}}`)
}

// stub wire types: a minimal encoding of the /v1/chat/completions response shape
// that the production chatResponse decoder reads.
type stubChatWire struct {
	Choices []stubChoiceWire `json:"choices"`
}

type stubChoiceWire struct {
	Message stubMessageWire `json:"message"`
}

type stubMessageWire struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []toolCall `json:"tool_calls,omitempty"`
}

// stubInferenceServer is a deterministic OpenAI/Ollama-compatible inference
// server. It serves /v1/chat/completions and replays queued responses in order;
// once exhausted it repeats the final scripted response. Requests are captured
// for assertions.
type stubInferenceServer struct {
	server    *httptest.Server
	mu        sync.Mutex
	responses []stubResponse
	idx       int
	requests  []chatRequest
}

// newStubInferenceServer starts a stub server with the given scripted responses
// and registers cleanup on the test.
func newStubInferenceServer(t *testing.T, responses ...stubResponse) *stubInferenceServer {
	t.Helper()
	s := &stubInferenceServer{responses: responses}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.server.Close)
	return s
}

// URL is the base inference URL to pass to the agent inference functions.
func (s *stubInferenceServer) URL() string { return s.server.URL }

// callCount returns how many inference requests the stub has served.
func (s *stubInferenceServer) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// lastRequest returns the most recently received chat request (for assertions).
func (s *stubInferenceServer) lastRequest() (chatRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return chatRequest{}, false
	}
	return s.requests[len(s.requests)-1], true
}

func (s *stubInferenceServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" {
		http.Error(w, "stub: unsupported path "+r.URL.Path, http.StatusNotFound)
		return
	}

	var req chatRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	s.mu.Lock()
	s.requests = append(s.requests, req)
	var resp stubResponse
	switch {
	case s.idx < len(s.responses):
		resp = s.responses[s.idx]
		s.idx++
	case len(s.responses) > 0:
		resp = s.responses[len(s.responses)-1]
	}
	s.mu.Unlock()

	status := resp.status
	if status == 0 {
		status = http.StatusOK
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp.errorBody))
		return
	}

	wire := stubChatWire{}
	if !resp.noChoices {
		wire.Choices = []stubChoiceWire{{Message: stubMessageWire{
			Role:      "assistant",
			Content:   resp.content,
			ToolCalls: resp.toolCalls,
		}}}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(wire)
}
