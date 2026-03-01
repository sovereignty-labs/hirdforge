package tasklife

import (
	"strings"
	"testing"
)

func TestOptimizeDelegationCommandTierKeepsIssue(t *testing.T) {
	in := DelegationFormat{
		Task:     "Implement the gateway notification endpoint",
		Issue:    "Dashboard clients do not receive updates.",
		Steps:    []string{"Add POST /api/v1/notify", "Broadcast to websocket clients"},
		DoneWhen: "Notifications appear in polling and websocket streams.",
	}

	out := OptimizeDelegation("qwen3:30b", in)
	if !strings.Contains(out, "ISSUE: Dashboard clients do not receive updates.") {
		t.Fatalf("expected full command-tier delegation, got:\n%s", out)
	}
}

func TestOptimizeDelegationStrikeTierDropsIssue(t *testing.T) {
	in := DelegationFormat{
		Task:     "Implement the gateway notification endpoint",
		Issue:    strings.Repeat("verbose ", 30),
		Steps:    []string{"Add POST /api/v1/notify", "Broadcast to websocket clients", "Store notifications in memory"},
		DoneWhen: "Notifications appear in polling and websocket streams.",
	}

	out := OptimizeDelegation("gpt-4.1-mini", in)
	if strings.Contains(out, "ISSUE:") {
		t.Fatalf("expected strike tier to omit issue section, got:\n%s", out)
	}
	if estimateTokenCount(out) > 500 {
		t.Fatalf("expected strike tier output <= 500 tokens, got %d", estimateTokenCount(out))
	}
}

func TestOptimizeDelegationReserveTierMinimizesContent(t *testing.T) {
	in := DelegationFormat{
		Task:     "Implement the gateway notification endpoint",
		Issue:    "Dashboard clients do not receive updates.",
		Steps:    []string{"Add POST /api/v1/notify", "Broadcast to websocket clients", "Store notifications in memory"},
		DoneWhen: "Notifications appear in polling and websocket streams.",
	}

	out := OptimizeDelegation("unknown-model", in)
	if strings.Contains(out, "ISSUE:") {
		t.Fatalf("expected reserve/default output to omit issue, got:\n%s", out)
	}
}

func TestOptimizeDelegationCodexMiniSpecialHandling(t *testing.T) {
	in := DelegationFormat{
		Task:     "Handle infrastructure security cleanup for the cluster",
		Issue:    "Use curl -X POST to hit the endpoint.",
		Steps:    []string{"Use curl -X POST to test the cluster endpoint", "Write the security note"},
		DoneWhen: "Infrastructure security wording is removed.",
	}

	out := OptimizeDelegation("codex-mini", in)
	lower := strings.ToLower(out)
	if strings.Contains(lower, "infrastructure") || strings.Contains(lower, "security") || strings.Contains(lower, "cluster") {
		t.Fatalf("expected codex-mini output to strip infrastructure/security terms, got:\n%s", out)
	}
	if strings.Contains(out, "curl -X POST") {
		t.Fatalf("expected codex-mini output to avoid raw curl instructions, got:\n%s", out)
	}
	if estimateTokenCount(out) > 200 {
		t.Fatalf("expected codex-mini output <= 200 tokens, got %d", estimateTokenCount(out))
	}
}
