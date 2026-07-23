// Package sandbox implements the reset-to-clean per-task execution
// environment (D-SANDBOX) as a k8s Job in the dedicated sandbox namespace.
// Contract: docs/specs/contracts/SANDBOX_LIFECYCLE.md. The k8s API is spoken
// as raw REST over the in-cluster service account — the same dependency-free
// pattern as cmd/gateway/cluster.go — so tests drive it with a plain
// httptest server.
package sandbox

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// K8sClient is a minimal Kubernetes REST client. BaseURL/Token/HTTPClient are
// injectable so tests can point it at a fake API server.
type K8sClient struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

// InClusterClient builds a client from the pod's service-account files,
// mirroring cmd/gateway/cluster.go initK8s. Returns an error (not a silent
// disable) — the sandbox is load-bearing; degrading it silently is a doctrine
// violation.
func InClusterClient() (*K8sClient, error) {
	tok, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return nil, fmt.Errorf("sandbox: no serviceaccount token: %w", err)
	}
	ca, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("sandbox: no serviceaccount CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("sandbox: invalid serviceaccount CA")
	}
	return &K8sClient{
		BaseURL: "https://kubernetes.default.svc",
		Token:   strings.TrimSpace(string(tok)),
		HTTPClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		},
	}, nil
}

func (c *K8sClient) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.HTTPClient.Do(req)
}

// doJSON performs a request and returns the response body, treating any
// non-2xx as an error carrying the API server's message.
func (c *K8sClient) doJSON(ctx context.Context, method, path string, body io.Reader) ([]byte, error) {
	resp, err := c.do(ctx, method, path, body, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return data, fmt.Errorf("k8s %s %s: %s: %s", method, path, resp.Status, truncate(string(data), 300))
	}
	return data, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
