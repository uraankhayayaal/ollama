package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// readFilesOne вызывает ReadFiles и разбирает типизированный ответ.
func readFilesOne(t *testing.T, ops *FileOps, args map[string]any) []FileContentResult {
	t.Helper()
	raw, err := ops.ReadFiles(args)
	if err != nil {
		t.Fatalf("ReadFiles: %v", err)
	}
	var res []FileContentResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("разбор ответа: %v (%s)", err, raw)
	}
	return res
}

// TestReadFilesRespectsLimits проверяет, что большой файл НЕ обрезается молча,
// а возвращает пагинацию (has_more + next_offset), и что суммарный лимит за
// вызов по-прежнему пропускает оставшиеся файлы.
func TestReadFilesRespectsLimits(t *testing.T) {
	dir := t.TempDir()
	// Содержимое big.txt помечено номером блока, чтобы страницы дочитки
	// различались: на однородном файле соседние порции неотличимы.
	var big strings.Builder
	for i := 0; i < 10; i++ {
		big.WriteString(strings.Repeat(fmt.Sprintf("[%02d]", i), 10))
	}
	files := map[string]string{
		"big.txt":   big.String(),
		"small.txt": "hello",
		"late.txt":  "later",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	ops := &FileOps{OutputDir: dir}
	t.Setenv("CODEGEN_READ_MAX_FILE", "50")

	// big.txt больше порции: первая страница = 50 байт, хвост доступен через
	// offset, а не потерян.
	first := readFilesOne(t, ops, map[string]any{"filenames": []string{"big.txt"}})
	if len(first) != 1 {
		t.Fatalf("ожидали 1 запись, got %d: %#v", len(first), first)
	}
	if first[0].Status != "success" {
		t.Fatalf("big.txt должен читаться успешно: %#v", first[0])
	}
	if len(first[0].Content) != 50 {
		t.Errorf("ожидали порцию в 50 байт, got %d", len(first[0].Content))
	}
	if !first[0].HasMore {
		t.Errorf("файл больше порции — ожидался has_more: %#v", first[0])
	}
	if first[0].NextOffset != 50 {
		t.Errorf("next_offset = %d, ожидалось 50", first[0].NextOffset)
	}
	if !strings.Contains(first[0].Message, "offset=50") {
		t.Errorf("сообщение должно учить модель дочитать через offset: %q", first[0].Message)
	}

	// Вторая страница того же файла дочитывается через offset и не повторяет
	// первую.
	second := readFilesOne(t, ops, map[string]any{"filenames": []string{"big.txt"}, "offset": first[0].NextOffset})
	if len(second) != 1 || second[0].Status != "success" || len(second[0].Content) == 0 {
		t.Fatalf("вторая страница должна дочитать остаток: %#v", second)
	}
	if first[0].Content == second[0].Content {
		t.Errorf("вторая страница повторяет первую — offset не применён")
	}
	if second[0].NextOffset <= first[0].NextOffset {
		t.Errorf("дочитка не продвинулась: %d -> %d", first[0].NextOffset, second[0].NextOffset)
	}

	// Суммарный бюджет: подбираем так, чтобы после big.txt маленький файл
	// поместился, а последний — нет.
	t.Setenv("CODEGEN_READ_MAX_TOTAL", strconv.Itoa(50+5))

	results := readFilesOne(t, ops, map[string]any{
		"filenames": []string{"big.txt", "small.txt", "late.txt"},
	})
	if len(results) != 3 {
		t.Fatalf("ожидали 3 ответа, got %d: %#v", len(results), results)
	}
	if results[0].Status != "success" || !results[0].HasMore {
		t.Errorf("big.txt должен вернуть пагинацию: %#v", results[0])
	}
	if results[1].Status != "success" || results[1].Content != "hello" {
		t.Errorf("small.txt должен прочитаться целиком: %#v", results[1])
	}
	if results[2].Status != "skipped" {
		t.Errorf("late.txt должен быть пропущен суммарным лимитом: %#v", results[2])
	}
}

// TestReadFilesCapsFilesPerRound проверяет мягкий лимит на число файлов за
// вызов: первые N читаются, остальные получают «skipped» с подсказкой, а сам
// вызов НЕ падает (модель не должна терять запрос и бросаться в петлю).
func TestReadFilesCapsFilesPerRound(t *testing.T) {
	dir := t.TempDir()
	names := []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("body of "+n), 0644); err != nil {
			t.Fatal(err)
		}
	}

	ops := &FileOps{OutputDir: dir}
	t.Setenv("CODEGEN_READ_MAX_FILES", "3")

	res := readFilesOne(t, ops, map[string]any{"filenames": names})
	if len(res) != len(names) {
		t.Fatalf("ожидали запись на каждый запрошенный файл (%d), got %d", len(names), len(res))
	}
	for i := 0; i < 3; i++ {
		if res[i].Status != "success" || res[i].Content != "body of "+names[i] {
			t.Errorf("файл %s должен прочитаться: %#v", names[i], res[i])
		}
	}
	for i := 3; i < len(names); i++ {
		if res[i].Filename != names[i] {
			t.Errorf("пропущенный файл должен сохранять имя: %#v", res[i])
		}
		if res[i].Status != "skipped" {
			t.Errorf("файл %s должен быть пропущен: %#v", names[i], res[i])
		}
		if !strings.Contains(res[i].Message, "ReadFiles") {
			t.Errorf("пропущенный файл должен получить подсказку дочитать: %q", res[i].Message)
		}
	}
}

// TestReadFilesKeepsScopeGuard проверяет, что пагинация не обошла защиту
// области работы: файл вне scope и выход за пределы каталога по-прежнему
// отклоняются (регрессия на «сырой» os.Open без ResolvePath/allowed).
func TestReadFilesKeepsScopeGuard(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "proj")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inside.txt"), []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(outside, []byte("TOP SECRET"), 0644); err != nil {
		t.Fatal(err)
	}

	// Выход за пределы OutputDir через "..".
	ops := &FileOps{OutputDir: dir}
	res := readFilesOne(t, ops, map[string]any{"filenames": []string{"../secret.txt"}, "offset": 2})
	if len(res) != 1 || res[0].Status != "error" {
		t.Fatalf("выход за пределы каталога должен быть отклонён: %#v", res)
	}
	if strings.Contains(res[0].Message, "TOP SECRET") {
		t.Fatalf("содержимое файла за пределами каталога не должно попадать в ответ")
	}

	// Абсолютный путь.
	res = readFilesOne(t, ops, map[string]any{"filenames": []string{outside}})
	if len(res) != 1 || res[0].Status != "error" {
		t.Fatalf("абсолютный путь должен быть отклонён: %#v", res)
	}

	// Scope.
	ops2 := &FileOps{OutputDir: root}
	ops2.SetScope([]string{"proj/inside.txt"})
	res = readFilesOne(t, ops2, map[string]any{"filenames": []string{"secret.txt"}})
	if len(res) != 1 || res[0].Status != "error" {
		t.Fatalf("файл вне scope должен быть отклонён: %#v", res)
	}
	if res = readFilesOne(t, ops2, map[string]any{"filenames": []string{"proj/inside.txt"}}); res[0].Status != "success" {
		t.Fatalf("файл внутри scope должен читаться: %#v", res)
	}
}

// TestReadFilesOffsetBeyondEOFAndBinary проверяет границы дочитки и отказ
// на бинарнике (в контекст не должны попадать сырые байты).
func TestReadFilesOffsetBeyondEOFAndBinary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin.dat"), []byte{'a', 0, 'b', 'c'}, 0644); err != nil {
		t.Fatal(err)
	}

	ops := &FileOps{OutputDir: dir}

	res := readFilesOne(t, ops, map[string]any{"filenames": []string{"small.txt"}, "offset": 999})
	if len(res) != 1 || res[0].Status != "error" || !strings.Contains(res[0].Message, "offset") {
		t.Fatalf("offset за пределами файла должен быть отказом: %#v", res)
	}

	// Отрицательный offset не должен падать — приводится к началу файла.
	if res = readFilesOne(t, ops, map[string]any{"filenames": []string{"small.txt"}, "offset": -10}); res[0].Status != "success" || res[0].Content != "hello" {
		t.Fatalf("отрицательный offset должен читать с начала: %#v", res)
	}

	res = readFilesOne(t, ops, map[string]any{"filenames": []string{"bin.dat"}})
	if len(res) != 1 || res[0].Status != "error" || !strings.Contains(res[0].Message, "бинар") {
		t.Fatalf("бинарный файл должен быть отклонён: %#v", res)
	}
}

// TestReadFilesChunkDoesNotSplitRune проверяет, что граница порции не
// разрезает многобайтовый UTF-8 символ: каждая страница — валидный текст,
// а вместе страницы восстанавливают исходный файл.
func TestReadFilesChunkDoesNotSplitRune(t *testing.T) {
	dir := t.TempDir()
	// Кириллица по 2 байта на букву — при нечётной порции грашка попадёт внутрь.
	want := strings.Repeat("я", 100)
	if err := os.WriteFile(filepath.Join(dir, "ru.txt"), []byte(want), 0644); err != nil {
		t.Fatal(err)
	}

	ops := &FileOps{OutputDir: dir}
	t.Setenv("CODEGEN_READ_CHUNK", "51") // нечётная порция

	var got strings.Builder
	offset := 0
	for i := 0; i < 10; i++ {
		res := readFilesOne(t, ops, map[string]any{
			"filenames": []string{"ru.txt"},
			"offset":    offset,
		})
		if len(res) != 1 || res[0].Status != "success" {
			t.Fatalf("страница %d: %#v", i, res)
		}
		if !utf8.ValidString(res[0].Content) {
			t.Fatalf("страница %d содержит невалидный UTF-8: %q", i, res[0].Content)
		}
		got.WriteString(res[0].Content)
		offset = res[0].NextOffset
		if !res[0].HasMore {
			break
		}
		if offset <= 0 {
			t.Fatalf("дочитка не продвинулась: %#v", res[0])
		}
	}
	if got.String() != want {
		t.Errorf("страницы не восстановили файл: got %d байт, want %d", len(got.String()), len(want))
	}
}

// TestReadFilesOffsetInsideRune проверяет, что offset, попавший внутрь
// многобайтового символа, не приводит к молчаливой порче содержимого: json
// заменил бы невалидные байты на U+FFFD (модель получила бы мусор), поэтому
// инструмент отвечает отказом со ссылкой на next_offset.
func TestReadFilesOffsetInsideRune(t *testing.T) {
	dir := t.TempDir()
	want := strings.Repeat("я", 20) // 40 байт
	if err := os.WriteFile(filepath.Join(dir, "ru.txt"), []byte(want), 0644); err != nil {
		t.Fatal(err)
	}

	ops := &FileOps{OutputDir: dir}
	// Байт 1 — вторая половина символа «я» (0x8F).
	res := readFilesOne(t, ops, map[string]any{"filenames": []string{"ru.txt"}, "offset": 1})
	if len(res) != 1 || res[0].Status != "error" {
		t.Fatalf("offset внутри символа должен быть отказом: %#v", res)
	}
	if strings.Contains(res[0].Message, "�") {
		t.Errorf("ответ не должен содержать подменённых символов: %q", res[0].Message)
	}
	if !strings.Contains(res[0].Message, "next_offset") {
		t.Errorf("отказ должен подсказать использовать next_offset: %q", res[0].Message)
	}
}

// TestReadFilesTruncatedTailRune проверяет, что файл, оборванный на середине
// символа, дочитывается без зацикливания: последняя страница возвращает
// has_more=false, а не ошибку и не вечный «ещё один offset».
func TestReadFilesTruncatedTailRune(t *testing.T) {
	dir := t.TempDir()
	// 3 полных символа (6 байт) + одинокий стартовый байт D1.
	if err := os.WriteFile(filepath.Join(dir, "cut.txt"), []byte("\xd1\x8f\xd1\x8f\xd1\x8f\xd1"), 0644); err != nil {
		t.Fatal(err)
	}

	ops := &FileOps{OutputDir: dir}
	t.Setenv("CODEGEN_READ_CHUNK", "4")

	var got strings.Builder
	offset := 0
	for i := 0; i < 5; i++ {
		res := readFilesOne(t, ops, map[string]any{"filenames": []string{"cut.txt"}, "offset": offset})
		if len(res) != 1 || res[0].Status != "success" {
			t.Fatalf("страница %d: %#v", i, res)
		}
		got.WriteString(res[0].Content)
		if !res[0].HasMore {
			break
		}
		if res[0].NextOffset <= offset {
			t.Fatalf("дочитка застряла на offset %d: %#v", offset, res[0])
		}
		offset = res[0].NextOffset
	}
	if got.String() != "яяя" {
		t.Errorf("ожидалось %q, получено %q", "яяя", got.String())
	}
}
