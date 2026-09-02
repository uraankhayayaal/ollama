package gitlab

import "testing"

func TestParseURL(t *testing.T) {
	cfg, err := ParseURL("https://gitlab.com/it-yakutia/botsad.ru/-/merge_requests/7", "tok")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.ProjID != "it-yakutia%2Fbotsad.ru" {
		t.Errorf("неверный ProjID: %s", cfg.ProjID)
	}
	if cfg.MRIID != "7" {
		t.Errorf("неверный MRIID: %s", cfg.MRIID)
	}
	if cfg.BaseURL != "https://gitlab.com" {
		t.Errorf("неверный BaseURL: %s", cfg.BaseURL)
	}
}

func TestParseURLInvalid(t *testing.T) {
	if _, err := ParseURL("https://gitlab.com/it-yakutia/botsad.ru", "tok"); err == nil {
		t.Error("ожидалась ошибка для не-MR URL")
	}
}
