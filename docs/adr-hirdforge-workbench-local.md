# ADR: Hirdforge Workbench Local Mode

## Status

Accepted.

## Decision

Hirdforge will add a local-first Workbench mode inside the existing repository.

Workbench is the new product entry point. Kubernetes, Helm, Gitea webhook automation, multi-agent peer routing, Hunters, auto-merge, and A2A task coordination are not part of the Workbench product path.

The existing Hirdforge services remain preserved while Workbench is developed as a new vertical slice.

## Product Shape

Hirdforge Workbench is a local/server-first AI coding workbench.

It opens a local Git repository, connects to an OpenAI-compatible model provider, runs a Builder loop, exposes tool activity, runs validations, shows diffs, requires human approval, and writes session memory to Seidr/HEAT.

## Component Roles

- Builder: executes coding work.
- Cortex: orchestrates events, lifecycle, routing, validations, and state.
- Seidr/HEAT: stores and retrieves memory, recall, validation, reflection, and audit data.
- Lockbox: manages approval and audit boundaries.
- UI: exposes the workbench, activity stream, validation state, memory, and approvals.

## Week-One Scope

In scope:

- Local Workbench command.
- Local repo selection.
- Provider configuration.
- Single Builder session.
- Cortex event stream.
- Validation runner.
- Git diff display.
- Human approve/reject flow.
- Seidr recall/write integration.
- Local web UI served by Hirdforge.

Out of scope:

- Kubernetes.
- Helm.
- Hunters.
- Auto-merge.
- A2A task coordination.
- Gitea webhook automation.
- Multi-agent peer routing.
- Polished Windows installer.
- Multi-tenant team mode.

## Success Criteria

A user can:

1. Start Hirdforge Workbench locally.
2. Open a Git repo.
3. Connect a model provider.
4. Give Builder a goal.
5. Watch the Builder tool loop.
6. Run validations.
7. Review a diff.
8. Approve or reject the result.
9. Save a session summary to Seidr.
10. Start a later session with prior memory available.

## Notes

Cortex is not memory. Cortex is the deterministic event spine and lifecycle orchestrator.

Seidr/HEAT is the cognitive memory system.

A2M remains part of Seidr/HEAT. A2A is not on the Workbench product path.
