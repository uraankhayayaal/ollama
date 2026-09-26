// Git-workflow «эпик = релизная ветка, задача = фича-ветка» (Ф-1, основа).
//
// Здесь живут merge-примитивы и детекция конфликтов БЕЗ изменения рабочей
// копии:
//
//   - MergeBranch — вливание ветки в текущую (--no-ff, merge-коммит).
//   - MergeBase/MergeTree/ConflictingFiles — предсказание конфликтов через
//     `git merge-tree` (старый трёхаргументный формат: base/our/their), не
//     трогающее index и рабочую копию. Конфликтные пути извлекаются из
//     diff-вывода по маркерам `<<<<<<< .our` / `=======` / `>>>>>>> .their`.
//   - CreateBranch/BranchExists — заведение веток эпиков/задач без переключения
//     на них (текущая ветка и рабочая копия агента не трогаются).
//
// Старый формат `git merge-tree <base> <ours> <theirs>` работает во всех
// git-версиях (в т.ч. < 2.38, где нет `--write-tree`), поэтому выступает
// единым путём и связывается с перечнем git-команд в hermetic-тестах.
package gitops

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

// MergeResult — исход слияния ветки в текущую.
type MergeResult struct {
	// Message — сообщение merge-коммита.
	Message string
	// FastForward — слияние было fast-forward. Всегда false: MergeBranch
	// использует --no-ff, чтобы фича-ветка оставалась видимой в истории.
	FastForward bool
	// AlreadyMerged — вливаемый источник уже является анцестором целевой
	// ветки (ничего менять не нужно). Заполняется MergeFeature.
	AlreadyMerged bool
}

// MergeBranch вливает ветку branch в текущую (HEAD) через `git merge --no-ff`
// и фиксирует merge-коммит с сообщением message. При конфликте git оставляет
// маркеры в рабочей копии и возвращает ненулевой код — ошибка распространяется.
// Перед вызовом убедитесь, что рабочая копия чиста (Dirty == false).
func (r *Repo) MergeBranch(ctx context.Context, branch, message string) (*MergeResult, error) {
	if r == nil || r.Root == "" {
		return nil, fmt.Errorf("gitops: пустой Repo")
	}
	if strings.TrimSpace(branch) == "" {
		return nil, fmt.Errorf("gitops: пустое имя ветки для слияния")
	}
	msg := strings.TrimSpace(message)
	if msg == "" {
		return nil, fmt.Errorf("gitops: пустое сообщение merge-коммита")
	}
	if _, err := r.ex.Exec(ctx, r.Root, "git", "merge", "--no-ff", "-m", msg, branch); err != nil {
		return nil, fmt.Errorf("gitops: git merge %s: %w", branch, err)
	}
	return &MergeResult{Message: msg}, nil
}

// MergeBase вычисляет общую точку отхода (merge-base) двух tree-ish refs.
func (r *Repo) MergeBase(ctx context.Context, a, b string) (string, error) {
	if r == nil || r.Root == "" {
		return "", fmt.Errorf("gitops: пустой Repo")
	}
	out, err := r.ex.Exec(ctx, r.Root, "git", "merge-base", a, b)
	if err != nil {
		return "", fmt.Errorf("gitops: git merge-base %s %s: %w", a, b, err)
	}
	base := strings.TrimSpace(out)
	if base == "" {
		return "", fmt.Errorf("gitops: merge-base %s %s пустой", a, b)
	}
	return base, nil
}

// MergeTree detэктует конфликты трёхстороннего merge (base ← ours+theirs)
// через `git merge-tree` и возвращает список конфликтующих путей. Рабочая
// копия и index НЕ изменяются. Возвращает nil, если слияние чистое.
func (r *Repo) MergeTree(ctx context.Context, base, ours, theirs string) ([]string, error) {
	if r == nil || r.Root == "" {
		return nil, fmt.Errorf("gitops: пустой Repo")
	}
	for _, ref := range []struct{ name, val string }{{"base", base}, {"ours", ours}, {"theirs", theirs}} {
		if strings.TrimSpace(ref.val) == "" {
			return nil, fmt.Errorf("gitops: пустой аргумент %s для merge-tree", ref.name)
		}
	}
	out, err := r.ex.Exec(ctx, r.Root, "git", "merge-tree", base, ours, theirs)
	if err != nil {
		return nil, fmt.Errorf("gitops: git merge-tree %s %s %s: %w", base, ours, theirs, err)
	}
	return mergeTreeConflicts(out), nil
}

// ConflictingFiles возвращает пути, которые конфликтуют при вливании ветки
// other в текущую (HEAD): merge-base(HEAD, other) + git merge-tree. Рабочая
// копия не изменяется; пустой список означает чистое слияние.
func (r *Repo) ConflictingFiles(ctx context.Context, other string) ([]string, error) {
	if r == nil || r.Root == "" {
		return nil, fmt.Errorf("gitops: пустой Repo")
	}
	base, err := r.MergeBase(ctx, "HEAD", other)
	if err != nil {
		return nil, err
	}
	return r.MergeTree(ctx, base, "HEAD", other)
}

// CreateBranch создаёт ветку branch от точки base БЕЗ переключения на неё:
// текущая ветка, index и рабочая копия не трогаются. Используется для
// заведения веток эпиков (от main) и задач (от ветки эпика) по статусным
// событиям доски. Если ветка уже существует — возвращается ошибка (проверяйте
// BranchExists заранее для идемпотентности).
func (r *Repo) CreateBranch(ctx context.Context, branch, base string) error {
	if r == nil || r.Root == "" {
		return fmt.Errorf("gitops: пустой Repo")
	}
	if strings.TrimSpace(branch) == "" || strings.TrimSpace(base) == "" {
		return fmt.Errorf("gitops: требуется ветка и точка отхода")
	}
	if _, err := r.ex.Exec(ctx, r.Root, "git", "branch", branch, base); err != nil {
		return fmt.Errorf("gitops: создание ветки %s от %s: %w", branch, base, err)
	}
	return nil
}

// BranchExists сообщает, существует ли локальная ветка branch в клоне.
func (r *Repo) BranchExists(ctx context.Context, branch string) (bool, error) {
	if r == nil || r.Root == "" {
		return false, fmt.Errorf("gitops: пустой Repo")
	}
	name := strings.TrimPrefix(branch, "refs/heads/")
	if name == "" {
		return false, fmt.Errorf("gitops: пустое имя ветки")
	}
	// --quiet: отсутствующая ветка — ненулевой код без вывода; Executor
	// возвращает ошибку, которую трактуем как «ветки нет».
	if _, err := r.ex.Exec(ctx, r.Root, "git", "rev-parse", "--verify", "--quiet", "refs/heads/"+name); err != nil {
		return false, nil
	}
	return true, nil
}

// Rebase перемещает ветку branch на вершину ветки onto (git rebase onto).
// Возвращает ошибку при конфликте (рабочая копия и ветка не трогаются).
func (r *Repo) Rebase(ctx context.Context, branch, onto string) error {
	if r == nil || r.Root == "" {
		return fmt.Errorf("gitops: пустой Repo")
	}
	if strings.TrimSpace(branch) == "" || strings.TrimSpace(onto) == "" {
		return fmt.Errorf("gitops: требуются ветка и точка отхода")
	}
	if _, err := r.ex.Exec(ctx, r.Root, "git", "rebase", onto, branch); err != nil {
		return fmt.Errorf("gitops: git rebase %s %s: %w", onto, branch, err)
	}
	return nil
}

// SanitizeBranchName приводит произвольный ID (эпика/задачи из LLM-схем) к
// допустимому имени git-ветки: буквы/цифры (в т.ч. юникод) сохраняются,
// остальное (пробелы, ~^:?*[\\ и пр.) заменяется на '_'; добиваются точки,
// запрещённые подстроки. Санитизация консервативна: даже странные ID не
// сломают `git branch`.
func SanitizeBranchName(id string) string {
	s := strings.TrimSpace(id)
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	out = strings.Trim(out, "_")
	// git check-ref-format запрещает подстроку ".." — заменяем на '_'.
	for strings.Contains(out, "..") {
		out = strings.ReplaceAll(out, "..", "_")
	}
	// Запрещённые слова git check-ref-format.
	switch out {
	case "", ".":
		return "id"
	}
	return out
}

// mergeTreeConflicts разбирает вывод старого `git merge-tree <base> <ours>
// <theirs>` и возвращает конфликтующие пути. Формат — блок на каждый
// затронутый файл (заголовок вроде «changed in both», «added in local»,
// «deleted in remote», строки "base/our/their <mode> <sha> <path>") с
// последующими diff-хунками. Маркеры конфликта появляются только в хунках
// конфликтующих файлов:
//
//	changed in both
//	  base   100644 <sha> f.txt
//	  our    100644 <sha> f.txt
//	  their  100644 <sha> f.txt
//	@@ -1,2 +1,6 @@
//	+<<<<<<< .our
//	 line1-b1
//	+=======
//	+line1-b2
//	+>>>>>>> .their
//	 line2
//
// Для БИНАРНЫХ файлов git хунков не печатает вовсе — только предупреждение
// ПЕРЕД блоком файла (плюс сам блок с base/our/their):
//
//	warning: Cannot merge binary files: logo.png (.our vs. .their)
//	changed in both
//	  base   100644 <sha> logo.png
//	  our    100644 <sha> logo.png
//	  their  100644 <sha> logo.png
//
// Такие файлы — тоже конфликт, но без маркеров: раньше они молча терялись,
// релиз падал в общий 502 вместо 409 со списком файлов, а авто-синхрон/epic
// считал бинарник «чистым» и молча брал одну из сторон.
func mergeTreeConflicts(out string) []string {
	var conflicts []string
	seen := make(map[string]bool)
	add := func(path string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		conflicts = append(conflicts, path)
	}

	// Проход 1: бинарные конфликты по warning-строкам.
	for _, line := range strings.Split(out, "\n") {
		if path, ok := binaryConflictPath(line); ok {
			add(path)
		}
	}

	// Проход 2: текстовые конфликты по маркерам в хунках.
	cur := ""
	for _, line := range strings.Split(out, "\n") {
		if path, ok := mergeTreeFilePath(line); ok {
			cur = path
			continue
		}
		if cur == "" || seen[cur] {
			continue
		}
		if isConflictMarker(line) {
			add(cur)
		}
	}
	return conflicts
}

// binaryConflictPath разбирает warning git о бинарном конфликте:
// «warning: Cannot merge binary files: <path> (<ours> vs. <theirs>)».
// Путь может содержать пробелы и « (», поэтому суффикс « (… vs. …)» режется
// с конца строки. Возвращает (путь, ok).
func binaryConflictPath(line string) (string, bool) {
	const prefix = "warning: Cannot merge binary files: "
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, prefix) {
		return "", false
	}
	rest := strings.TrimSpace(s[len(prefix):])
	i := strings.LastIndex(rest, " (")
	if i <= 0 || !strings.HasSuffix(rest, ")") {
		return "", false
	}
	path := strings.TrimSpace(rest[:i])
	if path == "" {
		return "", false
	}
	return path, true
}

// mergeTreeFilePath извлекает путь файла из строки блока merge-tree вида
// "  base   100644 <sha> <path>" / "  our  ..." / "  their ...". Возвращает
// (путь, ok). Хэш ограничен фиксированной длиной (40/64 hex), поэтому путь с
// пробелами сохраняется (берём остаток строки после SHA).
func mergeTreeFilePath(line string) (string, bool) {
	if len(line) < 2 || line[0] != ' ' || line[1] != ' ' {
		return "", false
	}
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return "", false
	}
	if fields[0] != "base" && fields[0] != "our" && fields[0] != "their" {
		return "", false
	}
	mode := fields[1]
	if mode == "" || mode[0] < '0' || mode[0] > '7' {
		return "", false
	}
	sha := fields[2]
	if !isHexHash(sha) {
		return "", false
	}
	idx := strings.Index(line, sha)
	if idx < 0 {
		return "", false
	}
	path := strings.TrimSpace(line[idx+len(sha):])
	if path == "" {
		return "", false
	}
	return path, true
}

// isHexHash проверяет, что строка — полный git-хэш (40 или 64 hex-цифры).
func isHexHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') && !(r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// isConflictMarker проверяет, что строка — маркер конфликта в diff-выводе
// merge-tree (может быть префиксован '+' как добавленная строка).
func isConflictMarker(line string) bool {
	s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "+"))
	switch s {
	case "<<<<<<< .our", "=======", ">>>>>>> .their":
		return true
	}
	return false
}
