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

Состояние последней сессии (петля FEL-02 на живом прогоне, отпечаток состояния
по проверке): агент 20+ раундов гонял одну и ту же падающую проверку, меняя
ТОЛЬКО фильтр вывода (`npm test src/pages/Login.test.tsx 2>&1 | grep -A5` →
`| grep -B3 -A20` → `| grep -i "console|warn"`). Отпечаток состояния по
нормализованному ВЫВОДУ проверки такую петлю пропускал: grep печатает разные
куски одного и того же падения, вывод менялся каждый раунд. Ключ отпечатка —
теперь нормализованная САМА проверка (`runner/loopstate.go:verifyIdentity`:
отбрасывается конвейер после `|` с флагами, перенаправления и каталог в
`cd X &&`); счётчики ведутся ПО ПРОВЕРКЕ в map (`stateRuns`), а не по
«последней проверке раунда», иначе чередование двух падающих проверок обнуляло
бы отсчёт. Нормализованный вывод остался в тексте вмешательства (диагноз модели).
Вторая половина правки — доставка вмешательства: пороги конфликтовали (разрыв на
3-м падении, вмешательство на 5-м — недостижимо), а сообщение дописывалось в
конец раунда, т. е. доезжало на 1–2 раунда позже и пропадало вместе с
схлопнутой историей. Теперь `LOOP_STATE_WARN` (2, зажат ниже
`LOOP_STATE_REPEATS`) готовит предупреждение, а `runner.go` внедряет его в
СЛЕДУЮЩИЙ запрос (после сжатия истории, до вызова LLM). Блокировка проверки
переехала с точной сигнатуры на нормализованную проверку (иначе смена флагов grep
её обходила) и больше НЕ обнуляет отсчёт петли — иначе блокировка вечно
обнуляла бы счётчик и разрыв не наступал бы никогда. FEL-02 освобождена через
штатный хук доски: `PUT …/tasks/FEL-02 {"status":"done"}` → авто-коммит `5943050`
(8 файлов, +382) → авто-MR PR #12 → авто-мёрдж в релиз эпика `ARCH-04` (`e307000`);
её тесты всё ещё падают — `window.location.href = '/'` в JSDOM не поддерживается
(ровно этот случай теперь описан во вмешательстве). Сервер перезапущен на новом
бинаре (`./ai serve`, порт 8090), оркестрация `mytrip` продолжена, пошла FEL-03.
Тесты: `TestVerifyIdentityIgnoresPipelineAndFlags`,
`TestLoopDetectorWarnsOneRoundBeforeBreak`, `TestLoopBlockedVerifySurvivesArgumentChange`,
`TestLoopBlockedRoundKeepsStateSeries` + доработан сквозной
`TestGenerateStateHashBreaksLoopWithVaryingArgs` (предупреждение в запросе раньше
разрыва, через `recordingProvider`). Документация:
`docs/plans/PLAN-2026-10-03-done-loop-breaker.md` (разделы про отпечаток и
вмешательство + живая проверка на FEL-02), `LOOP_STATE_WARN` в
`docs/30-reference/environment-variables.md` и `.env.example`, разделы
«Отпечаток состояния»/«Verify guard» в `docs/20-features/agents-and-roles.md`,
новая запись в `docs/40-operations/troubleshooting.md`.

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

Состояние последней сессии (PLAN-2026-09-28-done-rag-main-freshness,
жалоба «переключаю эпик — агент в analysis не видит изменений предыдущего»,
остался ручной E2E): пользователь одобрил все три причины. Ф-1 явное имя
RAG-проекта: `tools/fileops.go` `FileOps.Project` + `SetProjectName` +
`ProjectName`, `tools/codesearch.go`/`ragstatus.go` на `tools.projectNameOf`
(было `basename(OutputDir)` — у агента задачи это `.wt-task-…`, проект не
находился), `agents/developer/developer.go:ReindexTouched`,
`agents/architect|chatassist` (Project при создании),
`agents/planner/kanban.go:specialistFor` (знает и проект, и worktree).
Ф-2 `server/ragref.go` (новый): ветка читается из git-объектов —
`collectRefItems` (`git ls-tree -r -z --long` + `git show ref:<путь>`,
`parseLsTreeEntry`, `indexablePath/Content`, `maxRefFileBytes`),
`Session.IndexRefBackground` (индекс ветки) и `IndexRefDiffBackground` +
`collectRefDiff` (`git diff base...ref`, `git cat-file -s`) — индекс ИЗМЕНЕНИЙ
ветки задачи относительно main (остальное покрывает индекс main; полная
индексация worktree стоила бы эмбеддингов всего проекта на каждый старт
задачи). Явный `branch` больше не переименовывает снимок checkout:
`IndexBackground(branch)` = ref, `IndexBackground("")` = каталог. Ф-3
`rag.Client.BranchIndexed` (`rag/status.go`) + пропуск прогона с отдельным
статусом «RAG-индекс проекта актуален» (не путать с «обновлено 0 файлов»);
single-flight стал ПО ВЕТКЕ (`sess.indexing map[string]bool`, `indexSlotKey`) —
иначе переиндексация main съедала бы индексацию ветки задачи. Хуки:
`server/gitflow.go` (после реального merge эпика `afterMainChanged`; при
`already=true` только проверка индекса main — сохранён тест «уже слитая ветка
не должна мутировать git»), `server/gitflow_auto.go:66` (синхронизация эпика)
и `:164` (старт задачи — дифф ветки), `server/session.go:285` (фоновый merge
main в ai/<имя> до первого агента). Merge в рабочую копию: только чистое
дерево, `git merge --abort` при конфликте (`abortMerge`), пропуск при
отсутствии сдвига HEAD (`headSHA`) — правки агентов не теряются. Тесты
`server/ragref_test.go` (новый, 10 шт.: разбор ls-tree/отсевы/бинарник/ошибки,
дифф ветки, релиз→индекс main, грязное дерево, конфликт+abort, без сдвига
HEAD, актуальный индекс, хук диффа, хуки без сессии). Верификация зелёная:
`go build`/`go vet`/`go test` по перечню выше + `./rag/ ./server/ ./runner/
./gitops/`. Документация:
`docs/plans/PLAN-2026-09-28-done-rag-main-freshness.md` (+ строка в
`docs/plans/README.md`), раздел «Хуки git-flow (без env)» и правки
CodeSearch/IndexBackground в `docs/20-features/rag.md`, хуки RAG в
`docs/20-features/gitops-workflow.md`, две записи в
`docs/40-operations/troubleshooting.md`.

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
