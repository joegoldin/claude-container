package cmd

import "testing"

func TestExtractAuthURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain claude.com URL surrounded by paragraph breaks",
			in:   "Welcome\n\nhttps://claude.com/cai/oauth/authorize?code=true\n\nPaste code",
			want: "https://claude.com/cai/oauth/authorize?code=true",
		},
		{
			name: "plain claude.ai URL surrounded by paragraph breaks",
			in:   "Welcome\n\nhttps://claude.ai/oauth/authorize?code=true\n\nPaste",
			want: "https://claude.ai/oauth/authorize?code=true",
		},
		{
			name: "URL wrapped across multiple LF newlines",
			in:   "URL:\nhttps://claude.ai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d\n1962f5e&response_type=code\n\nPaste",
			want: "https://claude.ai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&response_type=code",
		},
		{
			name: "URL wrapped across CRLF newlines",
			in:   "https://claude.com/cai/oauth?a=1\r\n&b=2\r\n\r\nrest",
			want: "https://claude.com/cai/oauth?a=1&b=2",
		},
		{
			name: "stops at space (not a URL continuation)",
			in:   "https://claude.ai/oauth?x=y rest of line",
			want: "https://claude.ai/oauth?x=y",
		},
		{
			name: "stops when line break is followed by non-URL char",
			in:   "https://claude.ai/oauth?x=y\n(Press c to copy)",
			want: "https://claude.ai/oauth?x=y",
		},
		{
			name: "no URL",
			in:   "Welcome to Claude\nPress c",
			want: "",
		},
		{
			name: "percent-encoded query string with continuation",
			in:   "https://claude.com/cai/oauth?redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2F\ncode%2Fcallback\n",
			want: "https://claude.com/cai/oauth?redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractAuthURL(tc.in)
			if got != tc.want {
				t.Errorf("extractAuthURL\n  got:  %q\n  want: %q", got, tc.want)
			}
		})
	}
}
