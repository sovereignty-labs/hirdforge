package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectModelTemplate(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"Qwen-Agent", "qwen"},
		{"qwen2.5-coder-32b", "qwen"},
		{"claude-sonnet-4", "claude"},
		{"claude-opus-4-7", "claude"},
		{"anthropic/claude-3", "claude"},
		{"gpt-5.2", "gpt"},
		{"gpt-4o", "gpt"},
		{"o1-preview", "gpt"},
		{"o3-mini", "gpt"},
		{"gemma-4", "gemini"},
		{"gemini-2.0-flash", "gemini"},
		{"unknown-model", "default"},
		{"", "default"},
		{"   ", "default"},
		// Substring matches anywhere in the model string still work.
		{"my-custom-qwen-fork", "qwen"},
	}
	for _, c := range cases {
		if got := selectModelTemplate(c.model); got != c.want {
			t.Errorf("selectModelTemplate(%q) = %q, want %q", c.model, got, c.want)
		}
	}
}

func TestLoadModelTemplateEmbedded(t *testing.T) {
	// With no persona repo configured, every model falls through to the
	// embedded template for its tier. The embedded content is shipped in
	// templates/*.txt and verified by the substring expected for each tier.
	cases := []struct {
		model        string
		wantName     string
		wantSubstr   string
	}{
		{"Qwen-Agent", "qwen", "DO THE TASK IN THE MESSAGE."},
		{"claude-sonnet-4", "claude", "Use tools proactively"},
		{"gpt-5", "gpt", "Think step by step"},
		{"gemma-4", "gemini", "Rigorously adhere"},
		{"unknown", "default", "DO THE TASK IN THE MESSAGE."},
	}
	for _, c := range cases {
		name, content, source := loadModelTemplate(c.model, "")
		if name != c.wantName {
			t.Errorf("loadModelTemplate(%q) name = %q, want %q", c.model, name, c.wantName)
		}
		if source != "embedded" {
			t.Errorf("loadModelTemplate(%q) source = %q, want embedded", c.model, source)
		}
		if !strings.Contains(content, c.wantSubstr) {
			t.Errorf("loadModelTemplate(%q) content missing %q; got:\n%s", c.model, c.wantSubstr, content)
		}
	}
}

func TestLoadModelTemplateOverride(t *testing.T) {
	// Persona-repo override at <persona>/templates/<name>.txt wins over the
	// embedded default. Set up a fake persona dir, drop a qwen.txt with
	// custom content, then verify loadModelTemplate returns the file's
	// content with source="persona".
	persona := t.TempDir()
	dir := filepath.Join(persona, "templates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	override := "OVERRIDE: this is the persona-repo qwen template.\n"
	if err := os.WriteFile(filepath.Join(dir, "qwen.txt"), []byte(override), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}

	name, content, source := loadModelTemplate("Qwen-Agent", persona)
	if name != "qwen" {
		t.Fatalf("name = %q, want qwen", name)
	}
	if source != "persona" {
		t.Fatalf("source = %q, want persona", source)
	}
	if content != override {
		t.Fatalf("content = %q, want %q", content, override)
	}

	// A different model with no override file falls back to embedded.
	name, content, source = loadModelTemplate("claude-sonnet-4", persona)
	if name != "claude" || source != "embedded" {
		t.Errorf("missing-override fallthrough: name=%q source=%q, want claude/embedded", name, source)
	}
	if !strings.Contains(content, "Use tools proactively") {
		t.Errorf("embedded claude content missing expected substring; got:\n%s", content)
	}
}

// TestResolveModelTemplateFlagOverride exercises the --model-template flag
// path: a non-empty override forces selection regardless of the --model
// value. Empty override falls through to auto-detect.
func TestResolveModelTemplateFlagOverride(t *testing.T) {
	// Force qwen template even when model says claude.
	name, _, source := resolveModelTemplate("claude-sonnet-4", "qwen", "")
	if name != "qwen" || source != "embedded" {
		t.Errorf("override path: name=%q source=%q, want qwen/embedded", name, source)
	}

	// Empty override → auto-detect from model.
	name, _, _ = resolveModelTemplate("claude-sonnet-4", "", "")
	if name != "claude" {
		t.Errorf("auto-detect path: name=%q, want claude", name)
	}

	// Whitespace-only override is treated as empty.
	name, _, _ = resolveModelTemplate("claude-sonnet-4", "   ", "")
	if name != "claude" {
		t.Errorf("whitespace override: name=%q, want claude", name)
	}
}
