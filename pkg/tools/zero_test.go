package tools

import (
	"testing"
)

func TestZero(t *testing.T) {
	// Comparable types
	tests := []struct {
		name string
		want interface{}
		got  interface{}
	}{
		{
			name: "int",
			want: 0,
			got:  Zero[int](),
		},
		{
			name: "string",
			want: "",
			got:  Zero[string](),
		},
		{
			name: "bool",
			want: false,
			got:  Zero[bool](),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %v, want %v", tc.got, tc.want)
			}
		})
	}

	// Uncomparable types
	t.Run("slice is nil", func(t *testing.T) {
		if got := Zero[[]int](); got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})

	t.Run("pointer is nil", func(t *testing.T) {
		if got := Zero[*int](); got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})
}
