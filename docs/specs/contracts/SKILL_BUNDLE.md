# Contract 8 — Skill + Memory-Scope Loading (O-SKILL-BUNDLE)

**Status: DRAFT 2026-07-24 — awaiting Kit's approval (stop-and-ask #2).** The
second load-bearing contract for the skill-dispatch track (P2.6). Companion to
O-PROFILE (Contract 7 — the mechanical loop config) and the Dispatch Envelope
(Contract 2 — `bundle: {skills, memory_scopes, profile}`). Where a *profile* is
*how* the loop runs, a *bundle's skills + memory_scopes* are *what the agent
knows* for this task. Grounded in the code: skills already load from a
persona/skills repo (`cmd/agent/skills.go`), and recall/remember already take a
collection (`pkg/tools` recall/remember); this contract makes both selectable
per-task via the envelope, with **no Seidr API change**.

## Semantics (the part that matters)

1. **`skills` resolve to skill files loaded into the prompt at dispatch.** Each
   name in `bundle.skills` names a file in the personas/skills repo; its content
   is appended to the system prompt (after persona + procedure, before the task).
   A skill file added to that repo changes agent behaviour with **no code
   change** — that is the whole point.
2. **`memory_scopes` resolve to Seidr collection names — a naming convention,
   not an API change.** recall/remember are scoped to the listed collections;
   the agent sees and writes only those. Empty ⇒ the agent's own default
   collection (today's behaviour), so unset bundles are unchanged.
3. **Loading is deterministic and loud.** Same bundle + same skills-repo commit ⇒
   same loaded content. A named skill that does not resolve is a **loud** dispatch
   failure (event-logged), never a silent skip — a task dispatched believing it
   had a skill it didn't is a correctness bug of the v1 class.
4. **Skills are knowledge, never authority.** A skill file cannot grant a tool,
   widen the write boundary, or alter the mechanical done-gate. It is prompt text.
   Capability comes only from the profile (O-PROFILE). This separation is what
   lets skills be an open, fast-moving set while capability stays a closed,
   reviewed one.

## Shape (the envelope's `bundle`, already `envelope_version: 1`)

```jsonc
"bundle": {
  "profile": "builder",              // O-PROFILE (Contract 7)
  "skills": [                        // ordered; resolved to files, appended in order
    "go/testing-conventions",
    "hirdforge/commit-style"
  ],
  "memory_scopes": [                 // Seidr collection names
    "skill:go",
    "repo:kit/hirdforge"
  ]
}
```

## Skill resolution

- **Source:** the personas/skills repo (the same repo persona files already load
  from — `--persona-repo`). A skill name `a/b` maps to `skills/a/b.md` in that
  repo at the pinned commit.
- **Pinning:** the dispatch records the skills-repo commit it resolved against, so
  a run's loaded skills are reproducible. (Phase 1 already clones the persona repo
  read-only; this reuses that.)
- **Placement in prompt:** `[model template] + [persona] + [profile procedure] +
  [skills, in order] + [task]`. Skills are guidance parts; the M5 output-discipline
  rule already tells the model that harness-injected parts are instructions, not
  the user's task.
- **Budget:** skills count against the context budget (M4); an oversized skill set
  is truncated with an announced marker, never silently.

## Memory-scope resolution

- `memory_scopes` is passed to the recall/remember tools as the collection set
  (recall queries across them; remember writes to the first, or a designated
  primary). This is a **naming convention over the existing Seidr collection
  parameter** — no new Seidr endpoint, no schema change.
- Convention (proposed): `skill:<name>` for skill-scoped lessons (HEAT promotion
  target), `repo:<owner>/<repo>` for repo-scoped memory, `agent:<name>` for the
  agent's own. Unset ⇒ `agent:<name>` (today's default).

## Determinism obligations (audit hooks)

- Resolved `(skill_name → repo_path @ commit)` and `memory_scopes` are recorded on
  the task record.
- Unknown skill name OR unreachable skills repo ⇒ loud dispatch failure, event
  logged; no silent degrade.
- The bundle is data in the envelope; Cortex does not interpret skill *content*
  (never inference in the coordination path).

## Acceptance (what "approved + built" means)

1. Two routes with different bundles dispatch agents with **different loaded
   skills and memory scopes**, provable from the task record.
2. Adding a skill file to the personas/skills repo changes agent behaviour with
   **no code change** (the mechanism's whole value).
3. Seidr recall/remember are scoped to the bundle's `memory_scopes`; an agent in
   scope `repo:X` does not see `repo:Y` memory.
4. An unresolved skill fails the dispatch loudly.

## Explicitly deferred

Skill *composition/priority* beyond ordered append (v1 = ordered concatenation).
Automatic skill *selection* by Cortex from task content (v1 = the route/bundle
names skills explicitly; auto-selection would risk an inference step in
coordination). HEAT lesson → skill *promotion* automation (Phase 6 territory; the
`skill:<name>` memory scope is the seam left for it).
