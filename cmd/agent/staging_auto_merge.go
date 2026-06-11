package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// staging_auto_merge.go is step 5 of the e2e -> A1 sequence
// (docs/e2e-staging-harness-contract.md): feature-flagged A1 staging auto-merge.
//
// It flips the step-4 dry-run gate from log-only to actually merging — but only
// when the STAGING_AUTO_MERGE flag is explicitly enabled AND the outcome is
// eligible per the existing evaluateStagingGate policy. The actual merge is
// performed exclusively through the stagingMerger interface, so the policy/
// controller code never touches a real API; the only thing that talks to Gitea
// is the isolated giteaStagingMerger adapter. Default behavior is non-merging:
// with the flag off (the default) nothing merges and nothing is logged, and no
// production code path constructs a real merger or calls the controller yet.

// stagingAutoMergeEnvVar enables A1 staging auto-merge. Unset or any non-truthy
// value means OFF (the default).
const stagingAutoMergeEnvVar = "STAGING_AUTO_MERGE"

// stagingAutoMergeMsg is the structured-log message key for an auto-merge decision.
const stagingAutoMergeMsg = "staging_auto_merge"

var (
	errStagingMergeNoRef    = errors.New("staging auto-merge: missing or unparseable PR reference")
	errStagingMergeNoMerger = errors.New("staging auto-merge: no merger configured")
)

// envFlagEnabled reports whether env var name holds an explicit truthy value.
func envFlagEnabled(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// stagingAutoMergeEnabled reports whether A1 staging auto-merge is turned on. It
// defaults to false; only an explicit truthy value enables it.
func stagingAutoMergeEnabled() bool { return envFlagEnabled(stagingAutoMergeEnvVar) }

// stagingMerger is the only seam that performs a real PR merge. The controller
// calls exclusively through this interface; production wires a giteaStagingMerger
// and tests wire a fake. MergePR must fail closed (return an error) rather than
// merge on incomplete input.
type stagingMerger interface {
	MergePR(ref prRef) error
}

// mergeCandidate bundles a finished run's outcome with its CI/check status. The
// check status is supplied by the caller (the future controller) rather than
// queried here — A1 does not query Gitea checks yet. ChecksGreen is a bool whose
// zero value is false, so a missing/unknown status fails closed (never merges).
// ChecksSummary is optional human/audit detail (e.g. "3/3 passed", "pending").
type mergeCandidate struct {
	Outcome       runOutcome
	ChecksGreen   bool
	ChecksSummary string
}

// prURLRefPattern extracts owner, repo and index from a Gitea PR URL of the form
// scheme://host/<owner>/<repo>/pulls/<n>.
var prURLRefPattern = regexp.MustCompile(`(?i)^https?://[^/]+/([^/]+)/([^/]+)/pulls/(\d+)`)

// prRefFromOutcome derives a concrete PR reference (owner/repo/index) from a
// runOutcome. A bare PR number is not enough to address a merge target, so a
// number-only outcome fails closed (ok == false).
func prRefFromOutcome(o runOutcome) (prRef, bool) {
	if u := strings.TrimSpace(o.PRURL); u != "" {
		if m := prURLRefPattern.FindStringSubmatch(u); m != nil {
			idx, _ := strconv.Atoi(m[3])
			if m[1] != "" && m[2] != "" && idx > 0 {
				return prRef{Owner: m[1], Repo: m[2], Index: idx}, true
			}
		}
	}
	return prRef{}, false
}

// maybeAutoMergeStaging is the controller. It merges the PR ONLY when the
// auto-merge flag is enabled AND the outcome is eligible per evaluateStagingGate
// AND the candidate's CI checks are green AND a concrete PR reference can be
// derived. It never merges by default, never panics, and logs a structured
// staging_auto_merge decision for every flag-on evaluation. Merge errors are
// returned and logged, never swallowed.
func maybeAutoMergeStaging(c mergeCandidate, merger stagingMerger) (merged bool, err error) {
	if !stagingAutoMergeEnabled() {
		return false, nil // default: non-merging and silent
	}

	o := c.Outcome
	d := evaluateStagingGate(o)
	fields := map[string]interface{}{
		"eligible":           d.Eligible,
		"reason":             d.Reason,
		"outcome_kind":       string(d.Kind),
		"termination_reason": d.TerminationReason.String(),
		"checks_green":       c.ChecksGreen,
	}
	if c.ChecksSummary != "" {
		fields["checks_summary"] = c.ChecksSummary
	}
	if d.PRURL != "" {
		fields["pr_url"] = d.PRURL
	}
	if d.PRNumber > 0 {
		fields["pr_number"] = d.PRNumber
	}

	if !d.Eligible {
		fields["merged"] = false
		logJSON("info", stagingAutoMergeMsg, fields)
		return false, nil
	}

	// A1 hard precondition: never merge unless CI checks are explicitly green.
	// A missing/unknown status is false, so this fails closed.
	if !c.ChecksGreen {
		fields["merged"] = false
		fields["reason"] = "ineligible: CI checks not green"
		logJSON("info", stagingAutoMergeMsg, fields)
		return false, nil
	}

	ref, ok := prRefFromOutcome(o)
	if !ok {
		fields["merged"] = false
		fields["error"] = errStagingMergeNoRef.Error()
		logJSON("warn", stagingAutoMergeMsg, fields)
		return false, errStagingMergeNoRef
	}
	if merger == nil {
		fields["merged"] = false
		fields["error"] = errStagingMergeNoMerger.Error()
		logJSON("error", stagingAutoMergeMsg, fields)
		return false, errStagingMergeNoMerger
	}

	if mErr := merger.MergePR(ref); mErr != nil {
		fields["merged"] = false
		fields["error"] = mErr.Error()
		logJSON("error", stagingAutoMergeMsg, fields)
		return false, mErr
	}

	fields["merged"] = true
	logJSON("info", stagingAutoMergeMsg, fields)
	return true, nil
}

// giteaStagingMerger is the isolated real adapter: the only code that calls the
// Gitea merge API. Base URL and token are injected (constructor args / config),
// with no hardcoded secrets. It is not constructed by any production path in this
// PR — wiring it in is a later, deliberate step.
type giteaStagingMerger struct {
	baseURL    string
	token      string
	mergeStyle string
	httpClient *http.Client
}

// newGiteaStagingMerger builds the adapter with an injected base URL and token.
func newGiteaStagingMerger(baseURL, token string) *giteaStagingMerger {
	return &giteaStagingMerger{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:      strings.TrimSpace(token),
		mergeStyle: "merge",
		httpClient: http.DefaultClient,
	}
}

// MergePR merges the referenced PR via the Gitea merge API. It fails closed on an
// incomplete ref or unconfigured adapter, and surfaces a non-2xx response as an
// error.
func (m *giteaStagingMerger) MergePR(ref prRef) error {
	if ref.Owner == "" || ref.Repo == "" || ref.Index <= 0 {
		return fmt.Errorf("staging merger: incomplete PR ref %+v", ref)
	}
	if m.baseURL == "" || m.token == "" {
		return errors.New("staging merger: not configured (base URL or token missing)")
	}

	endpoint := fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls/%d/merge", m.baseURL, ref.Owner, ref.Repo, ref.Index)
	body, err := json.Marshal(map[string]string{"Do": m.mergeStyle})
	if err != nil {
		return fmt.Errorf("staging merger: marshal request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("staging merger: build request: %w", err)
	}
	req.Header.Set("Authorization", "token "+m.token)
	req.Header.Set("Content-Type", "application/json")

	client := m.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("staging merger: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("staging merger: gitea returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
