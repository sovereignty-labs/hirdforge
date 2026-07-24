package main

// v2 Cortex wiring (P1.1). The Cortex module lives in internal/cortex; this
// file is the gateway glue: the --cortex-config flag, the issues-webhook
// ingest path, and the read endpoints. With no --cortex-config the module is
// entirely disabled and v1 behavior is untouched.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.hirdforge.com/kit/hirdforge/internal/cortex"
	"git.hirdforge.com/kit/hirdforge/internal/profile"
	"git.hirdforge.com/kit/hirdforge/internal/sandbox"
)

// cortexDispatchOptions carries the gateway flags for the dispatcher.
type cortexDispatchOptions struct {
	AgentImage    string
	SandboxNS     string
	CredSecret    string
	CloneBase     string
	BaseBranch    string
	InferenceURL  string
	Model         string
	InferenceKey  string
	AgentSoul     string
	MaxToolRounds int
	// Profiles are the loaded harness profiles (O-PROFILE), keyed by name. The
	// dispatcher resolves each task's bundle.profile against this set.
	Profiles map[string]profile.Profile
	// SkillsRepoURL is the tokenless git URL the sandbox resolves bundle.skills
	// against (O-SKILL-BUNDLE); auth rides the guard-primed credential helper.
	SkillsRepoURL string
}

// initCortexDispatcher wires the P1.3 dispatcher: sandbox Jobs, Gitea PR
// observation, and the gate-event feedback loop into Cortex.HandleEvent.
// Outside a cluster the dispatcher cannot run; that is a LOUD log line and
// tasks visibly stay queued — never a silent degrade.
func (g *gateway) initCortexDispatcher(opts cortexDispatchOptions) {
	if g.cortex == nil {
		return
	}
	if opts.AgentImage == "" {
		log.Printf("cortex: DISPATCHER DISABLED — no --cortex-agent-image; queued tasks will not dispatch")
		return
	}
	client, err := sandbox.InClusterClient()
	if err != nil {
		log.Printf("cortex: DISPATCHER DISABLED — %v; queued tasks will not dispatch", err)
		return
	}
	d := &cortex.Dispatcher{
		Store:        g.cortex.Store(),
		Sandbox:      sandbox.NewK8sSandbox(client, opts.SandboxNS),
		PRLookup:     g.cortexPRLookup,
		DiffFetch:    g.cortexDiffFetch,
		ReviewLookup: g.cortexReviewLookup,
		Events:       func(ev cortex.Event) { _, _ = g.cortex.HandleEvent(ev) },
		AgentImage:   opts.AgentImage,
		// Constant base; the per-task tool set / step cap / procedure / context
		// budget are appended from the resolved profile (O-PROFILE) at dispatch.
		AgentCommandBase: []string{
			"valhalla-agent", "-one-shot",
			"-envelope", "/task/envelope.json",
			"-workspace", "/work/repo",
			"-soul", opts.AgentSoul,
			"-inference-url", opts.InferenceURL,
			"-model", opts.Model,
			"-api-key", opts.InferenceKey,
			"-gitea-url", g.giteaURL,
			"-inference-timeout", "300",
		},
		Profiles:      opts.Profiles,
		SkillsRepoURL: opts.SkillsRepoURL,
		CloneURLBase:  strings.TrimSuffix(opts.CloneBase, "/"),
		BaseBranch:    opts.BaseBranch,
		CredentialRef: opts.CredSecret,
	}
	g.cortex.OnTaskQueued = func(route *cortex.Route, taskID string) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
			defer cancel()
			if err := d.DispatchTask(ctx, g.cortex.Config(), route, taskID); err != nil {
				log.Printf("cortex: ERROR dispatching %s: %v", taskID, err)
			}
		}()
	}
	g.cortexSandboxDestroy = makeSandboxDestroyer(client, opts.SandboxNS)
	g.cortex.OnReviewerDispatch = func(route *cortex.Route, ev cortex.Event) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
			defer cancel()
			if err := d.DispatchReviewer(ctx, g.cortex.Config(), route, ev); err != nil {
				log.Printf("cortex: ERROR reviewer dispatch %s: %v", ev.TaskID, err)
			}
		}()
	}
	log.Printf("cortex: dispatcher enabled (ns=%s image=%s)", opts.SandboxNS, opts.AgentImage)

	// Robustness against gateway restarts (PERSISTENCE.md: no silent zombies).
	// The sandbox Jobs outlive the gateway, but the in-memory waiter goroutines
	// do not — so on startup reconcile in-flight tasks (re-attach where the Job
	// survives, fail loudly where it's gone), and run a timeout watchdog that
	// reaps any task stuck past its deadline.
	go d.ReconcileOnStartup(context.Background(), g.cortex.Config(), opts.SandboxNS)
	go d.RunTimeoutWatchdog(context.Background(), time.Minute)
}

// cortexPRLookup observes whether an open PR exists for a head branch — the
// collect step's Gitea observation (never agent-reported).
func (g *gateway) cortexPRLookup(ctx context.Context, repo, headBranch string) (int64, bool, error) {
	var prs []struct {
		Number int64 `json:"number"`
		Head   struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	path := fmt.Sprintf("/api/v1/repos/%s/pulls?state=open&limit=50", repo)
	status, _, err := giteaGetJSONWithStatus(http.DefaultClient, g.giteaURL, g.giteaToken, path, &prs)
	if err != nil {
		return 0, false, err
	}
	if status != http.StatusOK {
		return 0, false, fmt.Errorf("gitea pulls: status %d", status)
	}
	for _, pr := range prs {
		if pr.Head.Ref == headBranch {
			return pr.Number, true, nil
		}
	}
	return 0, false, nil
}

// giteaIssuesPayload is the subset of Gitea's "issues" webhook payload Cortex
// consumes. The fired label rides in Label; the issue's full label set rides
// in Issue.Labels.
type giteaIssuesPayload struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Issue struct {
		Number int64  `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	} `json:"issue"`
	Label struct {
		Name string `json:"name"`
	} `json:"label"`
}

// handleCortexIssuesEvent parses a Gitea "issues" webhook body and routes the
// label event through Cortex. Called from handleGiteaWebhook AFTER HMAC
// validation. Actions other than label application are ignored (that is a
// logged no-op, not a silent one — Cortex records a decision either way when
// the event reaches it).
func (g *gateway) handleCortexIssuesEvent(body []byte) {
	if g.cortex == nil {
		return
	}
	var p giteaIssuesPayload
	if err := json.Unmarshal(body, &p); err != nil {
		log.Printf("cortex: issues webhook: bad payload: %v", err)
		return
	}
	// Gitea fires action "label_updated" (and some versions "labeled") when a
	// label lands on an issue.
	if p.Action != "label_updated" && p.Action != "labeled" {
		return
	}
	labels := make([]string, 0, len(p.Issue.Labels))
	for _, l := range p.Issue.Labels {
		labels = append(labels, l.Name)
	}
	fired := strings.TrimSpace(p.Label.Name)
	firedSet := []string{fired}
	if fired == "" {
		// Some Gitea versions omit the fired label on label_updated; fall
		// back to evaluating every label currently on the issue. Route
		// matching stays exact-equality either way.
		firedSet = labels
	}
	for _, label := range firedSet {
		if label == "" {
			continue
		}
		ev := cortex.Event{
			Type:        cortex.EventIssueLabeled,
			Repo:        p.Repository.FullName,
			Label:       label,
			IssueNumber: p.Issue.Number,
			IssueTitle:  p.Issue.Title,
			IssueBody:   p.Issue.Body,
			IssueLabels: labels,
		}
		if _, err := g.cortex.HandleEvent(ev); err != nil {
			log.Printf("cortex: ERROR handling %s: %v", ev.Type, err)
		}
	}
}

// giteaReviewPayload is the subset of Gitea's pull_request_review webhook.
type giteaReviewPayload struct {
	Action string `json:"action"`
	Review struct {
		Type    string `json:"type"`
		Content string `json:"content"`
	} `json:"review"`
	PullRequest struct {
		Number int64 `json:"number"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

// handleCortexReviewEvent maps a Gitea review webhook to the mechanical
// pr.review_submitted event. Review content rides as data only.
func (g *gateway) handleCortexReviewEvent(body []byte) {
	if g.cortex == nil {
		return
	}
	var p giteaReviewPayload
	if err := json.Unmarshal(body, &p); err != nil {
		log.Printf("cortex: review webhook: bad payload: %v", err)
		return
	}
	state := ""
	switch {
	case strings.Contains(p.Review.Type, "approved"):
		state = "APPROVED"
	case strings.Contains(p.Review.Type, "rejected"), strings.Contains(p.Review.Type, "request_changes"):
		state = "REQUEST_CHANGES"
	default:
		return // comment-only reviews are not verdicts
	}
	ev := cortex.Event{
		Type:        cortex.EventPRReviewSubmitted,
		Repo:        p.Repository.FullName,
		PRNumber:    p.PullRequest.Number,
		ReviewState: state,
		ReviewBody:  p.Review.Content,
		Actor:       p.Sender.Login,
	}
	if _, err := g.cortex.HandleEvent(ev); err != nil {
		log.Printf("cortex: ERROR handling %s: %v", ev.Type, err)
	}
}

// handleCortexPRMergedEvent feeds pr.merged into Cortex (validate-on-merge).
func (g *gateway) handleCortexPRMergedEvent(repo string, prNumber int64, actor string) {
	if g.cortex == nil {
		return
	}
	ev := cortex.Event{Type: cortex.EventPRMerged, Repo: repo, PRNumber: prNumber, Actor: actor}
	if _, err := g.cortex.HandleEvent(ev); err != nil {
		log.Printf("cortex: ERROR handling %s: %v", ev.Type, err)
	}
}

// cortexDiffFetch retrieves the PR's unified diff and head/base refs.
func (g *gateway) cortexDiffFetch(ctx context.Context, repo string, prNumber int64) (string, string, string, error) {
	var pr struct {
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	status, _, err := giteaGetJSONWithStatus(http.DefaultClient, g.giteaURL, g.giteaToken,
		fmt.Sprintf("/api/v1/repos/%s/pulls/%d", repo, prNumber), &pr)
	if err != nil || status != http.StatusOK {
		return "", "", "", fmt.Errorf("gitea pr %d: status %d err %v", prNumber, status, err)
	}
	resp, err := giteaRequest(http.DefaultClient, http.MethodGet, g.giteaURL, g.giteaToken,
		fmt.Sprintf("/api/v1/repos/%s/pulls/%d.diff", repo, prNumber), nil)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	diff, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", "", err
	}
	return string(diff), pr.Head.Ref, pr.Base.Ref, nil
}

// cortexReviewLookup observes the latest review verdict on a PR.
func (g *gateway) cortexReviewLookup(ctx context.Context, repo string, prNumber int64) (string, string, bool, error) {
	var reviews []struct {
		State string `json:"state"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	status, _, err := giteaGetJSONWithStatus(http.DefaultClient, g.giteaURL, g.giteaToken,
		fmt.Sprintf("/api/v1/repos/%s/pulls/%d/reviews", repo, prNumber), &reviews)
	if err != nil {
		return "", "", false, err
	}
	if status != http.StatusOK {
		return "", "", false, fmt.Errorf("gitea reviews: status %d", status)
	}
	for i := len(reviews) - 1; i >= 0; i-- {
		switch reviews[i].State {
		case "APPROVED", "REQUEST_CHANGES":
			return reviews[i].State, reviews[i].User.Login, true, nil
		}
	}
	return "", "", false, nil
}

// registerCortexRoutes wires the v2 read surface (observability contract §2;
// the full P1.8 surface widens this).
func registerCortexRoutes(mux *http.ServeMux, g *gateway) {
	mux.HandleFunc("/api/v1/cortex/routes", func(w http.ResponseWriter, r *http.Request) {
		if g.cortex == nil {
			http.Error(w, "cortex disabled", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, g.cortex.Config())
	})
	mux.HandleFunc("/api/v1/cortex/log", func(w http.ResponseWriter, r *http.Request) {
		if g.cortex == nil {
			http.Error(w, "cortex disabled", http.StatusNotFound)
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		writeJSON(w, http.StatusOK, g.cortex.RecentDecisions(limit))
	})
	mux.HandleFunc("/api/v1/cortex/tasks", func(w http.ResponseWriter, r *http.Request) {
		if g.cortex == nil {
			http.Error(w, "cortex disabled", http.StatusNotFound)
			return
		}
		f := cortex.TaskFilter{
			Status: r.URL.Query().Get("status"),
			Agent:  r.URL.Query().Get("agent"),
			Repo:   r.URL.Query().Get("repo"),
		}
		f.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
		tasks, err := g.cortex.Store().ListTasks(f)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if tasks == nil {
			tasks = []cortex.TaskRecord{}
		}
		writeJSON(w, http.StatusOK, tasks)
	})
}
