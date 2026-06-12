package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// staging_checks.go is the checks-source helper from the A1 activation checklist
// (docs/a1-staging-auto-merge-activation-checklist.md, step 1). It determines
// whether a PR's head SHA has all required CI contexts green, producing the
// (checksGreen, summary) pair that feeds mergeCandidate.ChecksGreen /
// mergeCandidate.ChecksSummary.
//
// It fails CLOSED: anything other than "every required context reported success"
// yields checksGreen=false. The real Gitea call is isolated behind the
// checksClient interface, so the policy is testable with fakes/httptest and no
// production path calls this yet.

// commitStatus is one CI context's status for a commit.
type commitStatus struct {
	Context string
	State   string // success | pending | failure | error | ...
}

// combinedStatus is the rolled-up status for a commit SHA.
type combinedStatus struct {
	State    string // worst-case rollup state
	Statuses []commitStatus
}

// checksClient fetches the combined status for a commit SHA. The real
// implementation calls the Gitea combined-status API; tests use a fake or an
// httptest-backed adapter. No production path constructs a real one yet.
type checksClient interface {
	CombinedStatus(owner, repo, sha string) (combinedStatus, error)
}

// stagingChecksGreen reports whether every required context is green for the
// given PR head SHA, with a human-readable summary for ChecksSummary. It fails
// closed on a nil client, an API error, empty/malformed data, an empty required
// set, a missing required context, or any non-success state (pending, failure,
// error, ...).
func stagingChecksGreen(client checksClient, owner, repo, sha string, required []string) (bool, string) {
	if client == nil {
		return false, "checks: no client configured"
	}
	if len(required) == 0 {
		// Without a known required set we cannot assert "all required green".
		return false, "checks: no required contexts configured"
	}

	cs, err := client.CombinedStatus(owner, repo, sha)
	if err != nil {
		return false, "checks: api error: " + err.Error()
	}
	if len(cs.Statuses) == 0 {
		return false, "checks: no statuses reported"
	}

	// Latest status per context (the API may list historical entries newest-first;
	// keep the first seen for each context).
	states := make(map[string]string, len(cs.Statuses))
	for _, s := range cs.Statuses {
		ctx := strings.TrimSpace(s.Context)
		if ctx == "" {
			continue
		}
		if _, seen := states[ctx]; !seen {
			states[ctx] = strings.ToLower(strings.TrimSpace(s.State))
		}
	}

	var issues []string
	for _, ctx := range required {
		st, ok := states[ctx]
		switch {
		case !ok:
			issues = append(issues, "missing:"+ctx)
		case st != "success":
			issues = append(issues, ctx+"="+st)
		}
	}
	if len(issues) > 0 {
		return false, "checks not green: " + strings.Join(issues, ", ")
	}
	return true, fmt.Sprintf("checks: %d/%d required green", len(required), len(required))
}

// giteaChecksClient is the isolated real adapter: the only code that calls the
// Gitea combined-status API. Base URL and token are injected (no hardcoded
// secrets). It is not constructed by any production path in this PR.
type giteaChecksClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// newGiteaChecksClient builds the adapter with an injected base URL and token.
func newGiteaChecksClient(baseURL, token string) *giteaChecksClient {
	return &giteaChecksClient{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:      strings.TrimSpace(token),
		httpClient: http.DefaultClient,
	}
}

type giteaCombinedStatusWire struct {
	State    string `json:"state"`
	Statuses []struct {
		Context string `json:"context"`
		Status  string `json:"status"`
		State   string `json:"state"`
	} `json:"statuses"`
}

// CombinedStatus fetches and decodes the Gitea combined status for a SHA. It
// returns an error (so callers fail closed) on an incomplete ref, an
// unconfigured adapter, a non-2xx response, or a malformed body.
func (c *giteaChecksClient) CombinedStatus(owner, repo, sha string) (combinedStatus, error) {
	if owner == "" || repo == "" || sha == "" {
		return combinedStatus{}, fmt.Errorf("checks client: incomplete ref %q/%q@%q", owner, repo, sha)
	}
	if c.baseURL == "" || c.token == "" {
		return combinedStatus{}, errors.New("checks client: not configured (base URL or token missing)")
	}

	endpoint := fmt.Sprintf("%s/api/v1/repos/%s/%s/commits/%s/status", c.baseURL, owner, repo, sha)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return combinedStatus{}, fmt.Errorf("checks client: build request: %w", err)
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")

	client := c.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return combinedStatus{}, fmt.Errorf("checks client: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return combinedStatus{}, fmt.Errorf("checks client: gitea returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var wire giteaCombinedStatusWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return combinedStatus{}, fmt.Errorf("checks client: malformed response: %w", err)
	}

	out := combinedStatus{State: strings.ToLower(strings.TrimSpace(wire.State))}
	for _, s := range wire.Statuses {
		state := s.Status
		if state == "" {
			state = s.State
		}
		out.Statuses = append(out.Statuses, commitStatus{Context: s.Context, State: state})
	}
	return out, nil
}
