# TEST_REPORT.md — QA: регрессионные тесты зацикливания ReadFiles на несуществующих файлах

**Задача:** BUG-01-T2
**Роль:** QA Engineer
**Дата:** 2025-01-15

## Статус: FAILED (блокирующий дефект)

## Итог

| Метрика | Значение |
|---|---|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test ./tools/ -v` | **FAIL** — 6 из 6 новых тестов FAIL, остальные 120+ PASS |
| Новые тесты (unit) | 3/3 FAIL |
| Новые тесты (интеграционные) | 3/3 FAIL |
| Существующие тесты | 120+ PASS (регрессий нет) |

## Пройденные тесты (новые, из BUG-01-T2)

Ни один из 6 новых тестов не прошёл. Все 6 FAIL по одной и той же причине: **production-код `tools/fileops.go` не реализует контракт BUG-01-T1**.

## Список FAIL-тестов

### Unit-тесты (`tools/fileops_missing_test.go`)

| Тест | Ожидаемый результат | Фактический результат |
|---|---|---|
| `TestReadFilesMissingFileHint` | `status:"error"`, `message` содержит «не существует» и «List», `hint:"true"`, `retry:"forbidden"` | `status:"error"`, `message:"open /tmp/.../nope/missing.go: no such file or directory"`, `hint` отсутствует, `retry` отсутствует |
| `TestReadFilesMissingFileRepeatLimit` | 2-й вызов: `message` содержит «повторная попытка 2» и «запрещены» | `message:"open /tmp/.../nope/missing.go: no such file or directory"` — без «повторная попытка 2», без «запрещены» |
| `TestReadFilesMissingDir` | `message` содержит «директория» | `message:"read /tmp/.../somedir: is a directory"` — без «директория» |

### Интеграционные тесты (`tools/fileops_missing_integration_test.go`)

| Тест | Ожидаемый результат | Фактический результат |
|---|---|---|
| `TestReadFilesNoLoopOnMissing` | 3 вызова: каждый `status:"error"`, нарастающая подсказка, `hint:"true"`, `retry:"forbidden"` | Все 3 вызова: `message:"open /tmp/.../nope/missing.go: no such file or directory"`, без `hint`, без `retry`, без «не существует», без «повторная попытка N» |
| `TestReadFilesMixedExistingAndMissing` | 2 элемента: success для existing (content совпадает), error+hint для missing | existing: success (content совпадает) — OK; missing: `hint` отсутствует, `retry` отсутствует |
| `TestReadFilesSetOutputDirResetsCounter` | После `SetOutputDir` счётчик обнуляется, 1-я подсказка «Не повторяй вызов» без «повторная попытка» | `message:"open /tmp/.../nope/missing.go: no such file or directory"` — без «не существует», без «Не повторяй вызов» |

## Корневая причина

Production-код `tools/fileops.go` **не содержит** элементов контракта BUG-01-T1:

1. **`FileOps`** (L25-55) — отсутствуют поля `readAttempts map[string]int` и `readAttemptsMu sync.Mutex`
2. **`readMaxMissingAttempts`** — константа не определена
3. **`readMissingHint`** — метод не реализован
4. **`ReadResult`** (L262-273) — при ошибке `os.ReadFile` возвращает сырую `err.Error()` без обработки `os.IsNotExist`, без `hint`, без «не существует»
5. **`ReadFiles`** (L656-749) — не добавляет `retry:"forbidden"` в error-объекты
6. **Директория** — при чтении директории `os.ReadFile` возвращает `"is a directory"` без обработки

## Багрепорт

Создан багрепорт **BUG-01-T2-DEF-01** (статус: new) — production-код не реализует контракт BUG-01-T1.

## Вывод

Задача BUG-01-T1 (Senior Go Developer) переведена в статус `done`, но production-код не изменён. Контракт не реализован. Это блокирующий дефект — без реализации контракта QA-тесты не могут пройти. Требуется возврат BUG-01-T1 на доработку.

## Команды запуска

```bash
go build ./...
go vet ./...
go test ./tools/ -run 'TestReadFiles' -v
go test ./tools/ -v
```
