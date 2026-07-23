package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	tasklifepkg "git.hirdforge.com/kit/hirdforge/pkg/tasklife"
	taskspkg "git.hirdforge.com/kit/hirdforge/pkg/tasks"
)

type serverDeps struct {
	episodic            bool
	workspace           string
	webhookSecret       string
	agentName           string
	peerNames           []string
	processConversation conversationProcessor
	taskStore           *taskspkg.Store
	taskTracker         *tasklifepkg.TaskTracker
	sovereignStates     map[string]bool
	sovereignReporter   *tasklifepkg.SovereignReporter
	taskCancelMu        *sync.Mutex
	taskCancels         map[string]context.CancelFunc
	requirePRPattern    string
	completionMaxNudges int
	reviewTracker       *reviewContextTracker
	agentDescription    string
	a2aRuntime          *a2aRuntime
}

func registerRoutes(mux *http.ServeMux, deps serverDeps) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, dashboardHTML)
	})
	mux.HandleFunc("/.well-known/agent.json", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"name":        deps.agentName,
			"description": deps.agentDescription,
			"url":         agentBaseURL(r),
			"version":     "1.0.0",
			"capabilities": map[string]interface{}{
				"streaming":         true,
				"pushNotifications": true,
			},
			"skills": enabledTools,
			"interfaces": []map[string]string{
				{
					"protocol": "jsonrpc",
					"url":      agentBaseURL(r) + "/a2a",
				},
				{
					"protocol": "grpc",
					"url":      agentGRPCURL(r),
				},
			},
			"provider": map[string]string{
				"organization": "Hirdforge",
				"url":          "https://hirdforge.com",
			},
		})
	})
	if deps.a2aRuntime != nil {
		mux.HandleFunc("/a2a", deps.a2aRuntime.handleJSONRPC)
	}
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(statusPayload())
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(statusPayload())
	})
	mux.HandleFunc("/api/v1/files", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		files, err := listWorkspaceFiles(deps.workspace, 1000)
		if err != nil {
			incError("workspace list failed", err, nil)
			http.Error(w, "failed listing files", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(files)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = io.WriteString(w, metricsText())
	})
	mux.HandleFunc("/webhook/gitea", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024))
		if err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(deps.webhookSecret) == "" {
			http.Error(w, "webhook secret not configured", http.StatusServiceUnavailable)
			return
		}
		sig := r.Header.Get("X-Gitea-Signature")
		if !validateGiteaHMAC(deps.webhookSecret, body, sig) {
			incError("webhook signature validation failed", fmt.Errorf("invalid signature"), map[string]interface{}{
				"event":    r.Header.Get("X-Gitea-Event"),
				"delivery": r.Header.Get("X-Gitea-Delivery"),
			})
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		logJSON("info", "gitea webhook received", map[string]interface{}{
			"event":       r.Header.Get("X-Gitea-Event"),
			"delivery":    r.Header.Get("X-Gitea-Delivery"),
			"payload_len": len(body),
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	registerSessionRoutes(mux, deps)
	registerTaskRoutes(mux, deps)
}
