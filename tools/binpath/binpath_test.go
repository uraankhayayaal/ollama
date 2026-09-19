// Тесты binpath: поиск бинарников в избыточных каталогах (LSP_BIN_PATH,
// GOBIN/GOPATH, ~/go/bin, Homebrew), расширение PATH для дочерних процессов,
// обработка имён-путей и неисполняемых файлов. Кэш каталогов сбрасывается
// через Reset после изменения переменных окружения.

package binpath

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeBin создаёт исполняемый файл name в директории dir и возвращает
// его абсолютный путь.
func writeFakeBin(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestLookInExtraPath(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeBin(t, dir, "fake-lsp")
	t.Setenv(ExtraPathEnv, dir)
	Reset()
	t.Cleanup(Reset)

	if got, ok := Look("fake-lsp"); !ok || got != bin {
		t.Fatalf("Look через LSP_BIN_PATH: got=%q ok=%v want=%q", got, ok, bin)
	}
	if !Available("fake-lsp") {
		t.Fatalf("Available(fake-lsp) = false, want true")
	}
	if _, ok := Look("no-such-bin-xyz"); ok {
		t.Fatalf("Look несуществующего бинарника должен вернуть ok=false")
	}
}

func TestLookInPathFirst(t *testing.T) {
	// Бинарник из PATH процесса имеет приоритет над каталогами LSP_BIN_PATH.
	dir := t.TempDir()
	bin := writeFakeBin(t, dir, "fake-lsp2")
	t.Setenv(ExtraPathEnv, dir)
	t.Setenv("PATH", os.Getenv("PATH")) // PATH не трогаем — бинарника там нет
	Reset()
	t.Cleanup(Reset)

	if got, ok := Look("fake-lsp2"); !ok || got != bin {
		t.Fatalf("Look должен найти бинарник из extra-каталогов: got=%q ok=%v", got, ok)
	}

	// Но если бинарник есть и в PATH, и в extra — берём из PATH.
	pathDir := t.TempDir()
	pathBin := writeFakeBin(t, pathDir, "fake-lsp2")
	t.Setenv("PATH", pathDir)
	Reset()
	if got, ok := Look("fake-lsp2"); !ok || got != pathBin {
		t.Fatalf("Приоритет PATH: got=%q ok=%v want=%q", got, ok, pathBin)
	}
}

func TestLookAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeBin(t, dir, "tool")
	got, ok := Look(bin)
	if !ok || got != bin {
		t.Fatalf("Look по абсолютному пути: got=%q ok=%v want=%q", got, ok, bin)
	}
}

func TestLookSkipsNonExecutable(t *testing.T) {
	dir := t.TempDir()
	// Обычный файл без бита исполнения.
	if err := os.WriteFile(filepath.Join(dir, "plain"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv(ExtraPathEnv, dir)
	Reset()
	t.Cleanup(Reset)

	if _, ok := Look("plain"); ok {
		t.Fatalf("неисполняемый файл не должен быть найден как бинарник")
	}
}

func TestDirsDedupAndExisting(t *testing.T) {
	t.Setenv(ExtraPathEnv, ".")
	Reset()
	t.Cleanup(Reset)

	dirs := Dirs()
	if len(dirs) == 0 {
		t.Fatalf("Dirs() не должен быть пустым (текущая директория существует)")
	}
	for _, d := range dirs {
		if d == "" {
			t.Fatalf("Dirs() вернул пустую строку")
		}
		if !strings.HasPrefix(d, "/") {
			t.Fatalf("Dirs() должен содержать абсолютные пути, got %q", d)
		}
		st, err := os.Stat(d)
		if err != nil || !st.IsDir() {
			t.Fatalf("Dirs() вернул несуществующий каталог %q", d)
		}
	}
	// Дубликаты исключены.
	seen := make(map[string]bool)
	for _, d := range dirs {
		if seen[d] {
			t.Fatalf("дубликат каталога в Dirs(): %q", d)
		}
		seen[d] = true
	}
}

func TestPathEnvContainsDirs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ExtraPathEnv, dir)
	Reset()
	t.Cleanup(Reset)

	pe := PathEnv()
	if !strings.Contains(pe, dir) {
		t.Fatalf("PathEnv() должен содержать %q, got %q", dir, pe)
	}
	// Ни один каталог исходного PATH не должен пропасть (порядок не ломается).
	peParts := make(map[string]bool, 0)
	for _, p := range filepath.SplitList(pe) {
		peParts[p] = true
	}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p != "" && !peParts[p] {
			t.Fatalf("PathEnv() потерял каталог PATH %q: %s", p, pe)
		}
	}
}

func TestEnvExtendsPathAndDedupsKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ExtraPathEnv, dir)
	Reset()
	t.Cleanup(Reset)

	base := []string{"FOO=1", "PATH=/usr/bin", "FOO=2", "BAR=x"}
	env := Env(base)

	got := make(map[string]string)
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("некорректная запись окружения %q", kv)
		}
		got[k] = v
	}
	if got["FOO"] != "2" {
		t.Fatalf("повтор ключа должен дать последнее значение, got FOO=%q", got["FOO"])
	}
	if !strings.Contains(got["PATH"], dir) {
		t.Fatalf("PATH должен быть расширен каталогом %q, got %q", dir, got["PATH"])
	}
	// Каждый ключ ровно один раз.
	counts := make(map[string]int)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		counts[k]++
	}
	for k, n := range counts {
		if n != 1 {
			t.Fatalf("ключ %q встречается %d раз(а)", k, n)
		}
	}
}

func TestExtraDirsGoAndBrew(t *testing.T) {
	home := t.TempDir()
	mybin := filepath.Join(home, "mybin")
	if err := os.MkdirAll(mybin, 0o755); err != nil {
		t.Fatalf("mkdir mybin: %v", err)
	}
	gopath := filepath.Join(home, "gopath")
	if err := os.MkdirAll(filepath.Join(gopath, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir gopath/bin: %v", err)
	}
	t.Setenv("GOBIN", mybin)
	t.Setenv("GOPATH", gopath)
	Reset()
	t.Cleanup(Reset)

	all := strings.Join(Dirs(), "\n")
	if !strings.Contains(all, mybin) {
		t.Fatalf("GOBIN не попал в Dirs(): %s", all)
	}
	if !strings.Contains(all, filepath.Join(gopath, "bin")) {
		t.Fatalf("GOPATH/bin не попал в Dirs(): %s", all)
	}
	if runtime.GOOS == "darwin" && !strings.Contains(all, "/opt/homebrew/bin") {
		t.Fatalf("Homebrew-каталог не попал в Dirs(): %s", all)
	}
}
