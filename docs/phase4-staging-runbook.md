# Phase 4: Next / Staging Lane Runbook

## Overview

Phase 4 introduces a `next` branch build lane and an associated staging environment for dry-run
observation of the Hirdforge agent's completed-session controller.

## The `next` Branch Lane

When a push hits the `next` branch:

1. **build.yaml** triggers six `build-*` jobs. Each job checks `BRANCH_NAME == "next"` and:
   - Builds the image (agent, gateway, seidr, lockbox, hirdforge, openai-mcp-tunnel)
   - Tags it `sha-next-$SHA_SHORT`
   - Pushes to the internal Gitea registry
   - **Never** overwrites `:latest`

2. **build.yaml** also triggers the `update-infra` job on `main` pushes only. This job clones
   `kit/asgard-infra`, updates image tag references in staging YAML manifests, and opens a PR.
   It does **not** auto-merge.

3. Staging image tags are immutable per-commit: each commit on `next` gets a unique `sha-next-`
   tag.

## `.autonomy.yaml` Control Surface

Location: `.autonomy.yaml` at the repo root.

- **autonomy level: `A0`** — fully human-gated. No autonomous merge or deployment.
- Each class (docs, tests, skills, code, infra-staging, infra-prod-bump, infra-production) has
  `level: A0`.
- `.autonomy.yaml` is a kill-switch and control surface only. It does **not** activate A1, A2, or
  A3.

## Dry-Run Observation

Dry-run observation is wired into `cmd/agent/tasks.go` and activates only when:

- `STAGING_DRY_RUN_GATE=1` (environment variable on the staging deployment)
- The completed session's PR matches `STAGING_DRY_RUN_SCOPE`
- All required checks in `STAGING_DRY_RUN_REQUIRED_CHECKS` report success
- The PR is against `STAGING_DRY_RUN_BASE_BRANCH` (= `next`)

The controller assembles a merge candidate from outcome + CI checks + scope allow-list and
emits a `dry_run_would_merge` log. It never merges.

## Verifying ArgoCD Sync (asgard-infra)

1. After `next` builds and `update-infra` opens its PR in `asgard-infra`, merge the PR.
2. ArgoCD syncs `infrastructure/asgard-staging/` to the `asgard-staging` namespace.
3. Verify in the ArgoCD UI or with:

   ```
   argocd app get hirdforge-staging -n argocd
   ```

## Finding `staging_dry_run_gate` Logs

Logs are emitted by the agent's `staging_gate.go` at INFO level on startup:

```
staging_dry_run_gate=true
```

Additional dry-run logs include:

- `dry_run_scope_match=true/false`
- `dry_run_checks_passed=true/false`
- `dry_run_would_merge=true/false`

View with:

```
kubectl logs -n asgard-staging -l asgard.io/agent=staging-warrior --tail=200 \
  | grep -i dry_run
```

## Pre-Conditions for Real A1 Activation

Before promoting dry-run observation to real A1 auto-merge:

1. `.autonomy.yaml` must have `code` and `infra-staging` classes at `A1` or higher.
2. `STAGING_AUTO_MERGE=1` must be set on the staging deployment.
3. A1 merge must be gated on:
   - PR against `next` branch
   - All required checks passing
   - No `selfHeal` or automated sync in staging
   - Human-approved PR in asgard-infra
4. Production `STAGING_AUTO_MERGE` must remain unset.
5. No production deployment may be auto-merged without a human gate.
