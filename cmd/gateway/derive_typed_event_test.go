package main

import "testing"

func TestDeriveTypedEventFromToolCall(t *testing.T) {
	cases := []struct {
		name      string
		input     map[string]interface{}
		wantOK    bool
		wantType  string
		wantExtra map[string]interface{}
	}{
		{
			name: "read tool maps to file_read with path",
			input: map[string]interface{}{
				"type":       "tool_call",
				"session_id": "s1",
				"tool_call": map[string]interface{}{
					"name":      "read",
					"arguments": map[string]interface{}{"path": "cmd/gateway/main.go"},
				},
			},
			wantOK:    true,
			wantType:  "file_read",
			wantExtra: map[string]interface{}{"path": "cmd/gateway/main.go"},
		},
		{
			name: "write tool maps to file_write with status=writing",
			input: map[string]interface{}{
				"tool_call": map[string]interface{}{
					"name":      "write",
					"arguments": map[string]interface{}{"path": "docs/foo.md", "content": "hi"},
				},
			},
			wantOK:    true,
			wantType:  "file_write",
			wantExtra: map[string]interface{}{"path": "docs/foo.md", "status": "writing"},
		},
		{
			name: "edit tool maps to file_write with status=writing",
			input: map[string]interface{}{
				"tool_call": map[string]interface{}{
					"name":      "edit",
					"arguments": map[string]interface{}{"path": "README.md"},
				},
			},
			wantOK:    true,
			wantType:  "file_write",
			wantExtra: map[string]interface{}{"path": "README.md", "status": "writing"},
		},
		{
			name: "git-clone with stringified JSON arguments",
			input: map[string]interface{}{
				"tool_call": map[string]interface{}{
					"name":      "git-clone",
					"arguments": `{"repo":"kit/hirdforge","branch":"main"}`,
				},
			},
			wantOK:    true,
			wantType:  "git_clone",
			wantExtra: map[string]interface{}{"repo": "kit/hirdforge", "branch": "main"},
		},
		{
			name: "create-pr maps to pr_create with head as branch fallback",
			input: map[string]interface{}{
				"tool_call": map[string]interface{}{
					"name":      "create-pr",
					"arguments": map[string]interface{}{"repo": "kit/foo", "head": "fix/x"},
				},
			},
			wantOK:    true,
			wantType:  "pr_create",
			wantExtra: map[string]interface{}{"repo": "kit/foo", "branch": "fix/x"},
		},
		{
			name: "exec without command still produces exec event",
			input: map[string]interface{}{
				"tool_call": map[string]interface{}{
					"name":      "exec",
					"arguments": map[string]interface{}{},
				},
			},
			wantOK:   true,
			wantType: "exec",
		},
		{
			name: "read without path returns no translation",
			input: map[string]interface{}{
				"tool_call": map[string]interface{}{
					"name":      "read",
					"arguments": map[string]interface{}{},
				},
			},
			wantOK: false,
		},
		{
			name: "unknown tool returns no translation",
			input: map[string]interface{}{
				"tool_call": map[string]interface{}{
					"name":      "task_status",
					"arguments": map[string]interface{}{"task_id": "abc"},
				},
			},
			wantOK: false,
		},
		{
			name:   "missing tool_call returns no translation",
			input:  map[string]interface{}{"type": "tool_call"},
			wantOK: false,
		},
		{
			name: "nested function.name is honored when top-level name is empty",
			input: map[string]interface{}{
				"tool_call": map[string]interface{}{
					"function":  map[string]interface{}{"name": "git-commit"},
					"arguments": map[string]interface{}{"repo": "kit/x", "branch": "main"},
				},
			},
			wantOK:    true,
			wantType:  "git_commit",
			wantExtra: map[string]interface{}{"repo": "kit/x", "branch": "main"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := deriveTypedEventFromToolCall(tc.input)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (got=%v)", ok, tc.wantOK, got)
			}
			if !ok {
				return
			}
			if got["type"] != tc.wantType {
				t.Errorf("type = %v, want %v", got["type"], tc.wantType)
			}
			for k, want := range tc.wantExtra {
				if got[k] != want {
					t.Errorf("%s = %v, want %v", k, got[k], want)
				}
			}
			// Translation must not lose the session_id / other passthrough metadata.
			if sid, ok := tc.input["session_id"]; ok {
				if got["session_id"] != sid {
					t.Errorf("session_id passthrough lost: got %v want %v", got["session_id"], sid)
				}
			}
		})
	}
}
