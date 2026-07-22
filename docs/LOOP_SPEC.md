# The Loop — Hirdforge v2 product spec, drawn from the reference implementation

**What this is.** The reference implementation of the loop hirdforge v2 must reproduce
is Kit working with Claude. This document is that loop, abstracted from a real full-day
session (2026-07-22) into the eight moves the product must make feel natural — and the
cross-cutting properties without which it feels like a toy. It is the human-experience
layer of `HIRDFORGE_V2_PRD.md`; where the PRD says *how coordination stays reliable*,
this says *how the work should feel to the operator*. The UI, the Concierge, and the
control surface are all judged against this.

## The eight moves

1. **Intent arrives in loose words.** The operator states a goal conversationally, often
   fuzzy and evolving ("add X to the homepage", "we need a better gauge on this", "figure
   out which of these to back"). Intent *sharpens through dialogue*, it is not filled into
   a form up front. → *The front door is a conversation, not a ticket template.*

2. **Ground before acting.** Before anything runs, the system reads live reality — what is
   actually deployed, what is true *now* — and distrusts stale docs. Today this caught a
   stale DNS assumption, a hobbled benchmark, and a "ready to go" that wasn't. → *Every
   plan is built against observed state, and observed state is always one glance away.*

3. **Plan held loosely, revised as it learns.** A plan forms, then bends when reality
   contradicts it. The plan is a living object the operator can see and the system amends
   as evidence lands — never a fixed script executed blindly. → *The plan is visible,
   legible, and revisable; changes to it are events.*

4. **The human gates direction, not steps.** The operator steers at chunk boundaries —
   approves a direction, corrects an overreach, decides between options — and is *not*
   asked to approve every step. Judgment checkpoints, not step-clicking. → *Control surface
   = dispatch / retry / cancel / approve at meaningful boundaries; everything else flows.*

5. **Execute by doing and by delegating.** Work happens directly when small, and fans out
   to parallel workers when scale helps — long/parallel work runs in the background and
   *announces itself* when done. The operator is never blocked watching a spinner. →
   *Fan-out and background execution are first-class and observable, not hidden.*

6. **Verify mechanically.** Results are checked against ground truth — hit the endpoint,
   read the actual number, confirm the service is up — never "I think it worked." This is
   D-GATE in the human loop: *done* is something you can point at. → *Every completion
   shows its evidence.*

7. **Report honestly, surface don't bury.** Outcomes are stated plainly, failures and
   corrections included and up front (a crashed dependency, a wrong earlier claim, a
   hobbled measurement). Loud, not buried in a report. → *Escalation and error are prominent
   UI events, never a silent degrade or a footnote.*

8. **The loop iterates with accumulating context.** The operator reacts, redirects, and the
   loop continues — with the whole session's context intact across turns. → *Continuity and
   memory (Seidr) are the substrate; nothing starts cold that shouldn't.*

## The cross-cutting properties (why it works at all)

- **One coherent mind, split without leaking.** Today it worked because a single entity
  held judgment + full context + the ability to both do and delegate. v2 splits that into
  a smart **interlocutor** (deep lane), a **deterministic coordinator** (Cortex, never a
  model), and a **fleet** (fast workers). The whole game is making that split *feel like one
  mind* — which is a coordination-reliability problem (the PRD's thesis) **and** a UI
  coherence problem (this doc). Both, or it feels like a toy.
- **Bias to forward motion.** Act when there's enough to act on; don't over-defer; don't
  survey options you won't take. The operator felt momentum all day, not deliberation.
- **Reversible by default, gated when not.** Reversible actions proceed; irreversible or
  outward-facing ones confirm first. Critical resources are never touched casually.
- **Nothing self-certified by vibes.** Coordination and completion are mechanical; judgment
  is the model's; the two never trade places (PRD Principle 1).

## What this demands of the surfaces

- **Talk surface (moves 1, 4, 7, 8):** the conversational front door to the interlocutor —
  the thing that makes it feel like the desktop apps the operator already loves.
- **Observability surface (moves 2, 5, 6):** live fleet + pipeline + Cortex log + task
  detail with evidence. The "visibility" — present, powerful, but disclosed, not dumped.
- **Control surface (moves 4, 5):** dispatch / retry / cancel / approve, woven into both
  the thread and the pipeline view; the plan shown before it runs.
- **Model/lane surface (property 1):** the brains the system runs on — GPU/lane/model
  state, swap/launch — folded into the app (the anvil integration), so the system that
  does the work also manages the minds that do it.
- **Settings:** a true, sectioned settings surface — not a config file, not a wall of
  inputs. General · Models & Lanes · Connections (forge/MCP/Lockbox) · Fleet · Appearance
  · Advanced/Observability.

*The loop is the yardstick. Any hirdforge v2 surface that doesn't serve one of the eight
moves is engineer-ballast; any move it can't make feel natural is an unfinished edge.*
