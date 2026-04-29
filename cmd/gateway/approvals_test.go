package main

import "testing"

func TestApprovalAgentName(t *testing.T) {
	tests := []struct {
		name string
		item approvalQueueItem
		want string
	}{
		{
			name: "explicit agent name wins",
			item: approvalQueueItem{
				AgentName: "jeeves",
				Params: map[string]interface{}{
					"agent": "wrong",
				},
			},
			want: "jeeves",
		},
		{
			name: "falls back to agent key",
			item: approvalQueueItem{
				Params: map[string]interface{}{
					"agent": "ivar",
				},
			},
			want: "ivar",
		},
		{
			name: "falls back to from key",
			item: approvalQueueItem{
				Params: map[string]interface{}{
					"from": "jeeves",
				},
			},
			want: "jeeves",
		},
		{
			name: "falls back to requested_by key",
			item: approvalQueueItem{
				Params: map[string]interface{}{
					"requested_by": "ragnar",
				},
			},
			want: "ragnar",
		},
		{
			name: "returns unknown when empty",
			item: approvalQueueItem{
				Params: map[string]interface{}{},
			},
			want: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := approvalAgentName(tt.item); got != tt.want {
				t.Fatalf("approvalAgentName()=%q want %q", got, tt.want)
			}
		})
	}
}
