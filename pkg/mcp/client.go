package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type MCPToolDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

type MCPToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

type Client struct {
	serverURL  string
	agentName  string
	sessionID  string
	httpClient *http.Client
	requestID  int64
}

func NewClient(serverURL string) *Client {
	return &Client{
		serverURL:  serverURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func NewClientWithAgent(serverURL, agentName string) *Client {
	client := NewClient(serverURL)
	client.agentName = stringsTrim(agentName)
	return client
}

func (c *Client) nextID() int64 {
	return atomic.AddInt64(&c.requestID, 1)
}

func (c *Client) send(req jsonRPCRequest) (*jsonRPCResponse, error) {
	return c.sendInternal(req, false)
}

func (c *Client) sendInternal(req jsonRPCRequest, retried bool) (*jsonRPCResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequest(http.MethodPost, c.serverURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if c.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", c.sessionID)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}
	if resp.StatusCode == http.StatusNotFound && !retried && req.Method != "initialize" {
		// Session likely expired; re-initialize and retry once.
		c.sessionID = ""
		if err := c.Initialize(); err != nil {
			return nil, err
		}
		return c.sendInternal(req, true)
	}

	if req.ID == nil {
		return nil, nil
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, stringsTrim(string(respBody)))
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		return nil, fmt.Errorf("empty RPC response")
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return &rpcResp, nil
}

func (c *Client) Initialize() error {
	resp, err := c.send(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextID(),
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]interface{}{
				"name":    "hirdforge-agent",
				"version": "0.1.0",
			},
		},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	_ = resp
	_, _ = c.send(jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	return nil
}

func (c *Client) ListTools() ([]MCPToolDef, error) {
	resp, err := c.send(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextID(),
		Method:  "tools/list",
		Params:  map[string]interface{}{},
	})
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	var result struct {
		Tools []MCPToolDef `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parse tools: %w", err)
	}
	return result.Tools, nil
}

func (c *Client) CallTool(name string, arguments map[string]interface{}) (string, error) {
	callArgs := cloneArguments(arguments)
	if c.agentName != "" {
		if callArgs == nil {
			callArgs = map[string]interface{}{}
		}
		callArgs["_agent_name"] = c.agentName
	}
	resp, err := c.send(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextID(),
		Method:  "tools/call",
		Params: map[string]interface{}{
			"name":      name,
			"arguments": callArgs,
		},
	})
	if err != nil {
		return "", fmt.Errorf("call tool %s: %w", name, err)
	}
	var result MCPToolResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return "", fmt.Errorf("parse tool result: %w", err)
	}
	if result.IsError {
		var errText bytes.Buffer
		for _, c := range result.Content {
			if c.Type == "text" {
				errText.WriteString(c.Text)
				errText.WriteString("\n")
			}
		}
		return "", fmt.Errorf("%s", stringsTrim(errText.String()))
	}
	var output bytes.Buffer
	for _, c := range result.Content {
		if c.Type == "text" {
			output.WriteString(c.Text)
			output.WriteString("\n")
		}
	}
	return stringsTrim(output.String()), nil
}

func stringsTrim(s string) string {
	return string(bytes.TrimSpace([]byte(s)))
}

func cloneArguments(in map[string]interface{}) map[string]interface{} {
	if in == nil {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
