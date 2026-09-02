package forges

import "testing"

func TestDetectType(t *testing.T) {
	cases := []struct {
		url  string
		want kind
	}{
		{"https://gitlab.com/it-yakutia/botsad.ru/-/merge_requests/2", KindGitLab},
		{"https://github.com/user/repo/pull/10", KindGitHub},
		{"https://gitee.com/user/repo/pull/5", KindGitLab},
		{"https://gitlab.company.com/team/proj/-/merge_requests/3", KindGitLab},
		{"https://git.example.org/a/b/-/merge_requests/1", ""},
		{"https://example.com/x/y/pull/1", ""},
		{"не-улр", ""},
	}

	for _, c := range cases {
		if got := DetectType(c.url); got != c.want {
			t.Errorf("DetectType(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}