The repository `kit/benchfixture` is already cloned in your workspace at
`./benchfixture`. Do the following, working only through your tools.

1. Refactor the package. `benchfixture/metrics.go` contains 14 exported Format
   functions that all repeat the same validate → parse → clamp → format
   sequence. Extract that shared logic into ONE unexported pure helper function
   and rewrite all 14 functions to use it.

   Rules:
   - Do NOT change any exported function name or signature.
   - Do NOT change any output string or error behavior (the tests pin them).
   - Do NOT edit `benchfixture/metrics_test.go`.
   - Keep the code gofmt-clean. Run `go build ./...` and `go test ./...` in
     `./benchfixture` and fix anything red before moving on.

2. Commit and push. Use the `git-commit` tool with `repo: benchfixture`, a clear
   `message`, and a new `branch` (e.g. `refactor-metrics`). NOTE: git-commit may
   rename your branch (it prints `Pushed to <branch>` in its output) — read that
   output and use the EXACT pushed branch name in the next step.

3. Open the pull request. Use the `create-pr` tool with `repo: kit/benchfixture`,
   `head:` set to the exact branch name git-commit pushed, `base: main`, and a
   title. The PR will be REJECTED (404) if `head` is not the real pushed branch.

DONE WHEN: the PR is created. Report the PR URL in your final message.
Completion is verified mechanically — a real PR on the remote whose branch
carries the correct refactor — not by your own assessment. Do not reclone; do
not change scope.
