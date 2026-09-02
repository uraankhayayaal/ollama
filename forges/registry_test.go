package forges

import "testing"

func TestNewRegistryDispatch(t *testing.T) {
	got := map[kind]bool{}
	Register(KindGitLab, func(prURL, token string) (Forge, error) {
		got[KindGitLab] = true
		return &fakeForge{}, nil
	})
	Register(KindGitHub, func(prURL, token string) (Forge, error) {
		got[KindGitHub] = true
		return &fakeForge{}, nil
	})

	if _, err := New("https://gitlab.com/a/b/-/merge_requests/1", "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := New("https://github.com/a/b/pull/1", "t"); err != nil {
		t.Fatal(err)
	}
	if !got[KindGitLab] || !got[KindGitHub] {
		t.Fatalf("не сработал диспатч по хосту: %v", got)
	}

	if _, err := New("https://example.com/x/y/pull/1", "t"); err == nil {
		t.Fatal("ожидалась ошибка для неизвестного хоста")
	}
}

// fakeForge — заглушка для теста.
type fakeForge struct{}

func (fakeForge) GetDiff() (string, error)        { return "", nil }
func (fakeForge) PostComment(ReviewComment) error { return nil }
func (fakeForge) PostSummary(string) error        { return nil }
func (fakeForge) Approve(string) error            { return nil }
