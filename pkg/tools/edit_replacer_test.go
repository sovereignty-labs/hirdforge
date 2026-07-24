package tools

import "testing"

func TestReplacerCascade(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		oldStr   string
		newStr   string
		want     string
		strategy string
		wantErr  bool
		ambig    bool
	}{
		{
			name:     "exact match",
			content:  "a\nfoo bar\nb\n",
			oldStr:   "foo bar",
			newStr:   "foo baz",
			want:     "a\nfoo baz\nb\n",
			strategy: "exact",
		},
		{
			// Scenario 1: internal whitespace collapsed in old_str → strategy 2,
			// and the file's original spacing is what gets replaced.
			name:     "whitespace-normalized rescue",
			content:  "x\n    foo    bar\ny\n",
			oldStr:   "    foo bar",
			newStr:   "    foo QUX",
			want:     "x\n    foo QUX\ny\n",
			strategy: "whitespace",
		},
		{
			// Scenario 2: dropped indentation in a MULTILINE old_str → strategy 3
			// (a single-line old_str would exact-match as a substring regardless
			// of indent). The file's indentation is re-applied to the dedented
			// replacement.
			name:     "indentation-flexible rescue re-indents",
			content:  "func f() {\n        x := 1\n        return x\n}\n",
			oldStr:   "x := 1\nreturn x",
			newStr:   "x := 2\nreturn x",
			want:     "func f() {\n        x := 2\n        return x\n}\n",
			strategy: "indent",
		},
		{
			name:     "indent rescue multiline re-indent preserves relative",
			content:  "if x {\n    a()\n    b()\n}\n",
			oldStr:   "a()\nb()",
			newStr:   "a()\nc()",
			want:     "if x {\n    a()\n    c()\n}\n",
			strategy: "indent",
		},
		{
			// Scenario 3: genuinely ambiguous → fail with coaching, no guess.
			name:    "ambiguous exact fails",
			content: "dup\ndup\n",
			oldStr:  "dup",
			newStr:  "x",
			wantErr: true,
			ambig:   true,
		},
		{
			// Neither line exact-matches "a b" (both have a double space), but both
			// collapse to it under strategy 2 → ambiguous, must fail not guess.
			name:    "ambiguous fuzzy fails",
			content: "a  b\na  b\n",
			oldStr:  "a b",
			newStr:  "x",
			wantErr: true,
			ambig:   true,
		},
		{
			name:    "not found fails",
			content: "nothing here\n",
			oldStr:  "absent",
			newStr:  "x",
			wantErr: true,
		},
		{
			name:     "no trailing newline preserved",
			content:  "one\ntwo",
			oldStr:   "two",
			newStr:   "TWO",
			want:     "one\nTWO",
			strategy: "exact",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := applyReplacerCascade(tc.content, tc.oldStr, tc.newStr)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", res.Updated)
				}
				if me, ok := err.(*editMatchError); ok && me.ambiguous != tc.ambig {
					t.Errorf("ambiguous = %v, want %v (%v)", me.ambiguous, tc.ambig, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Updated != tc.want {
				t.Errorf("updated = %q, want %q", res.Updated, tc.want)
			}
			if res.Strategy != tc.strategy {
				t.Errorf("strategy = %q, want %q", res.Strategy, tc.strategy)
			}
		})
	}
}

func TestReindentOnlyOnCleanDedent(t *testing.T) {
	// old_str indent is NOT a prefix of the file indent (tabs vs spaces) → do not
	// guess; leave new_str untouched.
	got := reindentLines([]string{"x"}, "    ", "\t")
	if len(got) != 1 || got[0] != "x" {
		t.Errorf("mixed tabs/spaces must not be re-indented, got %q", got)
	}
	// Clean dedent → extra indent applied to non-blank lines only.
	got = reindentLines([]string{"a", "", "b"}, "\t\t", "")
	if got[0] != "\t\ta" || got[1] != "" || got[2] != "\t\tb" {
		t.Errorf("clean dedent re-indent wrong: %q", got)
	}
}
