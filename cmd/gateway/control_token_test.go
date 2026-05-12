package main

import "testing"

func TestControlTokenREStripsGemmaCallFormat(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "gemma call with trailing thought is fully stripped",
			in:   "call:task_status{task_id:abc-123}thought",
			want: "",
		},
		{
			name: "gemma exec call is stripped",
			in:   "before call:exec{command:ls -la} after",
			want: "before  after",
		},
		{
			name: "standalone closing brace plus thought is stripped",
			in:   "result}thought next",
			want: "result next",
		},
		{
			name: "bare tool_call tags without pipes are stripped",
			in:   "<tool_call>foo</tool_call>",
			want: "foo",
		},
		{
			name: "bare tool_response tags without pipes are stripped",
			in:   "<tool_response>bar</tool_response>",
			want: "bar",
		},
		{
			name: "channel tags in all four variants are stripped",
			in:   "<channel>a</channel><channel|>b<|channel>c",
			want: "abc",
		},
		{
			name: "qwen pipe tokens still stripped",
			in:   "<|im_start|>hello<|im_end|>",
			want: "hello",
		},
		{
			name: "ordinary text is untouched",
			in:   "I had a thought about it.",
			want: "I had a thought about it.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := controlTokenRE.ReplaceAllString(tc.in, "")
			if got != tc.want {
				t.Errorf("ReplaceAllString(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
