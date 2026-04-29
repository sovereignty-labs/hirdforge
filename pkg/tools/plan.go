package tools

import "fmt"

type PlanTool struct{}

func NewPlanTool() *PlanTool {
	return &PlanTool{}
}

func (t *PlanTool) Name() string {
	return "plan"
}

func (t *PlanTool) Description() string {
	return "Declare an ordered plan for a non-code task"
}

func (t *PlanTool) Parameters() map[string]string {
	return map[string]string{
		"steps": "Ordered list of plan step descriptions",
	}
}

func (t *PlanTool) Execute(args map[string]interface{}) ToolResult {
	steps, err := parsePlanSteps(args["steps"])
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	return ToolResult{Output: fmt.Sprintf("plan recorded with %d steps", len(steps))}
}

type PlanStepCompleteTool struct{}

func NewPlanStepCompleteTool() *PlanStepCompleteTool {
	return &PlanStepCompleteTool{}
}

func (t *PlanStepCompleteTool) Name() string {
	return "plan-step-complete"
}

func (t *PlanStepCompleteTool) Description() string {
	return "Mark a previously declared plan step as complete"
}

func (t *PlanStepCompleteTool) Parameters() map[string]string {
	return map[string]string{
		"step": "Zero-based index of the completed step",
	}
}

func (t *PlanStepCompleteTool) Execute(args map[string]interface{}) ToolResult {
	step := intArg(args, "step")
	if step < 0 {
		return ToolResult{Error: "step must be >= 0"}
	}
	return ToolResult{Output: fmt.Sprintf("plan step %d complete", step)}
}

func parsePlanSteps(raw interface{}) ([]string, error) {
	items, ok := raw.([]interface{})
	if !ok {
		if strings, ok := raw.([]string); ok {
			out := make([]string, 0, len(strings))
			for _, step := range strings {
				if step = stringFromAny(step); step != "" {
					out = append(out, step)
				}
			}
			if len(out) == 0 {
				return nil, fmt.Errorf("steps must not be empty")
			}
			return out, nil
		}
		return nil, fmt.Errorf("steps must be an array of strings")
	}

	out := make([]string, 0, len(items))
	for _, item := range items {
		step := stringFromAny(item)
		if step == "" {
			return nil, fmt.Errorf("steps must not contain empty values")
		}
		out = append(out, step)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("steps must not be empty")
	}
	return out, nil
}
