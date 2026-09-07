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

func TestCommentSignature(t *testing.T) {
	a := []ReviewComment{
		{FilePath: "internal/config/config.go", Line: 10, Text: "x"},
		{FilePath: "main.go", Line: 3, Text: "y"},
	}
	// Порядок и текст не влияют на сигнатуру — только file:line.
	b := []ReviewComment{
		{FilePath: "main.go", Line: 3, Text: "другой текст"},
		{FilePath: "internal/config/config.go", Line: 10, Text: "z"},
	}
	if CommentSignature(a) != CommentSignature(b) {
		t.Errorf("сигнатуры должны совпадать независимо от порядка/текста")
	}

	// Другое расположение — другая сигнатура.
	c := []ReviewComment{
		{FilePath: "internal/config/config.go", Line: 11, Text: "x"},
	}
	if CommentSignature(a) == CommentSignature(c) {
		t.Errorf("сигнатуры должны различаться при изменении file:line")
	}
}
