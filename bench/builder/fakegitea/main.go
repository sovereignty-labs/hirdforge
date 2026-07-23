// Command fakegitea is a faithful, offline stand-in for the slice of the Gitea
// API the full-flow builder benchmark exercises: create-pr and list-pulls.
//
// It exists so bench/builder/run-flow.sh can score the whole
// edit -> git-commit(push) -> create-pr flow against a *throwaway* remote with
// no live Gitea, GPU, or network. The ground truth it enforces mirrors real
// Gitea in the one way that matters for the benchmark: a pull request is only
// accepted when its head branch actually exists on the remote. Real Gitea
// answers create-pr with 404 when the head ref is missing (the exact friction a
// builder hits when git-commit renames its branch and the model opens the PR
// against the wrong name); the fake reproduces that, so a PR "observed" here is
// a PR that could have been observed in production.
//
// Accepted PRs are appended to -record as one JSON object per line — the
// harness reads that file as the mechanical ground truth after the run.
//
// stdlib only, single throwaway repo, no auth (the benchmark passes no token).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// pullsPath matches /api/v1/repos/{owner}/{repo}/pulls (the only mutating route
// the benchmark uses). owner/repo are captured but not enforced — the fake
// fronts a single throwaway origin, so head-ref existence is the real check.
var pullsPath = regexp.MustCompile(`^/api/v1/repos/([^/]+)/([^/]+)/pulls/?$`)

type pullRequest struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Head    ref    `json:"head"`
	Base    ref    `json:"base"`
}

type ref struct {
	Ref string `json:"ref"`
}

type server struct {
	origin string // path to the bare throwaway remote
	record string // append-only ground-truth log of accepted PRs

	mu    sync.Mutex
	next  int
	pulls []pullRequest // newest last
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18090", "listen address")
	origin := flag.String("origin", "", "path to the bare throwaway remote (required)")
	record := flag.String("record", "", "path to append accepted PRs as JSON lines (required)")
	flag.Parse()
	if *origin == "" || *record == "" {
		log.Fatal("fakegitea: -origin and -record are required")
	}

	s := &server{origin: *origin, record: *record, next: 1}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)

	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("fakegitea listening on %s (origin=%s)", *addr, *origin)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("fakegitea: %v", err)
	}
}

func (s *server) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/_health" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}
	m := pullsPath.FindStringSubmatch(r.URL.Path)
	if m == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found: " + r.URL.Path})
		return
	}
	owner, repo := m[1], m[2]
	switch r.Method {
	case http.MethodPost:
		s.createPR(w, r, owner, repo)
	case http.MethodGet:
		s.listPRs(w)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
	}
}

func (s *server) createPR(w http.ResponseWriter, r *http.Request, owner, repo string) {
	var body struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Head  string `json:"head"`
		Base  string `json:"base"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "invalid body: " + err.Error()})
		return
	}
	head := strings.TrimSpace(body.Head)
	base := strings.TrimSpace(body.Base)
	if base == "" {
		base = "main"
	}
	if head == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "head is required"})
		return
	}

	// Faithful to Gitea: refuse a PR whose head branch does not exist on the
	// remote. This is the branch-name friction the benchmark is meant to catch.
	if !s.branchExists(head) {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"message": fmt.Sprintf("head branch %q does not exist on the remote", head),
		})
		return
	}

	s.mu.Lock()
	num := s.next
	s.next++
	pr := pullRequest{
		Number:  num,
		Title:   strings.TrimSpace(body.Title),
		HTMLURL: fmt.Sprintf("http://%s/%s/%s/pulls/%d", r.Host, owner, repo, num),
		State:   "open",
		Head:    ref{Ref: head},
		Base:    ref{Ref: base},
	}
	s.pulls = append(s.pulls, pr)
	s.mu.Unlock()

	s.appendRecord(pr)
	writeJSON(w, http.StatusCreated, pr)
}

// listPRs returns the recorded PRs newest-first — the shape create-pr's
// conflict/verify paths read.
func (s *server) listPRs(w http.ResponseWriter) {
	s.mu.Lock()
	out := make([]pullRequest, 0, len(s.pulls))
	for i := len(s.pulls) - 1; i >= 0; i-- {
		out = append(out, s.pulls[i])
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (s *server) branchExists(branch string) bool {
	cmd := exec.Command("git", "--git-dir="+s.origin, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return cmd.Run() == nil
}

func (s *server) appendRecord(pr pullRequest) {
	f, err := os.OpenFile(s.record, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("fakegitea: open record: %v", err)
		return
	}
	defer f.Close()
	line, err := json.Marshal(pr)
	if err != nil {
		log.Printf("fakegitea: marshal record: %v", err)
		return
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		log.Printf("fakegitea: write record: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
