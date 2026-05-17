package main

import (
	"embed"
	"os"
	"path/filepath"
	"strings"
)

//go:embed templates/*.txt
var embeddedModelTemplates embed.FS

// selectModelTemplate picks a template name from the model identifier using
// substring matching. The matching is intentionally loose so model IDs like
// "Qwen-Agent", "claude-sonnet-4-6", "gpt-5.2", and "gemma-4" all route to
// the right template even as model vendors keep iterating on naming. The
// returned name is always one of {qwen, claude, gpt, gemini, default} —
// it's both the template name and the basename of the .txt file.
func selectModelTemplate(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(m, "qwen"):
		return "qwen"
	case strings.Contains(m, "claude"), strings.Contains(m, "anthropic"):
		return "claude"
	case strings.Contains(m, "gpt"), strings.Contains(m, "o1-"), strings.Contains(m, "o3-"):
		return "gpt"
	case strings.Contains(m, "gemini"), strings.Contains(m, "gemma"):
		return "gemini"
	default:
		return "default"
	}
}

// loadModelTemplate resolves a template name from the model identifier, then
// reads the template content. A persona-repo override at
// <personaRepoPath>/templates/<name>.txt wins over the embedded default,
// allowing operators to tweak behavioral anchoring per fleet without a
// binary rebuild. An empty personaRepoPath skips the override check.
//
// Returns the resolved template name and the content. The "source" string
// ("persona" or "embedded") is logged here and also returned so callers
// can include it in their own startup log lines.
func loadModelTemplate(model, personaRepoPath string) (name, content, source string) {
	name = selectModelTemplate(model)
	if path := personaTemplatePath(personaRepoPath, name); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			source = "persona"
			content = string(data)
			logJSON("info", "model template loaded", map[string]interface{}{"template": name, "source": source})
			return name, content, source
		}
	}
	data, err := embeddedModelTemplates.ReadFile("templates/" + name + ".txt")
	if err != nil {
		// Should not happen — the .txt files are //go:embed'd. If it does,
		// fall through with an empty template so the agent still starts and
		// the system prompt is just soul-only (current pre-template behavior).
		logJSON("warn", "model template embed read failed", map[string]interface{}{"template": name, "error": err.Error()})
		return name, "", "missing"
	}
	source = "embedded"
	content = string(data)
	logJSON("info", "model template loaded", map[string]interface{}{"template": name, "source": source})
	return name, content, source
}

// personaTemplatePath returns the candidate persona-override path for a
// template name. Returns "" when no persona repo is configured.
func personaTemplatePath(personaRepoPath, name string) string {
	personaRepoPath = strings.TrimSpace(personaRepoPath)
	if personaRepoPath == "" || strings.TrimSpace(name) == "" {
		return ""
	}
	return filepath.Join(personaRepoPath, "templates", name+".txt")
}

// resolveModelTemplate combines the --model-template flag override with
// auto-detection from --model, then loads the resulting template. An empty
// override falls back to auto-detect. The override is treated as a
// pass-through into selectModelTemplate, which means it can be any string
// that contains a known model substring (e.g. "qwen", "claude") — the same
// heuristics work in both directions.
func resolveModelTemplate(model, override, personaRepoPath string) (name, content, source string) {
	if o := strings.TrimSpace(override); o != "" {
		return loadModelTemplate(o, personaRepoPath)
	}
	return loadModelTemplate(model, personaRepoPath)
}
