# План: Фикс #3 — SanitizeJSON для mojibake (doubly-encoded UTF-8)

**Проблема:** `domain.UnmarshalTasks` при разборе JSON-ответа архитектора выдаёт ошибку `invalid character 'Ð' looking for beginning of value`. Причина: doubly-encoded UTF-8 (mojibake) — текст кириллицей дважды закодирован, и `sanitizeJSON` не умеет это чинить.

---

## 1. Диагностика

```
# Ошибка в логах:
éпик MYTRIP-V2-T3: архитектор не создал задач (доска и JSON-fallback: invalid character 'Ð' looking for beginning of value)

# Причиной является doubly-encoded UTF-8:
# Исходный текст: "Экспорт" → байты: D0 95 D0 BA D1 81 D0 BF D1 82 D1 82 D0 B3 D1 80 D1 82
# После двойной кодировки (UTF-8 → CP1251 → UTF-8): C3 90 C2 90 ...
# Go json.Unmarshal видит байты C3 90 как 'Ð' — невалидный JSON
```

`sanitizeJSON` работает по уровням юникода (rune), но не обрабатывает байтовые последовательности, которые выглядят как валидные UTF-8 символы, но фактически являются mojibake.

---

## 2. Решение

Добавить в `domain/unmarshal.go` функцию `fixMojibake`, которая:
1. Проверяет `utf8.ValidString(s)` — если строка валидна UTF-8, ничего не делать
2. При невалидности — использовать `strings.ToValidUTF8(s, replacement)`
3. Дополнительная проверка: после `extractJSON` попытаться декодировать как `UTF-8 → decode(CP1251) → encode(UTF-8)` — это стандартная фиксация mojibake от двойной кодировки

```go
// fixMojibake исправляет doubly-encoded UTF-8:
// если текст содержит характерные для mojibake последовательности
// (C3 XX, C2 XX за кириллическими диапазонами) — пытается восстановить
// изlatin-1 → UTF-8.
func fixMojibake(s string) string {
    if utf8.ValidString(s) {
        return s
    }
    // Быстрый фикс: заменить невалидные UTF-8 последовательности
    s = strings.ToValidUTF8(s, "?")
    // Попытка восстановить из latin-1 (двойная кодировка UTF-8 → CP1251 → UTF-8):
    // каждый байт → UTF-8 символ
    var b strings.Builder
    for _, r := range s {
        if r == 'Ð' || r == '£' || r == '¥' {
            // Эти символы — характерные признаки mojibake кириллицы
            // Декодируем обратно: байт r → UTF-8
            b.WriteRune(r)
        } else {
            b.WriteRune(r)
        }
    }
    return b.String()
}
```

### Улучшенный вариант

Проще и надёжнее — просто заменить `extractJSON` так, чтобы он работал с байтами, а не с символами. Или добавить `strings.ToValidUTF8` как первый шаг в `sanitizeJSON`:

```go
func extractJSON(s string) string {
    s = strings.ToValidUTF8(s, "")
    ...
}
```

---

## 3. Изменения по файлам

| Файл | Изменение |
|---|---|
| `domain/unmarshal.go` (строки 51-100) | `extractJSON`, `extractJSONArray` — добавить `strings.ToValidUTF8` как первый шаг |
| `domain/unmarshal.go` (строки 105-141) | `sanitizeJSON` — обработка невалидных UTF-8 рун через `writeUnicodeEscape` |

---

## 4. Порядок реализации

### Шаг 1: Fix `sanitizeJSON` для не-UTF-8 юникод-символов

- [ ] 1.1 В `sanitizeJSON` (строка 105) — добавить обработку не-ASCII символов как потенциального mojibake: `strings.ToValidUTF8` на входном `s` до цикла
- [ ] 1.2 В `extractJSON` (строка 51) — аналогично: `s = strings.ToValidUTF8(s, "")`
- [ ] 1.3 Тест: сымитировать mojibake-вход и проверить, что `UnmarshalTasks` парсит
- [ ] **Гейт:** `go build ./...`, `go test ./domain/...`

### Шаг 2: Добавить `fixMojibake` для двойной кодировки (опционально)

- [ ] 2.1 Создать функцию `fixMojibake(s string) string`
- [ ] 2.2 В `UnmarshalTasks` и `UnmarshalBacklog` — применять `fixMojibake` перед `extractJSON`
- [ ] 2.3 Тесты для кейсов двойной кодировки
- [ ] **Гейт:** `go test ./domain/...`

---

## 5. Тесты

```go
func TestUnmarshalTasksMojibake(t *testing.T) {
    // Симулируем двойную кодировку: «Экспорт» → UTF-8 → CP1251 → UTF-8
    // Результат содержит 'Ð' и другие символы mojibake
    input := `{"tasks":[{"task_id":"T1","title":"ÐÐ¼Ð¿Ð¾ÑÑ","description":"test"}]}`
    tasks, err := UnmarshalTasks(input)
    if err != nil {
        t.Fatalf("UnmarshalTasks с mojibake: %v", err)
    }
    if len(tasks) != 1 {
        t.Fatalf("ожидаем 1 задачу, получено %d", len(tasks))
    }
}
```

---

## 6. Риски

| Риск | Смягчение |
|---|---|
| `strings.ToValidUTF8` удаляет валидные данные | Маловероятно: удаляются только невалидные UTF-8 последовательности, которые и так не парсятся как JSON |
| Mojibake остаётся в поле `title` после разбора | JSON разберётся, но текст будет некорректным. Для критичных полей — добавить post-processing |
