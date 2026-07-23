# Steering — for the Claude building hirdforge v2

Read in this order: `HIRDFORGE_V2_PRD.md` (plan of record) → `LOOP_SPEC.md` (the felt
loop this must reproduce) → the UI mock (`ui-mock.html`, the north-star look). Then the
calls below, which come from a fresh outside read of the repo + a full working session
with the Sovereign.

## Accuracy note — what "ready" means (don't skip)
The **v2 plan is ready; the v2 code is not.** The repo holds two things: **v1/Workbench**
(built, ~11/12 UI phases, deployed-ish on Asgard) and **v2** (this PRD + specs, landed as
docs, ~zero code). Concretely: the module path was still `kitporath/project_valhalla`
until the Phase-0 rebrand landed (2026-07-23, now `git.hirdforge.com/kit/hirdforge`),
and `internal/workbench/cortex.go` is the *Workbench* cortex that D-PORT
says to **replace**, not the v2 deterministic Cortex. So you are starting the **Phase 1
walking skeleton**, reusing v1's agent loop / lockbox / seidr / gateway shell where the PRD
allows — not "finishing" a nearly-done thing. Report real state; never inherit a stale
doc's optimism (the repo's own ROADMAP/BUILD_LOG are frozen at Feb v0.0.1 — ignore them).

## The mandate: it must WORK (read this twice)
The mock is a **contract, not decoration.** Every state it shows is a promise the backend
has to keep: a task that says *building* is really running in an isolated sandbox; a
*gate: ci green* pill means a real CI status gated it; *evidence · secret Ready* means the
thing was actually verified, not asserted; the Lockbox approval is a real merge gate. **A
gorgeous cockpit over a flaky engine is the exact failure hirdforge has made before — and
it is worse than an ugly one, because it lies.** The whole reason v2 exists is the number
in the PRD: LLM-driven coordination compounds to ~33%; deterministic coordination is what
takes it past 80%. So the build order is non-negotiable: **make the loop actually complete
work reliably first, then dress it in this design.** Do not build a screen whose states you
cannot truthfully back. "Done" is mechanical (D-GATE) everywhere, including in the UI —
every "done" in the mock renders its evidence for exactly this reason. If a surface can't
show real evidence yet, it shows *pending*, never a green checkmark it hasn't earned.

## Steering calls (additions to the PRD, not overrides)

1. **The loop is the acceptance test.** Every surface and route must serve one of the eight
   moves in `LOOP_SPEC.md`. If it serves none, it's engineer-ballast — cut it. The felt
   quality target is "one coherent mind," even though it's three split parts.

2. **Bring the UI design language forward, even if the UI build stays Phase 5.** The PRD
   correctly builds backend-first — but the UI has been hirdforge's chronic failure, and
   the Sovereign cares about it more than any single backend feature. So: adopt the design
   *system* now (the tokens, type, layout in `ui-mock.html`) as the house style for *any*
   surface you touch from Phase 1 on — even a debug page inherits it. Don't ship another
   "engineer did the minimum" screen. The Pipeline UI build lands at Phase 5; the *look*
   starts at Phase 1.

3. **Consider reskinning v1/Workbench as the interim face.** While v2's backend grows, the
   Sovereign needs something that *feels* right to talk to now. Applying the mock's design
   system to the existing Workbench UI is likely a days-not-weeks win and buys a usable
   cockpit long before the v2 Pipeline UI exists. Evaluate it; don't assume.

4. **Fold model/lane management in (the "anvil" integration).** The Sovereign explicitly
   wants what ODS/Local-Studio do: the app that runs the work also manages the minds. The
   mock shows it — a **Fleet & models** surface + a **Models & lanes** settings section:
   per-lane GPU/VRAM/throughput, model swap, per-seat lane assignment (interlocutor /
   fleet / reviewer). Wire it to the existing lane fabric (LiteLLM on agent-host:4000, the
   per-host llama-servers), not a new controller. This closes the loop: the forge owns its
   own brains.

5. **The interlocutor seat = the deep lane (Nemotron‑3), decided.** It's the one seat where
   raw reasoning IQ is the whole job (it plans/scopes/dispatches; it does NOT coordinate —
   Cortex does that, deterministically). Keep instruction-following/speed concerns for the
   fleet and reviewer seats, not here. Steward (PRD Phase 4, renamed from Concierge at kickoff) is this seat's product name.

6. **Settings is a first-class surface, not a config file.** Sectioned, real controls,
   searchable later: General · Models & lanes · Connections (forge/MCP/Lockbox) · Fleet ·
   Appearance · Advanced/Observability. The mock shows the shape and the bar.

## The design system to inherit (from ui-mock.html)
- **Identity:** "the quiet forge" — calm, dark-first, depth-on-demand. Not Norse-military
  (D-BRAND drops that).
- **Color:** warm-graphite grounds; a single **copper-ember accent** (`#D08453` dark /
  `#B25E36` light) as the only bold move; status colors (sage/gold/red) kept *separate*
  from the accent. Cool-neutral light theme (not AI-cream). Both themes token-level, via
  `prefers-color-scheme` + `data-theme` override.
- **Type:** `system-ui` on purpose (native = the "feels like a real desktop app" the
  Sovereign is after); `ui-monospace` for the Cortex log and metrics; tabular-nums on all
  numbers.
- **Layout:** three-column desktop shell — threads · conversation · live inspector.
  Familiar on purpose; the craft is in spacing scale, state-encoding pills, evidence-on-
  done, and tasteful motion — that's what clears "engineer minimum."
- **Information design:** summary before detail; encode state in *form* (pill/stripe/dot),
  not just text; every "done" shows its evidence; escalation (Lockbox) is loud with a
  severity stripe — never a buried footnote.
