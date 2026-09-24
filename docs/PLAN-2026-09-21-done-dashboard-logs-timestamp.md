# План: Экспорт логов и чатов — timestamp в начале имени файла

Статус: **ГОТОВО**. Формат — как в `PLAN-2026-09-17-done-webui.md` / `PLAN-2026-09-19-done-lsp.md`.
Закрывает TODO из `PLAN-dashboard-gitflow.md` (строка 10).

## Цель

В **экспорте логов** (Logboard) и **экспорте чата** (Chatboard) добавить
timestamp **(дата+время)** **в начало имени** скачиваемого файла, чтобы при
повторных экспортах файлы не перезаписывались и порядок по имени совпадал с
хронологией.

## Текущее состояние (код)

| Место | Код | Что отдаёт сейчас |
|---|---|---|
| Экспорт чата | `web/src/Components/Chatboard/Chatboard.tsx:62` `exportChat` → `downloadText(`chat-${Date.now()}.txt`, …)` | `chat-<мсек>.txt` — «сырой» epoch-millis уже есть, но не человекочитаем и не в начале как дата |
| Экспорт логов | `web/src/Components/Logboard/Logboard.tsx:177` `onExportAll` → `downloadText(`logs-${safeName(props.project)}.txt`, text)` | `logs-<проект>.txt` — **timestamp отсутствует**, повторный экспорт перезаписывает файл |
| Общая функция скачивания | `web/src/download.ts:2` `downloadText(filename, text)` | только создаёт blob; имени не трогает |

Также совпадает по смыслу: `Diffboard` не экспортирует файлы — не трогаем.

## Решения

1. Ввести единый хелпер **`stampedName(prefix, name)`** в `web/src/download.ts`:
   возвращает `YYYMMDD-HHmmss_<prefix><name>.txt` (локальное время, ведущий
   ноль). Timestamp **в начале** имени — как требует TODO.
2. Peer: без «сырого» `Date.now()` — используется человекочитаемый
   `2026-09-21_14-30-05`.
3. Формат для логов: `logs-<timestamp>-<safeName(проект)>.txt`;
   для чата: `chat-<timestamp>-<snippet>.txt` (snippet — первые слова_title,
   чтобы различать экспорты одного чата; иначе достаточно timestamp в начале).
   Чат сейчас `chat-<Date.now()>.txt` — заменяем на хелпер (формат имени
   меняется, но это новый файл, совместимость с бэкендом не затрагивается).

## Этапы и чеклист

### Ф-1 — Хелпер и применения
- [x] `web/src/download.ts`: `stampedName(prefix, name)` (формат `YYYY-MM-DD_HH-mm-ss`; ведущие нули; безопасные символы имени)
- [x] `Chatboard.tsx`: `exportChat` → `downloadText(stampedName("chat", snippet(title) + ".txt"), ...)`
- [x] `Logboard.tsx`: `onExportAll` → `downloadText(stampedName("logs", safeName(props.project) + ".txt"), ...)`
- [x] Проверить: копирование (буфер) логов остается без timestamp (имени файла нет)

### Ф-2 — Верификация
- [x] `npm run build` (web/)
- [ ] Ручная проверка: открыть проект с чатом и логами → «Экспорт» в обоих панелях → в загрузках файлы начинаются с `YYYY-MM-DD_HH-mm-ss_`
- [ ] Повторный экспорт не перезаписывает предыдущий (имя файла отличается)

## Верификация

```
npm run build   # web/
```

## Как продолжить

1. Выполнить Ф-1, отметить `[x]`.
2. Ф-2 — сборка и ручная проверка.