# Hirdforge v2 — Decision Log

**Status:** Living document. Closed decisions are binding; do not relitigate them
in-session. Reopening one requires flagging it to Kit with new evidence — never a
silent deviation. Open items name who closes them and when.

Companion to HIRDFORGE_V2_PRD.md (plan of record) and the source specs in
`docs/source/`.

---

## Closed decisions

**D-SKELETON — Walking skeleton first.** The first build target is a thin
end-to-end vertical slice: one agent → Cortex router → mechanically-gated reviewer
→ one PR, dogfooded on this repo. It proves the entire determinism spine in the
smallest slice; the researcher role, fleet scaling, sandbox hardening, concierge,
and UI all layer on afterward. Rationale: earliest honest "it works" signal and
the fastest demoable loop. *2026-07-18.*

**D-SANDBOX — Native sandbox, rebuild the pattern.** Agents get isolation via a
reset-to-clean ephemeral sandbox rebuilt natively into hirdforge's dispatch, reusing
the *design* of the KWS ephemeral-VM approach (built there in ~an hour) — not a
runtime dependency on the KWS/omniagent deployment. The k8s scaffold existed
(empty `agent-testing` namespace, `sandbox-admin` RBAC, gVisor/Kata TODO) but the
execution was never built. Estimate corrected from "months (native gVisor from
scratch)" to "hours-to-days (proven pattern)." *2026-07-18.*

**D-INFRA — Two substrates, un-bundled.** Git and compute are separate homes:
- **Git side — KWS Gitea (`git.example.internal`), master → forge.example.internal failover mirror.**
  Repo, PRs, webhooks, CI. The platform wires into KWS Gitea: the gateway receives
  webhooks *from* it and opens PRs *on* it.
- **Compute side — Asgard k8s.** Gateway, agents, sandboxes, Postgres, Seidr run
  here (reuse existing Asgard Postgres/Seidr).
The original round-1 question compounded "where does it run" with "what Gitea does
it wire into"; those have different answers and must never be merged again.
**Open dependency:** KWS Gitea CI-runner availability — see O-CI. *2026-07-18.*

**D-WITNESS — Platform-first; Witness is a separate downstream project.** Hirdforge
is built as a fully general autonomous-build platform, start to finish. A
customized Witness adaptation comes after, in its own repo, riding on top of a
finished hirdforge. No Witness seams, no claim-protocol wiring in the core. The
"researcher" role is a general research/coding capability, not a Judge. *2026-07-18.*

**D-GATE — Per-route mechanical done-gate.** Each `cortex.yaml` route declares its
completion gate: `done_gate: ci-status | test-command | custom-validator`. Every
option is mechanical (a CI status, a test-command exit code, a validator exit
code); *which* mechanical check varies per capability, *that* it is mechanical is
invariant. **A model's free-text output never decides a completion or lifecycle
transition** — that is the v1 `pkg/tasklife` failure and is forbidden. Reviewer
judgment is a gate on top of the mechanical one, never a substitute. The routing
schema is a load-bearing contract (stop-and-ask #2) — draft before building.
*2026-07-18.*

**D-PORT — Build Cortex anew; borrow lessons, not code.** Workbench's cortex and
v2's cortex solve different problems. Workbench cortex is an in-process multi-lane
fan-out → aggregate → stage → apply engine, complex *because it has no git host in
its loop* (a desktop app writing to a local dir: staged worktrees, F1/F2 write
guards, aggregate-merge/divergence). v2's cortex is a *router* — webhook → route →
dispatch one agent → track lifecycle → route the PR — and it is leaner because
**Gitea does the staging (branch), review surface (PR), apply (merge), and gate
(Lockbox-on-merge)** that Workbench had to hand-build. "Porting" would collapse
into "rewriting for a different execution model" (in-memory→Postgres, operator-
driven→webhook-driven, local-apply→PR-based). Build fresh against the v2 spec.
Carry forward the four lessons (D-LESSONS), not the modules. Exception: if
*parallel-attempt-then-integrate* is ever wanted (N agents race a task, best/merged
wins), revisit Workbench's aggregate logic then — advanced mode, not baseline, and
even then git-branch merging obviates most of it. *2026-07-18.*

**D-BRAND — Rebrand (Phase 0).** Module path `git.hirdforge.com/kit/hirdforge`
→ **`git.example.internal/hirdforge/hirdforge`** (matches the master home; a module path is
an internal identifier, not a fetched URL — GitHub was never a candidate). Drop the
Valhalla-military flavor: **warrior→agent** (the binary is already `cmd/agent`),
**warband→fleet**, **chieftain→removed** (Cortex replaces it). Keep the component
names that are already good products: **Cortex, Seidr, Lockbox, concierge,
hirdforge**. The actual rename (go.mod + every import + vocabulary) is Phase 0
build work; the docs describe the target state. **Confirm-able:** whether Seidr,
SOUL, and concierge keep their names — flagged, low-churn find-replace if changed.
*2026-07-18.*

**D-CONTROL — Control surface = dispatch / retry / cancel.** The operator control
verbs wired up front are: manually dispatch a task, retry a failed one, cancel an
in-flight one. NOT pause/resume, gate-override, or live-intervene-in-a-warrior —
those seams are left clean in the observability contract but not built now. Any
future gate-override MUST be a loud audit event (never silent). *2026-07-18.*

**D-UISEQ — Backend first, then deep wireframe, then UI.** The backend is built and
confirmed before UI work. The observability/control API surface
(`OBSERVABILITY_CONTRACT.md`) is frozen first so the UI is wireframed against a
stable contract, not a moving target. High control + observability are wired up
front (in the backend contract), not bolted on. *2026-07-18.*

**D-LESSONS — The four carry-forward lessons are binding constraints.** From
Workbench, each earned:
1. **Ground-truth completion** (see D-GATE). Mechanical, never model-judged.
2. **Escalation with failure context.** A retry dispatch carries prior-agent,
   prior-failure-reason, and validator excerpt. Blind retry is a defect.
3. **A sanctioned revise path.** A reviewer "changes requested" routes back to a
   builder via a PR-review-event route; never a dead end.
4. **The reviewer sees the artifact.** Every gated role receives the material it
   judges (trivially satisfied by a PR diff; generalizes to all gated roles).
*2026-07-18.*

---

## Open items

**O-CI — Does KWS Gitea have working CI runners?** The platform wires into KWS
Gitea (D-INFRA), so a route whose `done_gate` is `ci-status` needs runners *there*.
Asgard's Gitea has runners, but the platform does not use Asgard's Gitea. Not
blocking — D-GATE is per-route, so the walking skeleton can use `test-command`
(sandbox exit code) instead. *Owner: confirm during Phase 0/1 infra setup; log the
answer here.* *2026-07-18.*

**O-BRAND-NAMES — Confirm Seidr / SOUL / concierge names.** D-BRAND keeps them by
default; Kit may redline. Trivial find-replace if changed, but do it in Phase 0
before the vocabulary is spread across docs and code. *Owner: Kit, at Phase 0.*

**O-SANDBOX-CONTRACT — The sandbox lifecycle contract.** allocate → checkout →
run → collect-PR → reset/destroy. Load-bearing (stop-and-ask #2). Draft as a
standalone contract before Phase 1 dispatch is wired. *Owner: draft at Phase 1
start, approve before building.*

**O-ROUTING-SCHEMA — The `cortex.yaml` routing + dispatch envelope + done-gate
schema.** The core new contract (stop-and-ask #2). Draft standalone, approve first.
*Owner: draft at Phase 1 start.*

**O-PERSISTENCE — The Postgres lifecycle-tracking schema.** Extends/replaces the
existing A2A task store per the v2 spec's Task shape. Load-bearing. *Owner: draft
at Phase 1 start.*

**O-WEBHOOK-SECRET — Webhook auth from KWS Gitea → Asgard gateway.** HMAC
validation reusing existing `webhook.go` patterns; confirm the secret provisioning
path across the two substrates. *Owner: Phase 1 infra.*
