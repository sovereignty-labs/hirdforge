# Contract 2 — The Dispatch Envelope

**Status: DRAFT, awaiting approval.** The deterministic message Cortex constructs
and delivers to exactly one agent. Today's dispatch wraps everything into prose
and regex-extracts facts back out (`wrapTaskForDispatch`,
`cmd/gateway/webhooks_support.go:408`; `extractObjectiveSentence`,
`sessions.go:99`) — the envelope replaces that with structure.

## Delivery

The gateway POSTs the envelope to the sandboxed agent process (see
SANDBOX_LIFECYCLE.md — in Phase 1 the envelope is the Job's input, mounted as
`/task/envelope.json` and mirrored in the env var `HF_ENVELOPE_PATH`). The agent
runs in **one-shot mode**: read envelope → do the work → exit. The agent's
existing `/tasks/send` server mode is untouched (v1 surfaces keep using it).

The prompt the model sees is **rendered from the envelope by a fixed Go
template** — same envelope ⇒ same prompt, byte for byte. Template text ships
with the route config, versioned in-repo.

## Shape (JSON, `envelope_version: 1`)

```jsonc
{
  "envelope_version": 1,
  "task_id": "hf-01J...",            // Cortex-minted, ULID
  "route_id": "build-on-label",
  "attempt": 1,                       // 1-based; >1 only via retry
  "role": "builder",                  // builder | reviewer

  "issue": {                          // the work source (null for pure-review dispatches)
    "repo": "kit/hirdforge",
    "number": 341,
    "title": "…",
    "body": "…",                      // verbatim; data, not instructions to Cortex
    "labels": ["agent:build"]
  },

  "git": {
    "clone_url": "https://git.hirdforge.com/kit/hirdforge.git",
    "base_branch": "main",
    "work_branch": "agent/hf-01J...", // Cortex-assigned; agent pushes ONLY here
    "push_credential_ref": "sandbox-git-cred"   // secret name; never inline
  },

  "bundle": { "skills": [], "memory_scopes": [], "profile": "default" },

  "done_when": "Open a PR from agent/hf-01J... to main on kit/hirdforge. The task is complete ONLY when `go build ./... && go vet ./... && go test ./...` exits 0 — this will be verified mechanically after you exit; your own assessment does not decide completion.",

  "failure_context": null,            // retry variant — D-LESSONS #2
  "review": null                      // reviewer variant — D-LESSONS #4
}
```

### Retry variant (`attempt > 1`)

```jsonc
"failure_context": {
  "prior_agent": "agent-1",
  "prior_attempt": 1,
  "reason": "gate_failed:test-command",
  "gate_excerpt": "--- FAIL: TestCortexRoute (0.00s)\n    routes_test.go:41: …",  // ≤ 4 KiB tail
  "reviewer_feedback": ["…"]          // populated when the revise route redispatches
}
```
Blind retry is a defect (D-LESSONS #2): Cortex MUST populate this from the
failed task's record on every retry/revise dispatch.

### Reviewer variant (`role: reviewer`)

```jsonc
"review": {
  "pr": { "repo": "kit/hirdforge", "number": 355, "head": "agent/hf-01J...", "base": "main" },
  "diff": "…",                        // the unified diff, inline, ≤ 256 KiB
  "diff_truncated": false,            // if true, agent must fetch the rest via list-pr-files
  "gate_result": { "type": "test-command", "passed": true, "excerpt": "ok  \tgit.hirdforge.com/kit/hirdforge/...  1.0s" }
}
```
The reviewer **sees the artifact** (D-LESSONS #4) and the mechanical gate's
evidence. Its verdict is delivered by submitting a real Gitea PR review
(`create-review` tool, `APPROVE` or `REQUEST_CHANGES`) — which returns to
Cortex as a `pr.review_submitted` webhook. The reviewer's prose is never parsed.

## Agent exit contract (what comes back)

The agent does not report status — it *exits*, and mechanics observe:
1. The **PR ref** is observed by Cortex from Gitea (the PR the work branch
   opened), not trusted from agent output. M1's structured exit
   (`session_termination` + `runOutcome`, `cmd/agent/termination.go`,
   `result_outcome.go`) is captured as *diagnostics* on the task record —
   never as a lifecycle cause.
2. Exit code 0 = the loop ended normally; non-zero = harness failure. Either
   way the done-gate then runs (DONE_GATE.md) and *it* decides.

## Determinism obligations

- Envelope assembly is a pure function `(route, event, task record) → envelope`
  — table-tested, no clock/model input except the minted `task_id`.
- Issue bodies and reviewer feedback ride as **data**; nothing in Cortex
  interprets them. Only the model sees them.
- Secrets never appear in the envelope; `push_credential_ref` names a k8s
  secret mounted by the sandbox.
