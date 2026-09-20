// Авто-резолв конфликтов мерджа (Ф-4).
//
// Вход инструмента резолва — незавершённый `git merge` в изолированном
// worktree: git оставляет в конфликтующих файлах маркеры
// `<<<<<<< ` / `=======` / `>>>>>>> ` и stage-записи (1/2/3) в индексе.
// Здесь живут:
//
//   - UnmergedFiles — список конфликтующих путей (`git ls-files -u`);
//   - TrivialResolve — авто-резолв «тривиальных» блоков: обе стороны
//     идентичны, расходятся только пробелами (форматтер/переносы) либо одна
//     сторона пустая (строки только добавлены/только удалены с одной стороны).
//     Такие блоки решаются сами (берём нашу/непустую сторону), сложные —
//     остаются маркерами для модели;
//   - HasConflictMarkers — проверка, что в файлах ещё остались маркеры;
//   - ConflictDiff / Stage — представление для модели и отметка резолва git-у;
//   - AddWorktree / RemoveWorktree — постоянный конфликтный worktree, в котором
//     живёт процесс «main → релизная ветка» до завершения (Ф-4, CSP).
package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Маркеры конфликта git в рабочей копии. Метки сторон (HEAD/main/…) различаются
// между запусками, поэтому детектируем по префиксам/точному равенству.
const (
	conflictMarkerOur   = "<<<<<<< " // начало блока: наша сторона
	conflictMarkerSep   = "======="  // разделитель
	conflictMarkerTheir = ">>>>>>> " // конец блока: чужая сторона
)

// ConflictBlock — один конфликтный регион внутри файла. Our/Their — тексты
// сторон БЕЗ маркеров (каждая строка, кроме последней, с переводом строки).
type ConflictBlock struct {
	Our   string
	Their string
}

// ScanConflictBlocks разбирает содержимое файла на конфликтные регионы.
// hasConflicts=false — маркеров в файле нет (чистый файл). Если маркеры
// «битые» (нет разделителя/закрывающего маркера до конца файла), возвращается
// частичный список блоков с hasConflicts=false — такой файл авто-резолву не
// подлежит и уходит модели как «сложный».
func ScanConflictBlocks(content string) ([]ConflictBlock, bool) {
	var blocks []ConflictBlock
	lines := strings.Split(content, "\n")
	i := 0
	for i < len(lines) {
		line := lines[i]
		if !isConflictLine(line, conflictMarkerOur) {
			i++
			continue
		}
		var our, their []string
		j := i + 1
		for j < len(lines) && lines[j] != conflictMarkerSep {
			our = append(our, lines[j])
			j++
		}
		if j >= len(lines) {
			return nil, false // разделитель не найден — маркеры битые
		}
		j++
		for j < len(lines) && !isConflictLine(lines[j], conflictMarkerTheir) {
			their = append(their, lines[j])
			j++
		}
		if j >= len(lines) {
			return nil, false // закрывающий маркер не найден
		}
		blocks = append(blocks, ConflictBlock{
			Our:   strings.Join(our, "\n"),
			Their: strings.Join(their, "\n"),
		})
		i = j + 1
	}
	if len(blocks) == 0 {
		return nil, false
	}
	return blocks, true
}

// ResolveConflictMarkers убирает из содержимого маркеры конфликта, разрешая
// каждый блок тривиально. Если в файле есть хотя бы один «сложный» блок (или
// маркеры битые) — возвращается исходное содержимое и false: файл уходит
// модели на ручной резолв. Гарантирует, что маркеры не удаляются частично.
func ResolveConflictMarkers(content string) (string, bool) {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	i := 0
	for i < len(lines) {
		line := lines[i]
		if !isConflictLine(line, conflictMarkerOur) {
			out = append(out, line)
			i++
			continue
		}
		var our, their []string
		j := i + 1
		for j < len(lines) && lines[j] != conflictMarkerSep {
			our = append(our, lines[j])
			j++
		}
		if j >= len(lines) {
			return content, false
		}
		j++
		for j < len(lines) && !isConflictLine(lines[j], conflictMarkerTheir) {
			their = append(their, lines[j])
			j++
		}
		if j >= len(lines) {
			return content, false
		}
		choice, ok := trivialChoice(our, their)
		if !ok {
			return content, false
		}
		out = append(out, choice...)
		i = j + 1
	}
	return strings.Join(out, "\n"), true
}

// trivialChoice выбирает текст стороны для тривиально разрешимого блока.
// Правила (см. шапку пакета): идентичные стороны → наша (для пустых обеих —
// ничего), расхождение только в пробелах → наша, одна сторона пустая → непустая.
// ok=false — блок «сложный», требует ручного (модельного) резолва.
func trivialChoice(our, their []string) ([]string, bool) {
	ourText := strings.Join(our, "\n")
	theirText := strings.Join(their, "\n")
	switch {
	case ourText == theirText:
		return our, true
	case ourText == "":
		return their, true
	case theirText == "":
		return our, true
	case normalizeWhitespace(ourText) == normalizeWhitespace(theirText):
		// Только пробелы/переносы строк (форматтеры) — берём нашу сторону.
		return our, true
	}
	return nil, false
}

// normalizeWhitespace приводит текст к «канонической» форме для сравнения:
// обрезает пробелы в каждой строке и убирает пустые строки (для сравнения
// форматтер-расхождений).
func normalizeWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	var b strings.Builder
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		b.WriteString(t)
		b.WriteByte('\n')
	}
	return b.String()
}

// isConflictLine проверяет, что строка — открывающий (markerOut) или
// закрывающий (markerTheir) маркер конфликта. Метка после маркера (HEAD/main)
// не проверяется: сеператор `=======` матчится точным равенством.
func isConflictLine(line, marker string) bool {
	if marker == conflictMarkerSep {
		return line == conflictMarkerSep
	}
	return strings.HasPrefix(line, marker)
}

// HasConflictMarkers возвращает пути из paths, в которых на диске (dir) ещё
// остались маркеры конфликта. Отсутствующие файлы пропускаются (это не
// marker-конфликт — «deleted by one side» обрабатывается отдельно). Пустой
// paths — тоже пустой результат без чтения диска.
func HasConflictMarkers(dir string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	var out []string
	for _, p := range paths {
		if !safePathRel(p) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			continue
		}
		has := false
		for _, ln := range strings.Split(string(data), "\n") {
			if isConflictLine(ln, conflictMarkerOur) ||
				isConflictLine(ln, conflictMarkerSep) ||
				isConflictLine(ln, conflictMarkerTheir) {
				has = true
				break
			}
		}
		if has {
			out = append(out, p)
		}
	}
	return out, nil
}

// TrivialResolve авто-разрешает тривиальные конфликты в файлах paths каталога
// dir (см. ResolveConflictMarkers). Возвращаются:
//
//	resolved — файлы, где все блоки разрешены (переписаны на диск, маркеров нет);
//	hard     — файлы со сложными/битыми маркерами или отсутствующие на диске
//	           (уходят модели для ручного резолва).
func TrivialResolve(dir string, paths []string) (resolved, hard []string, err error) {
	if len(paths) == 0 {
		return nil, nil, nil
	}
	for _, p := range paths {
		if !safePathRel(p) {
			hard = append(hard, p)
			continue
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		data, rerr := os.ReadFile(full)
		if rerr != nil {
			// Файл-конфликт отсутствует на диске (modify/delete) — маркерами
			// не решается: уходит модели.
			hard = append(hard, p)
			continue
		}
		resolvedContent, ok := ResolveConflictMarkers(string(data))
		if !ok {
			hard = append(hard, p)
			continue
		}
		if resolvedContent == string(data) {
			// Маркеров в файле не было (например, чисто добавлен одной стороной) —
			// резолв не нужен, но файл «закрыт» в индексе при Stage.
			resolved = append(resolved, p)
			continue
		}
		if werr := os.WriteFile(full, []byte(resolvedContent), 0o644); werr != nil {
			return nil, nil, fmt.Errorf("gitops: запись резолва %s: %w", full, werr)
		}
		resolved = append(resolved, p)
	}
	return resolved, hard, nil
}

// safePathRel отклоняет относительные пути, выходящие за пределы каталога
// (../, абсолютные, пустые) — paths приходят из git, но защита дешёвая.
func safePathRel(p string) bool {
	p = filepath.ToSlash(filepath.Clean(p))
	if p == "" || p == "." || p == ".." || filepath.IsAbs(filepath.FromSlash(p)) {
		return false
	}
	if strings.HasPrefix(p, "../") {
		return false
	}
	return true
}

// UnmergedFiles возвращает конфликтующие пути незавершённого merge
// (`git ls-files -u`: каждая строка "<mode> <sha> <stage>\t<path>"). Порядок —
// как в выводе git, дубликаты убираются (один путь на 2-3 stage).
func (r *Repo) UnmergedFiles(ctx context.Context) ([]string, error) {
	if r == nil || r.Root == "" {
		return nil, fmt.Errorf("gitops: пустой Repo")
	}
	out, err := r.ex.Exec(ctx, r.Root, "git", "ls-files", "-u")
	if err != nil {
		return nil, fmt.Errorf("gitops: git ls-files -u: %w", err)
	}
	var files []string
	seen := make(map[string]bool)
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimRight(ln, "\r")
		if ln == "" {
			continue
		}
		idx := strings.LastIndex(ln, "\t")
		if idx < 0 {
			continue
		}
		p := strings.TrimSpace(ln[idx+1:])
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		files = append(files, p)
	}
	return files, nil
}

// Stage помечает конфликтующие пути разрешёнными (`git add -- paths`), чтобы
// git считал их закрытыми после авто-резолва. Пустой список — no-op.
func (r *Repo) Stage(ctx context.Context, paths []string) error {
	if r == nil || r.Root == "" {
		return fmt.Errorf("gitops: пустой Repo")
	}
	if len(paths) == 0 {
		return nil
	}
	argv := make([]string, 0, len(paths)+2)
	argv = append(argv, "git", "add", "--")
	argv = append(argv, paths...)
	if _, err := r.ex.Exec(ctx, r.Root, argv...); err != nil {
		return fmt.Errorf("gitops: git add -- %s: %w", strings.Join(paths, " "), err)
	}
	return nil
}

// ConflictDiff возвращает комбинированный unified-дифф conflictных путей
// (`git diff -- paths`) — готовый материал для модели: обе версии и маркеры.
// Для пустого списка — пустая строка; при пустом выводе (например, файл
// добавлен/удалён одной стороной) инструмент дополняет чтением диска.
func (r *Repo) ConflictDiff(ctx context.Context, paths []string) (string, error) {
	if r == nil || r.Root == "" {
		return "", fmt.Errorf("gitops: пустой Repo")
	}
	if len(paths) == 0 {
		return "", nil
	}
	argv := make([]string, 0, len(paths)+2)
	argv = append(argv, "git", "diff", "--")
	argv = append(argv, paths...)
	out, err := r.ex.Exec(ctx, r.Root, argv...)
	if err != nil {
		return "", fmt.Errorf("gitops: git diff: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// AddWorktree добавляет постоянный worktree для существующей ветки branch
// (без -b: ветка уже создана в общем клоне). Возвращает Repo, работающий
// относительно нового каталога. Используется в Ф-4: конфликтный worktree
// живёт до завершения резолва (в отличие от временного worktree MergeFeature).
func (r *Repo) AddWorktree(ctx context.Context, worktreePath, branch string) (*Repo, error) {
	if r == nil || r.Root == "" {
		return nil, fmt.Errorf("gitops: пустой Repo")
	}
	if strings.TrimSpace(worktreePath) == "" || strings.TrimSpace(branch) == "" {
		return nil, fmt.Errorf("gitops: требуются путь worktree и ветка")
	}
	if _, err := r.ex.Exec(ctx, r.Root, "git", "worktree", "add", worktreePath, branch); err != nil {
		return nil, fmt.Errorf("gitops: создание worktree %s: %w", branch, err)
	}
	return &Repo{Root: worktreePath, Branch: branch, Base: branch, ex: r.ex}, nil
}

// RemoveWorktree снимает worktree (`git worktree remove --force`) и удаляет его
// каталог с диска. Идемпотентно к уже снятому/несуществующему каталогу.
func (r *Repo) RemoveWorktree(ctx context.Context, worktreePath string) error {
	if r == nil || r.Root == "" {
		return fmt.Errorf("gitops: пустой Repo")
	}
	if strings.TrimSpace(worktreePath) == "" {
		return fmt.Errorf("gitops: пустой путь worktree")
	}
	if _, err := r.ex.Exec(ctx, r.Root, "git", "worktree", "remove", "--force", worktreePath); err != nil {
		return fmt.Errorf("gitops: снятие worktree %s: %w", worktreePath, err)
	}
	_ = os.RemoveAll(worktreePath)
	return nil
}
