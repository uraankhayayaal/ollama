package lspclient

import (
	"ai/stackdetect"
	"go.lsp.dev/protocol"
	"testing"
)

// TestLanguageForPhp проверяет маппинг PHP-файлов в languageId LSP: и по
// расширению, и по стеку проекта по умолчанию.
func TestLanguageForPhp(t *testing.T) {
	if got := languageFor("src/App.php", stackdetect.KindUnknown); got != protocol.LanguageKindPHP {
		t.Fatalf(".php: got %q, want %q", got, protocol.LanguageKindPHP)
	}
	if got := languageFor("resources/views/welcome.phtml", stackdetect.KindUnknown); got != protocol.LanguageKindPHP {
		t.Fatalf(".phtml: got %q, want %q", got, protocol.LanguageKindPHP)
	}
	if got := languageFor("misc.conf", stackdetect.KindPhp); got != protocol.LanguageKindPHP {
		t.Fatalf("php-стек по умолчанию: got %q, want %q", got, protocol.LanguageKindPHP)
	}
}
