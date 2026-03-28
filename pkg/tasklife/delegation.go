package tasklife

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

type DelegationFormat struct {
	Task     string
	Issue    string
	Steps    []string
	DoneWhen string
}

var delegationHeaderRE = regexp.MustCompile(`(?im)^(TASK|ISSUE|STEPS|DONE WHEN)\s*:\s*`)

func ValidateDelegation(input string) (DelegationFormat, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return DelegationFormat{}, fmt.Errorf("missing TASK")
	}

	sections := parseDelegationSections(input)
	out := DelegationFormat{
		Task:     strings.TrimSpace(sections["TASK"]),
		Issue:    strings.TrimSpace(sections["ISSUE"]),
		Steps:    parseSteps(sections["STEPS"]),
		DoneWhen: strings.TrimSpace(sections["DONE WHEN"]),
	}

	// If no TASK: header was found, use the full input as the task.
	if out.Task == "" {
		out.Task = trimmed
	}

	return out, nil
}

func FormatDelegation(in DelegationFormat, tokenLimit int) string {
	body := formatDelegation(in, true)
	if tokenLimit > 0 && estimateTokenCount(body) > tokenLimit {
		body = formatDelegation(DelegationFormat{
			Task:     in.Task,
			Steps:    in.Steps,
			DoneWhen: in.DoneWhen,
		}, false)
	}
	return body
}

func parseDelegationSections(input string) map[string]string {
	matches := delegationHeaderRE.FindAllStringSubmatchIndex(input, -1)
	sections := map[string]string{}
	for i, match := range matches {
		header := strings.ToUpper(strings.TrimSpace(input[match[2]:match[3]]))
		start := match[1]
		end := len(input)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		sections[header] = strings.TrimSpace(input[start:end])
	}
	return sections
}

func parseSteps(raw string) []string {
	lines := strings.Split(raw, "\n")
	steps := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "-*")
		line = regexp.MustCompile(`^\d+\.\s*`).ReplaceAllString(line, "")
		line = strings.TrimSpace(line)
		if line != "" {
			steps = append(steps, line)
		}
	}
	return steps
}

func formatDelegation(in DelegationFormat, includeIssue bool) string {
	var b strings.Builder
	b.WriteString("TASK: ")
	b.WriteString(strings.TrimSpace(in.Task))
	b.WriteString("\n")
	if includeIssue && strings.TrimSpace(in.Issue) != "" {
		b.WriteString("ISSUE: ")
		b.WriteString(strings.TrimSpace(in.Issue))
		b.WriteString("\n")
	}
	b.WriteString("STEPS:\n")
	for i, step := range in.Steps {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, strings.TrimSpace(step)))
	}
	b.WriteString("DONE WHEN: ")
	b.WriteString(strings.TrimSpace(in.DoneWhen))
	return b.String()
}

func estimateTokenCount(text string) int {
	return int(math.Ceil(float64(len(strings.Fields(text))) * 1.3))
}
