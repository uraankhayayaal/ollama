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
	"ai/tools/binpath"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.lsp.dev/protocol"
)

// ServerCommand возвращает команду запуска языкового сервера в режиме stdio
// для стека проекта. Команду можно переопределить переменной окружения
// LSP_SERVER (готовая строка, например "gopls" или
// "typescript-language-server --stdio") — тогда стек не важен.
//
// Бинарник ищется не только в PATH процесса (он у сервера/демона часто урезан),
// но и в типовых каталогах установки через binpath.Look: ~/go/bin (gopls),
// префиксы npm/nvm (typescript-language-server, pyright-langserver), Homebrew.
// Возвращается АБСОЛЮТНЫЙ путь, чтобы запуск не зависел от PATH.
func ServerCommand(kind stackdetect.Kind, dir string) ([]string, error) {
	if v := strings.TrimSpace(os.Getenv("LSP_SERVER")); v != "" {
		fields := strings.Fields(v)
		if len(fields) == 0 {
			return nil, fmt.Errorf("LSP_SERVER пуст")
		}
		if bin, ok := binpath.Look(fields[0]); ok {
			fields[0] = bin
		}
		return fields, nil
	}
	switch kind {
	case stackdetect.KindGo:
		bin, ok := binpath.Look("gopls")
		if !ok {
			return nil, fmt.Errorf("языковой сервер для Go не найден: нужен gopls в PATH или ~/go/bin (go install golang.org/x/tools/gopls@latest) — навигация недоступна, используй ReadMap/ReadFiles")
		}
		return []string{bin}, nil
	case stackdetect.KindPhp:
		// intelephense — стандартный PHP-сервер (мода stdio), phpactor — запасной.
		if bin, ok := binpath.Look("intelephense"); ok {
			return []string{bin, "--stdio"}, nil
		}
		if bin, ok := binpath.Look("phpactor"); ok {
			return []string{bin, "language-server", "--stdio"}, nil
		}
		return nil, fmt.Errorf("языковой сервер для PHP не найден: нужен intelephense (npm i -g intelephense или composer global require phpactor/phpactor) — навигация недоступна, используй ReadMap/ReadFiles")
	case stackdetect.KindNode:
		bin, ok := binpath.Look("typescript-language-server")
		if !ok {
			return nil, fmt.Errorf("языковой сервер для TypeScript/JavaScript не найден: нужен typescript-language-server (npm i -g typescript-language-server typescript) — навигация недоступна, используй ReadMap/ReadFiles")
		}
		return []string{bin, "--stdio"}, nil
	case stackdetect.KindPython:
		bin, ok := binpath.Look("pyright-langserver")
		if !ok {
			return nil, fmt.Errorf("языковой сервер для Python не найден: нужен pyright-langserver (npm i -g pyright) — навигация недоступна, используй ReadMap/ReadFiles")
		}
		return []string{bin, "--stdio"}, nil
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
	case ".php", ".phtml":
		return protocol.LanguageKindPHP
	}
	switch kind {
	case stackdetect.KindGo:
		return protocol.LanguageKindGo
	case stackdetect.KindPhp:
		return protocol.LanguageKindPHP
	case stackdetect.KindNode:
		return protocol.LanguageKindTypeScript
	case stackdetect.KindPython:
		return protocol.LanguageKindPython
	default:
		return protocol.LanguageKindPlaintext
	}
}
