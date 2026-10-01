# AGENTS.md — правила для агентов (opencode / помощники)

## `temp/` — хранилище проектов (не трогать)

Каталог `temp/` — рабочее хранилище проектов, которые генерируют и дорабатывают
автоматические агенты-разработчики (backend/frontend и другие). Это **не исходный код
проекта**, а артефакты выполнения.

Правила для любого агента (включая opencode):

- **НЕ удаляй `temp/` целиком** и его содержимое по команде «почисти/очисти» —
  там живут пользовательские проекты.
- **НЕ редактируй** файлы внутри `temp/` напрямую, если задача не касается
  конкретного проекта: этот каталог — выходная директория агентов, а не
  редактируемый исходник.
- **НЕ коммить** содержимое `temp/` в git: оно игнорируется (см. `temp/.gitignore`),
  но не добавляй его в индексацию вручную.
- Агенты, работающие с проектом, используют единый путь
  `temp/<имя_проекта>/` (см. `projects.ProjectDir`) и не выходят за
  его пределы.
- Если задача требует изменить код внутри уже созданного проекта — работай через
  агента-разработчика (`backend`/`frontend`) соответствующей специализации, а не правь
  файлы в `temp/` как исходник.

## Сборка и проверка

Рабочая сборка (обходит известный блокер в `temp/`):

```
go build . ./agents/... ./tools/ ./board/
go vet . ./agents/... ./tools/ ./board/
go test . ./agents/... ./tools/ ./board/
```

Известные нюансы:
- `go build ./...` падает на `temp/moon-distance/internal/middleware/cors.go`
  (артефакт сгенерированного проекта) — используй перечень выше.
- `gofmt -l .` показывает 29 файлов с предсуществующими отклонениями
  форматирования (часто — отсутствующая завершающая строка). Это не гейт:
  запускай `gofmt -l`/`gofmt -w` только на своих файлах, иначе в коммит
  попадёт форматирование чужого кода.
- На macOS (GNU Make 3.81) падают два теста `agents/acceptor`:
  `TestAcceptBuildFallsBackToInfraMirror` и
  `TestAcceptBuildToolMissingSkips` — они ждут make-маркер
  (`Ошибка`/`Error 127`), которого нет в выводе локального GNU Make
  (`make: *** [build] Error 1`). Зависит от хоста, не регрессия.

## Токено-эффективные Bash-команды

Контекстное окно модели — конечный ресурс: `cat` целиком, `grep -r` без
исключений и полный вывод сборки съедают его быстрее, чем задача продвигается.

**Приоритет инструментов (что первично):**
1. Структурированные инструменты и LSP — поиск символов
   (`LspDefinition`/`LspReferences`/`LspHover`) и чтение кода
   (`List`/`ReadMap`/`ReadFiles` с `lines`).
2. Bash — только там, где аналога нет: git-контекст, компактный вывод
   проверок, точечный поиск по тексту.

**Разведка, поиск, чтение:**

| Задача | Команда | Приоритет |
|---|---|---|
| Структура (быстро) | `find . -maxdepth 3 -not -path '*/.*' -not -path '*node_modules*'` | фолбэк (дублирует `List`) |
| Поиск по тексту | `grep -rnw --exclude-dir={node_modules,.git,dist} "символ" .` | фолбэк (дублирует LSP/`CodeSearch`) |
| Импорты и заголовок | `sed -n '1,50p' путь` (или `head -n 50 путь`) | фолбэк (дублирует `ReadMap`) |
| Список функций (py) | `python3 -c "import ast; print([n.name for n in ast.parse(open('f.py').read()).body if isinstance(n, ast.FunctionDef)])"` | фолбэк |
| Git-контекст | `git log -n 3 --oneline -- путь`, `git blame -L 10,20 путь` | основной (аналога нет) |
| Компактные проверки | `pytest -q`, `npm test -- --silent`, `go test ./...` (без `-v`) | основной |
| Форматирование | `gofmt -l .` (без `-d`) | основной |
| Счёт вхождений | `grep -c`, `rg --count` | основной |

**Запрещено:**
- `cat`/`head`/`tail` файлов «в лоб» вместо инструментов чтения;
- `grep -r`/`rg` без `--exclude-dir` и без точечного шаблона;
- полный нефильтрованный вывод сборок и тестов (без `-v`, `--silent`, `-q`);
- «первые N строк файла» вместо карты кода/сигнатур;
- перебор окружения при отсутствии инструмента (`which`/`find` по всей ФС) —
  читай вывод команды и следуй подсказке инструмента.

Фильтровать вывод пайпом допустимо и нужно: `... 2>&1 | tail -40`,
`| grep -E "FAIL|error" -A 3`, `| head -60`, `> /dev/null && echo OK`.
Флаги из первых четырёх строк таблицы — именно **фолбэк**: при доступных
LSP/структурированных инструментах они дублируют их и тратят контекст.

**Внутренние агенты (`agents/*`)** — у них Bash-инструмента нет, есть только
`Run` (`tools/fileops.go`), который уже ограничивает объём вывода. Правила те
же: компактные флаги проверок (`make test`/`make lint`, `pytest -q`,
`npm test -- --silent`), git-контекст через `Run`, точечный `grep` через `Run`
только при `status skipped` у LSP/`CodeSearch`.

Состояние последней сессии (пустой удалённый репозиторий, E2E закрыт на живом
GitHub): `gitops.Clone` больше не падает на репозитории без коммитов.
`git clone` пустого репозитория проходит (exit 0 + «cloned an empty
repository»), но HEAD нерождённый, поэтому `rev-parse --abbrev-ref HEAD` давал
128 и Web UI отвечал 502 «ошибка клонирования: … ambiguous argument 'HEAD'».
`gitops/gitops.go`: `detectBaseBranch` (rev-parse → `symbolic-ref --short HEAD`
unborn-ветка → `origin/HEAD` для detached HEAD → константа
`DefaultBaseBranch`="main", ошибок не возвращает), `HasCommit` и
`seedBaseBranch` — базовая ветка создаётся и публикуется (README.md `# <имя>` →
`chore: initial commit (<base>)` → `git push -u origin <base>`), коммит сначала
от identity хоста, при отказе повтор с `-c user.name=AI -c
user.email=ai@localhost`. `server/git.go`: при ошибке клона полу-клон
`temp/<имя>` удаляется (иначе повтор упирается в 409) и `discardHalfClone`
убирает каталог от неудачного клона ТОГО ЖЕ origin при нуле коммитов и чистом
состоянии; чужой клон/наши коммиты/незакоммиченные правки не трогаются (409,
`sameGitRemote` нормализует только `.git` и слеш). Тесты: `gitops/gitops_test.go`
— `TestCloneSeedsEmptyRemoteBase` (посев), `TestCloneEmptyRemotePushFailure`,
`TestCloneDetachedHeadUsesOriginHead`, `TestCloneNoBranchSignalFallsBackToDefault`
(+ переписан `TestCloneDryRun` — добавилась проверка `rev-parse --verify`);
`gitops/cli_test.go` — `TestGitCLICloneEmptyRemoteSeedsBase` (реальный git на
пустом bare-origin: пуш базы, `git show refs/heads/main:README.md`,
merge-base, diff); `server/git_test.go` — `TestOpenGitProjectEmptyRemoteSeedsBase`,
`TestOpenGitProjectRemovesFailedCloneDir`, `TestOpenGitProjectDiscardsHalfClone`,
`TestOpenGitProjectKeepsForeignDir` (4 негативных случая в подтестах).
Верификация: `go build`/`go vet`/`go test` по перечню выше (зелёные, кроме двух
пред-существующих macOS-флаков `agents/acceptor`), `-race` на новых тестах.
Живой E2E на хосте: `go run . serve` (Redis 56379, Qdrant 6333, Ollama —
в Docker/локально) + `POST /api/projects {"path_or_git":
"git@github.com:uraankhayayaal/mytrip.git"}` → 200, в GitHub появились
`refs/heads/main` и `HEAD` на коммите `chore: initial commit (main)`, локально
ветки `main` (с upstream origin/main) и `ai/mytrip`, `merge-base main ai/mytrip`
разрешается, `GET /api/projects/mytrip/diff` → 200 (`files: null` — проект ещё
пуст). Документация: раздел «Пустой удалённый репозиторий» в
`docs/20-features/gitops-workflow.md`, три записи в
`docs/40-operations/troubleshooting.md` («ambiguous argument 'HEAD'», «каталог
уже существует и не пуст», «плохой origin»).

Состояние сессии PLAN-2026-09-27-done-branch-aware-rag (Ф-1..Ф-7
готовы, остался ручной E2E): версионированный RAG по веткам Git. Агент в
worktree `ai/task/<id>` через CodeSearch видит свою ветку + актуальный main и
НЕ видит изменений соседних эпиков. Ключевое: `rag/chunk.go` — `Chunk.Symbol`
+ `ChunkID(project, file, chunkKey)` (фолбэк `L<start_line>`; TS-символ только у
определений/методов, вызов `run()` символом не становится — иначе два вызова
делили бы chunk_id); `rag/index.go` — payload `branch`/`commit_sha`/`chunk_id`/
`content_hash`/`replaced_by`, `IndexOptions{Branch, CommitSHA}`,
`upsertChunks` (Scroll активных версий → `SetPayload(replaced_by, Wait: true)` →
Upsert; `pruneSuperseded` держит одно предыдущее поколение; пустой commit =
проект без Git, прежние точки удаляются по ID), `pointID` включает ветку,
коммит и content_hash — иначе незакоммиченная правка агента (главный сценарий
`runner.ReindexFiles` после мутации) терялась бы; `rag/branch.go` (новый) —
`MainBranch`, `DetectBranch`/`DetectCommit`/`DetectIndexOptions`, git-вызовы
через инъектируемый `rag.gitRunner`; `rag/search.go` — `SearchParams.Branch`,
`SearchResult.Branch/CommitSHA/ChunkID`, фильтр `should(активные ветка, активные
main)` + второй проход `dropOverriddenByBranch` (Scroll активных точек ветки ПО
ФАЙЛАМ результатов, отсечение перекрытых версий main по chunk_id — список
исключений в фильтр не вносим: `NewHasID` это ID точек, не chunk_id),
overfetch ×3 (мин. 12), порядок сборки выдачи: перекрытие → лимит → `maxTotal`;
`rag/client.go` — `QdrantStore` += `Scroll`/`SetPayload`; `rag/status.go` —
`ProjectInfo` считает только активные версии; `tools/codesearch.go` —
`CodeSearchParams.Branch`, `detectBranch` с кэшем, шапка
`[Файл: … | Ветка: … | Коммит: …]`, `ragLimits`/`ragSearchTimeout`;
`runner/reindex.go` — `ReindexFiles(..., rag.IndexOptions)`;
`agents/developer/developer.go` — `ReindexTouched` с
`rag.DetectIndexOptions(OutputDir)` + правило «поиск ветко-осознанный» в промпте;
`server/ragindex.go`/`actions.go`/`server.go` — `IndexBackground(ctx, branch)` и
`POST /api/projects/{id}/index?branch=`; `main.go` — CLI печатает ветку/коммит.
Тесты: `rag/memstore_test.go` (новый) — in-memory QdrantStore с семантикой
фильтров (must/must_not/should, match, is_null, is_empty, has_id) и
`textEmbedder`; на нём проверены версионирование, изоляция веток, идемпотентность
и выдача поиска. Тесты поймали 4 реальных дефекта: `IndexProject` искал
исчезнувшие файлы по ключам карты chunk_id вместо значений (файлы не устаревали),
`activeBranchCond` для main строил взаимоисключающие `Must: branch=main` +
`Should: is_null(branch)` (поиск из main не возвращал НИЧЕГО), `SetPayload` без
`Wait` (гонка с prune/поиском), идемпотентность по одному `commit_sha` теряла
незакоммиченные правки. Плюс флак тест-хелпера: `chunk_id` делится версиями
одной функции МЕЖДУ ветками, поэтому искать активную точку надо по
`(branch, chunk_id)` — `codeOf` без ветки зависел от порядка обхода map. Верификация зелёная: `go build`/`go vet`/`go test` по
перечню выше (в т.ч. `./rag/`, `./runner/`, `./server/`) + `npm run build` (web/).
Остался ручной E2E на живом Qdrant (шаги — в плане, раздел «Что осталось
пользователю»).

Состояние последней сессии (PLAN-2026-09-24-done-architect-intelligence, Ф-1..Ф-8, остался ручной E2E):
Ф-4 «Корректность задачи, паттерны, AskUser» — архитектор получил
`KanbanRunner.SetRAG` (Р-6) и `KanbanRunner.SetArchitectExtras` (Р-5),
применяемые в `phaseArchitect`/`phaseArchitectReview`/`phaseBugs` через
`prepareArchitect` (kanban.go); `sess.start` передаёт
`SetRAG(ragClient)` + `SetArchitectExtras(&askTool{b: sess}, newIndexBackgroundTool(sess))`
(server/session.go:235); CLI-канбан (main.go) — только `SetRAG(ragClient)`
(автономно, без AskUser/IndexBackground — degrade). Промпт архитектора —
секции «КОРРЕКТНОСТЬ ЗАДАЧИ (спрашивать, а не угадывать)» (AskUser с
`recommended=true` до публикации бэклога, иначе явные допущения в
`architecture_summary`), «ПАТТЕРНЫ ПРОЕКТИРОВАНИЯ» (REST/12-factor/KISS,
анти-GraphQL/devcontainer), «RAG-ИНДЕКС (предложи построить в фоне)»
(RagIndexStatus → AskUser → IndexBackground, продолжать проектирование).
Runner: `RequiredToolFirstRound` трактуется как группа обязательных
инструментов (предварительные чтения/AskUser разрешены) — правка только
doc-комментариев.

Ф-5 «Фоновая индексация RAG» — `server/ragindex.go`: мост
`IndexBackground` (безопасный, без подтверждения) в `ActionsBackend` +
`Session.IndexBackground` — горутина (walk + `IndexProject`, single-flight
через `sess.indexing`, отчёт в `chat.RoleStatus`/лог проекта), идемпотентный
прогон (`IndexProject` сперва очищает точки проекта). Фоновая индексация не
блокирует агентский цикл; клиент RAG создаётся фабрикой `buildProjectIndexer`
(замена для hermetic-тестов). `npm run build` web/ зелёный; `go test
./server/` — неизвестно 2 флаки чат-ассистента
(`TestChatAssistantCreatesBugAndTask`, `TestChatAssistantDeleteTaskAfterConfirm`),
падают и на чистой базе (не связаны с Ф-4/Ф-5). Опциональный нюанс Ф-5
сделан: REST `POST /api/projects/{id}/index` (`handleProjectIndex`,
server/server.go — 409 при идущей, 503 при недоступном RAG) + кнопка «Индекс
RAG» в `web/src/App.tsx` (head-actions); тесты `TestRESTProjectIndex` /
`TestRESTProjectIndexRAGUnavailable`.

Ф-6 «Кросс-функциональные инсайты» — `board/entity.go`: тип
`Opportunity{TargetRole, Suggestion}` + `Backlog.Opportunities []Opportunity`
(`json:"opportunities,omitempty"`). `agents/architect/agent.go`: опциональное
поле `opportunities` в схеме `submit_architecture_backlog`; `submitBacklog`
валидирует записи (непустые target_role+suggestion) — грязные деградируют в
`skipped_opportunities`, валидные складываются в `Summary` эпиков секцией
«КРОСС-ФУНКЦИОНАЛЬНЫЕ ВОЗМОЖНОСТИ (рекомендации смежным направлениям)»;
промпт — секция «КРОСС-ФУНКЦИОНАЛЬНЫЕ ВОЗМОЖНОСТИ (opportunities)» (в
основной фазе) и пометка «opportunities: <роль> — <предложение>» в описании
эпика исправления (экспертиза багов). Тесты: парсинг + round-trip в Summary
(`agents/architect/agent_test.go`, `board/entity_test.go`), опциональность
(старые вызовы без поля валидны), скип грязных записей.

Ф-7 «Верификация и полировка»: `go build/vet` по всем пакетам
(./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/) — зелёные;
`go test` — зелёные, кроме 2 пред-существующих флаков aссистента
(`TestChatAssistantCreatesBugAndTask`, `TestChatAssistantDeleteTaskAfterConfirm`,
падают и на чистой базе); `npm run build` web/ зелёный. Остался ручной E2E
на реальном проекте (Web UI, инфраструктура Redis/Qdrant/модель у
пользователя): новая задача → архитектор поднимает RAG-индекс в фоне по
согласию, декомпозирует с учётом стека и ролей; консольный проект — без
Frontend Lead. Далее по плану — Ф-8 и следующие фазы уже реализованы
(Ф-8 в списке: эпики из чата — ревизия архитектора).

Состояние последней сессии (PLAN-2026-09-24-done-makefile, Ф-1..Ф-5 готовы,
остался ручной E2E): корневой Makefile проекта (temp/<проект>/Makefile) —
единая точка входа команд субагентов и приёмки. Ф-1: `tools/stacktool.go`
`StackInfo.Makefile (json:"makefile")` + маркер «Makefile»; секция «MAKEFILE
ПРОЕКТА» в `architectureSystemPrompt` (обязательный эпик «Makefile проекта»,
эталонный контракт целей, assigned_role Backend Lead/DevOps Lead) + п.8
`bugExpertSystemPrompt`/п.7 `epicReviewSystemPrompt`. Ф-2: `developer.go:264`
п.5 (сначала ReadFiles Makefile → `make backend-*`/`make frontend-*`), QA
(`make test`/`make e2e`, приёмка `make build`/`make lint`), лиды backend/
frontend/devops/qa — «ЦЕЛЬ ПРОВЕРКИ ИЗ MAKEFILE» (devopslead — «ИНФРА-БЛОК
ЧЕРЕЗ MAKEFILE»). Ф-3: `acceptor/detect.go` `makefileLocate(root,dir)` (поиск
от подпроекта до корня приёмки) + `makefileTargets` (парсер целей, multi-target
`build test:`, пропуск `:=`, `.PHONY`, `%`, с переменными) + `makeCommand`/
`makeAnalyzeCommand` (test→lint)/`makeInfraMirror`; приоритет env ACCEPT_* →
make-цель → автодетект kind; make-команды исполняются в mkDir (Makefile);
фолбэк `make infra.<цель>` при недоступном инструменте хоста (make-специфичный
маркер «Ошибка/Error 127» в `toolMissing`, accept.go — в checks.go/run.go
правок нет); для run инфра-зеркало НЕ применяется. Ф-4: `devops/agent.go` п.5-6
(инфра-блок: up/down/logs/ps, зеркальные infra.<цель>, самозавершающийся e2e),
`fileops.go:991` missingToolHint → «make infra.<цель>». Верификация зелёная
(плюс 3 пред-существующих флака чат-ассистента на чистой HEAD:
`TestChatAssistantCreatesBugAndTask`, `TestChatAssistantDeleteTaskAfterConfirm`,
`TestChatAssistantPublishesBoardWhenIdle`).

Состояние после PLAN-2026-09-24-done-merge-conflict-board (Ф-1..Ф-5, остался
ручной E2E): конфликты мёрджа стали видимы на доске. Ф-1: `board/entity.go` —
`MergeConflictFiles []string` у `Epic`/`Task` (`json:"merge_conflict_files,
omitempty"`); запись/очистка в `autoCommitAndMergeTask` (done→релиз), `Session.
TaskMerge` (server/actions.go), REST `handleMergeTask`/`handleReleaseEpic`
(server/gitflow.go), авто-синхрон эпика `autoResolveMainSync` +
`clearEpicMergeConflict` (server/gitflow_auto.go:471), `handleEpicRebase`/
`handleEpicResolve` (server/gitflow_resolve.go); `RoleStatus`-уведомления.
Ф-2: блок «Конфликты мёрджа» в `chatAssistantPrompt` (server/chatassist.go) +
правило «не повторять TaskMerge» в `agents/chatassist/agent.go`. Ф-3:
`web/src/Types/Types.ts` `merge_conflict_files` + бейдж в модалках
`TaskModal`/`EpicModal` (TaskModal/styles.scss, EpicModal/styles.scss). Ф-4:
`TaskMerge` возвращает детерминированную конфликт-строку с файлами и точкой
резолва, состояние не меняется. Тесты: `TestTaskDoneAutoMergeConflict` (поле
f.txt), `TestMergeTaskConflict409` (409+поле), `TestMergeTaskConflictIdempotent`,
`TestMergeTaskClearsConflictField`, `TestMergeConflictFilesJSON` (board),
`TestChatAssistPromptShowsMergeConflict`; `go build/vet` по перечню AGENTS.md
зелёные, `go test` зелёные кроме 3 пред-существующих флаков чат-ассистента.
Состояние после 84ec174 (fix выбора модели в Web UI, остался ручной E2E):
дефекты переключателя исправлены. Главный — `providerResolve.setActive`
записывал новый выбор, но НЕ инвалидировал кэш (`done`), поэтому после
первого чата/запуска смена модели не действовала до перезапуска процесса.
`server/resolve.go` переписан: единое состояние выбора под `mu`
(`selection`/`setOverride`/`clearOverride`/`resolved`/`describe`), кэш
сбрасывается при смене, провайдер создаётся сразу (ошибка битого `base_url`
→ 400/502 в REST, а не на первом запросе к модели). Убрана гонка и
дублирование: поля `Server.activeProvider/activeModel` удалены — источник
правды только в `providerResolve`. `models/resolve.go` — `Selection`/
`Resolved`/`ResolveSelection` (явный выбор без `os.Setenv`), `Describe()`
даёт строку вида `ollama/qwen3-coder:30b (large=qwen3.6:35b-a3b)`;
`ResolveProvider()` — обёртка над `EnvSelection()` для CLI. `models/
providers_config.go` — кэш по пути + отпечатку (mtime+size) вместо
`sync.Once`: правка `providers.json` подхватывается без перезапуска, путь
через `PROVIDERS_CONFIG` (даёт тестовый шов). REST: `GET /api/providers`
(сортированный список, `current_provider`/`current_model`/
`current_large_model`/`override`/`error`), `POST /api/providers/select`
(`provider`/`model`/`large_model`/`reset`, валидация против `selectableModels`
= models+default_model+large_model, предупреждение `applies_to_running` +
`message`, если оркестрация уже идёт). Логи модели: `server: LLM <describe>`
на старте сервера и на каждый (ре)резолв, `[llm] модель запуска: …` на каждый
старт оркестрации (`Session.start`), `[llm] модель: …` на сообщение в чат.
Фронт: `ModelSelector` — два селекта (провайдер+модель, крупная модель) и
кнопка «сброс»; значение `<option>` — индекс в плоском списке, НЕ
`провайдер:модель` (иначе `qwen3-coder:30b` и `/models/T-pro-it-1.0`
ломались по двоеточию/слэшу), ошибки сервера больше не глушатся `catch(() =>
{})`, выбор откатывается при отказе; стили вынесены в
`ModelSelector/ModelSelector.scss` (блок из глобального `styles.scss`
удалён, мёртвый дубликат переписан), `Api.ts` — `SelectProviderBody`/
`SelectProviderResult`. Тесты: `server/resolve_test.go` (новый) —
`TestSelectProviderAppliesAfterFirstResolve` (регресс кэша), `…EnvModelAsDefault`,
`TestRESTProvidersReportsEnvSelection`, `TestRESTSelectProviderSwitchesModel`,
`…LargeModel`, `…LargeModelFallsBackToConfig`, `…WarnsAboutRunningOrchestration`,
`…RejectsUnknown`, `…BrokenBaseURL`, `TestProvidersConfigReloadsOnChange`;
хелпер `stubProviderResolve` заменил 4 места сборки `providerResolve{}`.
Документация: `providers.md` (переписан под providers.json + Web UI + таблица
логов), `rest-api.md` (секция «Модель и провайдеры» + 502),
`web-ui.md` («Выбор модели»), `troubleshooting.md` («Какая модель работает?»,
«выбор не применился», `PROVIDERS_CONFIG`, игнорируемые `OLLAMA_MODEL`),
`environment-variables.md` (MODEL/MODEL_LARGE/PROVIDERS_CONFIG, разобраны
устаревшие YANDEX_*/TRIM_*/REG_*), `quickstart`/`configuration`/
`installation`/`glossary`/`planner` — модель из `providers.json`, а не
`OLLAMA_MODEL`; в коде поправлены устаревшие комменты `models/OllamaModel.go`
(settings из providers.json, а не `OLLAMA_NUM_CTX`). Верификация зелёная: `go
build`/`go vet`/`go test` по перечню выше (в т.ч. `./models/`, `./server/`,
`-race` на новых тестах) + `npm run build` web/. Остался ручной E2E: выбрать
модель в UI до/после первого чата, убедиться по `logs/server.log` и
`logs/<проект>.log`, что строка `[llm] модель запуска` совпала с выбранным
значением.

Состояние последней сессии (E2E выбора модели закрыт): ручной E2E из предыдущей
сессии заменён постоянным живым тестом `server/e2e_model_selection_test.go`
(build-тег `e2e`, вне рабочего набора: `E2E_LIVE=1 go test -tags e2e -run
TestE2EModelSelection ./server/ -timeout 10m`). Тест поднимает настоящий сервер
(`httptest` + `newTestServer` с miniredis — живая нужна только Ollama), сам
подбирает две модели с tool-calls из `/api/tags` (`E2E_MODEL_SMALL`/
`E2E_MODEL_SECOND`/`E2E_MODEL_LARGE`, адрес `OLLAMA_BASE_URL`) и проверяет:
стартовый выбор из окружения, сортировку провайдеров, переключение через
`POST /api/providers/select` → подтверждение в `GET` (регресс кэша провайдера),
**реальный** чат `/api/projects/{id}/chat` с доказательством через `/api/ps`
(до запроса модели выгружены — загружена ровно выбранная), логи `server: LLM:`
в `server.log` и `[llm] модель:` в логе проекта, отказы (400 на неизвестную
модель/провайдер, битый `base_url` → 400 + ошибка в `GET`, `reset` →
`override=false`). Прогон зелёный (26s), `go build`/`go vet`/`go test` по
перечню AGENTS.md зелёные. Документация: новая секция «Живой E2E (опционально,
нужна Ollama)» в `docs/40-operations/testing.md`.
