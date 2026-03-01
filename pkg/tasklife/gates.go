package tasklife

import (
	"fmt"
	"regexp"
	"strings"
)

type CompletionGate struct {
	Name    string
	Pattern string
	Nudge   string
}

type GateCheckResult struct {
	Passed bool
	Failed []CompletionGate
	Nudges []string
}

func CheckCompletionGates(response string, gates []CompletionGate) (GateCheckResult, error) {
	result := GateCheckResult{Passed: true}
	for _, gate := range gates {
		re, err := regexp.Compile(gate.Pattern)
		if err != nil {
			return GateCheckResult{}, fmt.Errorf("compile gate %q: %w", gate.Name, err)
		}
		if re.MatchString(response) {
			continue
		}
		result.Passed = false
		result.Failed = append(result.Failed, gate)
		if msg := strings.TrimSpace(gate.Nudge); msg != "" {
			result.Nudges = append(result.Nudges, msg)
		}
	}
	return result, nil
}
