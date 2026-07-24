package tools

import "testing"

func TestReverse(t *testing.T) {
	tests := []struct {
		name string
		in   []int
		out  []int
	}{
		{
			name: "empty",
			in:   nil,
			out:  nil,
		},
		{
			name: "single element",
			in:   []int{1},
			out:  []int{1},
		},
		{
			name: "two elements",
			in:   []int{1, 2},
			out:  []int{2, 1},
		},
		{
			name: "three elements",
			in:   []int{1, 2, 3},
			out:  []int{3, 2, 1},
		},
		{
			name: "five elements",
			in:   []int{1, 2, 3, 4, 5},
			out:  []int{5, 4, 3, 2, 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Reverse(tc.in)
			if len(got) != len(tc.out) {
				t.Fatalf("got len %d, want %d", len(got), len(tc.out))
			}
			for i := range got {
				if got[i] != tc.out[i] {
					t.Fatalf("got[%d] = %v, want %v", i, got[i], tc.out[i])
				}
			}
		})
	}
}

func TestReverseDoesNotMutateInput(t *testing.T) {
	in := []int{1, 2, 3}
	_ = Reverse(in)
	// input should be unchanged
	if in[0] != 1 || in[1] != 2 || in[2] != 3 {
		t.Fatal("Reverse mutated input slice")
	}
}

func TestReverseStrings(t *testing.T) {
	in := []string{"a", "b", "c"}
	want := []string{"c", "b", "a"}
	got := Reverse(in)
	if len(got) != len(want) {
		t.Fatalf("got len %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
