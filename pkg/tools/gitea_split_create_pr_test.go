package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreatePRToolReturnsExistingPROn409(t *testing.T) {
	t.Helper()

	const (
		owner    = "kit"
		repo     = "hirdforge"
		head     = "feature-branch"
		base     = "main"
		prNum    = 282
		prURL    = "https://gitea.example.com/kit/hirdforge/pulls/282"
		prTitle  = "Improve PR creation"
		postBody = "create this PR"
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/"+owner+"/"+repo+"/pulls":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create-pr body: %v", err)
			}
			if body["title"] != prTitle {
				t.Fatalf("title = %q", body["title"])
			}
			if body["head"] != head {
				t.Fatalf("head = %q", body["head"])
			}
			if body["base"] != base {
				t.Fatalf("base = %q", body["base"])
			}
			if body["body"] != postBody {
				t.Fatalf("body = %q", body["body"])
			}
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`pull request already exists for these targets [head_branch: ` + head + `, base_branch: ` + base + `]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/"+owner+"/"+repo+"/pulls":
			if got := r.URL.Query().Get("state"); got != "open" {
				t.Fatalf("state query = %q", got)
			}
			if got := r.URL.Query().Get("head"); got != owner+":"+head {
				t.Fatalf("head query = %q", got)
			}
			if got := r.URL.Query().Get("base"); got != base {
				t.Fatalf("base query = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{
				{
					"number":   prNum,
					"html_url": prURL,
					"head": map[string]interface{}{
						"ref": head,
					},
					"base": map[string]interface{}{
						"ref": base,
					},
					"state": "open",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tool := NewCreatePRTool(&GiteaAPITool{
		GiteaURL: server.URL,
		Token:    "token",
		Client:   server.Client(),
	})

	res := tool.Execute(map[string]interface{}{
		"repo":  owner + "/" + repo,
		"head":  head,
		"base":  base,
		"title": prTitle,
		"body":  postBody,
	})
	if res.Error != "" {
		t.Fatalf("create-pr returned error: %s", res.Error)
	}
	if !strings.Contains(res.Output, prURL) {
		t.Fatalf("output missing PR URL: %q", res.Output)
	}
	if !strings.Contains(res.Output, "already exists") {
		t.Fatalf("output missing idempotent message: %q", res.Output)
	}
	if !strings.Contains(res.Output, "#282") {
		t.Fatalf("output missing PR number: %q", res.Output)
	}
}
