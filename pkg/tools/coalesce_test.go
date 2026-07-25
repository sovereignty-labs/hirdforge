package tools

import (
	"testing"
)

func TestCoalesce(t *testing.T) {
	tests := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{
			name: "empty input returns zero",
			got:  Coalesce[int](),
			want: 0,
		},
		{
			name: "all zero ints returns zero",
			got:  Coalesce(0, 0, 0),
			want: 0,
		},
		{
			name: "first value non-zero",
			got:  Coalesce(1, 2, 3),
			want: 1,
		},
		{
			name: "later value non-zero",
			got:  Coalesce(0, 0, 42),
			want: 42,
		},
		{
			name: "empty strings returns zero",
			got:  Coalesce("", "", ""),
			want: "",
		},
		{
			name: "first string non-zero",
			got:  Coalesce("hello", "world"),
			want: "hello",
		},
		{
			name: "later string non-zero",
			got:  Coalesce("", "", "found"),
			want: "found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Use type assertion to compare the concrete types
			switch got := tc.got.(type) {
			case int:
				want := tc.want.(int)
				if got != want {
					t.Fatalf("got %v, want %v", got, want)
				}
			case string:
				want := tc.want.(string)
				if got != want {
					t.Fatalf("got %v, want %v", got, want)
				}
			}
		})
	}

	// Pointer type tests
	t.Run("all nil pointers", func(t *testing.T) {
		got := Coalesce[*int](nil, nil, nil)
		if got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})

	t.Run("first non-nil pointer", func(t *testing.T) {
		v := 10
		got := Coalesce(&v, nil, nil)
		if got == nil || *got != 10 {
			t.Fatalf("got %v, want &10", got)
		}
	})

	t.Run("later non-nil pointer", func(t *testing.T) {
		v := 99
		got := Coalesce(nil, nil, &v)
		if got == nil || *got != 99 {
			t.Fatalf("got %v, want &99", got)
		}
	})
}
