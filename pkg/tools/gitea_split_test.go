package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCreateReviewToolCapturesSharedMemory(t *testing.T) {
	type rememberRequest struct {
		Agent   string   `json:"agent"`
		Content string   `json:"content"`
		Tags    []string `json:"tags"`
		Type    string   `json:"type"`
		Shared  bool     `json:"shared"`
	}

	rememberCh := make(chan rememberRequest, 1)
	memory := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/remember" {
			http.NotFound(w, r)
			return
		}
		var req rememberRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode remember request: %v", err)
		}
		rememberCh <- req
		w.WriteHeader(http.StatusOK)
	}))
	defer memory.Close()

	gitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/repos/kit/hirdforge/pulls/42/reviews" {
			http.NotFound(w, r)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode review body: %v", err)
		}
		if body["event"] != "APPROVED" {
			t.Fatalf("event = %q", body["event"])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"number":   42,
			"html_url": "https://gitea.example.com/kit/hirdforge/pulls/42",
		})
	}))
	defer gitea.Close()

	api := &GiteaAPITool{
		GiteaURL:       gitea.URL,
		ReviewersToken: "review-token",
		Client:         gitea.Client(),
	}
	tool := NewCreateReviewTool(api, memory.URL, "ragnar")

	body := "This review body is intentionally long so that the Seidr summary path has to truncate the content at two hundred characters. " + strings.Repeat("x", 240)
	res := tool.Execute(map[string]interface{}{
		"repo":  "kit/hirdforge",
		"index": 42.0,
		"state": "APPROVED",
		"body":  body,
	})
	if res.Error != "" {
		t.Fatalf("create-review failed: %s", res.Error)
	}
	if got := res.Output; got != "submitted APPROVED review on PR #42" {
		t.Fatalf("output = %q", got)
	}

	var req rememberRequest
	select {
	case req = <-rememberCh:
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive memory request")
	}

	if req.Agent != "ragnar" {
		t.Fatalf("agent = %q", req.Agent)
	}
	if !req.Shared {
		t.Fatal("shared flag was not set")
	}
	if req.Type != "observation" {
		t.Fatalf("type = %q", req.Type)
	}
	expectedPrefix := "REVIEW APPROVED | repo: kit/hirdforge | PR #42 | summary: "
	if !strings.HasPrefix(req.Content, expectedPrefix) {
		t.Fatalf("content prefix mismatch: %q", req.Content)
	}
	if !strings.Contains(strings.Join(req.Tags, ","), "review,ragnar,kit/hirdforge") {
		t.Fatalf("tags = %q", strings.Join(req.Tags, ","))
	}
	if len([]rune(req.Content)) <= len([]rune(expectedPrefix)) {
		t.Fatalf("content was not populated: %q", req.Content)
	}
}
