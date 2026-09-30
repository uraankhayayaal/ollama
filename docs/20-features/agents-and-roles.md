# Агенты и роли

Каждый агент — это системный промпт + набор инструментов + цикл
`runner.Runner`. Различие между агентами в основном в **правах на доске** и в
**том, что им разрешено делать с кодом**.

## Реестр ролей

| Роль | Пакет | CLI-команда | Назначение |
|---|---|---|---|
| Системный архитектор | `agents/architect/` | только через `kanban` / `serve` | Публикует эпики, ревизии, вердикты по багам |
| Backend Lead | `agents/backendlead/` | `backendlead` | Декомпозиция бэкенда |
| Frontend Lead | `agents/frontendlead/` | `frontendlead` | Декомпозиция UI, стейта, API-контрактов |
| DevOps Lead | `agents/devopslead/` | `devopslead` | Декомпозиция инфраструктуры |
| QA Lead | `agents/qalead/` | `qalead` | Тест-план, триаж багов |
| Разработчик | `agents/developer/` | `backend`, `frontend` | Генерация и правка кода |
| DevOps-инженер | `agents/devops/` | `devops` | Docker Compose, K8s, CI/CD |
| QA-инженер | `agents/qaengineer/` | `qa` | Автотесты, баг-репорты |
| Код-ревьюер | `agents/codereviewer/` | `review` | Ревью Merge Request |
| Чат-ассистент | `agents/chatassist/` | только через `serve` | Обслуживание чата |
| Приёмка | `agents/acceptor/` | `accept` + финальный шаг плана | Сборка/запуск/проверки без LLM |
| Планировщик | `agents/planner/` | `plan`, `kanban` | Декомпозиция и исполнение |

> **Архитектора и чат-ассистента нельзя запустить как standalone-команду** —
> они доступны только через `kanban` и `serve` (`main.go:141-260`). В
> `projectFromArgs` они тоже не перечислены (`main.go:391-402`).

## Права на доске

Инструменты `Board*` выдаются **только при подключённом `board.Store`**
(`tools/registry.go:34-36`). Без доски агент автоматически деградирует до
файловых инструментов, а модель не жжёт раунды на заведомо мёртвые вызовы.

| Агент | Эпики | Задачи | Баги |
|---|---|---|---|
| Архитектор | Create / Update / Delete / SetStatus | — | Review |
| Лиды направлений | — | Create / Update / Delete / SetStatus | — (QA Lead: List/Get/SetStatus) |
| Специалисты | — | Get / SetStatus | Create (QA) |
| Чат-ассистент | Create / Update / Delete / SetStatus | SetStatus **только** | Create / SetStatus / Review |
| Ревьюер | — | — | — |

Инструментов правки задач у чат-ассистента нет намеренно (закреплено тестом
`agents/chatassist/agent_test.go:99`): создавать задачи должен лид, иначе
декомпозиция не согласована с архитектурой.

## Инструменты по ролям

| Агент | Инструменты работы с кодом | Навигация/контекст |
|---|---|---|
| Архитектор | `List`, `ReadFiles` | `Lsp*`, `CodeSearch`, `RagIndexStatus`, `DetectStack` |
| Backend/Frontend Lead | `List`, `ReadFiles`, `ReadMap`, `WriteFiles`, `AppendFile` | `Lsp*` |
| DevOps Lead | `List`, `ReadFiles`, `ReadMap`, `WriteFiles`, `AppendFile` | — (LSP не выдаётся) |
| QA Lead | как Frontend Lead | `Lsp*` |
| Разработчик | `WriteFiles`, `ReadFiles`, `ReadMap`, `DeleteFiles`, `Run`, `List`, `AppendFile`, `SearchReplace`, `PatchFunction`, `PatchGoFunction` | `LspCheck`, `ReadAppLogs`, `Lsp*`, `CodeSearch` |
| DevOps-инженер | `WriteFiles`, `ReadFiles`, `DeleteFiles`, `AppendFile`, `List`, `Run` | `ReadAppLogs`, `DetectStack` |
| QA-инженер | `WriteFiles`, `ReadFiles`, `DeleteFiles`, `AppendFile`, `List`, `Run` | `Lsp*`, `ReadAppLogs` |
| Ревьюер | — | `ReviewMr`, `ApproveMr`, `NextChunk` |
| Планировщик | `List`, `ReadFiles` | `LspDefinition`, `LspReferences`, `LspHover` (без `LspCheck`) |

`SetRAG` / `SetBoardStore` / `SetArchitectExtras` **пересобирают** набор
инструментов на лету (`agents/architect/agent.go:106-114`,
`agents/frontendlead/agent.go:94-100`, `agents/planner/kanban.go:143,153`).

## Обязательные первые раунды

Агенты могут не вызывать инструмент в первом раунде — сначала изучить
структуру (`List` → `ReadFiles`). Но у лидов задан **групп** обязательных
инструментов (`RequiredToolGroups`), а не один:

```go
// agents/backendlead/agent.go:114-124
[]tools.RequiredToolGroup{
    {Tools: []string{"List", "ReadFiles"}},
    {Tools: []string{"ReadMap"}},
}
```

Предварительные чтения и `AskUser` при этом разрешены — трактовка «группы»
меняет только требование к финальному набору, а не запрещает предварительные
чтения.

У архитектора и ассистента первый раунд не ограничен (`{"", false}`).

## Модель для роли

Двухуровневая маршрутизация (`models/layered.go`):

- **Тяжёлая модель** — лиды, архитектор, ревьюер, эксперт по багам, планировщик.
- **Лёгкая модель** — разработчики, DevOps, QA.
- `LLM_ALWAYS_HEAVY=1` — всё через тяжёлую.

## Настройка ролей

### Лиды направлений

```bash
BACKEND_MAX_FILES=0          # лимит файлов за запуск (0 = без)
BACKEND_NO_OVERWRITE=false   # запрет перезаписи существующих файлов
BACKEND_PLAN_FILE=BACKEND_PLAN.json   # куда дублировать JSON-декомпозицию

FRONTEND_MAX_FILES=0
FRONTEND_NO_OVERWRITE=false
FRONTEND_PLAN_FILE=FRONTEND_PLAN.json

DEVOPS_MAX_FILES=0
DEVOPS_NO_OVERWRITE=false

QA_MAX_FILES=0
QA_NO_OVERWRITE=false
QA_REPORT_FILE=TEST_REPORT.md
```

Файл плана (`*_PLAN_FILE`) — отладочный артефакт: та же JSON-декомпозиция,
что ушла в модель, но сохранённая на диск. Пустое значение отключает.

### Общие для кодогенерации

```bash
CODEGEN_LANG=Go              # целевой язык
CODEGEN_MODULE=              # имя модуля для go.mod (пусто — модель придумывает)
CODEGEN_MAX_FILES=0          # лимит файлов за запуск
CODEGEN_NO_OVERWRITE=false   # запрет перезаписи
CODEGEN_SUMMARY_FILE=SUMMARY.md  # отчёт после генерации (пусто — выкл)
```

### RAG-контекст по ролям

```bash
RAG_PLANNER_CONTEXT=1        # вкл по умолчанию
RAG_ARCHITECT_CONTEXT=1      # вкл по умолчанию (нет в .env.example!)
RAG_ASSISTANT_CONTEXT=1      # вкл по умолчанию (нет в .env.example!)
```

Выключаются значением `0` / `false` / `off` / `no`.

### Автопереиндексация

```bash
RAG_AUTO_REINDEX=0           # по умолчанию выключено
```

Когда включено, разработчик после мутаций вызывает `ReindexTouched` —
затронутые файлы переиндексируются в Qdrant, исчезнувшие удаляются
(`runner/reindex.go`). Индексация ветко-осознанная: через
`rag.DetectIndexOptions(OutputDir)`.

Без Qdrant переиндексация молча пропускается — генерация продолжается.

## Как разрабатывать нового агента

1. Создать пакет в `agents/<имя>/` с `agent.go` и `config.go`.
2. Реализовать конструктор, собирающий набор через `tools.Select`.
3. Написать системный промпт.
4. При необходимости — зарегистрировать роль в `main.go` и в
   `projectFromArgs`.
5. Покрыть тестом (паттерн `agent_test.go` у соседних агентов).
6. Обновить документацию по
   [чеклисту добавления фичи](../40-operations/adding-a-feature.md).

## Связанное

- [Архитектор](architect.md) — верхний уровень
- [Kanban-доска](kanban-board.md) — что такое эпик и задача
- [Инструменты агентов](../30-reference/agent-tools.md)
- [Планировщик](planner.md)
- [Инфраструктура](../30-reference/infrastructure.md) — конфигурация по ролям
