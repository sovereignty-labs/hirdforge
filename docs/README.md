# Hirdforge v2 — Documentation

The charter set that drives the v2 rebuild. Read in this order.

1. **[`../CLAUDE.md`](../CLAUDE.md)** — session charter: how work is done
   (doctrine, stop-and-ask triggers, working rules, phase ritual).
2. **[`HIRDFORGE_V2_PRD.md`](HIRDFORGE_V2_PRD.md)** — plan of record: what and why
   (mission, the compound-reliability problem, component architecture, locked
   build calls, the six-phase roadmap).
3. **[`DECISIONS.md`](DECISIONS.md)** — closed decisions (binding) + open items.
   The four carry-forward lessons and every locked call live here in full.
4. **[`OBSERVABILITY_CONTRACT.md`](OBSERVABILITY_CONTRACT.md)** — the operator's
   observe + control surface. Frozen before UI work; the UI is wireframed against
   it.
5. **[`BUILDER_HARNESS.md`](BUILDER_HARNESS.md)** — the agent tool-loop reliability
   mechanisms (M1–M7), mapped to built vs. remaining.
6. **[`specs/PHASE1_EXECUTION_SPEC.md`](specs/PHASE1_EXECUTION_SPEC.md)** — the
   walking skeleton: the first build. Per-phase specs are drafted at each phase
   boundary.
7. **[`source/`](source/)** — the two originating specs (v2 architecture, builder
   harness). Read-only provenance. Where source and code disagree, the code wins.

**One-line orientation:** Hirdforge v2 is a human-supervised autonomous build
platform where **coordination is infrastructure, not agency** — a deterministic
router (Cortex) dispatches agents into isolated sandboxes, every completion is a
mechanical gate (never a model's word), and the write boundary is a PR merge
behind Lockbox.
