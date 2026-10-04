# PLAN-2026-10-04-todo-assistant-injections

Инъекции через Assistant для работающей модели (runtime injections).

Цель: дать возможность настраивать системные/промт-инъекции глобально и на уровне конкретного Assistant (и динамически в сессии), чтобы они применялись к работающей модели в реальном времени без перезагрузки сервера/агента, с чётким пайплайном приоритизации и безопасным мерджем.

---

## Этап 1: Термины, проблема, область

- [ ] 1.1. Определить ключевые термины: Injection (prompt/system/context), scope (global, assistant, project, session, turn), position (prepend/append/replace), apply-point (system, messages, pre-call).
- [ ] 1.2. Зафиксировать проблему: текущий Assistant не имеет механизма структурированных инъекций, применяемых в runtime при формировании запроса к LLM.
- [ ] 1.3. Определить границы: влияем только на формирование промпта/сообщений перед вызовом модели (не на хранение чата, не на инструменты, кроме передачи). Обратная совместимость обязательна.
- [ ] 1.4. Собрать референсы: `docs/20-features/chat-assistant.md`, `agents/chatassist/`, `llm/`, `server/llm*`, `models/assistant*`, точки сборки system prompt и формирования messages.

## Этап 2: Анализ текущей архитектуры

- [ ] 2.1. Найти, где формируется system prompt для работающей модели (chat session / LLM call). Просмотреть `server/chat*`, `server/llm*`, `llm/provider*`, `runner/*`.
- [ ] 2.2. Найти структуру Assistant (модель): `models/assistant*`, `config/assistant*`, загрузка ассистентов.
- [ ] 2.3. Определить точки применения в работающей сессии (runtime): до сборки system, при сборке messages, pre-call hooks (если есть).
- [ ] 2.4. Зафиксировать, какие поля уже есть (tools, system, instructions, temperature, model, etc.). Убедиться, что добавление не ломает сериализацию.
- [ ] 2.5. Составить карту потоков: Assistant -> Session -> LLM Request.

## Этап 3: Модель данных инъекций

- [ ] 3.1. Спроектировать тип `Injection`:
  - `id` (string, опц.), `name`, `scope` (global|assistant|session|runtime)
  - `target` (system|messages|user_last|assistant_last|before_tools|after_tools) — точка вставки
  - `position` (prepend|append|replace|inject_at_index)
  - `index` (int, опц.)
  - `content` (string, template) — основной текст инъекции
  - `when` (expr/condition, опц.) — условие применения (role, model, project, tools present)
  - `enabled` (bool, default true)
  - `priority` (int, default 0) — приоритет внутри scope
  - `vars` (map[string]any, опц.) — переменные шаблона
  - `tags` ([]string, опц.)
- [ ] 3.2. Добавить поле `injections` в модель Assistant: `[]Injection` (обратная совместимость, omitempty).
- [ ] 3.3. Добавить глобальные инъекции: конфиг уровня приложения (config/env или `opencode.json`/хранилище). Определить источник (file/env).
- [ ] 3.4. Добавить runtime-инъекции на уровне сессии (session-scoped) — возможность добавлять/удалять в работающей сессии без перезапуска ассистента.
- [ ] 3.5. Определить формат сериализации (JSON/YAML). Обновить схемы валидации Assistant.
- [ ] 3.6. Добавить миграции/валидацию (unknown fields, дубликаты id).

## Этап 4: Пайплайн применения (merging)

- [ ] 4.1. Определить порядок применения scope: `global` → `assistant` → `project` (если есть) → `session` → `runtime/turn`.
- [ ] 4.2. Правила приоритизации внутри scope: по `priority` (desc/high-first или asc по договорённости), при равенстве — порядок объявления (stable order).
- [ ] 4.3. Правила мержа по target+position:
  - `prepend` — добавляет перед целевым блоком (в порядке приоритета)
  - `append` — после (в порядке приоритета)
  - `replace` — замещает целевой блок (высший приоритет среди replace или явное правило)
  - `inject_at_index` — вставка по индексу в messages/system блоке
- [ ] 4.4. Дедупликация: по `id` (позже перекрывает), по содержимому (опц., флаг).
- [ ] 4.5. Обработка конфликтов replace: определить стратегию (last-write-wins по scope+priority, с логом/предупреждением).
- [ ] 4.6. Фильтрация `when` (условия): доступные переменные (model, provider, role, tools, project, turn, user, has_files, etc.). Реализовать безопасный eval (whitelist/JEXL/simple boolean).
- [ ] 4.7. Рендер шаблонов: поддержка переменных `{{vars.*}}`, `{{env.*}}`, `{{session.*}}`, `{{assistant.*}}` (sandbox, без выполнения кода).

## Этап 5: Интеграция в работающую модель (runtime apply)

- [ ] 5.1. Найти единое место сборки final system/messages перед LLM call (builder/assembler). Выделить `PromptAssembler`/`RequestBuilder`.
- [ ] 5.2. Внедрить этап `ApplyInjections` в этом билдере: вход (base system, messages, ctx), выход (modified).
- [ ] 5.3. Применять инъекции ПОСЛЕ базового system (assistant.system/instructions), но ДО инструментов/контекста (ясно зафиксировать порядок). Документировать.
- [ ] 5.4. Поддержка target=`messages`: вставка в массив messages (по role/index) с учётом prepend/append к user/assistant последним.
- [ ] 5.5. Session-scoped injections API: методы add/remove/update/list на сессии (hot apply, без рестарта). Хранить в `ChatSession`/контексте сессии.
- [ ] 5.6. Hot-reload: отслеживание изменений assistant/config (если ассистент перезагружен) — применять новые инъекции с следующего turn (не мутировать текущий запрос).
- [ ] 5.7. Per-turn/runtime injections: возможность передать инъекции в запросе (опц., с флагом safety/denylist).
- [ ] 5.8. Логирование: debug-логи применённых инъекций (id, scope, target, position) без утечки секретов в content (только метаданные).

## Этап 6: API/Конфиг и UX

- [ ] 6.1. Обновить загрузку Assistant (YAML/JSON): парсинг `injections` с валидацией.
- [ ] 6.2. Добавить глобальный конфиг: `injections` в app config (приоритет global). Определить путь конфигурации.
- [ ] 6.3. (Опц.) API для управления session injections: REST/WS (add/remove/list) — если есть чат API.
- [ ] 6.4. (Опц.) UI: отображение активных инъекций (debug/panel) — не блокер реализации ядра.
- [ ] 6.5. Документация структуры в `docs/20-features/chat-assistant.md` + примеры.

## Этап 7: Тестирование

- [ ] 7.1. Юнит-тесты модели: парсинг Injection, валидация, defaults.
- [ ] 7.2. Юнит-тесты пайплайна merge: порядок scope, priority, prepend/append/replace, inject_at_index.
- [ ] 7.3. Юнит-тесты условий `when` (whitelist, false/true пути).
- [ ] 7.4. Юнит-тесты шаблонов (vars/env/session с escaping).
- [ ] 7.5. Интеграционные тесты runtime: система формируется с инъекциями (prepend system, append system, messages target).
- [ ] 7.6. Тесты горячего применения (session add/remove влияет на следующий turn).
- [ ] 7.7. Тесты обратной совместимости: ассистент без injections работает как раньше.
- [ ] 7.8. Тесты replace-конфликтов и дедупликации (id).
- [ ] 7.9. Негативные: некорректный when, большой content, циклы шаблонов (защита).

## Этап 8: Безопасность, производительность, наблюдаемость

- [ ] 8.1. Whitelist переменных шаблонов/env (не пропускать секреты произвольно). Запретить доступ к чувствительным env по умолчанию.
- [ ] 8.2. Ограничение размера content (max per injection, total). Truncate/log при превышении.
- [ ] 8.3. Условия `when` только безопасные (boolean expr, без exec). Детерминированные.
- [ ] 8.4. Производительность: простая сборка (merge O(M) M=injections), шаблоны — лёгкие (replace), без тяжёлых вычислений в hot path.
- [ ] 8.5. Observability: метрики кол-ва применённых, счётчики по target/scope (опц.), трассировка (debug).
- [ ] 8.6. Избегать утечки PII в логи (логируем только метаданные инъекций).

## Этап 9: Фазы реализации (пошагово)

### Фаза 0 — Подготовка (анализ)
- [ ] 0.1. Прочитать 2.1–2.5, зафиксировать точки сборки system/messages (exact paths).
- [ ] 0.2. Согласовать модель Injection (3.1) перед кодом.

### Фаза 1 — Модель + парсинг
- [ ] 1.1. Добавить типы Injection в models/config (Go/TS в зависимости от кодовой базы). 
- [ ] 1.2. Расширить Assistant (injections []Injection).
- [ ] 1.3. Валидация + defaults + обратная совместимость.

### Фаза 2 — Пайплайн
- [ ] 2.1. Реализовать `injections/merger.go` (apply pipeline: collect+filter+sort+merge).
- [ ] 2.2. Юнит-тесты 7.1–7.4.

### Фаза 3 — Интеграция в runtime (работающая модель)
- [ ] 3.1. Внедрить PromptAssembler с ApplyInjections в точке LLM call.
- [ ] 3.2. Session-scoped storage + API методов.
- [ ] 3.3. Интеграционные тесты 7.5–7.6.

### Фаза 4 — Конфиг/глобальные + шаблоны
- [ ] 4.1. Глобальные инъекции (config).
- [ ] 4.2. Шаблонизатор с whitelist vars/env.
- [ ] 4.3. Условия `when`.

### Фаза 5 — Тесты, доки, полировка
- [ ] 5.1. Покрытие тестами (7.1–7.9), зелёные.
- [ ] 5.2. Обновить `docs/20-features/chat-assistant.md` (примеры injections).
- [ ] 5.3. Обновить схемы/примеры Assistant YAML/JSON.
- [ ] 5.4. Smoke-тест в работающей сессии (ручной E2E) — проверить prepend/append/replace, session add.

## Критерии приёмки

- [ ] Инъекции применяются к работающей модели в реальном времени (следующий turn видит изменения session injections, без перезапуска сервера).
- [ ] Пайплайн global→assistant→session соблюдён, priority+position работают корректно.
- [ ] Prepend/append/replace/inject_at_index работают по заявленным target.
- [ ] Обратная совместимость: ассистенты без `injections` не ломаются.
- [ ] Условия `when` фильтруют корректно, шаблоны рендерятся с whitelist.
- [ ] Тесты зелёные (unit+integration), нет регрессий существующих тестов.
- [ ] Документация обновлена, примеры рабочие.
- [ ] Безопасность: запрет чувствительных env по умолчанию, лимиты размера.

## Риски и митигации

- [ ] Конфликты replace: зафиксировать стратегию (scope priority + warning). Митигация — логирование + тесты.
- [ ] Утечка секретов в шаблонах: whitelist vars/env, denylist `OPENAI_API_KEY*`, `*_TOKEN`, `*_SECRET` по умолчанию.
- [ ] Переполнение промпта: лимиты total size + truncation + warning.
- [ ] Циклы/рекурсия шаблонов: max passes (1–2), детект зацикливания.
- [ ] Регрессии производительности: применять только при наличии инъекций (fast-path).
- [ ] Хрупкая интеграция (много точек сборки): вынести в единый PromptAssembler, покрыть тестами.
