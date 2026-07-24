package tools

import (
	"fmt"
	"regexp"
	"strings"
)

// M3 — Edit replacer cascade (BUILDER_HARNESS §M3).
//
// Quantized local models emit slightly-imprecise old_str: internal whitespace
// collapsed, or leading indentation dropped. When the intent is unambiguous the
// edit should be absorbed; when it isn't, it must FAIL WITH COACHING, never
// guess. Strategies, tried in order, uniqueness enforced at each:
//  1. exact match
//  2. whitespace-normalized — internal runs of spaces/tabs collapsed (indentation
//     still significant)
//  3. indentation-flexible — leading indentation ignored for matching, and the
//     file's actual indentation re-applied to the replacement
//
// Replacement always preserves the FILE's formatting: the fuzzy strategies locate
// the block in the file and substitute new_str there, so the model's imprecise
// whitespace never overwrites the file. The set starts at three and grows from
// our own rescue telemetry, not a ported list.

var internalWSRun = regexp.MustCompile(`[ \t]+`)

func leadingIndent(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

// collapseInternalWS keeps leading indentation but collapses internal whitespace
// runs to a single space (and trims trailing) — indentation-sensitive,
// internal-whitespace-insensitive. This is strategy 2's comparison key.
func collapseInternalWS(s string) string {
	lead := leadingIndent(s)
	rest := s[len(lead):]
	return lead + strings.TrimRight(internalWSRun.ReplaceAllString(rest, " "), " \t")
}

// stripIndent drops leading indentation only — indentation-insensitive,
// internal-whitespace-sensitive. Strategy 3's comparison key.
func stripIndent(s string) string {
	return strings.TrimLeft(s, " \t")
}

// editResult carries the outcome of a replacer cascade.
type editResult struct {
	Updated  string
	Strategy string // "exact" | "whitespace" | "indent"
}

// errEditNotFound and errEditAmbiguous distinguish the two failure modes so the
// tool can coach appropriately.
type editMatchError struct {
	ambiguous bool
	count     int
}

func (e *editMatchError) Error() string {
	if e.ambiguous {
		return fmt.Sprintf("old_str matches %d places", e.count)
	}
	return "old_str not found"
}

// applyReplacerCascade runs the strategies in order and returns the first
// UNIQUE match's result. An ambiguous match at any level fails immediately (no
// falling through to a looser strategy that might match elsewhere) — looseness
// must never turn one ambiguous edit into a confident wrong one.
func applyReplacerCascade(content, oldStr, newStr string) (editResult, error) {
	// Strategy 1: exact.
	if c := strings.Count(content, oldStr); c == 1 {
		return editResult{Updated: strings.Replace(content, oldStr, newStr, 1), Strategy: "exact"}, nil
	} else if c > 1 {
		return editResult{}, &editMatchError{ambiguous: true, count: c}
	}

	hadTrailingNL := strings.HasSuffix(content, "\n")
	fileLines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	oldLines := strings.Split(strings.TrimSuffix(oldStr, "\n"), "\n")
	if len(oldLines) == 0 || (len(oldLines) == 1 && oldLines[0] == "") {
		return editResult{}, &editMatchError{}
	}

	for _, strat := range []struct {
		name     string
		norm     func(string) string
		reindent bool
	}{
		{"whitespace", collapseInternalWS, false},
		{"indent", stripIndent, true},
	} {
		starts := blockMatches(fileLines, oldLines, strat.norm)
		if len(starts) > 1 {
			return editResult{}, &editMatchError{ambiguous: true, count: len(starts)}
		}
		if len(starts) == 1 {
			out := replaceBlock(fileLines, starts[0], oldLines, newStr, strat.reindent)
			if hadTrailingNL {
				out += "\n"
			}
			return editResult{Updated: out, Strategy: strat.name}, nil
		}
	}
	return editResult{}, &editMatchError{}
}

// blockMatches returns the start indices of every contiguous run of fileLines
// whose per-line normalization equals the normalized pattern.
func blockMatches(fileLines, patLines []string, norm func(string) string) []int {
	if len(patLines) == 0 || len(patLines) > len(fileLines) {
		return nil
	}
	normPat := make([]string, len(patLines))
	for i, l := range patLines {
		normPat[i] = norm(l)
	}
	var starts []int
	for i := 0; i+len(patLines) <= len(fileLines); i++ {
		match := true
		for j := range patLines {
			if norm(fileLines[i+j]) != normPat[j] {
				match = false
				break
			}
		}
		if match {
			starts = append(starts, i)
		}
	}
	return starts
}

// replaceBlock substitutes new_str for the matched block of file lines. When
// reindent is set (strategy 3), the file's extra leading indentation — the part
// of the file line's indent beyond what old_str carried — is prepended to each
// non-blank new_str line, so a dedented replacement lands at the file's depth.
func replaceBlock(fileLines []string, start int, oldLines []string, newStr string, reindent bool) string {
	var newLines []string
	if newStr != "" {
		newLines = strings.Split(newStr, "\n")
	}
	if reindent {
		newLines = reindentLines(newLines, leadingIndent(fileLines[start]), leadingIndent(oldLines[0]))
	}
	merged := make([]string, 0, len(fileLines)-len(oldLines)+len(newLines))
	merged = append(merged, fileLines[:start]...)
	merged = append(merged, newLines...)
	merged = append(merged, fileLines[start+len(oldLines):]...)
	return strings.Join(merged, "\n")
}

// reindentLines prepends the file's extra indentation to each non-blank line,
// but only when old_str's indent is a clean prefix of the file's (the dedent
// case). Otherwise it leaves new_str untouched — never guess at mixed tabs/spaces.
func reindentLines(newLines []string, fileIndent, oldIndent string) []string {
	if !strings.HasPrefix(fileIndent, oldIndent) {
		return newLines
	}
	extra := fileIndent[len(oldIndent):]
	if extra == "" {
		return newLines
	}
	out := make([]string, len(newLines))
	for i, l := range newLines {
		if strings.TrimSpace(l) == "" {
			out[i] = l
			continue
		}
		out[i] = extra + l
	}
	return out
}
