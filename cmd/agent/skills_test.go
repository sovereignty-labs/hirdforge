package main

import (
	"testing"
)

func TestParseFrontmatterSharedSkills(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "no frontmatter at all",
			input:    "# Agent Soul\nSome content without frontmatter",
			expected: nil,
		},
		{
			name:     "frontmatter with shared_skills returns the list",
			input:    "---\nshared_skills: [tool-a, tool-b]\n---\ncontent",
			expected: []string{"tool-a", "tool-b"},
		},
		{
			name:     "frontmatter with empty shared_skills returns nil",
			input:    "---\nshared_skills: []\n---\ncontent",
			expected: nil,
		},
		{
			name:     "frontmatter without the shared_skills key returns nil",
			input:    "---\nallowed_tools: [tool-a]\n---\ncontent",
			expected: nil,
		},
		{
			name:     "multiple skills in the list are all returned",
			input:    "---\nshared_skills: [git-clone, read, write, git-commit, gitea]\n---\ncontent",
			expected: []string{"git-clone", "read", "write", "git-commit", "gitea"},
		},
		{
			name:     "shared_skills with quoted strings",
			input:    "---\nshared_skills: [\"git-clone\", 'read']\n---\ncontent",
			expected: []string{"git-clone", "read"},
		},
		{
			name:     "shared_skills with trailing spaces",
			input:    "---\nshared_skills:  [ tool-a  ,  tool-b ]  \n---\ncontent",
			expected: []string{"tool-a", "tool-b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseFrontmatterSharedSkills(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("parseFrontmatterSharedSkills() returned %d items, want %d", len(result), len(tt.expected))
			}
			for i, item := range tt.expected {
				if i >= len(result) {
					t.Fatalf("parseFrontmatterSharedSkills() missing item at index %d", i)
				}
				if result[i] != item {
					t.Errorf("parseFrontmatterSharedSkills() item[%d] = %q, want %q", i, result[i], item)
				}
			}
		})
	}
}
