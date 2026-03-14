package tools

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type HTTPTool struct {
	Client       *http.Client
	SecureClient *http.Client
}

func NewHTTPTool() *HTTPTool {
	// Default client with system certs
	defaultClient := &http.Client{Timeout: 30 * time.Second}

	// Try to load K8s service account CA for in-cluster API calls
	secureClient := defaultClient
	caCert, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
	if err == nil {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(caCert) {
			secureClient = &http.Client{
				Timeout: 30 * time.Second,
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						RootCAs: pool,
					},
				},
			}
		}
	}

	return &HTTPTool{
		Client:       defaultClient,
		SecureClient: secureClient,
	}
}

func (t *HTTPTool) Name() string { return "http" }

func (t *HTTPTool) Description() string {
	return "Make HTTP requests. Methods: GET, POST, PUT, DELETE. Returns status code and body. Supports HTTPS to Kubernetes API."
}

func (t *HTTPTool) Parameters() map[string]string {
	return map[string]string{
		"method":  "HTTP method: GET, POST, PUT, DELETE (default GET)",
		"url":     "Full URL to request",
		"body":    "Request body (for POST/PUT)",
		"headers": "Optional headers as key:value pairs separated by semicolons (e.g. Content-Type:application/json;Authorization:Bearer abc)",
	}
}

func (t *HTTPTool) Execute(args map[string]interface{}) ToolResult {
	urlStr, _ := args["url"].(string)
	if urlStr == "" {
		return ToolResult{Error: "url is required"}
	}

	// Normalize in-cluster URLs: downgrade HTTPS to HTTP for .svc addresses.
	// Most cluster services (like gitea-http.gitea.svc:3000) are plain HTTP,
	// while LLMs sometimes generate HTTPS URLs.
	// Keep kubernetes.default.svc on HTTPS for API-server calls.
	if strings.HasPrefix(urlStr, "https://") &&
		(strings.Contains(urlStr, ".svc:") || strings.Contains(urlStr, ".svc.cluster.local") || strings.Contains(urlStr, ".svc/")) &&
		!strings.Contains(urlStr, "kubernetes.default.svc") {
		urlStr = "http://" + strings.TrimPrefix(urlStr, "https://")
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

	// Use SecureClient for kubernetes.default.svc URLs
	client := t.Client
	if strings.Contains(urlStr, "kubernetes.default.svc") {
		client = t.SecureClient
	}

	resp, err := client.Do(req)
	if err != nil {
		errText := strings.ToLower(err.Error())
		if strings.Contains(errText, "connection refused") {
			return ToolResult{Error: fmt.Sprintf("Connection refused at %s. Check if the service is running. Common internal URLs: gateway=http://gateway.valhalla.svc:8080, seidr=http://seidr.valhalla.svc:8082, gitea=http://gitea-http.gitea.svc.cluster.local:3000", urlStr)}
		}
		return ToolResult{Error: fmt.Sprintf("request failed: %s", err)}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to read response: %s", err)}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ToolResult{Error: "Authentication failed. For Gitea API, ensure Authorization header uses $GITEA_TOKEN. For internal services, most don't require auth."}
	}

	output := fmt.Sprintf("HTTP %d %s\n\n%s", resp.StatusCode, resp.Status, string(respBody))
	return ToolResult{Output: output}
}
