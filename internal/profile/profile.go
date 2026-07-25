// Package profile implements Contract 7 (O-PROFILE): the mechanical configuration
// of the agent loop — tool set, budgets, procedure, loop policies, completion —
// resolved by name from in-repo YAML so a route/bundle selects it declaratively.
//
// A profile can only NARROW capability, never smuggle authority: the write
// boundary (PR-behind-Lockbox) is invariant across profiles. The reviewer
// profile's lack of mutating tools is a security property enforced here at load,
// not a convenience.
package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// SupportedVersion is the only profile_version this loader understands. A file
// declaring another version fails loudly rather than loading a misread config.
const SupportedVersion = 1

// Profile is one resolved loop configuration. Fields mirror config/profiles/*.yaml.
type Profile struct {
	Version     int        `yaml:"profile_version"`
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	DisplayName string     `yaml:"display_name"` // user-facing name; a stand-in the user can replace (D-INTERLOCUTOR). Defaults to Name — never a brand string baked in Go.
	Tools       []string   `yaml:"tools"`
	Procedure   string     `yaml:"procedure"` // builder | reviewer | steward | none
	Budgets     Budgets    `yaml:"budgets"`
	Policies    Policies   `yaml:"policies"`
	Completion  Completion `yaml:"completion"`
}

type Budgets struct {
	MaxToolRounds int `yaml:"max_tool_rounds"`
	ReadMaxLines  int `yaml:"read_max_lines"`
	ReadMaxBytes  int `yaml:"read_max_bytes"`
	ExecMaxOutput int `yaml:"exec_max_output"`
	MaxContext    int `yaml:"max_context"`
}

type Policies struct {
	TodoReanchor     bool `yaml:"todo_reanchor"`
	ReadBeforeEdit   bool `yaml:"read_before_edit"`
	RedirectShellGit bool `yaml:"redirect_shell_git"`
	Compaction       bool `yaml:"compaction"`
	// ReadOnly makes the mutating-tool ban (O-PROFILE §4) apply to this profile
	// regardless of procedure, so a non-reviewer read-only agent (the interlocutor)
	// physically cannot carry edit/write/exec — a security property, not a promise.
	ReadOnly bool `yaml:"read_only"`
}

type Completion struct {
	Requires string `yaml:"requires"` // pr | review | none
}

// mutatingTools are the tools a read-only (reviewer) profile must never carry.
// Their absence is the prompt-injection safety property of O-PROFILE §4.
var mutatingTools = map[string]bool{
	"edit":       true,
	"write":      true,
	"exec":       true,
	"git-commit": true,
	"git-clone":  true,
}

var validProcedures = map[string]bool{"builder": true, "reviewer": true, "steward": true, "none": true}
var validCompletions = map[string]bool{"pr": true, "review": true, "none": true}

// Load reads and validates a single profile YAML file.
func Load(path string) (Profile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Profile{}, fmt.Errorf("profile %s: %w", path, err)
	}
	var p Profile
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return Profile{}, fmt.Errorf("profile %s: parse: %w", path, err)
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if err := p.validate(base); err != nil {
		return Profile{}, fmt.Errorf("profile %s: %w", path, err)
	}
	return p, nil
}

// LoadDir loads every *.yaml/*.yml profile in dir, keyed by name. An empty or
// missing dir is an error — a gateway that cannot resolve profiles must fail
// loudly, never dispatch with an unconfigured loop.
func LoadDir(dir string) (map[string]Profile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("profiles dir %s: %w", dir, err)
	}
	out := map[string]Profile{}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		p, err := Load(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if _, dup := out[p.Name]; dup {
			return nil, fmt.Errorf("profiles dir %s: duplicate profile name %q", dir, p.Name)
		}
		out[p.Name] = p
		names = append(names, p.Name)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("profiles dir %s: no profiles found", dir)
	}
	sort.Strings(names)
	return out, nil
}

func (p *Profile) validate(fileBase string) error {
	if p.Version != SupportedVersion {
		return fmt.Errorf("unsupported profile_version %d (want %d)", p.Version, SupportedVersion)
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if p.Name != fileBase {
		return fmt.Errorf("name %q must match filename %q", p.Name, fileBase)
	}
	// The display name is a user-replaceable stand-in (D-INTERLOCUTOR). When a
	// profile omits it, fall back to the profile Name — still config-sourced, so
	// no brand string is ever hardcoded in Go.
	if strings.TrimSpace(p.DisplayName) == "" {
		p.DisplayName = p.Name
	}
	if len(p.Tools) == 0 {
		return fmt.Errorf("tools must be non-empty")
	}
	if !validProcedures[p.Procedure] {
		return fmt.Errorf("procedure %q must be one of builder|reviewer|steward|none", p.Procedure)
	}
	if p.Completion.Requires != "" && !validCompletions[p.Completion.Requires] {
		return fmt.Errorf("completion.requires %q must be one of pr|review|none", p.Completion.Requires)
	}
	if p.Budgets.MaxToolRounds <= 0 {
		return fmt.Errorf("budgets.max_tool_rounds must be > 0")
	}
	// Security invariant (O-PROFILE §4): a read-only profile must not carry a
	// mutating tool. An agent that physically lacks edit/write/exec cannot be
	// prompt-injected into modifying code. This covers the reviewer (by procedure)
	// and the interlocutor (by procedure or the explicit read_only policy).
	if p.isReadOnlyIntent() {
		for _, t := range p.Tools {
			if mutatingTools[strings.TrimSpace(t)] {
				return fmt.Errorf("read-only profile must not grant mutating tool %q", t)
			}
		}
	}
	return nil
}

// isReadOnlyIntent reports whether the mutating-tool ban applies: a reviewer or a
// steward (by procedure or completion), or any profile that opts in via read_only.
func (p Profile) isReadOnlyIntent() bool {
	return p.Procedure == "reviewer" || p.Procedure == "steward" ||
		p.Completion.Requires == "review" || p.Policies.ReadOnly
}

// AgentArgs returns the profile-derived flags appended to the base agent command
// at dispatch. Only fields with agent CLI surface are emitted; the M4 read/exec
// budgets are enforced as tool defaults that the v1 profiles document (a per-
// profile budget flag is a reserved extension — both baseline profiles use the
// defaults these fields carry). Deterministic: same profile ⇒ same args.
func (p Profile) AgentArgs() []string {
	rounds := p.Budgets.MaxToolRounds
	args := []string{
		"-tools", strings.Join(p.Tools, ","),
		"-max-tool-rounds", strconv.Itoa(rounds),
		"-procedure", p.Procedure,
		"-max-context", strconv.Itoa(p.Budgets.MaxContext),
	}
	if c := strings.TrimSpace(p.Completion.Requires); c != "" {
		args = append(args, "-completion", c)
	}
	return args
}
