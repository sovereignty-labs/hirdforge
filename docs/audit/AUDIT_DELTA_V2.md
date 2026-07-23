# Audit Delta — v2 rebuild

**What this is** (per CLAUDE.md working rules): a running list of statements in
the source specs / charter docs that changes on this branch-line have made stale,
so ground truth stays current without re-auditing. Newest entries first.

---

## 2026-07-23 — Phase 1 built, deployed, accepted

The walking skeleton (P1.1–P1.8) is on `main`, deployed to Asgard, and proven
live: issue `kit/hirdforge#359` → PR #361 → `gate_passed:test-command exit 0` →
`review`. Acceptance recorded in `docs/specs/PHASE1_ACCEPTANCE.md`; Phase 2
drafted in `docs/specs/PHASE2_EXECUTION_SPEC.md`. Statements this makes stale:

- STEERING/PRD "the v2 *code* is ~zero" — no longer true; the coordination
  spine exists and runs. Phase-1 code is `internal/cortex`, `internal/sandbox`,
  and the `cmd/gateway` cortex glue.
- `internal/workbench/cortex.go` is now definitively superseded by
  `internal/cortex` (D-PORT executed): the deterministic v2 Cortex is built
  fresh, not ported. The Workbench cortex remains only as dead v1 code pending
  cutover.
- Sandbox egress is **permissive (allow-all)** as a deliberate temporary
  measure — the real clone blocker was gitea *ingress*, not sandbox egress.
  Re-tighten is a Phase-2 hardening item (see DECISIONS O-HARDEN).
- `CORTEX_AGENT_IMAGE` in the gateway deployment is pinned manually; the CI
  image-bump regex does not rewrite env values.
- The BUILDER_HARNESS "20/20 clean" benchmark result was **edit-only** and does
  not measure the git/PR flow — which the live loop showed is where the builder
  actually fails (~50%). The Phase-2 benchmark extension corrects this.

## 2026-07-23 — Phase 0 rebrand (`phase0/rebrand`)

**Module path renamed** to `git.hirdforge.com/kit/hirdforge` (D-BRAND, with the
corrected D-INFRA target — not `git.example.internal/hirdforge/hirdforge` as the pre-
correction docs wrote). Stale statements this supersedes:

- STEERING's accuracy note "module path is still `kitporath/project_valhalla`"
  (amended in place).
- Any source-spec reference to the `project_valhalla` module path.
- `proto/a2a/a2a.pb.go` still embeds the old path **inside its serialized
  descriptor bytes** — generation-time metadata only, deliberately untouched
  (editing it corrupts the length-prefixed descriptor). It self-corrects the
  next time `protoc` regenerates the file; `a2a.proto`'s `go_package` already
  points at the new path.

**D-INFRA corrected** (Kit-approved at the 2026-07-23 kickoff): master =
`git.hirdforge.com` (Gitea in Asgard), build/dev on agent-host, KWS Gitea wiring
dropped. Every doc statement placing the master on KWS `git.example.internal` or naming
forge.example.internal as failover mirror is superseded — CLAUDE.md, DECISIONS D-INFRA,
the PRD, PHASE1_EXECUTION_SPEC, and OBSERVABILITY_CONTRACT are updated; the
read-only `docs/source/` specs are not edited and should be read through this
correction.

**Names resolved** (O-BRAND-NAMES, Kit at kickoff): Seidr stays; **SOUL →
Persona**; **Concierge → Steward**. Applied across the v2 docs.

**Live v1 external references deliberately NOT renamed** (renaming them is a
behavior change, forbidden in Phase 0 — each migrates when the component that
owns it is rebuilt):

| Reference | Where | Why it stays for now |
|---|---|---|
| `warband_shared` Seidr collection | `pkg/tools/git.go`, `cmd/agent/skills.go` | Live collection name in the deployed Seidr; renaming breaks recall against existing data. |
| `warband` Gitea service account | `pkg/tools/git.go` (authenticated clone URL) | Live credential/account on the Gitea master. |
| `json:"warband"` wire fields | `cmd/gateway` (sessions, webhooks_support, …) | Consumed by the deployed v1 UI; wire format is behavior. |
| `soul.md` / `warrior/…` / `chieftain/…` persona paths | `pkg/tools`, `cmd/agent` (incl. `soul:` self-improvement PR titles) | Real file layout of the `hirdforge-personas` repo; renaming requires a coordinated cross-repo migration. |
| Gitea org `warband/*` repos | remotes on the master | External repo slugs, not code vocabulary. |

**Go-internal vocabulary** (identifiers not crossing a wire or an external
system) renamed warband→fleet in the same PR — compiler-verified, no behavior
change.

**Toolchain note:** agent-host had no Go; Go 1.25.9 installed at `~/.local/go`
(matches `go.mod`). `protoc` is not installed — regenerating `a2a.pb.go` waits
until it's needed and sanctioned.
