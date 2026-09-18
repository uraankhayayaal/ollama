// Package lspclient — нативный LSP-клиент для навигационных инструментов
// агента (definition/references/hover).
//
// Ф-3: в отличие от однократных CLI-чекеров Ф-1 (LspCheck), здесь языковой
// сервер запускается как долгоживущий процесс в режиме --stdio и общается с
// ним JSON-RPC по фреймингу LSP (Content-Length). Клиент кэшируется на проект
// (см. Manager), открывает документы (didOpen/didChange) и умеет завершаться
// через shutdown/exit. Сеть не используется: только локальные бинарники.
package lspclient

import (
	"ai/stackdetect"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.lsp.dev/protocol"
)

// ServerCommand возвращает команду запуска языкового сервера в режиме stdio
// для стека проекта. Команду можно переопределить переменной окружения
// LSP_SERVER (готовая строка, например "gopls" или
// "typescript-language-server --stdio") — тогда стек не важен.
func ServerCommand(kind stackdetect.Kind, dir string) ([]string, error) {
	if v := strings.TrimSpace(os.Getenv("LSP_SERVER")); v != "" {
		fields := strings.Fields(v)
		if len(fields) == 0 {
			return nil, fmt.Errorf("LSP_SERVER пуст")
		}
		return fields, nil
	}
	switch kind {
	case stackdetect.KindGo:
		if _, err := exec.LookPath("gopls"); err != nil {
			return nil, fmt.Errorf("языковой сервер для Go не найден: нужен gopls в PATH (go install golang.org/x/tools/gopls@latest) — навигация недоступна, используй ReadMap/ReadFiles")
		}
		return []string{"gopls"}, nil
	case stackdetect.KindNode:
		if _, err := exec.LookPath("typescript-language-server"); err != nil {
			return nil, fmt.Errorf("языковой сервер для TypeScript/JavaScript не найден: нужен typescript-language-server (npm i -g typescript-language-server typescript) — навигация недоступна, используй ReadMap/ReadFiles")
		}
		return []string{"typescript-language-server", "--stdio"}, nil
	case stackdetect.KindPython:
		if _, err := exec.LookPath("pyright-langserver"); err != nil {
			return nil, fmt.Errorf("языковой сервер для Python не найден: нужен pyright-langserver (npm i -g pyright) — навигация недоступна, используй ReadMap/ReadFiles")
		}
		return []string{"pyright-langserver", "--stdio"}, nil
	default:
		return nil, fmt.Errorf("стек проекта не определён — навигация по коду недоступна")
	}
}

// languageFor возвращает languageId для запроса didOpen по расширению файла,
// с фолбэком на язык стека проекта.
func languageFor(file string, kind stackdetect.Kind) protocol.LanguageKind {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".ts":
		return protocol.LanguageKindTypeScript
	case ".tsx":
		return protocol.LanguageKindTypeScriptReact
	case ".js", ".mjs", ".cjs":
		return protocol.LanguageKindJavaScript
	case ".jsx":
		return protocol.LanguageKindJavaScriptReact
	case ".go":
		return protocol.LanguageKindGo
	case ".py", ".pyi":
		return protocol.LanguageKindPython
	}
	switch kind {
	case stackdetect.KindGo:
		return protocol.LanguageKindGo
	case stackdetect.KindNode:
		return protocol.LanguageKindTypeScript
	case stackdetect.KindPython:
		return protocol.LanguageKindPython
	default:
		return protocol.LanguageKindPlaintext
	}
}
