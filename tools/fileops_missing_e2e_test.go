package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestReadFilesE2EHintToRetryForbidden — регрессионный e2e-тест полного цикла
// модели: вызов ReadFiles → получение hint → повторный вызов → получение
// retry:"forbidden".
//
// Защита от регрессии при будущих рефакторингах ReadFiles/ReadResult:
// если кто-то изменит логику подсчёта повторных попыток или формат
// сообщений об ошибке, тест упадёт.
func TestReadFilesE2EHintToRetryForbidden(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}

	// --- Шаг 1: Первый вызов с несуществующим файлом ---
	// Ожидаем: status=error, hint=true, retry=forbidden,
	// message содержит "не существует" и "List".
	raw1, err := ops.ReadFiles(map[string]any{"filenames": []string{"nonexistent.txt"}})
	if err != nil {
		t.Fatalf("ReadFiles (1-й вызов) вернул ошибку: %v", err)
	}
	var res1 []map[string]string
	if err := json.Unmarshal(raw1, &res1); err != nil {
		t.Fatalf("Разбор JSON (1-й вызов): %v", err)
	}
	if len(res1) != 1 {
		t.Fatalf("Ожидался массив из 1 объекта, got %d: %s", len(res1), raw1)
	}
	if res1[0]["status"] != "error" {
		t.Fatalf("1-й вызов: ожидался status=error, got %q", res1[0]["status"])
	}
	if res1[0]["hint"] != "true" {
		t.Fatalf("1-й вызов: ожидался hint=true, got %q", res1[0]["hint"])
	}
	if res1[0]["retry"] != "forbidden" {
		t.Fatalf("1-й вызов: ожидался retry=forbidden, got %q", res1[0]["retry"])
	}
	if !strings.Contains(res1[0]["message"], "не существует") {
		t.Fatalf("1-й вызов: message не содержит \"не существует\": %q", res1[0]["message"])
	}
	if !strings.Contains(res1[0]["message"], "List") {
		t.Fatalf("1-й вызов: message не содержит \"List\": %q", res1[0]["message"])
	}

	// --- Шаг 2: Второй вызов с тем же файлом ---
	// Ожидаем: status=error, hint=true, retry=forbidden,
	// message содержит "повторная попытка 2" и "запрещены".
	raw2, err := ops.ReadFiles(map[string]any{"filenames": []string{"nonexistent.txt"}})
	if err != nil {
		t.Fatalf("ReadFiles (2-й вызов) вернул ошибку: %v", err)
	}
	var res2 []map[string]string
	if err := json.Unmarshal(raw2, &res2); err != nil {
		t.Fatalf("Разбор JSON (2-й вызов): %v", err)
	}
	if len(res2) != 1 {
		t.Fatalf("Ожидался массив из 1 объекта, got %d: %s", len(res2), raw2)
	}
	if res2[0]["status"] != "error" {
		t.Fatalf("2-й вызов: ожидался status=error, got %q", res2[0]["status"])
	}
	if res2[0]["hint"] != "true" {
		t.Fatalf("2-й вызов: ожидался hint=true, got %q", res2[0]["hint"])
	}
	if res2[0]["retry"] != "forbidden" {
		t.Fatalf("2-й вызов: ожидался retry=forbidden, got %q", res2[0]["retry"])
	}
	if !strings.Contains(res2[0]["message"], "повторная попытка 2") {
		t.Fatalf("2-й вызов: message не содержит \"повторная попытка 2\": %q", res2[0]["message"])
	}
	if !strings.Contains(res2[0]["message"], "запрещены") {
		t.Fatalf("2-й вызов: message не содержит \"запрещены\": %q", res2[0]["message"])
	}

	// --- Шаг 3: Третий вызов с тем же файлом ---
	// Ожидаем: status=error, hint=true, retry=forbidden,
	// message содержит "повторная попытка 3".
	raw3, err := ops.ReadFiles(map[string]any{"filenames": []string{"nonexistent.txt"}})
	if err != nil {
		t.Fatalf("ReadFiles (3-й вызов) вернул ошибку: %v", err)
	}
	var res3 []map[string]string
	if err := json.Unmarshal(raw3, &res3); err != nil {
		t.Fatalf("Разбор JSON (3-й вызов): %v", err)
	}
	if len(res3) != 1 {
		t.Fatalf("Ожидался массив из 1 объекта, got %d: %s", len(res3), raw3)
	}
	if res3[0]["status"] != "error" {
		t.Fatalf("3-й вызов: ожидался status=error, got %q", res3[0]["status"])
	}
	if res3[0]["hint"] != "true" {
		t.Fatalf("3-й вызов: ожидался hint=true, got %q", res3[0]["hint"])
	}
	if res3[0]["retry"] != "forbidden" {
		t.Fatalf("3-й вызов: ожидался retry=forbidden, got %q", res3[0]["retry"])
	}
	if !strings.Contains(res3[0]["message"], "повторная попытка 3") {
		t.Fatalf("3-й вызов: message не содержит \"повторная попытка 3\": %q", res3[0]["message"])
	}

	// --- Шаг 4: Переключение OutputDir на новый временный каталог ---
	// Счётчик повторных попыток должен сброситься.
	dir2 := t.TempDir()
	ops.SetOutputDir(dir2)

	// --- Шаг 5: Четвёртый вызов с тем же файлом ---
	// Ожидаем: status=error, hint=true, retry=forbidden,
	// message содержит "не существует" и НЕ содержит "повторная попытка".
	raw4, err := ops.ReadFiles(map[string]any{"filenames": []string{"nonexistent.txt"}})
	if err != nil {
		t.Fatalf("ReadFiles (4-й вызов) вернул ошибку: %v", err)
	}
	var res4 []map[string]string
	if err := json.Unmarshal(raw4, &res4); err != nil {
		t.Fatalf("Разбор JSON (4-й вызов): %v", err)
	}
	if len(res4) != 1 {
		t.Fatalf("Ожидался массив из 1 объекта, got %d: %s", len(res4), raw4)
	}
	if res4[0]["status"] != "error" {
		t.Fatalf("4-й вызов: ожидался status=error, got %q", res4[0]["status"])
	}
	if res4[0]["hint"] != "true" {
		t.Fatalf("4-й вызов: ожидался hint=true, got %q", res4[0]["hint"])
	}
	if res4[0]["retry"] != "forbidden" {
		t.Fatalf("4-й вызов: ожидался retry=forbidden, got %q", res4[0]["retry"])
	}
	if !strings.Contains(res4[0]["message"], "не существует") {
		t.Fatalf("4-й вызов: message не содержит \"не существует\": %q", res4[0]["message"])
	}
	if strings.Contains(res4[0]["message"], "повторная попытка") {
		t.Fatalf("4-й вызов: message содержит \"повторная попытка\" — счётчик не сброшен: %q", res4[0]["message"])
	}
}
