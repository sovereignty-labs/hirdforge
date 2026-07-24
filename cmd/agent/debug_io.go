package main

import "strings"

// Diagnostic tool-I/O logging. The normal loop logs only tool NAMES and, on
// success, `{tool, success:true}` — not the exec command, the tool output, or
// the model's terminal message (only its length). That blind spot turned a
// benchmark failure into a multi-run hunt. With -debug-io on, a diagnostic run
// records the command it ran, what came back, and what the model finally said —
// truncated, and off by default so production logs are unaffected.

const debugIOMaxChars = 900

func truncForDebug(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > debugIOMaxChars {
		return s[:debugIOMaxChars] + "…[truncated]"
	}
	return s
}

// debugLogToolCall records the arguments the model actually passed (the exec
// command above all), so a diagnostic run can see WHAT it tried.
func debugLogToolCall(debugIO bool, sessionID, tool string, args map[string]interface{}) {
	if !debugIO {
		return
	}
	fields := map[string]interface{}{"session_id": sessionID, "tool": tool}
	if v, ok := args["command"].(string); ok {
		fields["command"] = truncForDebug(v)
	}
	if v, ok := args["path"].(string); ok {
		fields["path"] = v
	}
	if v, ok := args["old_str"].(string); ok {
		fields["old_str"] = truncForDebug(v)
	}
	logJSON("info", "tool_io_call", fields)
}

// debugLogToolResult records what a tool returned — including successful output,
// which the normal path omits.
func debugLogToolResult(debugIO bool, sessionID, tool, output, errStr string) {
	if !debugIO {
		return
	}
	logJSON("info", "tool_io_result", map[string]interface{}{
		"session_id": sessionID,
		"tool":       tool,
		"output":     truncForDebug(output),
		"error":      truncForDebug(errStr),
	})
}

// debugLogFinalContent records the model's terminal message (the loop otherwise
// logs only its character count).
func debugLogFinalContent(debugIO bool, sessionID, content string) {
	if !debugIO || strings.TrimSpace(content) == "" {
		return
	}
	logJSON("info", "final_content", map[string]interface{}{
		"session_id": sessionID,
		"content":    truncForDebug(content),
	})
}
