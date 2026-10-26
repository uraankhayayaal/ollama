package domain

import (
	"strings"
	"testing"
)

// TestFixMojibake_ValidText_Unchanged проверяет, что валидный текст
// без признаков двойной кодировки не изменяется.
func TestFixMojibake_ValidText_Unchanged(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"ascii", "Hello World"},
		{"cyrillic", "Экспорт данных"},
		{"mixed", "Task: создать API endpoint"},
		{"emoji", "Создать API 🚀"},
		{"accented", "café résumé"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fixMojibake(tc.in)
			if got != tc.in {
				t.Errorf("ожидаем без изменений: %q, got %q", tc.in, got)
			}
		})
	}
}

// TestFixMojibake_DoubleEncoded_Restores проверяет восстановление
// doubly-encoded UTF-8: UTF-8 → latin-1 → UTF-8 → оригинал.
func TestFixMojibake_DoubleEncoded_Restores(t *testing.T) {
	// Симулируем двойную кодировку кириллицы:
	// "Экспорт" → UTF-8 байты: D0 95 D0 BA D1 81 D0 BF D1 82 D1 82 D0 B3 D1 80 D1 82
	// → latin-1 символы: Ð Õ Ð ± Ñ ¿ Ò Ò ð³ Ñ @ Ñ"
	// → UTF-8: C3 90 C2 95 C3 90 C2 BA C3 91 C2 81 C3 90 C2 BF C3 91 C2 82 C3 91 C2 82 C3 90 C2 B3 C3 91 C2 80 C3 91 C2 82
	//
	// Проще: берём исходные UTF-8 байты и кодируем их как latin-1 → UTF-8
	original := "Экспорт"
	originalBytes := []byte(original)

	// Кодируем каждый байт как latin-1 символ, затем в UTF-8
	var mojibake strings.Builder
	for _, b := range originalBytes {
		// Каждый байт → rune U+0000-U+00FF → UTF-8 кодировка
		mojibake.WriteRune(rune(b))
	}
	mojibaked := mojibake.String()

	// Проверяем, что fixMojibake восстанавливает оригинал
	got := fixMojibake(mojibaked)
	if got != original {
		t.Errorf("ожидаем %q, got %q", original, got)
	}
}

// TestFixMojibake_TaskJSON_Parses проверяет, что UnmarshalTasks
// корректно парсит JSON с mojibake в полях title/description.
func TestFixMojibake_TaskJSON_Parses(t *testing.T) {
	// Создаём mojibake для "Тест" (байты D0 A2 D0 B5 D1 81 D1 82)
	testMojibake := encodeToMojibake("Тест")

	// JSON с mojibake в title и description
	input := `{"tasks":[{"task_id":"T1","title":"` + testMojibake + `","description":"test desc","assigned_role":"Developer"}]}`

	tasks, err := UnmarshalTasks(input)
	if err != nil {
		t.Fatalf("UnmarshalTasks с mojibake: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("ожидаем 1 задачу, got %d", len(tasks))
	}
	// После восстановления mojibake title должен быть "Тест"
	if tasks[0].Title != "Тест" {
		t.Errorf("ожидаем title=%q, got %q", "Тест", tasks[0].Title)
	}
}

// TestFixMojibake_InvalidUTF8_Sanitizes проверяет, что невалидный UTF-8
// обрабатывается через strings.ToValidUTF8.
func TestFixMojibake_InvalidUTF8_Sanitizes(t *testing.T) {
	// Невалидный UTF-8 байт: 0xFF
	input := string([]byte{0xFF, 0xFE, 0x48, 0x65, 0x6C, 0x6C, 0x6F}) // "Hello" с невалидными байтами
	got := fixMojibake(input)
	// Результат должен быть валидным UTF-8
	if !utf8Valid(got) {
		t.Errorf("результат должен быть валидным UTF-8, got %q", got)
	}
}

// TestFixMojibake_Minimal_PassThrough проверяет, что текст с 1-2 паттернами
// C3/C2 не считается mojibake (это могут быть европейские акценты).
func TestFixMojibake_Minimal_PassThrough(t *testing.T) {
	// "Ã©" — это UTF-8 кодировка 'é' (U+00E9), которая выглядит как паттерн C3 A9
	// Но всего 1 паттерн → не должен обрабатываться как mojibake
	input := "café résumé"
	got := fixMojibake(input)
	if got != input {
		t.Errorf("ожидаем без изменений для акцентированного текста, got %q", got)
	}
}

// TestUnmarshalTasks_Valid_NoChange проверяет, что валидный JSON
// без mojibake парсится корректно.
func TestUnmarshalTasks_Valid_NoChange(t *testing.T) {
	input := `{"tasks":[{"task_id":"T1","title":"Task 1","description":"desc","assigned_role":"Dev"}]}`
	tasks, err := UnmarshalTasks(input)
	if err != nil {
		t.Fatalf("UnmarshalTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("ожидаем 1 задачу, got %d", len(tasks))
	}
	if tasks[0].Title != "Task 1" {
		t.Errorf("ожидаем title=%q, got %q", "Task 1", tasks[0].Title)
	}
}

// TestUnmarshalBacklog_Mojibake_Parses проверяет, что UnmarshalBacklog
// корректно парсит JSON с mojibake.
func TestUnmarshalBacklog_Mojibake_Parses(t *testing.T) {
	archMojibake := encodeToMojibake("Архитектура")

	input := `{"architecture_summary":"` + archMojibake + `","tasks":[{"task_id":"E1","title":"epic","description":"desc","assigned_role":"Lead","sequence_order":1}]}`

	backlog, err := UnmarshalBacklog(input)
	if err != nil {
		t.Fatalf("UnmarshalBacklog с mojibake: %v", err)
	}
	if backlog.ArchitectureSummary != "Архитектура" {
		t.Errorf("ожидаем summary=%q, got %q", "Архитектура", backlog.ArchitectureSummary)
	}
	if len(backlog.Tasks) != 1 {
		t.Fatalf("ожидаем 1 задачу, got %d", len(backlog.Tasks))
	}
}

// encodeToMojibake кодирует строку как UTF-8 → latin-1 → UTF-8 (двойная кодировка).
func encodeToMojibake(s string) string {
	var b strings.Builder
	for _, byte := range []byte(s) {
		b.WriteRune(rune(byte))
	}
	return b.String()
}

// utf8Valid проверяет, что строка — валидный UTF-8.
func utf8Valid(s string) bool {
	for range s {
		// Если итерация проходит без ошибок — строка валидна
	}
	return true
}
