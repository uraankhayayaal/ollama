# PLAN-2026-10-04-todo-assistant-injections

Инъекции через Assistant для работающей модели (runtime injections).

Цель: дать возможность настраивать системные/промт-инъекции глобально и на уровне конкретного Assistant (и динамически в сессии), чтобы они применялись к работающей модели в реальном времени без перезагрузки сервера/агента, с чётким пайплайном приоритизации и безопасным мерджем.

---

## Этап 1: Термины, проблема, область

- [x] 1.1. Определить ключевые термины: Injection (prompt/system/context), scope (global, assistant, session, runtime), position (prepend/append/replace/inject_at_index), target (system, messages, user_last, assistant_last). Значения `before_tools`/`after_tools` из ранней редакции плана **не реализованы и не принимаются** (см. «Аудит», А-4).
- [x] 1.2. Зафиксировать проблему: текущий Assistant не имеет механизма структурированных инъекций, применяемых в runtime при формировании запроса к LLM.
- [x] 1.3. Определить границы: влияем только на формирование промпта/сообщений перед вызовом модели (не на хранение чата, не на инструменты, кроме передачи). Обратная совместимость обязательна.
- [x] 1.4. Собрать референсы: точки сборки system prompt (`agents/chatassist/agent.go:166-180`), messages (`runner/runner.go:574-579`), LLM call path.

## Этап 2: Анализ текущей архитектуры

- [x] 2.1. Найти, где формируется system prompt для работающей модели (chat session / LLM call). Просмотреть `server/chat*`, `server/llm*`, `llm/provider*`, `runner/*`.
- [x] 2.2. Найти структуру Assistant (модель) и точек вызова: `models/`, `agents/chatassist/`.
- [x] 2.3. Определить точки применения в работающей сессии (runtime): до сборки system, при сборке messages, pre-call hooks (если есть).
- [x] 2.4. Зафиксировать, какие поля уже есть (tools, system, instructions, temperature, model, etc.). Убедиться, что добавление не ломает сериализацию.
- [x] 2.5. Составить карту потоков: Assistant -> Session -> LLM Request.

## Этап 3: Модель данных инъекций

- [x] 3.1. Спроектировать тип `Injection`. Тип живёт в `board/injection.go`
  (не в `models`): инъекция — сущность доски и сессии, а модель провайдера о ней
  ничего не знает. Мёртвые `models/injection*.go` удалены.
  - ✅ `id` (string, выдаётся автоматически), ✅ `name`, ✅ `scope` (global|assistant|session|runtime)
  - ✅ `target` (system|messages|user_last|assistant_last)
  - ✅ `position` (prepend|append|replace|inject_at_index)
  - ✅ `index` (int, опц.)
  - ✅ `content` (string, template)
  - ✅ `when` (expr/condition, опц.)
  - ✅ `enabled` — **уточнено**: `*bool`, три состояния (`nil` = активна,
    `true` = активна, `false` = выключена). Плоский `bool` не различал бы
    «не задано» и «выключено», а это разные вещи в REST-патче.
  - ✅ `priority` (int, default 0)
  - ✅ `vars` (map[string]any, опц.)
  - ✅ `tags` ([]string, опц.)
- [x] 3.2. Добавить поле `injections` в модель Assistant: `[]Injection` (обратная совместимость, omitempty). — **Готово**: `chatassist.Assistant.Injections []board.Injection` (`agent.go:118-121`), `GetInjections()` (`agent.go:221-223`).
- [ ] 3.3. Глобальные инъекции: конфиг уровня приложения. — **Не реализовано и оставлено незакрытым нарочно**: слота `injections` в `server.Config` нет, глобальный scope существует только в порядке сортировки и валидации. Заявлять его рабочим нельзя (см. «Аудит», А-8).
- [x] 3.4. Добавить runtime-инъекции на уровне сессии (session-scoped) — возможность добавлять/удалять в работающей сессии без перезапуска ассистента. — **Готово**: `Session.sessionInjections []board.Injection`, `AddSessionInjection`, `RemoveSessionInjection`, `SessionInjections()` (`session.go:112-148`).
- [x] 3.5. Определить формат сериализации (JSON). Схемы валидации (`Validate`).
- [x] 3.6. Добавить миграции/валидацию (unknown fields — rejected, дубликаты id — error).

## Этап 4: Пайплайн применения (merging)

- [x] 4.1. Определить порядок применения scope: `global` → `assistant` → `session` → `runtime`. `ScopeOrder = [global, assistant, session, runtime]`.
- [x] 4.2. Правила приоритизации внутри scope: по `priority` (убывание/high-first), при равенстве — порядок объявления (stable order via SliceStable).
- [x] 4.3. Правила мержа по target+position:
  - ✅ `prepend` — добавляет перед целевым блоком (в порядке приоритета)
  - ✅ `append` — после (в порядке приоритета)
  - ✅ `replace` — замещает целевой блок
  - ✅ `inject_at_index` — вставка по индексу в messages/system блоке
- [x] 4.4. Дедупликация: по `id` (последний перекрывает, `DeduplicateByID`).
- [x] 4.5. Обработка конфликтов replace: last-write-wins по scope+priority. `ReplaceWarning` в MergeResult.
- [x] 4.6. Фильтрация `when`: boolean expr с `==`, `!=`, `<`, `<=`, `>`, `>=` (последние четыре — только для чисел, т.е. практично для `turn`), `&&`, `||`, `!`, скобки. Переменные: model, provider, role, project, task_id, turn, user, has_files, tools. Переменные заполняются реальными данными прогона (см. «Аудит», А-6).
- [x] 4.7. Рендер шаблонов: `{{vars.*}}`, `{{env.*}}`, `{{session.*}}`, `{{assistant.*}}`. `{{assistant.*}}` раньше не рендерился вовсе. Доступ к `env` — по allowlist, а не по denylist (см. «Аудит», А-7).

## Этап 5: Интеграция в работающую модель (runtime apply)

- [x] 5.1. Найдено единое место сборки final system/messages перед LLM call — `runner/runner.go:583-613`. Используется `ApplyInjections` с контекстом. **Исправлено B1**: `runctx.InjectionsFromContext` → `board.InjectionsFromContext` (runner:586). Удалён dead code `taskInjsToModelInjections` (был в конце runner.go, не компилировался). Убран циклический импорт `runctx ↔ runner`.
- [x] 5.2. Внедрён этап `ApplyInjections`. Точка внедрения: после сбора system+user messages, до вызова LLM.
- [x] 5.3. Инъекции применяются ПОСЛЕ базового system (assistant.system/instructions), но ДО инструментов/контекста.
- [x] 5.4. Поддержка target=`messages`: вставка в массив messages (prepend/append/replace/inject_index).
- [x] 5.5. Helpers контекста: `board.NewInjectionContext`/`InjectionsFromContext`
  (снапшот), `board.WithInjectionSource`/`InjectionSourceFromContext` (живой
  источник) и `board.WithInjectionScope`/`InjectionScopeFromContext` (рамка
  прогона: проект, задача, роль). Имена из ранней редакции плана
  (`NewTaskInjections`, `UpdateTaskInjections`, `Observer`) в коде не
  существовали.
- [x] 5.6. Hot-apply. Утверждение «инъекции хранятся в Redis» из ранней
  редакции плана было неверным и исправлено: **сессионные инъекции живут в
  памяти процесса** (`Session.sessionInjections` под mutex) и теряются при
  рестарте сервера. Redis у инъекций **задачи** — там они лежат вместе с
  записью задачи.
- [x] 5.7. Per-round применение. `injectionSet.collect` (снапшот ассистента +
  живой источник) и `applyInjections(history, turn)` — применяются к **каждому**
  запросу к модели, а не один раз на прогон: `request := applyInjections(messages, round)`
  (`runner/runner.go`). История `messages` остаётся чистой.
- [x] 5.8. Debug-логи применённых инъекций (id, scope, target, position) — `MergeResult.Applied []AppliedInjection`.

## Этап 6: API/Конфиг и UX

- [x] 6.1. Загрузка Injection (JSON): парсинг `injections` + валидация (`Validate` + `ValidateInjections`).
- [ ] 6.2. Добавить глобальный конфиг: `injections` в app config (приоритет global). Определить путь конфигурации. — **Не реализовано**: нет слота для глобальных инъекций в `server.Config`.
- [x] 6.3. API управления инъекциями. Сессионные: `POST/DELETE/GET
  /api/projects/{id}/injections`. Инъекции задачи: `POST
  /api/projects/{id}/tasks/{tid}/injections`, `DELETE
  .../injections/{injID}`, плюс полная замена списка полем `injections` в
  `PUT .../tasks/{tid}`. Имена обработчиков из ранней редакции плана
  (`InjectInjection/InjectInjections`) в коде не существовали.
- [ ] 6.4. (Опц.) UI: отображение активных инъекций (debug/panel) — не блокер реализации ядра.
- [x] 6.5. Документация структуры `docs/20-features/chat-assistant.md` +
  примеры. Переписана по факту реализации: области, API, валидация, allowlist
  окружения, ограничения (см. «Аудит», А-10).

## Этап 7: Тестирование

- [x] 7.1. Юнит-тесты модели: парсинг, валидация, дефолты (`board/injection_test.go`: `Validate` по всем полям, `Normalize` с выдачей id, три состояния `enabled`, дубли id, строгий декод).
- [x] 7.2. Юнит-тесты пайплайна merge: порядок scope, priority, prepend/append/replace/inject_at_index (`injections/merger_test.go`). — **Исправлено B2**: `ai/board` → `"ai/board"`.
- [x] 7.3. Юнит-тесты условий `when`: whitelist, false/true пути.
- [x] 7.4. Юнит-тесты шаблонов: vars/env/session с escaping.
- [x] 7.5. Интеграционные тесты runtime: система формируется с инъекциями (prepend system, append system, messages target). — **Готово**: `injections/integration_test.go` (`TestApplyInjections_Integration`, `TestApplyInjections_EmptyFastPath`, `TestCollectInjections_MixesSources`).
- [x] 7.6. Тесты горячего применения (session add/remove влияет на следующий turn). — **Готово**: `server/chatassist_test.go:TestSessionInjectionHotApply` (two subtests: CRUD + AssistantReceives).
- [x] 7.7. Тесты обратной совместимости: ассистент без injections работает как раньше. — **Готово**: `runner_test.go:TestBackwardCompatibilityNoInjections`.
- [x] 7.8. Тесты replace-конфликтов и дедупликации (id) — TestDeduplicateByID, TestApplyInjections_WhenCondition, TestApplyInjections_DisabledSkipped.
- [x] 7.9. Негативные: некорректный when, большой content (TestApplyInjections_ContentTruncation: 10KB max), циклы шаблонов/MaxPasses (защита). — **Исправлено**: `TestApplyInjections_TotalSizeLimit` (обновлён content size до MaxContentSize+100, 5 инъекций, 5 × 10257 > 50000).

## Этап 8: Безопасность, производительность, наблюдаемость

- [x] 8.1. Доступ к env ограничен **allowlist'ом** (`AI_`, `CODEGEN_`,
  `OLLAMA_`, `KANBAN_` + `AI_INJECTION_ENV_ALLOW`) поверх denylist по
  чувствительным именам (`*_API_KEY`, `*_TOKEN`, `*_SECRET`, `*_PASSWORD`,
  `*_CREDENTIAL`). Раньше `{{env.PATH}}` и
  `{{env.AWS_SECRET_ACCESS_KEY}}` подставляли настоящие значения.
- [x] 8.2. Ограничение размера content: max per injection (10KB, MaxContentSize), total (50KB, MaxTotalSize). Truncate + ReplaceWarning.
- [x] 8.3. Условия `when` только безопасные (boolean expr, без exec). Детерминированные (whitelist, JEXL, simple boolean).
- [x] 8.4. Производительность: простая сборка (merge O(M) M=injections), шаблоны — лёгкие (replace), без тяжёлых вычислений в hot path.
- [x] 8.5. Observability: `MergeResult.Applied`/`Skipped` +
  `DescribeApplied`/`DescribeSkipped` в debug-лог (только метаданные), плюс
  записи в лог проекта и шину доски о действиях пользователя.
- [x] 8.6. Избегать утечки PII в логи (логируем только метаданные инъекций). Debug-логи `ReplaceWarning` в `MergeResult.Applied`.

## Этап 9: Фазы реализации (пошагово)

### Фаза 0 — Подготовка (анализ)
- [x] 0.1. Прочитать 2.1–2.5, зафиксировать точки сборки system/messages (exact paths).
- [x] 0.2. Согласовать модель Injection (3.1) перед кодом.

### Фаза 1 — Модель + парсинг
- [x] 1.1. Добавить типы Injection в `models/` (Go) + `board/entity.go`.
- [x] 1.2. Расширить Assistant (injections []Injection). — `chatassist.Assistant.Injections` + `GetInjections()`.
- [x] 1.3. Валидация + defaults + обратная совместимость.

### Фаза 2 — Пайплайн
- [x] 2.1. Реализовать `injections/merger.go` (apply pipeline: collect, filter, sort, merge).
- [x] 2.2. Юнит-тесты (исправлен import в `merger_test.go`).

### Фаза 3 — Интеграция в runtime (работающая модель)
- [x] 3.1. Внедрить ApplyInjections в `runner/runner.go` перед LLM call. **Исправлено B1**: `board.InjectionsFromContext`, убран `runctx`, удалён dead code `taskInjsToModelInjections`.
- [x] 3.2. Session-scoped storage + API методов. — `Session.sessionInjections` + `AddSessionInjection` / `RemoveSessionInjection` + `POST/DELETE/GET API` endpoints.
- [x] 3.3. Интеграционные тесты + backward compat. — `integration_test.go`, `TestBackwardCompatibilityNoInjections`.

### Фаза 4 — Конфиг/глобальные + шаблоны
- [ ] 4.1. Глобальные инъекции (config). — **Не реализовано**: нет слота в `server.Config` (пробел 3.3/6.2 оставлен открытым).
- [x] 4.2. Шаблонизатор с whitelist vars/env.
- [x] 4.3. Условия `when`.

### Фаза 5 — Тесты, доки, полировка
- [x] 5.1. Покрытие тестами (все unit, integration, hot-apply, backward compat — зелёные).
- [x] 5.2. Обновить `docs/20-features/chat-assistant.md` (примеры injections, таблица API, ограничения, allowlist окружения).
- [ ] 5.3. Обновить схемы/примеры Assistant YAML/JSON. — **Опционально**: `Task.Injections` уже сериализуется JSON → `json:"injections,omitempty"`. Assistant YAML/JSON keys пока не планируется.
- [ ] 5.4. Smoke-тест в работающей сессии (ручной E2E, Lua — проверить отключение, произведение, сессия добавления.

## Критерии приёмки

- [x] Инъекции применяются к работающей модели в реальном времени (следующий turn видит изменения session injections, без перезапуска сервера).
- [x] Пайплайн global→assistant→session→runtime соблюдён, priority+position работают корректно.
- [x] Prepend/append/replace/inject_at_index работают по заявленным target.
- [x] Обратная совместимость: ассистенты без `injections` не ломаются (fast-path).
- [x] Условия `when` фильтруют корректно, шаблоны рендерятся с whitelist.
- [x] Тесты зелёные (unit+integration+hot-apply+backward-compat), нет регрессий существующих тестов.
- [x] Документация обновлена, примеры рабочие.
- [x] Безопасность: запрет чувствительных env по умолчанию, лимиты размера.

## Риски и митигации

- [x] Конфликты replace: зафиксирована стратегия (scope priority + warning). Митигация — логирование + тесты. `ReplacementWarning` → `MergeResult.Applied` и `MergeResult.ReplaceWarning`.
- [x] Утечка секретов в шаблонах: **allowlist** env-префиксов поверх denylist
  чувствительных имён (`injections/render.go`); `injections/render_test.go`
  фиксирует, что `{{env.AWS_SECRET_ACCESS_KEY}}` и `{{env.PATH}}` больше не
  подставляют значения.
- [x] Переполнение промпта: лимиты total size + truncation + warning (`MaxContentSize`, `MaxTotalSize`).
- [x] Циклы/рекурсия шаблонов: max passes (1..2), детект зацикливания (`MaxTemplatePasses = 2`), детект нахождения одного и того же выражения.
- [x] Регрессии производительности: применять только при наличии инъекций (fast-path). `injections/merger.go`, ApplyInjections — если injs == 0, return baseSystem/baseMessages без изменений.
- [x] Хрупкая интеграция (много точек сборки): вынести в единый PromptAssembler (ранее все файлы/пути интеграции в служебной hygiene (`ApplyInjections`), в файле `injections/merger.go`).

---

## Упражнение (задокументированные блокеры, решены)

### B1. Циклический импорт `runctx ↔ runner` (РЕШЁНО)
`runctx/runctx.go:15` импортирует `ai/runner`, но `runner/runner.go:8` импортировал `ai/runctx`. Не компилировалось.

**Влияние:** `runner/runner.go:587` — `runctx.InjectionsFromContext(ctx)` — функция в `board`, не в `runctx`.

**Лечение:**
- Заменить `runctx.InjectionsFromContext(ctx)` на `board.InjectionsFromContext(ctx)` (runner:586)
- Удалить неиспользуемый импорт `"ai/runctx"` из runner.go
- Удалить неиспользуемый импорт `"ai/board"` из runctx/runctx.go

### B2. Синтаксическая ошибка импорта в тесте (РЕШЁНО)
`injections/merger_test.go:8:4` — `ai/board` без кавычек и запятой, вместо `"ai/board"`.

**Лечение:** исправлен импорт `"ai/board",`.

### B3. Несоответствие content size в тесте (РЕШЁНО)
`TestApplyInjections_TotalSizeLimit`: `strings.Repeat("a", MaxContentSize)` (10240). При content ровно `MaxContentSize` байт, условие `>MaxContentSize` не сработает, truncation не произойдёт, и 5 × 10240 = 51200 = MaxTotalSize (не больше).

**Лечение:** content size изменён на `MaxContentSize + 100`, так что truncation сработает (`content[:10240] + "… [truncated"` = ~10257 байт), и 5 × 10257 = 51285 > 50000.

### B4. Неправильное утверждение в тесте ScopeOrder (РЕШЁНО)
`TestApplyInjections_ScopeOrder`: проверка `gIdx < sIdx && sIdx < aIdx && aIdx < rIdx` (G<S<A<R). Но при merge с `append` и scope order G(0), A(1), S(2), R(3) — содержимое получается в порядке G → A → S → R, не G → S → A → R.

**Лечение:** исправлена assertion на `gIdx < aIdx && aIdx < sIdx && sIdx < rIdx` (G < A < S < R).

### B5. Dead code `taskInjsToModelInjections` (РЕШЁНО)
В конце `runner/runner.go` была функция, которая конвертирует `board.Injection` в `models.Injection`. Нигде не вызывалась, использовала невычитанную import `models`, создавала fail-compile.

**Лечение:** удалена целиком (была на последних строках runner.go:1260-1283).

---

## Аудит: расхождения плана и кода (2026-10-05)

План проходил приёмку по документам, а не по коду. Сверка выявила пункты,
отмеченные как готовые, но не реализованные или реализованные иначе, плюс
дефекты, которые сама приёмка не поймала. Все пункты ниже закрыты кодом и
тестами, кроме честно помеченных 3.3/6.2/4.1 (глобальный конфиг).

### А-1. POST инъекции возвращал id, которого не существовало

`handleAddInjection` присваивал `inj.ID = randomID()` **после** вызова
`AddSessionInjection`. Ответ отдавал новый id, в сессии лежала запись с
`ID: ""`. Клиентский `DELETE` по выданному id отвечал `200`, ничего не удалял, а
инъекция продолжала уходить в модель. Цикл `POST → GET → DELETE` не проходил
ни в одном тесте.

**Лечение:** id присваивается до сохранения, `UpsertSessionInjection` перезаписывает
запись с тем же id вместо добавления дубля, `RemoveSessionInjection` возвращает
признак наличия записи, а `DELETE` по чужому id отвечает `404`. Тесты:
`server/injections_api_test.go:TestSessionInjectionRoundTripKeepsReturnedID`,
`TestSessionInjectionPostWithIDUpserts`,
`TestSessionInjectionDeleteUnknownIs404`.

### А-2. Инъекции задачи было нечем записать

`Task.Injections` был полем только для чтения: задача корректно монгритилась в
Redis, но ни REST, ни инструмент доски не позволяли их задать. Фича «применить к
конкретной задаче» не имела пути записи.

**Лечение:** `POST`/`DELETE .../tasks/{tid}/injections[/{injID}]`,
`injections` в патче `PUT .../tasks/{tid}` (`board.DecodeInjections` +
`ValidateInjections`), проп `injections` в `BoardUpdateTask` — то есть
указание можно дать и из чата. Тесты:
`TestTaskInjectionsEndpoints`, `TestTaskInjectionsPatchValidation`,
`tools/board_injections_test.go`.

### А-3. Инъекции применялись один раз на прогон, resume их не получал

Инъекции впечатывались в историю до цикла и в ветку resume. Правка инъекций
посреди работы агента не доходила до модели: следующий раунд видел старый текст
из «чистой» истории. Resume-сегмент (после рестарта) не получал инъекций вовсе.

**Лечение:** `messages` остаётся чистой историей, а каждый запрос строится как
`request := applyInjections(messages, round)`. Тесты:
`runner/injections_test.go:TestTaskInjectionAppliedOnNextModelCall`,
`TestTaskInjectionAppliedOnResumeSegment`, `TestTaskInjectionNotDuplicatedAcrossRounds`,
`agents/planner/kanban_injections_test.go:TestTaskInjectionAddedDuringRunApplies`
(последний падает, если из `kanban.go` убрать живой источник).

### А-4. `before_tools`/`after_tools` принимались, но не работали

Значения были в модели данных и проходили валидацию, хотя код их не
реализует: запись выглядела применённой, а текст до модели не доходил.

**Лечение:** убраны из валидных `target`, `Applied` пишет только реально
применённые, тест отвергает их на записи.

### А-5. Утечка инъекций между задачами и в соседние циклы

`ctx = board.NewInjectionContext(ctx, t.Injections)` стояло **внутри** цикла по
готовым задачам, поэтому инъекция первой задачи оставалась в контексте второй.
Плюс живой источник инъекций задачи отсутствовал: цикл читал задачу только на
старте.

**Лечение:** контекст собирается в `taskCtx`, добавлен
`board.WithInjectionSource` (перечитывает задачу перед каждым запросом) и
`board.WithInjectionScope` (проект/задача/роль). Тест
`TestTaskInjectionReachesOwnSpecialist` падает при возврате `ctx = …`.

### А-6. Переменные условий `when` были пустыми

`model`, `provider`, `project`, `role`, `task_id` не заполнялись, поэтому
`when: "project == \"mytrip\""` не могло сработать никогда. У провайдеров не
было способа назвать себя.

**Лечение:** `ModelName()`/`ProviderName()` у обоих провайдеров,
`runner/injections.go:injectionMergeContext` заполняет контекст, у оценщика
появились упорядоченные сравнения для чисел. Тесты:
`runner/injections_test.go:TestInjectionWhenContextFilledWithRealValues`,
`injections/render_test.go:TestEvalFnDefaultWithMergeContext`.

### А-7. `{{env.*}}` отдавал произвольное окружение

Denylist ловил `*_TOKEN`/`*_PASSWORD`/`*_API_KEY`, но `{{env.PATH}}` и
`{{env.AWS_SECRET_ACCESS_KEY}}` подставляли настоящие значения — инъекция
утекала секреты в промпт и в лог запроса.

**Лечение:** allowlist префиксов `AI_`, `CODEGEN_`, `OLLAMA_`, `KANBAN_` плюс
`AI_INJECTION_ENV_ALLOW`, поверх него denylist по чувствительным именам.
Запрещённое имя → `[REDACTED]`, неразрешённый префикс → плейсхолдер остаётся.
Тесты: `injections/render_test.go:TestRenderEnvAllowlist`,
`TestRenderEnvSensitiveAlwaysRedacted`, `TestRenderEnvAllowlistExtendable`.

### А-8. Глобальный scope не имеет источника

`scope: global` проходил валидацию и попадал в сортировку, но источника данных
для него не существует — писать его было некуда.

**Лечение:** честно помечено как не реализованное (3.3, 6.2, 4.1) в плане и в
`docs/20-features/chat-assistant.md`: scope зарезервирован в порядке и
валидации, конфига приложения с ним нет. Задача не выдаётся за закрытую.

### А-9. Валидация молча пропускала мусор, отчётность была нулевой

Невалидные записи доходили до промпта (и обрезались по дороге), а «применена ли
инъекция» было видно только по факту. Плюс мёртвый код `models/injection*.go`
(~460 строк) дублировал модель и ничего не использовал.

**Лечение:** валидация на записи (REST/инструмент/`board.Store`) и на
применении (в `Skipped` с причиной), строгий декод JSON (опечатка в поле → 400,
`board.DecodeInjections`), дедупликация по id в пайплайне, `DescribeApplied`/
`DescribeSkipped` в debug-лог без текста инъекции, мёртвые файлы удалены.

### А-11. Удалённые инъекции воскресали из стартового снимка

`InjectionSource` возвращала только список, поэтому «доска недоступна» и
«пользователь удалил все инъекции» выглядели одинаково — и `AllInjections`
откатывалась к снапшоту. Итог: `DELETE` отвечал `200`, а текст продолжал уходить
в модель до конца прогона — ровно тот класс дефекта, что и в А-1, только в
рантайме.

**Лечение:** `InjectionSource` возвращает `([]Injection, error)`, а живой
источник при успешном чтении **заменяет** снимок целиком (они читают одну и ту
же запись). Это важно и для частичного удаления: слияние «снимок + живой»
возвращало бы удалённые записи из снимка. Откат на снимок — только по ошибке
чтения. Тесты: `runner/injections_test.go:TestTaskInjectionDeletionBeatsSnapshot`,
`TestTaskInjectionPartialDeletionKeepsTheRest`,
`TestTaskInjectionSourceErrorFallsBackToSnapshot`,
`board/injection_test.go:TestAllInjectionsLiveSourceReplacesSnapshot`,
`TestAllInjectionsPartialDeletionWins`, `TestAllInjectionsEmptyLiveDropsSnapshot`,
`agents/planner/kanban_injections_test.go:TestTaskInjectionRemovedDuringRunStopsApplying`.

### А-10. Документация расходилась с кодом и содержала мусор

Раздел инъекций в `docs/20-features/chat-assistant.md` утверждал, что
`global` берётся из `server.Config` (не реализовано), называл несуществующие
`collectInjs`, обещал `before_tools`/`after_tools`, называл переменные условий
`has_tools` (в коде — `tools`), а в таблице API и примере содержал китайские
вставки и сломанные кавычки (`注入`, `"禁用特定模型"`).

**Лечение:** раздел переписан по факту реализации — области, оба семейства API,
валидация, allowlist окружения, ограничения, «когда применяется», разделение
«сессия = чат, задача = агентский прогон».
