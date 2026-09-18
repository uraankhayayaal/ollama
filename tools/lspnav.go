package tools

// Навигационные инструменты Ф-3: LspDefinition / LspReferences / LspHover.
//
// В отличие от LspCheck (однократный CLI-чекер, Ф-1), здесь используется
// долгоживущий языковой сервер проекта через пакет tools/lspclient: агент
// «ходит по коду» так же, как IDE, — находит определение символа, все ссылки
// на него и документацию (hover) без чтения десятков файлов. Если сервер не
// установлен — graceful degrade: status skipped и подсказка ReadMap/ReadFiles.
//
// Все три инструмента — read-only; при отсутствии сервера шаг не падает.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ai/stackdetect"
	"ai/tools/lspclient"
)

// Имена навигационных инструментов в реестре (см. registry.go newTool).
const (
	LspDefinition = "LspDefinition"
	LspReferences = "LspReferences"
	LspHover      = "LspHover"
)

// lspNavigator — точка входа к языковому серверу; в тестах подменяется
// фейковым Navigator, чтобы не запускать процесс.
var lspNavigator = func(ctx context.Context, dir string, kind stackdetect.Kind) (lspclient.Navigator, error) {
	return lspclient.Shared().Navigator(ctx, dir, kind)
}

// lspNavMode — вид навигационного запроса.
type lspNavMode int

const (
	lspNavDefinition lspNavMode = iota
	lspNavReferences
	lspNavHover
)

// lspNavParams — общие JSON-параметры навигации (line/col 1-based; для
// references — include_declaration).
type lspNavParams struct {
	File               string `json:"file"`
	Line               int    `json:"line"`
	Col                int    `json:"col"`
	IncludeDeclaration *bool  `json:"include_declaration"`
}

// lspProj — контекст файла: директория проекта (== CWD языкового сервера),
// стек и префикс проекта относительно OutputDir.
type lspProj struct {
	dir    string
	kind   stackdetect.Kind
	prefix string
}

// toOutput переводит путь из проекта в путь относительно OutputDir (единый
// формат путей файловых инструментов).
func (p lspProj) toOutput(f string) string {
	if p.prefix == "" {
		return f
	}
	return p.prefix + "/" + f
}

// lspDefinitionTool — обёртка LspDefinition в реестре.
type lspDefinitionTool struct{ ops *FileOps }

func (t *lspDefinitionTool) Name() string { return LspDefinition }
func (t *lspDefinitionTool) Definition() ToolDefinition {
	return lspNavToolDefinition(LspDefinition,
		"Используй этот инструмент, чтобы найти ОПРЕДЕЛЕНИЕ символа (функции, типа, переменной), не читая файлы целиком: "+
			"укажи файл и позицию (line/col, нумерация с 1). Возвращает компактный список locations с точными позициями — "+
			"экономит раунды и токены по сравнению с ReadFiles/ReadMap. Если языковой сервер не установлен — status skipped и подсказка использовать ReadMap.",
		map[string]any{
			"file": map[string]any{"type": "string", "description": "Относительный путь файла, например server/main.go"},
			"line": map[string]any{"type": "integer", "description": "Номер строки (с 1), где находится символ"},
			"col":  map[string]any{"type": "integer", "description": "Номер колонки (с 1) символа"},
		}, []string{"file", "line", "col"})
}
func (t *lspDefinitionTool) Execute(args map[string]any) ([]byte, error) {
	return t.ops.LspDefinition(args)
}

// lspReferencesTool — обёртка LspReferences в реестре.
type lspReferencesTool struct{ ops *FileOps }

func (t *lspReferencesTool) Name() string { return LspReferences }
func (t *lspReferencesTool) Definition() ToolDefinition {
	return lspNavToolDefinition(LspReferences,
		"Используй этот инструмент, чтобы найти ВСЕ ссылки на символ (функцию, тип, переменную) по коду проекта: "+
			"укажи файл и позицию определения (line/col, нумерация с 1). Незаменим перед рефакторингом: показывает, что ещё "+
			"сломается при изменении. Возвращает locations с точными позициями; если сервер не установлен — status skipped.",
		map[string]any{
			"file":                map[string]any{"type": "string", "description": "Относительный путь файла, например server/main.go"},
			"line":                map[string]any{"type": "integer", "description": "Номер строки (с 1) символа"},
			"col":                 map[string]any{"type": "integer", "description": "Номер колонки (с 1) символа"},
			"include_declaration": map[string]any{"type": "boolean", "description": "Включать само определение в список (по умолчанию true)"},
		}, []string{"file", "line", "col"})
}
func (t *lspReferencesTool) Execute(args map[string]any) ([]byte, error) {
	return t.ops.LspReferences(args)
}

// lspHoverTool — обёртка LspHover в реестре.
type lspHoverTool struct{ ops *FileOps }

func (t *lspHoverTool) Name() string { return LspHover }
func (t *lspHoverTool) Definition() ToolDefinition {
	return lspNavToolDefinition(LspHover,
		"Используй этот инструмент, чтобы быстро получить краткую справку о символе (сигнатуру функции, тип, документацию) "+
			"в позиции file:line:col (нумерация с 1), не читая окружающий код. Возвращает текст hover и диапазон символа; "+
			"если сервер не установлен — status skipped.",
		map[string]any{
			"file": map[string]any{"type": "string", "description": "Относительный путь файла, например server/main.go"},
			"line": map[string]any{"type": "integer", "description": "Номер строки (с 1)"},
			"col":  map[string]any{"type": "integer", "description": "Номер колонки (с 1)"},
		}, []string{"file", "line", "col"})
}
func (t *lspHoverTool) Execute(args map[string]any) ([]byte, error) {
	return t.ops.LspHover(args)
}

// lspNavDefinition собирает ToolDefinition навигационного инструмента.
func lspNavToolDefinition(name, desc string, props map[string]any, required []string) ToolDefinition {
	return ToolDefinition{
		Name:        name,
		Description: desc,
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             required,
			"additionalProperties": false,
		},
	}
}

// LspDefinition выполняет навигационный запрос «перейти к определению».
func (ops *FileOps) LspDefinition(args map[string]any) ([]byte, error) {
	return ops.lspNav(lspNavDefinition, args)
}

// LspReferences выполняет навигационный запрос «найти все ссылки».
func (ops *FileOps) LspReferences(args map[string]any) ([]byte, error) {
	return ops.lspNav(lspNavReferences, args)
}

// LspHover выполняет навигационный запрос «справка о символе».
func (ops *FileOps) LspHover(args map[string]any) ([]byte, error) {
	return ops.lspNav(lspNavHover, args)
}

// lspNav выполняет навигационный запрос: определяет проект/стек файла,
// поднимает (или берёт из кэша) языковой сервер и возвращает результат.
func (ops *FileOps) lspNav(mode lspNavMode, args map[string]any) ([]byte, error) {
	var p lspNavParams
	if raw, err := json.Marshal(args); err == nil {
		_ = json.Unmarshal(raw, &p)
	}
	if strings.TrimSpace(p.File) == "" {
		return lspJSON(map[string]any{"status": "error", "message": "не задан file"}), nil
	}
	if p.Line <= 0 {
		p.Line = 1
	}
	if p.Col <= 0 {
		p.Col = 1
	}

	proj, rel, err := ops.lspFileContext(p.File)
	if err != nil {
		return lspJSON(map[string]any{"status": "skipped", "message": err.Error() + " — используй ReadMap/ReadFiles"}), nil
	}

	// Внешний дедлайн с запасом на ленивый запуск сервера (initialize) и сам
	// запрос: у каждого шага внутри клиента свой LSP_TIMEOUT.
	ctx, cancel := context.WithTimeout(context.Background(), 2*lspclient.RequestTimeout())
	defer cancel()

	nav, err := lspNavigator(ctx, proj.dir, proj.kind)
	if err != nil {
		return lspJSON(map[string]any{"status": "skipped", "message": err.Error()}), nil
	}

	switch mode {
	case lspNavHover:
		h, err := nav.Hover(ctx, rel, p.Line, p.Col)
		if err != nil {
			return lspJSON(map[string]any{"status": "error", "message": "запрос к языковому серверу не удался: " + err.Error()}), nil
		}
		if h == nil || strings.TrimSpace(h.Contents) == "" {
			return lspJSON(map[string]any{"status": "success", "message": "нет информации о символе в указанной позиции"}), nil
		}
		res := map[string]any{"status": "success", "contents": h.Contents}
		if h.Line > 0 {
			res["range"] = map[string]any{"line": h.Line, "col": h.Col, "end_line": h.EndLine, "end_col": h.EndCol}
		}
		return lspJSON(res), nil
	case lspNavDefinition:
		locs, err := nav.Definition(ctx, rel, p.Line, p.Col)
		if err != nil {
			return lspJSON(map[string]any{"status": "error", "message": "запрос к языковому серверу не удался: " + err.Error()}), nil
		}
		return ops.lspLocations(proj, locs), nil
	default: // lspNavReferences
		include := true
		if p.IncludeDeclaration != nil {
			include = *p.IncludeDeclaration
		}
		locs, err := nav.References(ctx, rel, p.Line, p.Col, include)
		if err != nil {
			return lspJSON(map[string]any{"status": "error", "message": "запрос к языковому серверу не удался: " + err.Error()}), nil
		}
		return ops.lspLocations(proj, locs), nil
	}
}

// lspLocations сериализует список позиций с лимитами (LSP_MAX_LOCATIONS /
// LSP_MAX_OUTPUT), приводя пути к виду относительно OutputDir.
func (ops *FileOps) lspLocations(proj lspProj, locs []lspclient.Location) []byte {
	limit := lspMaxLocations()
	truncated := 0
	if len(locs) > limit {
		truncated = len(locs) - limit
		locs = locs[:limit]
	}

	out := make([]map[string]any, 0, len(locs))
	for _, l := range locs {
		item := map[string]any{
			"file": proj.toOutput(l.File),
			"line": l.Line,
			"col":  l.Col,
		}
		if l.EndLine > 0 {
			item["end_line"] = l.EndLine
			item["end_col"] = l.EndCol
		}
		out = append(out, item)
	}

	result := map[string]any{"status": "success", "count": len(locs), "locations": out}
	if truncated > 0 {
		result["truncated"] = truncated
	}
	if len(out) == 0 {
		result["message"] = "совпадений не найдено (проверь позицию file:line:col или перепроверь через ReadFiles)"
	}

	maxOut := lspLimits().maxOutput
	if b, _ := json.Marshal(result); len(b) > maxOut {
		for len(out) > 0 {
			out = out[:len(out)-1]
			truncated++
			result["locations"] = out
			result["truncated"] = truncated
			if b, _ = json.Marshal(result); len(b) <= maxOut {
				break
			}
		}
	}
	return lspJSON(result)
}

// lspMaxLocations — лимит числа позиций в ответе (LSP_MAX_LOCATIONS, по
// умолчанию 50).
func lspMaxLocations() int {
	if v := strings.TrimSpace(os.Getenv("LSP_MAX_LOCATIONS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 50
}

// lspFileContext определяет проект/стек файла и его путь относительно
// проекта. Логика та же, что у LspCheck (корень или подпроект монорепозитория).
func (ops *FileOps) lspFileContext(file string) (lspProj, string, error) {
	full, err := ops.ResolvePath(file)
	if err != nil {
		return lspProj{}, "", err
	}
	if st, serr := os.Stat(full); serr != nil || st.IsDir() {
		return lspProj{}, "", fmt.Errorf("файл не найден: %s", file)
	}

	dir := filepath.ToSlash(filepath.Clean(ops.OutputDir))
	rel := ops.relPath(full)
	proj, kind := lspProject(dir, []string{rel})
	if proj == "" {
		return lspProj{}, "", fmt.Errorf("не удалось определить стек проекта для %s", rel)
	}

	prefix := ""
	relProj := rel
	if proj != dir {
		if r, rerr := filepath.Rel(dir, proj); rerr == nil {
			prefix = filepath.ToSlash(r)
		}
		if prefix != "" {
			relProj = strings.TrimPrefix(rel, prefix+"/")
		}
	}
	return lspProj{dir: proj, kind: kind, prefix: prefix}, relProj, nil
}
