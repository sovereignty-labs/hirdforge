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

2. **build.yaml** includes an `update-infra` job that runs on `main` pushes only. This job clones
    `kit/asgard-infra`, updates image tag references in staging YAML manifests under
    `infrastructure/asgard-staging/`, and opens a PR. It does **not** auto-merge.
    The `update-infra` job does **not** run on `next` pushes — it only triggers when `main` is
    updated. This means the `next` → staging image tag bump path is not fully automated.

3. Staging image tags are immutable per-commit: each commit on `next` gets a unique `sha-next-`
   tag.

## `.autonomy.yaml` Control Surface

Location: `.autonomy.yaml` at the repo root.

- **autonomy level: `A0`** — fully human-gated. No autonomous merge or deployment is active.
- Each class has a `min_level` threshold that defines the autonomy level required for that class
  to be activated in the future. These thresholds are policy definitions only — they do not
  activate any automation while `autonomy` remains A0.

Current class thresholds:

| Class | `min_level` | Notes |
|-------|-------------|-------|
| docs | A2 | Documentation requires A2 or higher to activate |
| tests | A2 | Tests require A2 or higher to activate |
| skills | A2 | Skills require A2 or higher to activate |
| infra-staging | A1 | Staging infra is the lowest-threshold class |
| infra-prod-bump | A2 | Production bumps require A2 or higher |
| code | A3 | Source code requires A3 (maximum) |
| infra-production | A3 | Production infra requires A3 (maximum) |

- `.autonomy.yaml` is a kill-switch and control surface only. It does **not** activate A1, A2, or
  A3 while `autonomy` is `A0`.

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

1. `.autonomy.yaml` must have `code` and `infra-staging` classes with `min_level` at `A1` or
    higher.
2. `STAGING_AUTO_MERGE=1` must be set on the staging deployment.
3. A1 merge must be gated on:
    - PR against `next` branch
    - All required checks passing
    - No `selfHeal` or automated sync in staging
    - Human-approved PR in asgard-infra
4. Production `STAGING_AUTO_MERGE` must remain unset.
5. No production deployment may be auto-merged without a human gate.

## Phase 4 Status

This PR creates a **prepared staging substrate** — the infrastructure skeleton is in place
(namespace, deployment, service, ArgoCD application, dry-run env). It does **not** implement
full zero-human-action Phase 4 completion because:

- The `update-infra` job only runs on `main` pushes, not on `next` pushes.
- Image tag bumps from `next` → staging require a manual PR merge in asgard-infra.
- `STAGING_DRY_RUN_ENABLED` is not a recognized environment variable — only
  `STAGING_DRY_RUN_GATE=1` gates the dry-run controller.
