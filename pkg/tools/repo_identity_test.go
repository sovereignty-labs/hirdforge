package tools

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseOwnerRepoFromURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		url         string
		owner, repo string
		ok          bool
	}{
		{"https with .git", "https://git.hirdforge.com/kit/hirdforge.git", "kit", "hirdforge", true},
		{"https no .git", "https://git.hirdforge.com/kit/hirdforge", "kit", "hirdforge", true},
		{"http", "http://git.hirdforge.com/kit/hirdforge.git", "kit", "hirdforge", true},
		{"embedded creds", "https://token:secret@git.hirdforge.com/kit/hirdforge.git", "kit", "hirdforge", true},
		{"trailing slash", "https://git.hirdforge.com/kit/hirdforge/", "kit", "hirdforge", true},
		{"scp-like", "git@git.hirdforge.com:kit/hirdforge.git", "kit", "hirdforge", true},
		{"deep path keeps last two", "https://host/sub/group/owner/repo.git", "owner", "repo", true},
		{"empty", "", "", "", false},
		{"host only", "https://git.hirdforge.com/", "", "", false},
		{"no owner", "https://git.hirdforge.com/hirdforge", "", "", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			owner, repo, ok := parseOwnerRepoFromURL(tt.url)
			if ok != tt.ok || owner != tt.owner || repo != tt.repo {
				t.Fatalf("parseOwnerRepoFromURL(%q) = (%q,%q,%v), want (%q,%q,%v)",
					tt.url, owner, repo, ok, tt.owner, tt.repo, tt.ok)
			}
		})
	}
}

// gitInitWithOrigin creates a real repo at dir with the given origin URL.
func gitInitWithOrigin(t *testing.T, dir, origin string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", origin},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestOriginOwnerRepo(t *testing.T) {
	dir := t.TempDir()
	gitInitWithOrigin(t, dir, "https://git.hirdforge.com/kit/hirdforge.git")

	owner, name, ok := originOwnerRepo(dir)
	if !ok || owner != "kit" || name != "hirdforge" {
		t.Fatalf("originOwnerRepo = (%q,%q,%v), want (kit,hirdforge,true)", owner, name, ok)
	}

	// No git repo → not ok, no panic.
	if _, _, ok := originOwnerRepo(t.TempDir()); ok {
		t.Error("non-repo dir should not resolve an origin")
	}
	if _, _, ok := originOwnerRepo(""); ok {
		t.Error("empty repoDir should not resolve")
	}
}

func TestParseRepoResolvesOriginForBareName(t *testing.T) {
	// Root layout: the workspace root IS the repo, cloned from kit/hirdforge, and
	// the agent refers to it by the bare name "repo".
	work := t.TempDir()
	gitInitWithOrigin(t, work, "https://git.hirdforge.com/kit/hirdforge.git")

	api := NewGiteaAPITool("https://git.hirdforge.com", "tok")
	api.WorkDir = work

	// Bare name must resolve to the real owner/name via origin, NOT gitea_admin.
	if owner, name := api.parseRepo("repo"); owner != "kit" || name != "hirdforge" {
		t.Fatalf(`parseRepo("repo") = (%q,%q), want (kit,hirdforge)`, owner, name)
	}
	// An explicit owner/name is still honored verbatim.
	if owner, name := api.parseRepo("someone/thing"); owner != "someone" || name != "thing" {
		t.Fatalf(`parseRepo("someone/thing") = (%q,%q), want (someone,thing)`, owner, name)
	}

	// Subdir layout: repo lives at work/hirdforge.
	work2 := t.TempDir()
	gitInitWithOrigin(t, work2, "https://git.hirdforge.com/kit/hirdforge.git")
	parent := t.TempDir()
	// move: emulate a subdir by initializing directly under parent.
	sub := filepath.Join(parent, "hirdforge")
	if err := exec.Command("cp", "-r", work2, sub).Run(); err != nil {
		t.Fatalf("cp: %v", err)
	}
	api2 := NewGiteaAPITool("https://git.hirdforge.com", "tok")
	api2.WorkDir = parent
	if owner, name := api2.parseRepo("hirdforge"); owner != "kit" || name != "hirdforge" {
		t.Fatalf(`parseRepo("hirdforge") subdir = (%q,%q), want (kit,hirdforge)`, owner, name)
	}
}

func TestParseRepoFallsBackWithoutOrigin(t *testing.T) {
	// No WorkDir configured → legacy default-owner behavior (unchanged).
	api := NewGiteaAPITool("https://git.hirdforge.com", "tok")
	t.Setenv("GITEA_DEFAULT_OWNER", "")
	if owner, name := api.parseRepo("repo"); owner != "gitea_admin" || name != "repo" {
		t.Fatalf(`parseRepo("repo") no-workdir = (%q,%q), want (gitea_admin,repo)`, owner, name)
	}
}
