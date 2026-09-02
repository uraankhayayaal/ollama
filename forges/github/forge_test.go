package github

import "testing"

func TestParseURL(t *testing.T) {
	cfg, err := ParseURL("https://github.com/owner/repo/pull/42", "tok")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Owner != "owner" || cfg.Repo != "repo" || cfg.PRNumber != "42" {
		t.Errorf("неверный разбор: %+v", cfg)
	}
	if cfg.BaseURL != "https://api.github.com" {
		t.Errorf("неверный BaseURL: %s", cfg.BaseURL)
	}
}

func TestParseURLInvalid(t *testing.T) {
	if _, err := ParseURL("https://github.com/owner/repo", "tok"); err == nil {
		t.Error("ожидалась ошибка для не-PR URL")
	}
}
