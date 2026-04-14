# Contributing to Hirdforge

Thank you for your interest in contributing to Hirdforge. This document outlines the workflow, policies, and conventions that govern how changes are made to this project.

---

## How Pull Requests Work

All changes to Hirdforge must go through a pull request (PR). No direct pushes to `main` are permitted under any circumstances.

### The Review Process

1. **Open a PR against `main`**: Once your branch is complete, open a pull request targeting the `main` branch.
2. **Automated checks**: CI runs on every PR. All checks must pass before the PR can be merged.
3. **Code review**: At least one reviewer must approve the PR. Reviewers check for correctness, clarity, test coverage, and alignment with project conventions.
4. **Address feedback**: If a reviewer requests changes, make the updates on your branch. The branch will automatically update the PR.
5. **Squash and merge**: PRs are squash-merged into `main` to keep the commit history clean.

### What Reviewers Look For

- **Correctness**: Does the change do what it claims? Are edge cases handled?
- **Tests**: Are new behaviors covered by tests? Do existing tests still pass?
- **Clarity**: Is the code readable? Are comments used judiciously?
- **Consistency**: Does the change follow existing patterns and conventions?
- **Scope**: Is the PR focused on a single concern? Large, unfocused PRs are difficult to review.

---

## Castle vs. Countryside Merge Policy

Hirdforge's repositories are classified as either **Castle** or **Countryside**, each with its own merge policy.

### Castle Repositories

Castle repos are the core, foundational systems of the Hirdforge platform:

- `kit/hirdforge`
- `kit/asgard-infra`

**Policy**: Changes to Castle repos require review and approval from the **Sovereign** before merge.

- Builders must flag the PR for sovereign review.
- The PR must receive explicit Sovereign approval before it can be merged.
- Do not merge a Castle PR without sovereign sign-off.

### Countryside Repositories

Countryside repos contain higher-level, experimental, or workflow-oriented content:

- `kit/hirdforge-personas`
- `kit/hirdforge-tasks`

**Policy**: Builders can merge directly after completing and reviewing their own work.

- No sovereign review is required.
- Builders are expected to exercise good judgment and follow project conventions.
- Significant or architectural changes should still be discussed before implementation.

---

## Branch Naming Conventions

Branch names must be descriptive, lowercase, and use hyphens as separators. Avoid overly long names while ensuring the purpose of the branch is clear.

### Format

```
<type>/<description>
```

### Branch Types

| Type   | Purpose                          | Example                          |
| ------ | -------------------------------- | -------------------------------- |
| `feat` | New features or functionality     | `feat/dispatch-timeline-view`    |
| `fix`  | Bug fixes                        | `fix/delegation-timeline-category` |
| `docs` | Documentation changes            | `docs/contributing-guide`        |
| `refactor` | Code restructuring          | `refactor/gitea-split-tools`     |
| `chore` | Maintenance, tooling, CI/CD      | `chore/cleanup-ci-dead-images`   |

### Rules

- Use **lowercase letters only**.
- Use **hyphens** (`-`) to separate words. Do not use underscores or camelCase.
- Keep names **concise but descriptive**. A reviewer should understand the scope of the branch from its name alone.
- Do not include your agent name in the branch prefix unless required by tooling conventions.

### Examples

```
feat/add-health-check-endpoint
fix/vault-annotation-parsing
docs/update-architecture-diagram
refactor/split-gitea-mega-tool
chore/upgrade-go-1.23
```

---

## Commit Message Format

Commit messages should be clear and follow a conventional prefix format:

```
<type>: <short description>

[optional body with additional context]
```

**Types**: `feat`, `fix`, `docs`, `refactor`, `chore`, `test`, `ci`

**Examples**:

```
feat: add dispatch timeline view to gateway UI
fix: capitalize Do in mergePRTool payload for Gitea API
docs: add CONTRIBUTING.md with merge policy and conventions
chore: remove codex, astgraph, and worker from CI pipeline
```

---

## Getting Started

1. **Fork or clone** the repository you want to contribute to.
2. **Create a branch** following the naming conventions above.
3. **Make your changes** — write code, add tests, update docs.
4. **Validate your changes** — run linting, tests, and type checks.
5. **Open a PR** — reference any related issues using `Closes #<issue-number>`.
6. **Wait for review** — see the merge policy above for whether sovereign review is required.
7. **Merge** — once approved and CI passes, the PR will be squash-merged.

---

## Questions?

If you have questions about contributing, open an issue in the relevant repository or reach out to the project maintainers.
