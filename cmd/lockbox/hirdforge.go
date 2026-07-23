package main

// HirdforgeService is the v2 merge-authorization service (P1.7). Cortex
// queues a merge_pr action when a task reaches `approved`; the human approves
// it in the Lockbox queue; Execute then calls the gateway's internal merge
// endpoint, which performs the Gitea merge — the apply, and the sole write to
// a protected branch. A rejected queue entry never reaches Execute, so a
// rejection mechanically cannot merge.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

type HirdforgeService struct {
	GatewayURL string // e.g. http://gateway.asgard.svc:8080
	Secret     string // shared secret for the internal callback
	Client     *http.Client
}

func newHirdforgeServiceFromEnv() *HirdforgeService {
	url := strings.TrimSpace(os.Getenv("HIRDFORGE_GATEWAY_URL"))
	secret := strings.TrimSpace(os.Getenv("HIRDFORGE_MERGE_SECRET"))
	if url == "" {
		return nil
	}
	return &HirdforgeService{
		GatewayURL: strings.TrimSuffix(url, "/"),
		Secret:     secret,
		Client:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (h *HirdforgeService) Name() string               { return "hirdforge" }
func (h *HirdforgeService) SupportedActions() []string { return []string{"merge_pr"} }
func (h *HirdforgeService) IsWriteAction(action string) bool {
	return action == "merge_pr" // always queued for human approval
}

func (h *HirdforgeService) Execute(action string, params map[string]interface{}, _ ServiceCredential) (interface{}, error) {
	if action != "merge_pr" {
		return nil, fmt.Errorf("hirdforge: unsupported action %q", action)
	}
	taskID, _ := params["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("hirdforge: merge_pr requires task_id")
	}
	body, _ := json.Marshal(map[string]string{"task_id": taskID})
	req, err := http.NewRequest(http.MethodPost, h.GatewayURL+"/api/v1/cortex/internal/merge-approved", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hirdforge-Merge-Secret", h.Secret)
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hirdforge: gateway merge callback: %w", err)
	}
	defer resp.Body.Close()
	var out map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("hirdforge: gateway merge failed: status %d: %v", resp.StatusCode, out)
	}
	return out, nil
}
