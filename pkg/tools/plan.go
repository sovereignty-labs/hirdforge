package tools

import (
	"encoding/json"
	"fmt"
)

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

// parsePlanSteps accepts native arrays plus JSON-encoded string wrappers that
// some smaller models emit instead of structured arguments. Supported string
// forms are a JSON array directly or a JSON object containing a "steps" array.
// Successful string decoding recurses into the normal array validation path, so
// empty arrays and empty step values still hit the existing guards.
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
		if text, ok := raw.(string); ok {
			var arrayPayload []interface{}
			if err := json.Unmarshal([]byte(text), &arrayPayload); err == nil {
				return parsePlanSteps(arrayPayload)
			}
			var objectPayload map[string]interface{}
			if err := json.Unmarshal([]byte(text), &objectPayload); err == nil {
				if nested, ok := objectPayload["steps"]; ok {
					return parsePlanSteps(nested)
				}
			}
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
