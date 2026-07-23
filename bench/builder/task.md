Refactor the Go package in this workspace.

`metrics.go` contains 14 exported Format functions that all repeat the same
validate → parse → clamp → format sequence. Extract that shared logic into ONE
unexported pure helper function and rewrite all 14 functions to use it.

Rules:
- Do NOT change any exported function name or signature.
- Do NOT change any output string or error behavior (the tests pin them).
- Do NOT edit metrics_test.go.
- Keep the code gofmt-clean.

Work step by step: read the files first, plan the helper's signature, apply the
refactor, then run `go build ./...` and `go test ./...` and fix anything red.

DONE WHEN: `go test ./...` exits 0 in the workspace and the duplication is gone.
Completion will be verified mechanically after you finish — by running the
tests and inspecting the file — not by your own assessment.
