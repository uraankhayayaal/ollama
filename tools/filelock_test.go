package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestConcurrentWriteNoOverwriteSingleWinner — классическая TOCTOU-гонка:
// N горутин одновременно пишут один и тот же НОВЫЙ файл с NoOverwrite=true.
// Без пер-проектной блокировки все могут пройти check-then-write и успешно
// записать; с блокировкой ровно одна запись проходит, остальные получают
// «файл уже существует».
func TestConcurrentWriteNoOverwriteSingleWinner(t *testing.T) {
	dir := t.TempDir()
	const workers = 8

	ops := []*FileOps{}
	for i := 0; i < workers; i++ {
		op := &FileOps{OutputDir: dir, NoOverwrite: true}
		ops = append(ops, op)
	}

	var wg sync.WaitGroup
	successes := make([]bool, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if err := ops[idx].Write("shared.go", fmt.Sprintf("package main // %d\n", idx)); err != nil {
				errs[idx] = err
				return
			}
			successes[idx] = true
		}(i)
	}
	wg.Wait()

	got := 0
	for i, ok := range successes {
		if ok {
			got++
			continue
		}
		if errs[i] == nil || !strings.Contains(errs[i].Error(), "уже существует") {
			t.Errorf("рабочий %d: неожиданная ошибка: %v", i, errs[i])
		}
	}
	if got != 1 {
		t.Fatalf("ожидалась ровно одна успешная запись, получено %d", got)
	}
}

// TestConcurrentAppendNoCorruption — N горутин добавляют уникальные строки
// в один файл. Пер-проектная блокировка гарантирует, что ни одна строка не
// потеряется и не перемешается посимвольно.
func TestConcurrentAppendNoCorruption(t *testing.T) {
	dir := t.TempDir()
	op := &FileOps{OutputDir: dir, NoOverwrite: false}
	if err := op.Write("log.txt", ""); err != nil {
		t.Fatalf("инициализация: %v", err)
	}

	const workers = 16
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if err := op.AppendTo("log.txt", fmt.Sprintf("line-%02d\n", idx)); err != nil {
				t.Errorf("дозапись %d: %v", idx, err)
			}
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(dir, "log.txt"))
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	got := map[string]int{}
	for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		got[ln]++
	}
	if len(got) != workers {
		t.Fatalf("ожидалось %d уникальных строк, получено %d: %q", workers, len(got), string(data))
	}
	for i := 0; i < workers; i++ {
		key := fmt.Sprintf("line-%02d", i)
		if got[key] != 1 {
			t.Fatalf("строка %q встречается %dx (нужно ровно 1)", key, got[key])
		}
	}
}

// TestWriteHealsEmptyStub — запись внутрь «каталога», на месте которого лежит
// 0-байтовый файл-заглушка, лечит путь: заглушка удаляется, вместо неё
// создаются каталоги, файл записывается (см. ensureParentDirs).
func TestWriteHealsEmptyStub(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "server", "internal") // выглядит как каталог, но файл
	if err := os.MkdirAll(filepath.Join(dir, "server"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(stub, nil, 0o644); err != nil {
		t.Fatalf("создание заглушки: %v", err)
	}

	op := &FileOps{OutputDir: dir}
	if err := op.Write("server/internal/api.go", "package internal\n"); err != nil {
		t.Fatalf("Write в «каталог»-заглушку: %v", err)
	}

	st, err := os.Stat(stub)
	if err != nil {
		t.Fatalf("путь после лечения не существует: %v", err)
	}
	if !st.IsDir() {
		t.Fatalf("ожидался каталог, остался файл: %v", st.Mode())
	}
	if _, err := os.Stat(filepath.Join(stub, "api.go")); err != nil {
		t.Fatalf("записанный файл не найден: %v", err)
	}
}

// TestWriteRejectsNonEmptyBlocker — непустой файл на пути — это реальный код,
// а не заглушка: запись внутрь падает с ошибкой, блокер не удаляется.
func TestWriteRejectsNonEmptyBlocker(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "server", "pkg")
	if err := os.MkdirAll(filepath.Join(dir, "server"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(blocker, []byte("// реальный файл\n"), 0o644); err != nil {
		t.Fatalf("создание блокера: %v", err)
	}

	op := &FileOps{OutputDir: dir}
	if err := op.Write("server/pkg/x.go", "package pkg\n"); err == nil {
		t.Fatal("ожидалась ошибка при записи внутрь непустого файла-блокера")
	}

	data, err := os.ReadFile(blocker)
	if err != nil || string(data) != "// реальный файл\n" {
		t.Fatalf("блокер был повреждён или удалён: %v, %q", err, string(data))
	}
}

// TestAppendToHealsEmptyStub — AppendTo тоже проходит через ensureParentDirs и
// лечит пустую заглушку на пути файла.
func TestAppendToHealsEmptyStub(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "web", "app")
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(stub, nil, 0o644); err != nil {
		t.Fatalf("создание заглушки: %v", err)
	}

	op := &FileOps{OutputDir: dir}
	if err := op.AppendTo("web/app/route.txt", "hello\n"); err != nil {
		t.Fatalf("AppendTo в «каталог»-заглушку: %v", err)
	}

	st, err := os.Stat(stub)
	if err != nil || !st.IsDir() {
		t.Fatalf("ожидался каталог после лечения, err=%v", err)
	}
	data, err := os.ReadFile(filepath.Join(stub, "route.txt"))
	if err != nil || string(data) != "hello\n" {
		t.Fatalf("файл после лечения не записан: %v, %q", err, string(data))
	}
}