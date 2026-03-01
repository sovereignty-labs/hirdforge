package tasklife

import "strings"

type Tier string

const (
	TierCommand Tier = "command"
	TierStrike  Tier = "strike"
	TierReserve Tier = "reserve"
)

var ModelTiers = map[string]Tier{
	"codex-mini":   TierReserve,
	"gpt-4.1-mini": TierStrike,
	"gpt-4.1":      TierCommand,
	"qwen3:30b":    TierCommand,
}

func OptimizeDelegation(targetModel string, in DelegationFormat) string {
	model := strings.TrimSpace(strings.ToLower(targetModel))
	if model == "codex-mini" {
		return optimizeForCodexMini(in)
	}
	tier, ok := ModelTiers[model]
	if !ok {
		tier = TierReserve
	}
	switch tier {
	case TierStrike:
		return FormatDelegation(DelegationFormat{
			Task:     in.Task,
			Steps:    in.Steps,
			DoneWhen: in.DoneWhen,
		}, 500)
	case TierReserve:
		return FormatDelegation(minimizeDelegation(in), 200)
	case TierCommand:
		fallthrough
	default:
		return FormatDelegation(in, 0)
	}
}

func minimizeDelegation(in DelegationFormat) DelegationFormat {
	out := DelegationFormat{
		Task:     strings.TrimSpace(in.Task),
		DoneWhen: strings.TrimSpace(in.DoneWhen),
	}
	if len(in.Steps) > 2 {
		out.Steps = append([]string(nil), in.Steps[:2]...)
	} else {
		out.Steps = append([]string(nil), in.Steps...)
	}
	return out
}

func optimizeForCodexMini(in DelegationFormat) string {
	compact := minimizeDelegation(in)
	compact.Task = softenLanguage(stripInfraTerms(compact.Task))
	compact.DoneWhen = softenLanguage(stripInfraTerms(compact.DoneWhen))
	for i := range compact.Steps {
		step := stripInfraTerms(compact.Steps[i])
		step = strings.ReplaceAll(step, "curl -X POST", "send the request")
		step = strings.ReplaceAll(step, "curl", "use the request")
		compact.Steps[i] = softenLanguage(step)
	}
	return FormatDelegation(compact, 200)
}

func stripInfraTerms(text string) string {
	replacer := strings.NewReplacer(
		"infrastructure", "",
		"Infrastructure", "",
		"security", "",
		"Security", "",
		"Kubernetes", "",
		"kubernetes", "",
		"cluster", "",
		"Cluster", "",
	)
	return normalizeSpacing(replacer.Replace(text))
}

func softenLanguage(text string) string {
	text = normalizeSpacing(text)
	if text == "" {
		return ""
	}
	text = strings.TrimSuffix(text, ".")
	return text
}

func normalizeSpacing(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}
