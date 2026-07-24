# Contract 7 — The Harness Profile (O-PROFILE)

**Status: DRAFT 2026-07-24 — awaiting Kit's approval (stop-and-ask #2).** The
load-bearing contract for the skill-dispatch track (P2.6–P2.7). Companion to the
Dispatch Envelope (Contract 2), whose `bundle.profile` field names a profile, and
to BUILDER_HARNESS (M1–M7, now built for the builder). Grounded in the code as it
exists: the agent is a generic loop engine (`newConversationProcessor`) whose
behaviour is already parameterized by flags (`-tools`, `-max-tool-rounds`,
`-soul`, `-max-context`, budgets); a *profile* names and versions one such
configuration so a route can select it declaratively.

## What a profile IS (and is not)

A **profile** is the *mechanical* configuration of the agent loop — the "how the
work is done" knobs — as distinct from **skills** (knowledge/procedure content,
O-SKILL-BUNDLE) and from **memory scopes** (which Seidr collections are visible).

- *Profile* = tool set, budgets, step cap, loop policies, exit protocol, which
  base procedure to render. Mechanical. Small, closed set, versioned in-repo.
- *Skills* = per-task knowledge appended to the prompt. Open set, live in the
  personas/skills repo.
- *Memory scopes* = collection-naming convention for recall/remember.

The envelope's `bundle` already carries all three: `{skills, memory_scopes,
profile}`. This contract defines only `profile`.

## Semantics (the part that matters)

1. **A profile is resolved by name to a definition, deterministically.** Same
   profile name + same profile-config version ⇒ same agent configuration, byte
   for byte. Profiles are versioned in-repo (like route configs), never computed
   at dispatch from model output.
2. **Cortex does not interpret a profile.** The gateway maps the profile name to
   a fixed set of agent-process arguments/config at dispatch; the coordination
   path stays mechanical (Cortex never calls inference — doctrine).
3. **A profile can only narrow capability, never smuggle authority.** The write
   boundary (PR-behind-Lockbox) is invariant across profiles; a profile changes
   what tools/budgets the agent has, not what may be merged or how.
4. **The reviewer profile's tool restriction is a security property, not a
   convenience.** A reviewer with no edit/write/exec cannot be prompt-injected
   into modifying code — it *physically* lacks the tools. This is the motivating
   case for profiles.

## Schema (`profile_version: 1`)

Profiles live in-repo at `config/profiles/<name>.yaml`, loaded by the gateway at
startup (same lifecycle as `cortex.yaml` routes). A route/bundle references one by
name.

```yaml
profile_version: 1
name: builder
description: "Full build agent: edit + git + PR, M1–M7 reliability loop."

tools:                      # the EXACT tool set the agent is dispatched with
  - todo
  - exec
  - read
  - write
  - edit
  - git-clone
  - git-commit
  - git-diff
  - gitea                   # includes create-pr

procedure: builder          # which base operating procedure to render
                            # (procedure.go: builder | reviewer | none)

budgets:
  max_tool_rounds: 80       # step cap
  read_max_lines: 2000      # M4 read budget
  read_max_bytes: 51200
  exec_max_output: 30720    # M4 exec head+tail budget
  max_context: 0            # 0 = disable context trimming

policies:
  todo_reanchor: true       # M6 externalized-plan re-anchoring
  read_before_edit: true    # M4 staleness enforcement
  redirect_shell_git: true  # route mutating shell git to git-commit (verified push)
  compaction: false         # M7 (not built; reserved)

completion:                 # what "done" MEANS for this profile (agent-side gate)
  requires: pr              # pr | review | none
  # the MECHANICAL done-gate is still per-route (D-GATE); this is the agent's
  # own terminal-outcome expectation, honoring fail-open (PR | FAILED | NOOP |
  # QUESTION) per the anti-dilution rule.
```

## The two baseline profiles

**`builder`** — the profile the benchmark just validated at 9/10. Full tool set
above, `procedure: builder`, M1/M3/M4/M5/M6 policies on, `completion.requires:
pr`. This is today's hardcoded dispatch (`cortex.go`) lifted into a named,
versioned profile.

**`reviewer`** — read-only. `tools: [read, git-diff, gitea-review]` (no
`edit`/`write`/`exec`/`git-commit`), `procedure: reviewer`,
`completion.requires: review`, smaller `max_tool_rounds`. The review procedure
ends in `create-review` (APPROVE / REQUEST_CHANGES). Its lack of mutating tools
is the prompt-injection safety property. (P2.7 builds this; it also fixes the
live `no_review` gap via the same M-mechanisms applied to the review loop.)

## Resolution & determinism obligations (audit hooks)

- The gateway resolves `bundle.profile` → profile definition at dispatch; an
  unknown profile name is a **loud** dispatch failure (event-logged), never a
  silent fallback to `default`.
- The resolved `(profile_name, profile_version, tool_list)` is recorded on the
  task record, so a run's capabilities are reconstructable after the fact.
- Changing a profile file is a git-reviewed change; the profile_version bumps.
- A profile MUST NOT grant a tool that violates its `completion`/role intent
  (e.g. a reviewer profile with `edit`): validated at load, refused loudly.

## Acceptance (what "approved + built" means)

1. `config/profiles/builder.yaml` and `reviewer.yaml` exist; the gateway loads
   them and dispatches the exact tool set / budgets / procedure named.
2. Today's builder dispatch is byte-for-byte reproducible from `builder.yaml`
   (no behaviour change — the benchmark still scores ≥8/10).
3. A route selecting `reviewer` dispatches a read-only agent that cannot edit,
   provable from the task record's tool list; it submits a Gitea review verdict.
4. An unknown profile name fails the dispatch loudly with an event-log entry.

## Explicitly deferred

Per-profile model routing (a profile could pin a model/lane) — reserved, not in
v1. M7 compaction policy — reserved (`policies.compaction`), M7 unbuilt. Operator
override of a profile at dispatch — not built; if ever added, a loud audit event.
