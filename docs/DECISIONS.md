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
smallest slice; the researcher role, fleet scaling, sandbox hardening, steward,
and UI all layer on afterward. Rationale: earliest honest "it works" signal and
the fastest demoable loop. *2026-07-18.*

**D-SANDBOX — Native sandbox, rebuild the pattern.** Agents get isolation via a
reset-to-clean ephemeral sandbox rebuilt natively into hirdforge's dispatch, reusing
the *design* of the KWS ephemeral-VM approach (built there in ~an hour) — not a
runtime dependency on the KWS/omniagent deployment. The k8s scaffold existed
(empty `agent-testing` namespace, `sandbox-admin` RBAC, gVisor/Kata TODO) but the
execution was never built. Estimate corrected from "months (native gVisor from
scratch)" to "hours-to-days (proven pattern)." *2026-07-18.*

**D-INFRA — Traditional hirdforge topology, single home.** *(Corrected 2026-07-23,
approved by Kit at the v2 kickoff; supersedes the original two-substrate wording
that put the master on KWS `git.example.internal`.)*
- **Git master — `git.hirdforge.com` (Gitea, hosted in Asgard).** Repo, PRs,
  webhooks, CI. The platform wires into this Gitea: the gateway receives webhooks
  *from* it and opens PRs *on* it. KWS wiring is dropped; there is no
  forge.example.internal mirror in the loop.
- **Compute side — Asgard k8s.** Gateway, agents, sandboxes, Postgres, Seidr run
  here (reuse existing Asgard Postgres/Seidr). Deploys ride the existing
  `git.hirdforge.com` Gitea CI/CD pipeline (webhooks → CI → deploy).
- **Build/dev — agent-host.** Control + building consolidated on agent-host.
The round-1 lesson stands: "where does it run" and "what Gitea does it wire into"
are separate questions — they now happen to share the Asgard answer.
**Open dependency:** CI-runner availability on `git.hirdforge.com` — see O-CI.
*2026-07-18; corrected 2026-07-23.*

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

**D-BRAND — Rebrand (Phase 0).** Module path `github.com/kitporath/project_valhalla`
→ **`git.hirdforge.com/kit/hirdforge`** (matches the master home per the corrected
D-INFRA; a module path is an internal identifier, not a fetched URL — GitHub was
never a candidate). Drop the Valhalla-military flavor: **warrior→agent** (the
binary is already `cmd/agent`), **warband→fleet**, **chieftain→removed** (Cortex
replaces it). Keep the component names that are already good products: **Cortex,
Seidr, Lockbox, hirdforge**. Names resolved at kickoff (see O-BRAND-NAMES):
**Seidr stays; SOUL → Persona; Concierge → Steward.** The actual rename (go.mod +
every import + vocabulary) is Phase 0 build work. Live v1 external conventions
(`soul.md` paths in hirdforge-personas, the `warband_shared` Seidr collection, the
`warband` Gitea service account, `json:"warband"` wire fields) are **not** renamed
until the component that owns each is rebuilt — see AUDIT_DELTA_V2.
*2026-07-18; module path + names finalized 2026-07-23.*

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

**D-AUTONOMY — Autonomous build authority (Kit, 2026-07-23 kickoff).** The v2
rebuild is executed as autonomously as possible once the Task-0 contracts are
approved. Concretely granted:
1. **Merge authority:** in-phase PRs are self-merged by the builder when
   build/vet/tests are green. Kit reviews at phase boundaries (the CLAUDE.md
   ritual) and at every stop-and-ask trigger. Doctrine-touching and write-path
   changes still wait for Kit's merge. `main` remains push-protected — the PR
   flow itself is unchanged.
2. **Agent verification is the builder's job:** the builder agents must be
   verified working against the local model fabric before they carry real v2
   work. Verification targets: the **`qwen`** and **`qwen-reserved`** lanes
   (both Qwen3.6-27B dense — the 35B-A3B MoE was already swapped off the fabric
   2026-07-17 for weak agentic performance, per the LiteLLM config). The
   BUILDER_HARNESS benchmark gate (14-function refactor ≥8/10 clean) is the bar.
   **VERIFIED 2026-07-23: qwen 10/10 clean, qwen-reserved 10/10 clean**
   (`bench/builder/`, all four mechanical checks per round). The bar is met;
   builders are cleared to carry skeleton work.
3. **Lane use:** the model fabric (LiteLLM agent-host:4000 + anvil llama-servers) is
   free to drive for testing and benchmarks, no restrictions.
4. **Asgard authority:** full authority to create what the specs require in the
   cluster (sandbox namespace, NetworkPolicies, secrets, Postgres reuse) — via
   `asgard-infra` GitOps PRs where that is the convention, direct kubectl where
   it isn't. Existing Asgard Postgres + the `infrastructure/sandbox/` scaffold
   are the reuse targets.
*2026-07-23.*

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

**O-FAT-AGENT-IMAGE — The v1 `valhalla-agent` image cannot be built by the
airgapped CI runners.** `Dockerfile.agent` pulls kubectl/helm/kubeseal/talosctl/
crane/yq from the public internet; the Gitea runners have none, so the
`build-agent` job could only ever fail. It fired on every push touching
`cmd/agent/` or `pkg/` — exactly the v2 builder-harness work — so `main` went red
on our own mechanism merges while every other job stayed green (run 1195:
`build-agent` FAILURE, `build-agent-sandbox`/gateway/seidr/lockbox/hirdforge all
success). **Disabled behind `vars.BUILD_FAT_AGENT_IMAGE` 2026-07-24** so CI
signal is trustworthy again. Nothing consumed the artifact: all v1 agent
Deployments in asgard-infra pin an old sha (`sha-2ccfdc9` / `sha-e2b0f9a`) and no
manifest tracks `:latest`; the v2 dispatch path uses `valhalla-agent-sandbox`,
built from the lean airgap-safe `Dockerfile.agent-sandbox`. **To re-enable**, the
tool downloads must come from an in-cluster mirror first — otherwise flipping the
variable just restores the red. Decide whether the v1 fat image is still wanted
at all, or whether those legacy agents retire with v1. *2026-07-24.*

**O-CI — Does `git.hirdforge.com` Gitea have working CI runners?** *(Retargeted by
the D-INFRA correction — the KWS question is moot.)* The platform wires into
`git.hirdforge.com`, so a route whose `done_gate` is `ci-status` needs runners
there. Evidence suggests yes (the repo's `.gitea/workflows/` and its CI-rebuild
history ran against this master) — confirm live during Phase 1 infra setup and log
the answer here. Not blocking — D-GATE is per-route, so the walking skeleton uses
`test-command` (sandbox exit code). *2026-07-18; retargeted 2026-07-23.*

**O-BRAND-NAMES — RESOLVED 2026-07-23 (Kit, at kickoff).** **Seidr stays.
SOUL → Persona. Concierge → Steward.** Applied across the v2 docs in Phase 0.
Live v1 conventions that carry the old names (`soul.md` files in
hirdforge-personas, `soul:` self-improvement PR titles) migrate when the persona
loading is rebuilt — tracked in AUDIT_DELTA_V2, not renamed blind.

**O-SANDBOX-CONTRACT — The sandbox lifecycle contract.** allocate → checkout →
run → collect-PR → reset/destroy. Load-bearing (stop-and-ask #2).
**DRAFTED 2026-07-23** — `docs/specs/contracts/SANDBOX_LIFECYCLE.md` (per-task
k8s Job in a `hirdforge-sandbox` namespace). *Awaiting Kit's approval before
building.*

**O-ROUTING-SCHEMA — The `cortex.yaml` routing + dispatch envelope + done-gate
schema.** The core new contract (stop-and-ask #2). **DRAFTED 2026-07-23** —
`docs/specs/contracts/ROUTING_SCHEMA.md`, `DISPATCH_ENVELOPE.md`, and
`DONE_GATE.md` (ordered first-match routes; structured envelope; gate runs the
check itself, agent opinion is not an input; reviewer verdict = Gitea PR-review
webhook, never parsed prose). *Awaiting Kit's approval before building.*

**O-PERSISTENCE — The Postgres lifecycle-tracking schema.** Extends/replaces the
existing A2A task store per the v2 spec's Task shape. Load-bearing.
**DRAFTED 2026-07-23** — `docs/specs/contracts/PERSISTENCE.md` (`cortex_tasks` +
`cortex_transitions` + `cortex_decisions` beside the untouched v1 A2A store;
transition-with-reason enforced transactionally). *Awaiting Kit's approval
before building.*

**O-HARDEN — Phase-1 deferred hardening (fold into Phase 2).** Non-blocking
items the Phase-1 build deferred to reach a working loop, tracked so they are
not lost: (a) **sandbox egress re-lock** — currently allow-all; re-tighten to
{DNS, gitea toServices, inference} now that the gitea *ingress* allowlist is the
real allowance; (b) **dedicated Lockbox merge secret** — stop reusing
`warband-gitea-token`; (c) **CI image-bump for `CORTEX_AGENT_IMAGE`** — the env
value isn't covered by the bump regex, so the sandbox agent image is pinned by
hand; (d) **in-cluster GOPROXY** — the baked module cache breaks if a task adds
a dependency (offline `go test`). Full detail in
`PHASE2_EXECUTION_SPEC.md#deferred-phase-1-hardening`. *Owner: Phase 2.*

**O-CI — RESOLVED 2026-07-23.** `git.hirdforge.com` Gitea has working CI runners
(the Quality Gates + build workflows run against it; branch protection enforces
green status checks). The walking skeleton's done-gate uses `test-command`
(sandbox exit code) regardless, so `ci-status` remains an available-but-unused
gate type.

**O-SANDBOX-CONTRACT / O-ROUTING-SCHEMA / O-PERSISTENCE — RESOLVED 2026-07-23.**
All three contracts were drafted, approved (PR #330), and *implemented and
proven live* in Phase 1. The sandbox contract was amended (container-sequencing
in place of exec-into-a-pause-sidecar, Kit-approved) — see
`docs/specs/contracts/`.

**O-WEBHOOK-SECRET — Webhook auth from `git.hirdforge.com` Gitea → Asgard
gateway.** HMAC validation reusing existing `webhook.go` patterns. Both ends now
live in Asgard (corrected D-INFRA), which simplifies secret provisioning —
confirm the path anyway. *Owner: Phase 1 infra. Retargeted 2026-07-23.*
