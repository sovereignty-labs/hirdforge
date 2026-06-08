package main

import "testing"

func TestExtractCloneWorkspacePath(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "path with trailing slash",
			output: "Cloning into '/workspace/repo'...\nremote: Enumerating objects: 1, done.\nCloning into '/workspace/repo' to /workspace/repo/\n",
			want:   "/workspace/repo/",
		},
		{
			name:   "path without trailing slash",
			output: "Cloning into 'repo'...\nCloning into '/workspace/repo' to /workspace/repo\n",
			want:   "/workspace/repo",
		},
		{
			name:   "empty string",
			output: "",
			want:   "",
		},
		{
			name:   "path with spaces",
			output: "Cloning into 'my repo'...\nCloning into '/workspace/my repo' to /workspace/my repo\n",
			want:   "/workspace/my repo",
		},
		{
			name:   "last line fallback when no marker",
			output: "Some log line\n/workspace/fallback",
			want:   "/workspace/fallback",
		},
		{
			name:   "with in marker",
			output: "Cloning into 'repo'...\nCloning into '/workspace/repo' in /workspace/repo\n",
			want:   "/workspace/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCloneWorkspacePath(tt.output)
			if got != tt.want {
				t.Errorf("extractCloneWorkspacePath(%q) = %q, want %q", tt.output, got, tt.want)
			}
		})
	}
}
