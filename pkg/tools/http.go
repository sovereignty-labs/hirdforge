package tools

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type HTTPTool struct {
	Client *http.Client
}

func NewHTTPTool() *HTTPTool {
	return &HTTPTool{
		Client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (t *HTTPTool) Name() string { return "http" }

func (t *HTTPTool) Description() string {
	return "Make HTTP requests. Methods: GET, POST, PUT, DELETE. Returns status code and body."
}

func (t *HTTPTool) Parameters() map[string]string {
	return map[string]string{
		"method":  "HTTP method: GET, POST, PUT, DELETE (default GET)",
		"url":     "Full URL to request",
		"body":    "Request body (for POST/PUT)",
		"headers": "Optional headers as key:value pairs separated by semicolons (e.g. Content-Type:application/json;Authorization:token abc)",
	}
}

func (t *HTTPTool) Execute(args map[string]interface{}) ToolResult {
	urlStr, _ := args["url"].(string)
	if urlStr == "" {
		return ToolResult{Error: "url is required"}
	}

	method, _ := args["method"].(string)
	if method == "" {
		method = "GET"
	}
	method = strings.ToUpper(method)

	bodyStr, _ := args["body"].(string)
	headerStr, _ := args["headers"].(string)

	var reqBody io.Reader
	if bodyStr != "" {
		reqBody = bytes.NewBufferString(bodyStr)
	}

	req, err := http.NewRequest(method, urlStr, reqBody)
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to create request: %s", err)}
	}

	if headerStr != "" {
		pairs := strings.Split(headerStr, ";")
		for _, pair := range pairs {
			kv := strings.SplitN(strings.TrimSpace(pair), ":", 2)
			if len(kv) == 2 {
				req.Header.Set(strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1]))
			}
		}
	}

	resp, err := t.Client.Do(req)
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("request failed: %s", err)}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to read response: %s", err)}
	}

	output := fmt.Sprintf("HTTP %d %s\n\n%s", resp.StatusCode, resp.Status, string(respBody))
	return ToolResult{Output: output}
}
