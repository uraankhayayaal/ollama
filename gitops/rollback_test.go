package gitops

// Тесты ручного отката задачи (Ф-6, этапы 3 и 5,
// PLAN-2026-10-05-todo-kanban-rollback.md). Прогоняют настоящий git CLI: гард
// ветки задачи, reset/clean, несуществующий SHA и — главное — рекурсивный
// возврат сабмодуля к gitlink'у целевого коммита, который `git reset --hard`
// на верхнем уровне не трогает.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// rollbackFixture: bare-origin родителя с сабмодулем → клон → ветка задачи с
// двумя раундами правок (в родителе и в сабмодуле) и незакоммиченным мусором.
type rollbackFixture struct {
	root    string // worktree задачи
	subRoot string // worktree задачи в сабмодуле
	shaBase string
	sha1    string
	sha2    string
	subSHA1 string
	subSHA2 string
}

func rbRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v в %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

func rbWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRollbackFixture(t *testing.T) *rollbackFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	ctx := context.Background()
	ex := CLIExecutor{}
	tmp := t.TempDir()

	// --- сабмодуль: bare-origin с двумя коммитами ---
	subRemote := filepath.Join(tmp, "sub-origin.git")
	rbRun(t, tmp, "init", "--bare", "-b", "main", subRemote)
	subSrc := filepath.Join(tmp, "sub-src")
	rbRun(t, tmp, "clone", subRemote, subSrc)
	rbRun(t, subSrc, "config", "user.email", "t@example.com")
	rbRun(t, subSrc, "config", "user.name", "Test")
	rbWrite(t, filepath.Join(subSrc, "sub.go"), "package sub\n")
	rbRun(t, subSrc, "add", "-A")
	rbRun(t, subSrc, "commit", "-m", "sub 1")
	rbRun(t, subSrc, "push", "-u", "origin", "main")

	// --- родитель: bare-origin с сабмодулем, один коммит ---
	parentRemote := filepath.Join(tmp, "parent-origin.git")
	rbRun(t, tmp, "init", "--bare", "-b", "main", parentRemote)
	parentSrc := filepath.Join(tmp, "parent-src")
	rbRun(t, tmp, "clone", parentRemote, parentSrc)
	rbRun(t, parentSrc, "config", "user.email", "t@example.com")
	rbRun(t, parentSrc, "config", "user.name", "Test")
	rbWrite(t, filepath.Join(parentSrc, "a.go"), "package app\n")
	if _, err := ex.Exec(ctx, parentSrc, "git", "-c", "protocol.file.allow=always", "submodule", "add", "-b", "main", subRemote, "sub"); err != nil {
		t.Fatalf("submodule add: %v", err)
	}
	rbRun(t, parentSrc, "add", "-A")
	rbRun(t, parentSrc, "commit", "-m", "parent 1")
	rbRun(t, parentSrc, "push", "-u", "origin", "main")

	// --- клон проекта и ветка задачи ---
	root := filepath.Join(tmp, "clone")
	rbRun(t, tmp, "clone", parentRemote, root)
	rbRun(t, root, "config", "user.email", "t@example.com")
	rbRun(t, root, "config", "user.name", "Test")
	rbRun(t, root, "checkout", "-b", TaskBranchPrefix+"t1")
	// Сабмодуль в клоне не инициализирован: без update --init каталог sub
	// пуст, и любой git в нём находит репозиторий РОДИТЕЛЯ (поиск вверх по
	// дереву) — откат проверял бы не то.
	rbRun(t, root, "-c", "protocol.file.allow=always", "submodule", "update", "--init")
	subRoot := filepath.Join(root, "sub")

	f := &rollbackFixture{root: root, subRoot: subRoot, shaBase: rbRev(t, root, "HEAD")}
	// sub-репозиторий тоже стоит на ветке задачи (её создаёт сервер).
	rbRun(t, subRoot, "checkout", "-b", TaskBranchPrefix+"t1/sub")

	// Раунд 1: правки в сабмодуле и в родителе. .gitignore (его заводит
	// bootstrap worktree задачи, этап 1.6) коммитится вместе с ними — иначе
	// node_modules не был бы игнорируемым и clean -fdq снёс бы его.
	rbWrite(t, filepath.Join(root, ".gitignore"), "node_modules/\n")
	rbWrite(t, filepath.Join(subRoot, "sub.go"), "package sub\n\n// раунд 1\n")
	rbRun(t, subRoot, "add", "-A")
	rbRun(t, subRoot, "commit", "-m", "sub round 1")
	rbWrite(t, filepath.Join(root, "a.go"), "package app\n\n// раунд 1\n")
	rbRun(t, root, "add", "-A")
	rbRun(t, root, "commit", "-m", "round 1")
	f.sha1, f.subSHA1 = rbRev(t, root, "HEAD"), rbRev(t, subRoot, "HEAD")

	// Раунд 2 — тот, к которому откатываемся.
	rbWrite(t, filepath.Join(subRoot, "sub.go"), "package sub\n\n// раунд 2\n")
	rbRun(t, subRoot, "add", "-A")
	rbRun(t, subRoot, "commit", "-m", "sub round 2")
	rbWrite(t, filepath.Join(root, "a.go"), "package app\n\n// раунд 2\n")
	rbRun(t, root, "add", "-A")
	rbRun(t, root, "commit", "-m", "round 2")
	f.sha2, f.subSHA2 = rbRev(t, root, "HEAD"), rbRev(t, subRoot, "HEAD")

	// Незакоммиченный мусор: изменённый отслеживаемый файл, новый
	// неотслеживаемый и игнорируемый (его сносить нельзя).
	rbWrite(t, filepath.Join(root, "a.go"), "package app\n\n// мусор\n")
	rbWrite(t, filepath.Join(root, "junk.txt"), "мусор\n")
	rbWrite(t, filepath.Join(root, "node_modules", "dep.js"), "vendor\n")
	return f
}

func rbRev(t *testing.T, dir, ref string) string {
	t.Helper()
	return strings.TrimSpace(rbRun(t, dir, "rev-parse", ref))
}

func rbCat(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestRollbackWorktreeRestoresCommittedState — откат к sha1 возвращает и
// содержимое файла, и HEAD, и вычищает незакоммиченный мусор; игнорируемое
// (node_modules) остаётся на месте.
func TestRollbackWorktreeRestoresCommittedState(t *testing.T) {
	f := newRollbackFixture(t)
	ctx := context.Background()
	res, err := RollbackWorktree(ctx, CLIExecutor{}, f.root, f.sha1)
	if err != nil {
		t.Fatalf("RollbackWorktree: %v", err)
	}
	if res.SHA != f.sha1 {
		t.Fatalf("sha=%q, ожидалось %q", res.SHA, f.sha1)
	}
	if got := rbRev(t, f.root, "HEAD"); got != f.sha1 {
		t.Fatalf("HEAD=%s, ожидалось %s", got, f.sha1)
	}
	if body := rbCat(t, filepath.Join(f.root, "a.go")); body != "package app\n\n// раунд 1\n" {
		t.Fatalf("a.go: %q", body)
	}
	if _, err := os.Stat(filepath.Join(f.root, "junk.txt")); !os.IsNotExist(err) {
		t.Fatalf("неотслеживаемый junk.txt не убран: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "node_modules", "dep.js")); err != nil {
		t.Fatalf("игнорируемый node_modules снесён: %v", err)
	}
	if out := rbRun(t, f.root, "status", "--porcelain"); out != "" {
		t.Fatalf("дерево не чистое: %q", out)
	}
}

// TestRollbackWorktreeRevertsSubmoduleRecursively — ядро этапа 5.1: gitlink в
// индексе родителя при откате остаётся новым (round 2), поэтому без рекурсии
// сабмодуль продолжил бы работу на отменённом коде. После отката сабмодуль
// стоит на своём коммите раунда 1, и gitlink в индексе родителя на него же.
func TestRollbackWorktreeRevertsSubmoduleRecursively(t *testing.T) {
	f := newRollbackFixture(t)
	ctx := context.Background()
	res, err := RollbackWorktree(ctx, CLIExecutor{}, f.root, f.sha1)
	if err != nil {
		t.Fatalf("RollbackWorktree: %v", err)
	}
	if len(res.Submodules) != 1 || res.Submodules[0] != "sub" || !res.GitlinksStaged {
		t.Fatalf("результат отката: %+v", res)
	}
	if got := rbRev(t, f.subRoot, "HEAD"); got != f.subSHA1 {
		t.Fatalf("HEAD сабмодуля=%s, ожидался коммит раунда 1 %s", got, f.subSHA1)
	}
	if body := rbCat(t, filepath.Join(f.subRoot, "sub.go")); body != "package sub\n\n// раунд 1\n" {
		t.Fatalf("sub.go: %q", body)
	}
	// Индекс родителя смотрит на откатанный коммит сабмодуля: иначе
	// последующий коммит раунда поднял бы отменённый код обратно.
	if got := rbRev(t, f.root, ":sub"); got != f.subSHA1 {
		t.Fatalf("gitlink в индексе=%s, ожидался %s", got, f.subSHA1)
	}
}

// TestRollbackWorktreeIgnoresMissingSubmoduleSHA — если целевой коммит
// родителя не содержит сабмодуля, откат не падает: gitlink'а нет — откатывать
// нечего.
func TestRollbackWorktreeIgnoresMissingSubmoduleSHA(t *testing.T) {
	f := newRollbackFixture(t)
	ctx := context.Background()
	if _, err := RollbackWorktree(ctx, CLIExecutor{}, f.root, f.shaBase); err != nil {
		t.Fatalf("откат к базе (сабмодуля нет): %v", err)
	}
	if got := rbRev(t, f.root, "HEAD"); got != f.shaBase {
		t.Fatalf("HEAD=%s, ожидалась база %s", got, f.shaBase)
	}
}

// TestRollbackWorktreeIsIdempotent — повторный откат к той же точке ничего не
// ломает (кнопка «откатить» может быть нажата дважды).
func TestRollbackWorktreeIsIdempotent(t *testing.T) {
	f := newRollbackFixture(t)
	ctx := context.Background()
	if _, err := RollbackWorktree(ctx, CLIExecutor{}, f.root, f.sha1); err != nil {
		t.Fatalf("первый откат: %v", err)
	}
	if _, err := RollbackWorktree(ctx, CLIExecutor{}, f.root, f.sha1); err != nil {
		t.Fatalf("повторный откат: %v", err)
	}
	if got := rbRev(t, f.root, "HEAD"); got != f.sha1 {
		t.Fatalf("HEAD=%s, ожидалось %s", got, f.sha1)
	}
	if got := rbRev(t, f.subRoot, "HEAD"); got != f.subSHA1 {
		t.Fatalf("HEAD сабмодуля=%s, ожидался %s", got, f.subSHA1)
	}
}

// TestRollbackWorktreeRejectsUnknownSHA — опечатка в цели не должна откатить
// дерево «куда-нибудь» (git reset на несуществующем ref ушёл бы в никуда).
func TestRollbackWorktreeRejectsUnknownSHA(t *testing.T) {
	f := newRollbackFixture(t)
	ctx := context.Background()
	_, err := RollbackWorktree(ctx, CLIExecutor{}, f.root, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatal("откат к несуществующему коммиту должен отказать")
	}
	// Ошибка типизирована: сервер отличает опечатку в SHA (400) от сбоя git (502).
	if !errors.Is(err, ErrNoSuchCommit) {
		t.Fatalf("ошибка не типизирована ErrNoSuchCommit: %v", err)
	}
	if got := rbRev(t, f.root, "HEAD"); got != f.sha2 {
		t.Fatalf("HEAD=%s, отказ изменил дерево: ожидался %s", got, f.sha2)
	}
}

// TestRollbackWorktreeRejectsSharedClone — гард области: общий клон проекта
// (ветка ai/<проект>) откатывать нельзя, его делят все шаги плана.
func TestRollbackWorktreeRejectsSharedClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	dir := t.TempDir()
	rbRun(t, dir, "init", "-b", "ai/app")
	rbRun(t, dir, "config", "user.email", "t@example.com")
	rbRun(t, dir, "config", "user.name", "Test")
	rbWrite(t, filepath.Join(dir, "a.go"), "package app\n")
	rbRun(t, dir, "add", "-A")
	rbRun(t, dir, "commit", "-m", "init")
	before := rbRev(t, dir, "HEAD")

	rbWrite(t, filepath.Join(dir, "a.go"), "package app\n\n// чужая работа\n")
	if _, err := RollbackWorktree(context.Background(), CLIExecutor{}, dir, before); err == nil {
		t.Fatal("откат в общем клоне должен отказать")
	}
	if body := rbCat(t, filepath.Join(dir, "a.go")); body != "package app\n\n// чужая работа\n" {
		t.Fatalf("работа в общем клоне потеряна: %q", body)
	}
}

// TestRollbackWorktreeRejectsEmptyTarget — без цели отказ (иначе «откатить к
// пустоте» = снести ветку).
func TestRollbackWorktreeRejectsEmptyTarget(t *testing.T) {
	f := newRollbackFixture(t)
	if _, err := RollbackWorktree(context.Background(), CLIExecutor{}, f.root, "  "); err == nil {
		t.Fatal("пустая цель отката должна отказать")
	}
	if got := rbRev(t, f.root, "HEAD"); got != f.sha2 {
		t.Fatalf("HEAD=%s, ожидался %s", got, f.sha2)
	}
}
