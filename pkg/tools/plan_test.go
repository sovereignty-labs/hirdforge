package tools

import "testing"

func TestParsePlanSteps(t *testing.T) {
	tests := []struct {
		name    string
		input   interface{}
		want    []string
		wantErr string
	}{
		{
			name:  "native interface array",
			input: []interface{}{"a", "b", "c"},
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "native string array",
			input: []string{"a", "b", "c"},
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "json string array",
			input: `["a","b","c"]`,
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "json string object with steps",
			input: `{"steps":["a","b"]}`,
			want:  []string{"a", "b"},
		},
		{
			name:    "empty array",
			input:   []interface{}{},
			wantErr: "steps must not be empty",
		},
		{
			name:    "json string empty array",
			input:   `[]`,
			wantErr: "steps must not be empty",
		},
		{
			name:    "json string empty value",
			input:   `["a",""]`,
			wantErr: "steps must not contain empty values",
		},
		{
			name:    "json object with empty step value",
			input:   `{"steps":["a",""]}`,
			wantErr: "steps must not contain empty values",
		},
		{
			name:    "non array non string",
			input:   42,
			wantErr: "steps must be an array of strings",
		},
		{
			name:    "malformed json string",
			input:   `["a",`,
			wantErr: "steps must be an array of strings",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parsePlanSteps(test.input)
			if test.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error, got nil with %#v", got)
				}
				if err.Error() != test.wantErr {
					t.Fatalf("err=%q want %q", err.Error(), test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("len(got)=%d want %d; got=%#v", len(got), len(test.want), got)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("got[%d]=%q want %q; got=%#v", i, got[i], test.want[i], got)
				}
			}
		})
	}
}
