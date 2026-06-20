# Hirdforge Workbench — operator loop API smoke

A copy-paste sequence that exercises the full first operator loop against a
running `hirdforge-workbench` using only the local HTTP API and a tiny mock
provider. No browser, no external services.

The loop:

```
Project → Provider → Architect → Cortex Task → Builder Proposal → Aggregate
→ Review → Lockbox Approval → Apply Preview → Explicit Apply → Validation
```

## 1. Mock provider

The backend calls `<base_url>/chat/completions`. One combined response satisfies
the Architect (`{message, spec}`), Builder proposal (`{summary, files}`) and
Reviewer (`{verdict, summary, risks, recommendations}`) parsers — each ignores
the other's fields:

```python
# mockprov.py — python3 mockprov.py  (listens on :7807)
import http.server, json
CONTENT = json.dumps({
    "message": "refined",
    "spec": {"goal": "ship it well", "constraints": [], "affected_areas": [],
             "acceptance_criteria": [], "risks": [], "open_questions": [], "suggested_lanes": ["builder"]},
    "summary": "add the feature",
    "files": [{"path": "newfile.go", "action": "create", "content": "package main\n", "rationale": "scaffold"}],
    "verdict": "approve", "risks": [], "recommendations": [],
})
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers.get('Content-Length', 0)))
        body = json.dumps({"choices": [{"message": {"role": "assistant", "content": CONTENT}}]}).encode()
        self.send_response(200); self.send_header('Content-Type', 'application/json'); self.end_headers(); self.wfile.write(body)
    def log_message(self, *a): pass
http.server.HTTPServer(('127.0.0.1', 7807), H).serve_forever()
```

## 2. Start the workbench

```bash
go build -o /tmp/hf-wb ./cmd/hirdforge-workbench/
HIRDFORGE_WORKBENCH_PORT=7806 /tmp/hf-wb &
B=http://127.0.0.1:7806
P=$(mktemp -d)          # the project that will be written to
```

## 3. The loop (each step gated by the previous)

```bash
# Project + Provider (the api key is never echoed back)
curl -s -X POST "$B/api/workbench/project/open" -d "{\"path\":\"$P\"}"
curl -s -X POST "$B/api/workbench/provider" \
  -d '{"base_url":"http://127.0.0.1:7807","api_key":"FAKE","model":"mock"}'

# Architect: session → message → accept (spec drafted by the provider)
SID=$(curl -s -X POST "$B/api/workbench/architect/session" -d '{"goal":"ship it"}' | jq -r .id)
curl -s -X POST "$B/api/workbench/architect/message" -d "{\"session_id\":\"$SID\",\"message\":\"refine\"}"
curl -s -X POST "$B/api/workbench/architect/accept"  -d "{\"session_id\":\"$SID\"}"

# Cortex task (idempotent per accepted session) → builder/reviewer lanes
TASK=$(curl -s -X POST "$B/api/workbench/architect/cortex-task" -d "{\"session_id\":\"$SID\"}")
BLANE=$(echo "$TASK" | jq -r '.lanes[] | select(.role=="builder")  | .id' | head -1)
RLANE=$(echo "$TASK" | jq -r '.lanes[] | select(.role=="reviewer") | .id' | head -1)

# Builder proposal (reuses an existing "proposed" proposal for the lane)
curl -s -X POST "$B/api/workbench/cortex/lane/propose" -d "{\"lane_id\":\"$BLANE\"}"

# Aggregate → Review (review reuses an existing review per aggregate+lane)
AGG=$(curl -s -X POST "$B/api/workbench/cortex/aggregate" -d '{}' | jq -r .id)
curl -s -X POST "$B/api/workbench/cortex/aggregate/review" \
  -d "{\"aggregate_id\":\"$AGG\",\"lane_id\":\"$RLANE\"}"

# Lockbox: request (reuses pending/approved per aggregate) → approve (human gate)
LB=$(curl -s -X POST "$B/api/workbench/cortex/aggregate/lockbox" -d "{\"id\":\"$AGG\"}" | jq -r .id)
curl -s -X POST "$B/api/workbench/lockbox/approve" -d "{\"id\":\"$LB\"}"

# Apply preview — READ-ONLY, never writes files (200 ready / 409 blocked / 502 failed)
curl -s -X POST "$B/api/workbench/cortex/apply/preview" -d "{\"lockbox_request_id\":\"$LB\"}"

# Explicit apply — THE write boundary. Requires the approved Lockbox request +
# aggregated aggregate + open project. Idempotent: a repeat returns the existing
# applied result and does NOT re-write files.
curl -s -X POST "$B/api/workbench/cortex/apply" -d "{\"lockbox_request_id\":\"$LB\"}"
cat "$P/newfile.go"     # the only file written; confined to the project root

# Post-apply validation — bounded local command (no shell), captured output
curl -s -X POST "$B/api/workbench/cortex/apply/validate" -d '{"command":"echo validation-ran"}'
```

## 4. Reload / hydration reads

The UI rebuilds the whole run from these GETs on boot (404 = "nothing yet",
treated as normal empty state, never an error):

```
GET /api/workbench/project
GET /api/workbench/provider
GET /api/workbench/architect/session   GET /api/workbench/architect/sessions
GET /api/workbench/cortex/task          GET /api/workbench/cortex/lane/proposals
GET /api/workbench/cortex/aggregate     GET /api/workbench/cortex/aggregate/review
GET /api/workbench/lockbox/requests     GET /api/workbench/cortex/apply/preview
GET /api/workbench/cortex/apply         GET /api/workbench/cortex/apply/validation
GET /api/workbench/events
```

## 5. Safety boundaries (do not bypass)

- **Preview is read-only** — it never writes files.
- **Apply writes files** and is gated by an *approved* Lockbox request whose
  `proposal_id` is `aggregate:<id>`, a non-conflicted (`aggregated`) aggregate,
  and an open project. Writes go through a path resolver confined to the project
  root and touch only the aggregate's listed files.
- **Apply is idempotent** — a repeat returns the existing applied result and does
  not re-write. A failed apply is retryable.
- In the UI, apply runs **only** from an explicit click after a `confirm()`;
  never on load, preview, or approval.
- Provider API keys are stored on the backend and never returned by any read.
