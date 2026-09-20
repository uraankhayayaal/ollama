// Инструмент ResolveGitConflicts — авто-резолв конфликтов мерджа (Ф-4).
//
// Работает в каталоге незавершённого `git merge` (постоянный конфликтный
// worktree Ф-4 или любой другой клон со stage-конфликтами): OutputDir (FileOps)
// инструмента указывает на этот каталог. Модель получает конфликтный
// unified-дифф (`git diff`), правит файлы штатными пишущими инструментами
// (SearchReplace/WriteFiles — те же помечают файлы в FileOps.touched для
// повторной ЛСП-проверки), затем действием resolve проверяет готовность,
// запускает ЛСП по затронутым файлам и коммитит резолв (завершая merge).
//
// Действия:
//
//	conflicts (по умолчанию) — список конфликтных путей + авто-резолв
//	    тривиальных блоков (gitops.TrivialResolve) + их stage; возвращает
//	    «сложные» файлы (с маркерами) и diff для правок моделью;
//	resolve — повторный авто-резолв, проверка «маркеров не осталось»,
//	    ЛСП по затронутым файлам, git add -A + git commit (конец merge).
package tools

import (
	"ai/gitops"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ResolveGitConflicts — имя инструмента в реестре (см. registry.go newTool).
const ResolveGitConflicts = "ResolveGitConflicts"

// resolveGitConflictsTool — обёртка инструмента в реестре. ex nil → git CLI.
type resolveGitConflictsTool struct {
	ops *FileOps
	ex  gitops.Executor
}

func (t *resolveGitConflictsTool) Name() string { return ResolveGitConflicts }

func (t *resolveGitConflictsTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name: ResolveGitConflicts,
		Description: "Используй этот инструмент, когда нужно решить конфликты git-мерджа (Ф-4): " +
			"находит конфликтующие файлы, автоматически разрешает тривиальные блоки (идентичные/типа " +
			"форматтеров/одна сторона пустая), сложные отдаёт тебе (unified-дифф с маркерами) для правки " +
			"через SearchReplace/WriteFiles. Внимание: OutputDir инструмента — это конфликтный worktree проекта, " +
			"изменения применяются ТОЛЬКО к нему. После правок вызови с action=resolve: инструмент проверит, " +
			"что маркеров не осталось, прогонит ЛСП по затронутым файлам и закоммитит резолв (завершит merge). " +
			"Коммит на удалённый сервер НЕ пушит — финальный merge в main делает сервер после обязательной приёмки.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"conflicts", "resolve"},
					"description": "conflicts — показать конфликты и авто-резолвить тривиальные (по умолчанию); resolve — проверить готовность, ЛСП и закоммитить резолв.",
				},
				"files": map[string]any{
					"type":        "array",
					"description": "Опционально (для action=resolve): конфликтные пути из предыдущего ответа, чтобы инструмент проверил именно их.",
					"items":       map[string]any{"type": "string"},
				},
				"message": map[string]any{
					"type":        "string",
					"description": "Опционально (для action=resolve): сообщение коммита резолва. По умолчанию «резолв конфликтов».",
				},
			},
			"required":             []string{},
			"additionalProperties": false,
		},
	}
}

// resolveParams — параметры инструмента (action/files/message).
type resolveParams struct {
	Action  string   `json:"action"`
	Files   []string `json:"files"`
	Message string   `json:"message"`
}

func (t *resolveGitConflictsTool) Execute(args map[string]any) ([]byte, error) {
	var p resolveParams
	if raw, err := json.Marshal(args); err == nil {
		_ = json.Unmarshal(raw, &p)
	}
	if t.ops == nil || t.ops.OutputDir == "" {
		return resolveJSON(map[string]any{
			"status":  "error",
			"message": "OutputDir не задан — инструмент работает в каталоге незавершённого merge (конфликтный worktree проекта)",
		}), nil
	}

	ex := t.ex
	if ex == nil {
		ex = gitops.CLIExecutor{}
	}
	repo := gitops.RepoFromState(ex, t.ops.OutputDir, "", "", "")
	ctx := context.Background()

	switch strings.TrimSpace(p.Action) {
	case "resolve":
		return t.actionResolve(ctx, repo, p)
	default: // "" или "conflicts"
		return t.actionConflicts(ctx, repo)
	}
}

// actionConflicts — конфликты конфликтного каталога: список, авто-резолв
// тривиальных, stage резолвов, diff «сложных» для модели.
func (t *resolveGitConflictsTool) actionConflicts(ctx context.Context, repo *gitops.Repo) ([]byte, error) {
	files, err := repo.UnmergedFiles(ctx)
	if err != nil {
		return resolveJSON(map[string]any{
			"status":  "error",
			"message": "не удалось получить список конфликтующих файлов: " + err.Error(),
		}), nil
	}
	if len(files) == 0 {
		return resolveJSON(map[string]any{
			"status":  "ok",
			"message": "конфликтующих файлов нет — merge уже завершён или каталог не в состоянии конфликта",
		}), nil
	}

	resolved, hard, err := gitops.TrivialResolve(t.ops.OutputDir, sortDedup(files))
	if err != nil {
		return resolveJSON(map[string]any{"status": "error", "message": "авто-резолв: " + err.Error()}), nil
	}
	if len(resolved) > 0 {
		if err := repo.Stage(ctx, resolved); err != nil {
			return resolveJSON(map[string]any{"status": "error", "message": "git add резолвов: " + err.Error()}), nil
		}
	}

	diff, _ := repo.ConflictDiff(ctx, hard)
	msg := fmt.Sprintf("Авто-резолвил %d файлов (маркеры убраны, застейджены).", len(resolved))
	if len(hard) > 0 {
		msg += fmt.Sprintf(" Осталось %d файлов с маркерами — исправь их SearchReplace/WriteFiles по diff ниже, затем вызови action=resolve (files=[%s]).",
			len(hard), strings.Join(hard, ", "))
	}

	// Если комбинированный diff пуст (файл добавлен/удалён одной стороной) —
	// покажем его содержимое с маркерами как есть.
	if diff == "" {
		if body := markeredBody(t.ops.OutputDir, hard); body != "" {
			diff = body
		}
	}

	return resolveJSON(map[string]any{
		"status":   "conflicts",
		"files":    hard,
		"resolved": resolved,
		"diff":     diff,
		"message":  msg,
	}), nil
}

// actionResolve — финализация резолва: разупрощает остатки, проверяет, что
// маркеров не осталось, прогоняет ЛСП по затронутым файлам и коммитит.
func (t *resolveGitConflictsTool) actionResolve(ctx context.Context, repo *gitops.Repo, p resolveParams) ([]byte, error) {
	files := p.Files
	if len(files) == 0 {
		if unmerged, err := repo.UnmergedFiles(ctx); err == nil {
			files = unmerged
		}
	}

	// Повторный авто-резолв: модель могла не тронуть тривиальные остатки.
	resolved, _, err := gitops.TrivialResolve(t.ops.OutputDir, files)
	if err != nil {
		return resolveJSON(map[string]any{"status": "error", "message": "авто-резолв: " + err.Error()}), nil
	}
	if len(resolved) > 0 {
		if err := repo.Stage(ctx, resolved); err != nil {
			return resolveJSON(map[string]any{"status": "error", "message": "git add резолвов: " + err.Error()}), nil
		}
	}

	unmerged, _ := repo.UnmergedFiles(ctx)
	markers, err := gitops.HasConflictMarkers(t.ops.OutputDir, files)
	if err != nil {
		return resolveJSON(map[string]any{"status": "error", "message": "проверка маркеров: " + err.Error()}), nil
	}
	remaining := union(unmerged, markers)
	if len(remaining) > 0 {
		diff, _ := repo.ConflictDiff(ctx, remaining)
		return resolveJSON(map[string]any{
			"status":  "still_conflicts",
			"files":   remaining,
			"diff":    diff,
			"message": "В файлах ещё остались маркеры конфликта или незакрытые записи индекса — исправь их SearchReplace/WriteFiles и вызови action=resolve снова",
		}), nil
	}

	// ЛСП по затронутым мутациями файлам (SearchReplace/WriteFiles) + конфликтным.
	touched := t.ops.TakeTouched()
	check := append([]string(nil), touched...)
	for _, f := range files {
		if !sliceContains(check, f) {
			check = append(check, f)
		}
	}
	if len(check) > 0 {
		diags, hadMutation := t.ops.LspCheckFiles(check)
		if hadMutation && len(diags) > 0 {
			return resolveJSON(map[string]any{
				"status":      "lsp",
				"diagnostics": diags,
				"message":     "после резолва ЛСП нашёл проблемы в затронутых файлах — исправь и повтори action=resolve",
			}), nil
		}
	}

	msg := strings.TrimSpace(p.Message)
	if msg == "" {
		msg = "резолв конфликтов"
	}
	if err := repo.Commit(ctx, msg); err != nil {
		return resolveJSON(map[string]any{
			"status":  "error",
			"message": "git commit резолва: " + err.Error() + " — убедись, что конфликты застейджены",
		}), nil
	}
	return resolveJSON(map[string]any{
		"status":  "resolved",
		"message": "Конфликты разрешены и закоммичены («" + msg + "»). Резолв завершён — сервер выполнит обязательную приёмку и сольёт в main по POST .../epics/:eid/resolve.",
	}), nil
}

// markeredBody читает содержимое файлов с маркерами (fallback diffа для
// файлов, добавленных/удалённых одной стороной).
func markeredBody(dir string, paths []string) string {
	var b strings.Builder
	for _, clean := range paths {
		data, rerr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(clean)))
		if rerr != nil {
			continue
		}
		b.WriteString("=== " + clean + " ===\n")
		b.Write(data)
		if !strings.HasSuffix(string(data), "\n") {
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

// resolveJSON сериализует результат инструмента.
func resolveJSON(v map[string]any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// sortDedup сортирует и убирает дубликаты путей.
func sortDedup(files []string) []string {
	out := append([]string(nil), files...)
	sort.Strings(out)
	i := 0
	for j := 0; j < len(out); j++ {
		if i == 0 || out[j] != out[i-1] {
			out[i] = out[j]
			i++
		}
	}
	return out[:i]
}

// union склеивает списки путей без дубликатов.
func union(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var out []string
	for _, l := range [][]string{a, b} {
		for _, p := range l {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// sliceContains — вхождение строки в срез.
func sliceContains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
