package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// staging_observation.go is the live dry-run observation wiring (Phase 4). It
// connects completed-session data to the dry-run controller (runStagingDryRun)
// so that, ONLY when STAGING_DRY_RUN_GATE is enabled, a completed session emits a
// structured staging_dry_run_gate observation. It is observation-only:
//
//   - it never calls maybeAutoMergeStaging,
//   - it never reads or wires STAGING_AUTO_MERGE,
//   - it performs no merge, no PR mutation, and no token-dependent write — the
//     only network calls are read-only Gitea lookups (PR head ref + combined
//     status), and only when fully configured,
//   - it fails closed (logs a fail_closed decision) on any missing config,
//     missing checks source, missing PR extraction, or unresolvable head ref,
//   - it never logs credentials or auth headers.
//
// Env config (all required to leave fail-closed; default is unset -> fail closed):
//   STAGING_DRY_RUN_GATE            enable observation (off by default)
//   STAGING_DRY_RUN_SCOPE           allow-list "owner/repo/branch[,owner/repo/branch...]"
//   STAGING_DRY_RUN_REQUIRED_CHECKS comma-separated required check contexts
//   STAGING_DRY_RUN_BASE_BRANCH     the staging base branch to observe against

// prHeadResolver resolves a PR's head ref (SHA or branch) so checks can be
// queried. The real impl reads the Gitea PR; tests inject a fake.
type prHeadResolver interface {
	HeadRef(owner, repo string, index int) (string, error)
}

// stagingDryRunObserver bundles the read-only dependencies for live dry-run
// observation. Built from the agent's existing config in production; injected in
// tests.
type stagingDryRunObserver struct {
	checks         checksClient
	heads          prHeadResolver
	scope          *stagingScope
	requiredChecks []string
	baseBranch     string
}

// complete reports whether the observer has everything needed to evaluate.
func (o *stagingDryRunObserver) complete() bool {
	return o != nil && o.checks != nil && o.heads != nil && o.scope != nil &&
		len(o.requiredChecks) > 0 && strings.TrimSpace(o.baseBranch) != ""
}

// newStagingDryRunObserverFromEnv builds an observer from env config plus the
// agent's existing Gitea base URL and token. Returns (nil, false) when anything
// required is missing, so callers fail closed. The token is passed to the
// read-only clients but never logged.
func newStagingDryRunObserverFromEnv(giteaURL, giteaToken string) (*stagingDryRunObserver, bool) {
	giteaURL = strings.TrimSpace(giteaURL)
	giteaToken = strings.TrimSpace(giteaToken)
	scopeSpec := strings.TrimSpace(os.Getenv("STAGING_DRY_RUN_SCOPE"))
	required := splitCSV(os.Getenv("STAGING_DRY_RUN_REQUIRED_CHECKS"))
	baseBranch := strings.TrimSpace(os.Getenv("STAGING_DRY_RUN_BASE_BRANCH"))

	if giteaURL == "" || giteaToken == "" || scopeSpec == "" || len(required) == 0 || baseBranch == "" {
		return nil, false
	}
	scope := parseStagingScope(scopeSpec)
	if scope == nil || len(scope.allowed) == 0 {
		return nil, false
	}
	return &stagingDryRunObserver{
		checks:         newGiteaChecksClient(giteaURL, giteaToken),
		heads:          newGiteaPRHeadResolver(giteaURL, giteaToken),
		scope:          scope,
		requiredChecks: required,
		baseBranch:     baseBranch,
	}, true
}

// observeCompletedSessionDryRun runs the dry-run staging observation for a
// completed session. It is a no-op when STAGING_DRY_RUN_GATE is off. When on, it
// emits a structured staging_dry_run_gate log: fail_closed on missing
// config/checks/extraction, otherwise the dry-run controller's would_allow /
// would_block decision. It never merges and never calls maybeAutoMergeStaging.
func observeCompletedSessionDryRun(obs *stagingDryRunObserver, sessionID, taskID, finalContent string) {
	if !stagingDryRunGateEnabled() {
		return
	}
	if !obs.complete() {
		emitStagingDryRunFailClosed(sessionID, taskID, "staging dry-run not configured")
		return
	}
	if strings.TrimSpace(finalContent) == "" {
		emitStagingDryRunFailClosed(sessionID, taskID, "no session output to extract")
		return
	}

	outcome := classifyRunOutcome(finalContent, terminationCompleted)
	ref, ok := prRefFromOutcome(outcome)
	if !ok {
		emitStagingDryRunFailClosed(sessionID, taskID, "no PR reference extracted from completed session")
		return
	}

	headRef, err := obs.heads.HeadRef(ref.Owner, ref.Repo, ref.Index)
	if err != nil || strings.TrimSpace(headRef) == "" {
		// Generic reason — never echo upstream error detail that could carry URLs.
		emitStagingDryRunFailClosed(sessionID, taskID, "could not resolve PR head ref")
		return
	}

	runStagingDryRun(stagingRunInput{
		SessionID:         sessionID,
		FinalContent:      finalContent,
		TerminationReason: terminationCompleted,
		HeadSHA:           headRef,
		BaseBranch:        obs.baseBranch,
		RequiredChecks:    obs.requiredChecks,
	}, obs.checks, obs.scope)
}

// emitStagingDryRunFailClosed logs a fail-closed dry-run observation. It never
// includes credentials.
func emitStagingDryRunFailClosed(sessionID, taskID, reason string) {
	fields := map[string]interface{}{
		"dry_run":     true,
		"decision":    "fail_closed",
		"would_merge": false,
		"reason":      reason,
	}
	if sessionID != "" {
		fields["session_id"] = sessionID
	}
	if taskID != "" {
		fields["task_id"] = taskID
	}
	logJSON("info", stagingDryRunGateMsg, fields)
}

// splitCSV splits a comma-separated env value into trimmed, non-empty entries.
func splitCSV(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseStagingScope parses "owner/repo/branch[,owner/repo/branch...]" into a
// stagingScope. Malformed entries are skipped; an all-malformed spec yields an
// empty (allow-nothing) scope.
func parseStagingScope(spec string) *stagingScope {
	var entries []stagingScopeEntry
	for _, e := range splitCSV(spec) {
		parts := strings.Split(e, "/")
		if len(parts) != 3 {
			continue
		}
		entries = append(entries, stagingScopeEntry{Owner: parts[0], Repo: parts[1], Branch: parts[2]})
	}
	return newStagingScope(entries...)
}

// giteaPRHeadResolver is the isolated read-only adapter that resolves a PR's head
// ref via the Gitea API. Base URL and token are injected; the token is never
// logged. It performs no writes.
type giteaPRHeadResolver struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func newGiteaPRHeadResolver(baseURL, token string) *giteaPRHeadResolver {
	return &giteaPRHeadResolver{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:      strings.TrimSpace(token),
		httpClient: http.DefaultClient,
	}
}

func (r *giteaPRHeadResolver) HeadRef(owner, repo string, index int) (string, error) {
	if owner == "" || repo == "" || index <= 0 {
		return "", fmt.Errorf("pr head resolver: incomplete ref %q/%q#%d", owner, repo, index)
	}
	if r.baseURL == "" || r.token == "" {
		return "", fmt.Errorf("pr head resolver: not configured")
	}
	endpoint := fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls/%s", r.baseURL, owner, repo, strconv.Itoa(index))
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("pr head resolver: build request: %w", err)
	}
	req.Header.Set("Authorization", "token "+r.token)
	req.Header.Set("Accept", "application/json")

	client := r.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("pr head resolver: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		_, _ = io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("pr head resolver: gitea returned %d", resp.StatusCode)
	}
	var wire struct {
		Head struct {
			Sha string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return "", fmt.Errorf("pr head resolver: malformed response: %w", err)
	}
	if s := strings.TrimSpace(wire.Head.Sha); s != "" {
		return s, nil
	}
	if rf := strings.TrimSpace(wire.Head.Ref); rf != "" {
		return rf, nil
	}
	return "", fmt.Errorf("pr head resolver: no head ref in response")
}
