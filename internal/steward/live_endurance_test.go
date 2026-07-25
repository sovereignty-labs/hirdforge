//go:build live

// Live endurance validation (P4.5) against a real model — NOT run in CI (the
// `live` build tag gates it). Run manually:
//
//	INFER_URL=http://203.0.113.20:8002 INFER_KEY=local INFER_MODEL=qwen-reserved \
//	  go test -tags live -run TestLiveEndurance ./internal/steward/ -v -timeout 20m
//
// It drives a long conversation through the real Engine with a real TurnRunner and
// a real Fold summarizer, proving: the older span folds, the active plan stays
// pinned past the window, and — the point — the model still answers a question
// about a fact stated early in the session AFTER that turn was folded into the
// summary. That is "hold the session together for as long as we can" on live
// hardware, not a stub.
package steward

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// liveModel is a minimal OpenAI-compatible chat client. Endpoint/key/model are
// configuration (env), honoring the "configurable, collapsible to one endpoint"
// constraint even here.
type liveModel struct {
	url, key, model string
	http            *http.Client
}

func newLiveModel() liveModel {
	return liveModel{
		url:   envOr("INFER_URL", "http://203.0.113.20:8002"),
		key:   envOr("INFER_KEY", "local"),
		model: envOr("INFER_MODEL", "qwen-reserved"),
		http:  &http.Client{Timeout: 4 * time.Minute},
	}
}

func (m liveModel) complete(ctx context.Context, messages []map[string]string, maxTokens int) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": m.model, "messages": messages, "max_tokens": maxTokens, "temperature": 0,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.url, "/")+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.key)
	resp, err := m.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("inference %d: %s", resp.StatusCode, string(raw)[:min(200, len(raw))])
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return "", fmt.Errorf("decode: %v (%s)", err, string(raw)[:min(200, len(raw))])
	}
	return out.Choices[0].Message.Content, nil
}

const stewardSystemPrompt = `You are the interlocutor: the main chat agent the operator talks to. Read-only; you propose, you never act.
Reply in plain PROSE — normal conversation. Do NOT wrap your reply in JSON.
ONLY when the operator wants concrete work, append after your prose reply a plan as a single fenced JSON block:
` + "```json" + `
{"id":"<slug>","title":"<line>","steps":[{"id":"s1","title":"<t>","detail":"<d>","gate":"ci-status","needs_operator":false}]}
` + "```" + `
Use gate one of ci-status|test-command|custom-validator|operator; a human-held step uses gate "operator" and needs_operator true. A question or chat gets prose and NO plan block. Keep replies short.`

// liveRunner is a real TurnRunner: it threads the SessionContext (summary + pinned
// plan + recent turns) into a chat request, so the model gets exactly what the
// endurance policy decided to keep.
type liveRunner struct{ m liveModel }

func (r liveRunner) RunTurn(ctx context.Context, _, message string, sc SessionContext) (string, error) {
	msgs := []map[string]string{{"role": "system", "content": stewardSystemPrompt}}
	if sc.Summary != "" {
		msgs = append(msgs, map[string]string{"role": "system", "content": "Summary of earlier conversation (older turns, condensed): " + sc.Summary})
	}
	if sc.ActivePlan != nil {
		pj, _ := json.Marshal(sc.ActivePlan)
		msgs = append(msgs, map[string]string{"role": "system", "content": "The active plan under discussion (pinned): " + string(pj)})
	}
	for _, t := range sc.Recent {
		msgs = append(msgs, map[string]string{"role": "user", "content": t.Message})
		msgs = append(msgs, map[string]string{"role": "assistant", "content": t.Reply})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": message})
	return r.m.complete(ctx, msgs, 1200)
}

// liveSummarizer folds older turns into a running summary via the model.
type liveSummarizer struct{ m liveModel }

func (s liveSummarizer) Fold(ctx context.Context, prior string, older []Turn) (string, error) {
	var b strings.Builder
	if prior != "" {
		b.WriteString("Existing summary so far:\n" + prior + "\n\n")
	}
	b.WriteString("Fold these additional earlier turns into the summary, preserving concrete facts (names, hosts, numbers, decisions). Return ONLY the updated summary prose, no JSON:\n")
	for _, t := range older {
		fmt.Fprintf(&b, "- USER: %s\n  YOU: %s\n", t.Message, t.Reply)
	}
	// Generous budget: qwen-reserved is a reasoning model that spends tokens
	// thinking before it emits the summary; too small a budget yields empty content.
	return s.m.complete(ctx, []map[string]string{
		{"role": "system", "content": "You compress conversation history without losing concrete facts. Answer directly with the summary; keep thinking brief."},
		{"role": "user", "content": b.String()},
	}, 2000)
}

func TestLiveEndurance(t *testing.T) {
	m := newLiveModel()
	// Sanity: the endpoint answers before we invest 20 turns.
	if _, err := m.complete(context.Background(), []map[string]string{{"role": "user", "content": "say ok"}}, 8); err != nil {
		t.Fatalf("inference endpoint %s/%s unreachable: %v", m.url, m.model, err)
	}

	var folds int
	e := NewEngine(liveRunner{m}).
		WithSummarizer(liveSummarizer{m}).
		WithRecentWindow(4).
		OnCompact(func(_ string, folded int) { folds += folded; t.Logf("compacted: folded %d turns", folded) })

	sid := "live"
	ctx := context.Background()

	// Turn 1 plants a concrete fact the model must recall much later, after it is
	// folded away.
	secret := "the target host is studio.example.internal and the cert CA is example-internal-ca"
	script := []string{
		"Remember this for later: " + secret + ". Just acknowledge.",
		"What's the weather like on a typical build day? Just chat.",
		"Tell me a one-line fun fact about Go.",
		"What's your favorite kind of gate in a CI pipeline?",
		"Chat: what makes a good commit message?",
		"Another aside: why is idempotency nice in dispatch?",
		"Keep chatting: what's a sandbox good for?",
		"One more: why prefer mechanical gates over model judgment?",
		"Small talk: what's satisfying about a green test suite?",
		"Aside: what's a good branch naming habit?",
	}
	for i, msg := range script {
		out, err := e.Chat(ctx, sid, msg)
		if err != nil {
			t.Fatalf("turn %d (%q): %v", i, msg, err)
		}
		t.Logf("turn %d reply: %s", i, truncate(out.Reply, 100))
	}

	// By now the first turn is far outside the recent window of 4 and must have
	// been folded into the summary.
	if folds == 0 {
		t.Fatal("expected folding past the window; none happened")
	}
	if e.store.FoldedCount(sid) == 0 || e.store.Summary(sid) == "" {
		t.Fatalf("no running summary after a long session (folded=%d)", e.store.FoldedCount(sid))
	}
	t.Logf("running summary after %d turns (%d folded): %s", len(script), e.store.FoldedCount(sid), truncate(e.store.Summary(sid), 300))

	// THE test: the model must still recall the early fact, which now lives only in
	// the summary — proving endurance carried it past compaction.
	out, err := e.Chat(ctx, sid, "Back to the start — what host and CA did I ask you to remember?")
	if err != nil {
		t.Fatalf("recall turn: %v", err)
	}
	t.Logf("recall reply: %s", out.Reply)
	lower := strings.ToLower(out.Reply)
	if !strings.Contains(lower, "studio.example.internal") || !strings.Contains(lower, "example-internal-ca") {
		t.Fatalf("model lost the early fact past compaction — endurance failed.\nreply: %s\nsummary: %s", out.Reply, e.store.Summary(sid))
	}
	t.Log("PASS: early fact survived compaction via the running summary")
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
