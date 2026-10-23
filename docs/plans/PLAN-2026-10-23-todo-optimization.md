# PLAN-2026-10-23-todo-optimization — Оптимизация: детерминированные функции + производительность

Дата: 2026-10-23
Статус: todo
Автор: opencode (full-project analysis)
Контекст: Полный аудит выявил: (1) 4 дубликата truncate-логики, шаблоны/конды в замыканиях с глобальными deps; (2) N+1 Redis в board store — до 49 RTT на одну операцию AllDone; (3) json.Marshal в hot path loop; (4) uncached filesystem walk; (5) глобальный mutex в logger.

## 1. Обнаруженные проблемы

### 1.1 Производительность (по severity)

| ID | Severity | Тип | Файл(ы) | Описание |
|---|---|---|---|---|
| **P-C1** | CRITICAL | N+1 Redis | `infrastructure/board/store_task.go:358-375` | `ListTasks`: SMembers + NxGET (20 задач = 21 RTT) |
| **P-C2** | CRITICAL | N+1 Redis chain | `infrastructure/board/store_meta.go:99-133` | `AllDone`: ListTasks + ListEpics + ListBugs = ~49 RTT |
| **P-C3** | CRITICAL | N+1 Redis cascade | `infrastructure/board/store_epic.go:190-290` | setEpicTasksStatus, pauseEpicTasks, resumeEpicTasks, DeleteEpic (вложенный N+1: каждая задача = 5-8 RTT) |
| **P-H1** | HIGH | Hot-path alloc | `application/usecases/loop.go:21-25` | `CallSignature` = json.Marshal(args) на каждый tool-call |
| **P-H2** | HIGH | Uncached FS walk | `shared/projects/projects.go:31-43` | `moduleRoot()` — os.Stat на каждый вызов |
| **P-H3** | HIGH | Full msg rebuild | `infrastructure/llm/OpenAIProvider.go:85-124` | Полная реконверсия []Message на каждый LLM вызов |
| **P-M1** | MEDIUM | Global mutex | `shared/logging/logging.go:269-301` | sync.Mutex на каждый log write — goroutine pileup |
| **P-M2** | MEDIUM | Full struct R/W | `infrastructure/board/store_task.go:106-111` | `SetTaskInjections`: полный read-modify-write Task (~500+ байт) |
| **P-M3** | MEDIUM | Blocking counter | `infrastructure/kafkarelay/relay.go:248-258` | Синхронный INCR в Kafka write loop — 1-3ms на шаг |
| **P-M4** | MEDIUM | Regression recompute | `shared/tokens/predict.go:94-180` | `NewPredictor` пересчитывает XTX + Gauss на каждый вызов |
| **P-L1** | LOW | Double string alloc | `domain/sanitize.go:37-38` | Два strings.ReplaceAll -> два intermediate string |

### 1.2 Код-качество (детерминизм)

| ID | Severity | Тип | Файл(ы) | Описание |
|---|---|---|---|---|
| **Q-H1** | HIGH | Closure to pure func | `infrastructure/injections/render.go:84-126` | `RenderFnDefault` читает глобальный `envAllowlist` + `os.LookupEnv` |
| **Q-H2** | HIGH | Closure to pure func | `infrastructure/injections/render.go:132-154` | `EvalFnDefault` — чистая логика, но вложена в infra |
| **Q-H3** | HIGH | Duplication | `merger.go:354`, `digest.go:348`, `loopstate.go:200`, `compress.go:174` | 4 дубликата truncate logic |
| **Q-M1** | MEDIUM | Hidden state | `domain/log/digest.go:229-235` | `logTemplate` — пакетный срез `logTemplateRules`, невозможно кастомизировать |
| **Q-M2** | MEDIUM | Infra contamination | `infrastructure/chat/store.go:176-248` | Сериализация ChatMessage <-> Redis fields смешана с infra |
| **Q-M3** | MEDIUM | Overloaded func | `domain/diff/diff.go:42-173` | `ParseBlock` делает 3 вещи: path, status, count |
| **Q-M4** | MEDIUM | Hardcoded normalizer | `application/usecases/loopstate.go:64-168` | Хардкод нормализации команд |

### 1.3 Уже хорошо (не трогать)

- `infrastructure/llm/toollama.go` — чистые package-level функции
- `domain/sanitize.go:SanitizeToolOutput` — идеальная чистая функция
- `shared/tokens/pricing.go` — чистые value-receiver методы
- `shared/tokens/scoped.go:ParseScope` — чистая
- `domain/injection.go:Validate` — чистый метод
- `application/usecases/policy.go:ToolResultFailed` — чистая
- `shared/tokens/predict.go:median, solve` — чистые
- `domain/plan.go:ComputeWaves` — чистый метод (топ. сортировка)

## 2. План правок (по приоритету)

### 2.1 P-C1/P-C2/P-C3 — N+1 Redis Pipeline (CRITICAL)

**Файлы:** `infrastructure/board/store_task.go`, `store_epic.go`, `store_meta.go`
**Охват:** ~300 строк изменений + тесты

**Что исправить:**

- [ ] Создать helper `func batchGet(client *redis.Client, keys []string) ([]interface{}, []error)` — оберточка над `client.Pipelined()`
- [ ] `ListTasks` (store_task.go:358-375): заменить for + GetTask на batch — один Pipelined с GET
- [ ] `ListEpics` (store_epic.go:91-108): аналогично
- [ ] `AllDone` (store_meta.go:99-133): один Pipelined для всех keys всех типов -> 1 RTT вместо ~49
- [ ] `CreateTask` (store_task.go:36-40): batch EXISTS для всех dependencies
- [ ] Каскады статуса (store_epic.go:190-264): batch SET + SADD для всех задач в один pipeline
- [ ] `DeleteEpic` (store_epic.go:281-290): batch Del + SRem
- [ ] `MoveTask` (store_task.go:444-471): reads -> pipeline, writes -> pipeline

**Тесты:**
- [ ] `TestListTasksSingleRoundTrip` — 1 RTT для 20 задач (mock redis)
- [ ] `TestAllDoneSingleRoundTrip` — 1 RTT для 20 задач + 5 эпиков + 3 багов
- [ ] `TestCascadeStatusSingleRoundTrip` — 1 RTT для batch status change

**Критерий успеха:** 70-90% снижение latency board операций; 1 RTT вместо O(N).

---

### 2.2 Q-H3 — Унификация Truncate (HIGH)

**Файлы:** `infrastructure/injections/merger.go`, `domain/log/digest.go`, `application/usecases/loopstate.go`, `application/usecases/compress.go`
**Охват:** ~40 строк (4 функции -> 1 + 4 вызова)

**Что исправить:**

- [ ] Создать `shared/strutil/truncate.go` с `func Truncate(s string, maxBytes int, marker string) string`
- [ ] UTF-8 boundary safety из TruncateLogText — default поведение
- [ ] Заменить все 4 дубликата: merger.go:354, digest.go:348, loopstate.go:200, compress.go:174

**Тесты:**
- [ ] TestTruncateUTF8Boundary, TestTruncateNoOp, TestTruncateMarkerVariants

**Критерий успеха:** 4 дубликата -> 1 функция, ~30 строк экономии, UTF-8 safety везде.

---

### 2.3 Q-H1 — RenderFnDefault -> чистая функция (HIGH)

**Файл:** `infrastructure/injections/render.go:84-126`
**Охват:** ~42 строки

**Что исправить:**

- [ ] Извлечь named function с параметрами: content, MergeContext, envAllowlist, envLookup
- [ ] Переписать как named function (не closure)
- [ ] В MergeInjections передать os.LookupEnv и envAllowlist явно

**Тесты:**
- [ ] TestRenderTemplateBasicVars, TestRenderTemplateEnvLookup, TestRenderTemplateAllowlist

**Критерий успеха:** тестирование без моков os.LookupEnv; глобальный envAllowlist удалён.

---

### 2.4 Q-H2 — EvalFnDefault -> чистая функция (HIGH)

**Файл:** `infrastructure/injections/render.go:132-154`
**Охват:** ~22 строки

**Что исправить:**

- [ ] Извлечь `func EvaluateCondition(when string, lookup map[string]string) (bool, error)`
- [ ] Помощники (evalExpr, evalAnd, evalNot и т.д.) — оставить как есть (уже чистые)

**Тесты:**
- [ ] TestEvaluateConditionBasic (==, !=), Comparison (<, >, <=, >=), Logical (&&, ||, !, ())

**Критерий успеха:** EvaluateCondition переиспользуема для board-фильтров, config-правил.

---

### 2.5 P-H1 — CallSignature без json.Marshal (HIGH)

**Файл:** `application/usecases/loop.go:21-25`
**Охват:** ~10 строк

**Что исправить:**

- [ ] Заменить json.Marshal(args) на deterministic string с strings.Builder + sorted keys
- [ ] Или: encoding/json с sync.Pool[bytes.Buffer] для reuse

**Тесты:**
- [ ] TestCallSignatureDeterministic, TestCallSignatureEmptyArgs, Benchmark

**Критерий успеха:** 10-30ms экономии на agent run; детерминированный результат.

---

### 2.6 P-H2 — Кэш moduleRoot (HIGH)

**Файл:** `shared/projects/projects.go:31-43`
**Охват:** ~5 строк

**Что исправить:**

- [ ] Заменить на sync.OnceValue (Go 1.21+)

**Тесты:**
- [ ] TestModuleRootCached — второй вызов не делает FS access

**Критерий успеха:** near-zero latency после первого вызова; один os.Stat per process.

---

### 2.7 P-H3 — OpenAI message conversion cache (HIGH)

**Файл:** `infrastructure/llm/OpenAIProvider.go:85-124`
**Охват:** ~40 строк

**Что исправить:**

- [ ] Вариант A (simple): кэшировать oaTools — вычислять один раз при инициализации
- [ ] Вариант B (amortized): pointer comparison req.Messages, reuse конвертированный массив

**Тесты:**
- [ ] TestOpenAIProviderToolsCached, TestOpenAIProviderMessagesReused

**Критерий успеха:** 5-15ms экономии на LLM call (при 50+ message history).

---

### 2.8 Q-M1 — Параметризировать logTemplate (MEDIUM)

**Файл:** `domain/log/digest.go:229-235`
**Охват:** ~20 строк

**Что исправить:**

- [ ] Сделать logTemplateRules настраиваемым параметром
- [ ] Экспортировать `func BuildLogTemplate(rules []TemplateRule) func(string) string`

**Критерий успеха:** кастомные правила нормализации на проект.

---

### 2.9 Q-M2 — Вынести сериализацию ChatMessage (MEDIUM)

**Файл:** `infrastructure/chat/store.go:176-248`
**Охват:** ~47 строк

**Что исправить:**

- [ ] Создать domain/chat_codec.go или infrastructure/chat/codec.go:
  ```go
  func MessageToFields(m Message) map[string]any
  func MessageFromFields(values map[string]any) Message
  ```

**Критерий успеха:** сериализация тестируема без Redis; reusable для file-based persistence.

---

### 2.10 P-M1 — Logger: per-project mutex или async writer (MEDIUM)

**Файл:** `shared/logging/logging.go:269-301`
**Охват:** ~15 строк

**Что исправить:**

- [ ] Вариант A: per-project sync.Mutex в AppLogs
- [ ] Вариант B: buffered async writer (channel + goroutine)
- [ ] Вариант C: POSIX atomic writes (для коротких записей < PIPE_BUF)

**Критерий успеха:** отсутствие goroutine pileup при параллельных агентах.

---

### 2.11 P-M2 — Redis HSET для патчей (MEDIUM)

**Файл:** `infrastructure/board/store_task.go:106-111`
**Охват:** ~10 строк

**Что исправить:**

- [ ] SetTaskInjections -> client.HSET(key, "injections", ...) вместо полного GET->modify->SET
- [ ] Аналогично для SetAgentState, SetComments и других partial updates

**Критерий успеха:** один Redis call вместо трёх (GET + modify + SET).

---

### 2.12 P-M3 — Local atomic counter для Kafka (MEDIUM)

**Файл:** `infrastructure/kafkarelay/relay.go:248-258`
**Охват:** ~10 строк

**Что исправить:**

- [ ] r.counter.next() -> atomic.AddUint64 local, с периодическим sync в Redis

**Критерий успеха:** 1-3ms экономии на Kafka step.

---

### 2.13 P-M4 — Кэш Predictor (MEDIUM)

**Файл:** `shared/tokens/predict.go:94-180`
**Охват:** ~10 строк (кэш обёртка)

**Что исправить:**

- [ ] Predictor кэшировать в use-case/service с invalidation на history change

**Критерий успеха:** O(1) predict после первого расчёта.

---

### 2.14 P-L1 — strings.NewReplacer для sanitize (LOW)

**Файл:** `domain/sanitize.go:37-38`
**Охват:** ~3 строки

**Что исправить:**

- [ ] Заменить два ReplaceAll на один `strings.NewReplacer("\r\n", "\n", "\r", "\n")`

**Критерий успеха:** одна аллокация вместо двух на каждый LLM response.

## 3. Порядок внедрения

| Фаза | ID | Что | Оценка |
|---|---|---|---|
| **Фаза 1 (критично)** | P-C1/P-C2/P-C3 | Redis Pipeline для board store | ~300 строк, 3 файла + тесты |
| **Фаза 2 (быстрый win)** | Q-H3, P-H2, P-L1 | Truncate унификация, moduleRoot cache, NewReplacer | ~60 строк, 3 файла |
| **Фаза 3 (качество)** | Q-H1, Q-H2, Q-M2 | Render/Eval чистые функции, chat codec | ~100 строк, 2 файла |
| **Фаза 4 (оптимизация)** | P-H1, P-H3, P-M1..P-M4 | CallSignature, OpenAI cache, logger, HSET, Kafka, Predictor | ~120 строк, 6 файлов |

## 4. Критерии успеха (общие)

1. `go build ./...` — exit 0
2. `go test -race -count=1 ./...` — exit 0 (включая новые тесты)
3. `go vet ./...` — exit 0
4. `gofmt -l .` — пусто (для изменённых файлов)
5. `make depgate` — exit 0
6. Redis round-trips для board операций: 1 вместо O(N)
7. Нет дубликата truncate-логики

## 5. Зависимости

- Нет внешних зависимостей. Все правки локальные.
- Фаза 1 (Redis pipeline) может быть выполнена независимо.
- Фаза 2 (быстрый win) может быть выполнена независимо.
- Фаза 3 (качество) может быть выполнена независимо.
- Фаза 4 (оптимизация) может быть выполнена независимо.
