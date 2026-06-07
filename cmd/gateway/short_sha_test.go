package main

import (
	"testing"
)

func TestShortSHA(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "40-char SHA truncated to 7",
			in:   "1234567890abcdef1234567890abcdef12345678",
			want: "1234567",
		},
		{
			name: "exactly 7 chars unchanged",
			in:   "1234567",
			want: "1234567",
		},
		{
			name: "short input unchanged",
			in:   "abc",
			want: "abc",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
		{
			name: "whitespace-padded trimmed then truncated",
			in:   "  1234567890  ",
			want: "1234567",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shortSHA(tt.in)
			if got != tt.want {
				t.Errorf("shortSHA(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
