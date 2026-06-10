# Phase 0 Verification Note

Closeout record for **Phase 0** of `hirdforge-testing-validation-autonomy-spec.md`,
following PRs #301–#304. This note documents what is verified, what choices were
made, and what remains unknown. It is the in-repo record for a spec that lives
outside the repo.

## What shipped in Phase 0 (PRs #301–#304)

- **#301** — go tests run in the quality gate (`go test ./... -count=1`).
- **#302** — quality tool versions pinned (staticcheck `v0.7.0`, gocyclo `v0.6.0`).
- **#303** — oversized Go file check enforced (`.quality/oversized-go-allowlist.txt`, threshold 1500 lines).
- **#304** — dormant `update-infra` auto-merge removed.
- **This PR** — coverage baseline + non-regression ratchet, and this note.

## Verification items

### V1 — Required status check status

**Status: partially verified — needs a manual Gitea branch-protection check.**

The `Quality Gates` workflow (`.gitea/workflows/quality.yaml`) runs on every
`pull_request` and on push to `main`. It now gates: gofmt, staticcheck, gocyclo,
`go vet`, `go test`, the coverage ratchet, and the oversized-file check.

Whether the workflow is configured as a **required** status check on `main`
(i.e. branch protection blocks merge on failure) is set in Gitea repo settings,
not in the repo, and could not be confirmed from the working tree. **Action
remaining:** confirm in Gitea → repo Settings → Branches → `main` protection that
`Quality Gates` is listed under required status checks. Until then, the gate runs
but may not be merge-blocking.

### V3 — Local race result vs CI non-race choice

**Status: verified.**

- Local: `go test ./... -race -count=1` passes with **no data races** (exit 0),
  all packages OK.
- CI choice: the quality gate runs `go test ./... -count=1` **without** `-race`.

Rationale for the CI non-race choice: `-race` requires CGO, and the project
builds Go services with `CGO_ENABLED=0` for static Alpine images; the quality
container (`golang:1.25-alpine`) would need a C toolchain for `-race`, and the
race run is ~1.5–2x slower. The race detector passing locally is recorded here as
the periodic-but-not-per-PR signal. **Possible next step:** add an opt-in or
scheduled `-race` job rather than gating every PR.

## Coverage baseline (this PR)

Captured with `scripts/coverage-ratchet.sh generate` (Go 1.25.9). See
`.quality/coverage-baseline.txt`. Per-package statement coverage:

| Package | Coverage |
| --- | --- |
| cmd/agent | ~20.0% (jitters 19.9–20.0%) |
| cmd/gateway | 16.1% |
| cmd/lockbox | 3.2% |
| pkg/tasklife | 89.4% |
| pkg/tasks | 70.3% |
| pkg/tools | 19.1% |
| pkg/workspace | 79.4% |
| **total** | **19.1%** |

The ratchet (`scripts/coverage-ratchet.sh check`, wired into the quality gate)
enforces `current >= baseline - 0.5pp` per package. The 0.5pp tolerance is ~5x
the observed run-to-run jitter in `cmd/agent`. Coverage is measured only over
packages that have tests (no-test packages break `-coverprofile` on toolchains
missing the `covdata` tool, which was observed locally).

## Remaining unknowns

### V2 — (unknown)
Not resolved in Phase 0. Carry forward to the next phase. The spec's V2 criterion
was not actioned by PRs #301–#304 and has no in-repo artifact yet.

### V4 — (unknown)
Not resolved. No in-repo artifact; carry forward.

### V5 — (unknown)
Not resolved. No in-repo artifact; carry forward.

### V6 — (unknown)
Not resolved. No in-repo artifact; carry forward.

> Note: the exact V2/V4/V5/V6 criteria are defined in the external spec
> `hirdforge-testing-validation-autonomy-spec.md`, which is not present in this
> repo. They are recorded here as open so the closeout is honest about scope.
> Reconcile each against the spec text when it is available.

## Validation run for this PR

- `go test ./... -count=1` — pass
- `go test ./... -coverprofile=/tmp/hirdforge-cover.out` — coverage written; total 19.1%
  (note: exits non-zero on local toolchains missing `covdata`; the ratchet scopes
  to test packages to avoid this — clean in CI)
- `go vet ./...` — clean
- `gofmt -l .` — clean
- `go test ./... -race -count=1` — pass, no data races (V3)
- `scripts/coverage-ratchet.sh check` — pass (and verified to fail on a seeded regression)
