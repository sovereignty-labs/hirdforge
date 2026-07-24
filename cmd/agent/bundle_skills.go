package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// O-SKILL-BUNDLE (Contract 8): a task's `bundle.skills` resolve to files in the
// personas/skills repo (skills/<name>.md), loaded into the prompt in order.
// Loading is deterministic and LOUD — an unresolved skill fails the dispatch, so
// a task never runs believing it had a skill it didn't (a v1-class correctness
// bug). Skills are knowledge, never authority: they are prompt text only, and
// cannot grant a tool or widen the write boundary (that is the profile's job).

// parseCSVList splits a comma-separated flag value into trimmed, non-empty items.
func parseCSVList(v string) []string {
	var out []string
	for _, item := range strings.Split(v, ",") {
		if s := strings.TrimSpace(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// resolveBundleSkills reads the named skills from skillsRoot/skills/<name>.md and
// concatenates them in order. It returns the rendered block, the names actually
// loaded (audit), or a loud error on the first name that does not resolve.
func resolveBundleSkills(skillsRoot string, names []string) (string, []string, error) {
	if len(names) == 0 {
		return "", nil, nil
	}
	var b strings.Builder
	loaded := make([]string, 0, len(names))
	for _, name := range names {
		rel, err := safeSkillPath(name)
		if err != nil {
			return "", nil, err
		}
		p := filepath.Join(skillsRoot, "skills", rel)
		data, err := os.ReadFile(p)
		if err != nil {
			return "", nil, fmt.Errorf("skill %q did not resolve (skills/%s.md): %w", name, name, err)
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			return "", nil, fmt.Errorf("skill %q is empty (skills/%s.md)", name, name)
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "## Skill: %s\n\n%s", name, content)
		loaded = append(loaded, name)
	}
	return b.String(), loaded, nil
}

// safeSkillPath maps a skill name "a/b" to the relative path "a/b.md", rejecting
// any name that would escape the skills directory (a defense against a malformed
// or hostile bundle — skills are data, but resolution must stay in-tree).
func safeSkillPath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("empty skill name")
	}
	if strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("illegal skill name %q", name)
	}
	clean := filepath.Clean(name)
	if clean == "." || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("illegal skill name %q", name)
	}
	return clean + ".md", nil
}
