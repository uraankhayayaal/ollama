// Политика индексации RAG по содержимому ветки Git (срез F3k Этапа 6).
//
// Ветка читается из git-объектов (`git ls-tree` + `git show`), а не из рабочей
// копии: checkout проекта может стоять на ветке агента и отставать от main.
// Здесь — детерминированные функции сбора снапшотов ветки/изменений и проверки
// актуальности индекса; оркестрация (фоновые горутины, single-flight, отчёты в
// чат) остаётся у вызывающего адаптера (`server`).
package rag

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"ai/domain"
	"ai/domain/services"
	"ai/logging"
)

// MaxRefFileBytes — предел размера файла, который читается из ветки (крупные
// вендоренные блоки в индекс не идут).
const MaxRefFileBytes = 2_000_000

// RefSnapshot — содержимое ветки/ref, готовое к индексации.
type RefSnapshot struct {
	// Branch — имя ветки, под которым точки попадут в индекс (payload branch).
	Branch string
	// CommitSHA — коммит, снятый снапшот (версия индексации); пусто — без git.
	CommitSHA string
	// Items — файлы ветки для полной индексации.
	Items []domain.IndexItem
}

// Indexer — узкий порт векторной памяти для индексации (реализует
// infrastructure/rag.Client; выделен для hermetic-тестов без сети).
type Indexer interface {
	EnsureCollection(ctx context.Context) (int, error)
	IndexProject(ctx context.Context, projectName string, files []domain.IndexItem, opts domain.IndexOptions) (*domain.IndexResult, error)
	// BranchIndexed — есть ли активные точки ветки на этом коммите: хуки
	// поддержания индекса не должны переиндексировать ветку без изменений.
	BranchIndexed(ctx context.Context, projectName, branch, commit string) (bool, error)
}

// BranchIndexUpToDate — активные точки ветки на этом коммите уже есть:
// переиндексация не нужна (проверка дешевле прогона и индекс не трогает).
func BranchIndexUpToDate(ctx context.Context, indexer Indexer, project, branch, commit string) (bool, error) {
	if strings.TrimSpace(commit) == "" {
		return false, nil
	}
	done, err := indexer.BranchIndexed(ctx, project, branch, commit)
	if err != nil {
		return false, err
	}
	if done {
		logging.For(project).Infof("rag: индекс ветки %s (%s) актуален — повторная индексация не нужна",
			branch, commit[:min(len(commit), 8)])
	}
	return done, nil
}

// LoadRefItems читает файлы ветки прямо из git: `git ls-tree -r -z --long`
// даёт список blob-ов с размерами, содержимое — `git show ref:<путь>`. Рабочая
// копия не читается и не меняется, поэтому индекс ветки всегда соответствует
// её содержимому, даже если checkout проекта отстал от main.
func LoadRefItems(ctx context.Context, ex services.Executor, dir, ref string) (RefSnapshot, error) {
	if ex == nil {
		return RefSnapshot{}, fmt.Errorf("не задан git-исполнитель")
	}
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(ref) == "" {
		return RefSnapshot{}, fmt.Errorf("не заданы каталог и ветка для индексации")
	}
	snap := RefSnapshot{Branch: ref}

	out, err := ex.Exec(ctx, dir, "git", "ls-tree", "-r", "-z", "--long", ref)
	if err != nil {
		return RefSnapshot{}, fmt.Errorf("git ls-tree %s: %w", ref, err)
	}
	for _, rec := range strings.Split(out, "\x00") {
		if strings.TrimSpace(rec) == "" {
			continue
		}
		path, size, ok := ParseLsTreeEntry(rec)
		if !ok || !IndexablePath(path) {
			continue
		}
		// Размер известен из ls-tree: пустые и крупные файлы не читаем вовсе.
		if size <= 0 || size > MaxRefFileBytes {
			continue
		}
		content, cerr := ex.Exec(ctx, dir, "git", "show", ref+":"+path)
		if cerr != nil {
			logging.Warnf("rag: git show %s:%s: %v", ref, path, cerr)
			continue
		}
		if !IndexableContent(content) {
			continue
		}
		snap.Items = append(snap.Items, domain.IndexItem{
			Path:    path,
			Scope:   domain.ScopeForPath(path),
			Content: content,
		})
	}

	// Коммит ветки — версия индексации. Без него точки всё равно заменяют друг
	// друга (проект без git), но с ним повторный прогон идемпотентен.
	if sha, serr := ex.Exec(ctx, dir, "git", "rev-parse", ref); serr == nil {
		snap.CommitSHA = strings.TrimSpace(sha)
	}
	return snap, nil
}

// ParseLsTreeEntry разбирает запись `git ls-tree --long`: метаданные до
// табуляции (mode type sha size) и путь после. Возвращает путь, размер и
// признак, что запись разобрана и это обычный blob (не симлинк и не поддерево).
func ParseLsTreeEntry(rec string) (path string, size int64, ok bool) {
	meta, path, found := strings.Cut(rec, "\t")
	if !found {
		return "", 0, false
	}
	fields := strings.Fields(meta)
	if len(fields) < 4 || fields[1] != "blob" || fields[0] == "120000" {
		return "", 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(fields[3]), 10, 64)
	if err != nil {
		return "", 0, false
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", 0, false
	}
	return path, n, true
}

// IndexablePath повторяет политику отбора ProjectWalker для пути из ветки:
// служебные каталоги (.git, node_modules, build, ...) и скрытые файлы/каталоги
// (.env, .github) не индексируются.
func IndexablePath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" || strings.HasPrefix(path, "/") {
		return false
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "" {
			return false
		}
		if strings.HasPrefix(seg, ".") || domain.IsIgnoredDir(seg) {
			return false
		}
	}
	return true
}

// IndexableContent отбрасывает пустые, крупные и бинарные (NUL в префиксе)
// файлы — те же правила, что у обхода рабочей копии.
func IndexableContent(content string) bool {
	if content == "" || len(content) > MaxRefFileBytes {
		return false
	}
	probe := content
	if len(probe) > 1024 {
		probe = probe[:1024]
	}
	return !bytes.ContainsRune([]byte(probe), 0)
}

// LoadRefDiff собирает файлы `git diff --name-only base...ref` и читает их
// содержимое из ref: содержимое рабочей копии не используется (она может
// отставать), коммит берётся по ref. Основной сценарий — старт задачи: «свои»
// файлы ветки индексируются под веткой задачи, остальное покрывает индекс main.
func LoadRefDiff(ctx context.Context, ex services.Executor, dir, ref, base string) (RefSnapshot, error) {
	if ex == nil {
		return RefSnapshot{}, fmt.Errorf("не задан git-исполнитель")
	}
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(ref) == "" || strings.TrimSpace(base) == "" {
		return RefSnapshot{}, fmt.Errorf("не заданы каталог и ветки для индексации изменений")
	}
	out, err := ex.Exec(ctx, dir, "git", "diff", "--name-only", "-z", base+"..."+ref)
	if err != nil {
		return RefSnapshot{}, fmt.Errorf("git diff %s...%s: %w", base, ref, err)
	}
	files := make([]string, 0, 8)
	for _, p := range strings.Split(out, "\x00") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		size, ok := RefFileSize(ctx, ex, dir, ref, p)
		if !ok || size > MaxRefFileBytes {
			continue
		}
		files = append(files, p)
	}
	snap := RefSnapshot{Branch: ref, Items: []domain.IndexItem{}}
	if sha, serr := ex.Exec(ctx, dir, "git", "rev-parse", ref); serr == nil {
		snap.CommitSHA = strings.TrimSpace(sha)
	}
	for _, p := range files {
		content, cerr := ex.Exec(ctx, dir, "git", "show", ref+":"+p)
		if cerr != nil {
			logging.Warnf("rag: git show %s:%s: %v", ref, p, cerr)
			continue
		}
		if !IndexableContent(content) {
			continue
		}
		snap.Items = append(snap.Items, domain.IndexItem{
			Path:    p,
			Scope:   domain.ScopeForPath(p),
			Content: content,
		})
	}
	return snap, nil
}

// RefFileSize — размер blob-а файла в ветке (`git cat-file -s ref:path`):
// нужен, чтобы не читать крупные файлы и не заводить одинаковые точки для
// удалённых/переименованных путей. ok=false — пути в ветке нет (удалён) либо
// путь не индексируется.
func RefFileSize(ctx context.Context, ex services.Executor, dir, ref, path string) (int64, bool) {
	if !IndexablePath(path) {
		return 0, false
	}
	out, err := ex.Exec(ctx, dir, "git", "cat-file", "-s", ref+":"+path)
	if err != nil {
		return 0, false
	}
	n, perr := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if perr != nil {
		return 0, false
	}
	return n, true
}
