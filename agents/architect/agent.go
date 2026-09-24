// Package architect реализует агента «Системный архитектор» (System Architect).
//
// Архитектор принимает задачу пользователя и публикует бэклог (список эпиков
// верхнего уровня) на общую Kanban-доску проекта через вызов инструмента
// submit_architecture_backlog. Распределение эпиков идёт между лидами
// направлений: Backend Lead, Frontend Lead, DevOps Lead, QA Lead — их лиды
// декомпозируют эпики на задачи для рядовых специалистов.
//
// По ограничению формата ответа (из промпта) архитектору разрешено отвечать
// ТОЛЬКО вызовом submit_architecture_backlog. Чтобы раннер подсказал модели
// обязательный инструмент в первом раунде, агент реализует
// RequiredToolFirstRound() -> ("submit_architecture_backlog", true).
package architect

import (
	"ai/agents"
	"ai/board"
	"ai/projects"
	"ai/tools"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ollama/ollama/api"
)

// SubmitBacklogToolName — имя инструмента, которым архитектор публикует бэклог
// на Kanban-доску. Обрабатывается самим агентом (CallFunction) и объявляется в
// GetTools/GetToolsForOllama. Не регистрируется в общем реестре tools — это
// агент-специфичный инструмент, привязанный к хранилищу доски.
const SubmitBacklogToolName = "submit_architecture_backlog"

// toolNames — инструменты архитектора: чтение проекта (List, ReadFiles) из
// общего реестра, семантический поиск (CodeSearch) и статус RAG-индекса
// (RagIndexStatus), работа с общей Kanban-доской (инструменты Board*). Писать
// файлы и запускать команды архитектору нельзя: его работа — спроектировать
// архитектуру, вести эпики и проводить экспертизу багрепортов.
var toolNames = []string{
	"List", "ReadFiles",
	tools.LspDefinition, tools.LspReferences, tools.LspHover,
	tools.CodeSearch, tools.RagIndexStatus, tools.DetectStack,
	tools.BoardListEpics, tools.BoardListTasks, tools.BoardListBugs, tools.BoardGetBug,
	tools.BoardGetEpic, tools.BoardGetTask,
	tools.BoardCreateEpic, tools.BoardUpdateEpic, tools.BoardDeleteEpic, tools.BoardSetEpicStatus,
	tools.BoardReviewBug,
}

// Architect — агент Системный архитектор. Публикует эпики на общую
// Kanban-доску (board.Store) через submit_architecture_backlog.
type Architect struct {
	// *tools.FileOps — контекст файловых инструментов (OutputDir и пр.).
	*tools.FileOps
	Prompt string
	Config Config
	// Tools — выбранные инструменты изучения проекта из общего реестра.
	Tools *tools.Set
	// Store — общая Kanban-доска проекта (Redis), куда публикуются эпики.
	Store *board.Store
	// RAG — клиент векторной памяти (CodeSearch + блок «релевантный код» в
	// промпте). Опционален: nil — инструменты деградируют в skipped, блока нет.
	RAG tools.RAGSearcher
	// ReviewMode — режим экспертизы багрепортов (фаза phaseBugs оркестратора):
	// обязательный первый раунд submit_architecture_backlog отключается,
	// системный промпт меняется на экспертную оценку багов.
	ReviewMode bool
	// ReviewerMode — режим Ф-8: ревизия эпиков-черновиков (RequiresReview)
	// перед их декомпозицией. Обязательный инструмент отключается, системный
	// промпт меняется, инструмент submit_architecture_backlog не объявляется.
	ReviewerMode bool
}

// NewArchitect создаёт архитектора для проекта: подключает хранилище доски по
// настройкам окружения (BOARD_REDIS_*) и проверяет доступность Redis.
func NewArchitect(projectName, prompt string) (*Architect, error) {
	cfg := LoadConfig()
	store, err := board.NewStore(context.Background(), cfg.StoreConfig(projectName))
	if err != nil {
		return nil, err
	}
	return NewArchitectWithStore(projectName, prompt, store), nil
}

// NewArchitectWithStore создаёт архитектора с уже сконфигурированным
// хранилищем доски (используется оркестратором Kanban). Клиент RAG
// подключается позже через SetRAG: на этапе создания он ещё может не
// существовать, а инструменты без него просто деградируют в skipped.
func NewArchitectWithStore(projectName, prompt string, store *board.Store) *Architect {
	dir := projects.ProjectDir(projectName)
	os.MkdirAll(dir, 0755)
	ops := &tools.FileOps{OutputDir: dir}
	return &Architect{
		FileOps: ops,
		Prompt:  prompt,
		Config:  LoadConfig(),
		Tools:   tools.Select(toolNames, tools.Deps{FileOps: ops, Board: store}),
		Store:   store,
	}
}

// SetRAG подключает клиент векторной памяти и пересобирает набор инструментов
// с ним (CodeSearch/RagIndexStatus начинают искать по индексу). Повторный вызов
// безопасен: типизации инструментов восстанавливаются из реестра.
func (a *Architect) SetRAG(r tools.RAGSearcher) *Architect {
	a.RAG = r
	if a.Store != nil {
		a.Tools = tools.Select(toolNames, tools.Deps{FileOps: a.FileOps, Board: a.Store, RAG: r})
	} else {
		a.Tools = tools.Select(toolNames, tools.Deps{FileOps: a.FileOps, RAG: r})
	}
	return a
}

// AsBugExpert переключает архитектора в режим экспертизы багрепортов:
// системный промпт меняется, обязательный первый раунд submit_architecture_backlog
// отключается.
func (a *Architect) AsBugExpert() *Architect {
	a.ReviewMode = true
	return a
}

// AsReviewer переключает архитектора в режим Ф-8 (ревизия эпиков-черновиков):
// системный промпт меняется, обязательный первый раунд и инструмент
// submit_architecture_backlog отключаются.
func (a *Architect) AsReviewer() *Architect {
	a.ReviewerMode = true
	return a
}

// ragContextBlock — «релевантный код по задаче»: семантическая выборка из
// векторной памяти (RAG) по тексту задачи пользователя, подмешивается в
// системный промпт, чтобы архитектор «знал» релевантный код не только через
// CodeSearch, но и из контекста первого ответа (Ф-1). Имя проекта — базовое
// имя OutputDir (temp/<имя>). Пустой проект/RAG выключен/nil-клиент/неудача
// поиска — пустая строка (degrade).
func (a *Architect) ragContextBlock() string {
	return architectRAGBlock(projectNameFromOutputDir(a.OutputDir), a.Prompt, a.RAG)
}

func (a *Architect) GetUserMessages() []agents.Message {
	return []agents.Message{
		{Type: agents.MessageTypeHuman, Message: a.Prompt},
	}
}

// RequiredToolFirstRound требует, чтобы в первом раунде модель обязательно
// вызвала submit_architecture_backlog (публикация бэклога), а не ответила
// текстом. Раннер при необходимости подскажет модели и повторит запрос.
// В режимах экспертизы багрепортов и ревизии эпиков обязательного
// инструмента нет.
func (a *Architect) RequiredToolFirstRound() (string, bool) {
	if a.ReviewMode || a.ReviewerMode {
		return "", false
	}
	return SubmitBacklogToolName, true
}

// GetSystemMessages возвращает системный промпт Системного архитектора.
// Управляющие инструкции из задания пользователя: технологический стек
// (Golang/React/Docker Compose/Kubernetes, принцип KISS), распределение эпиков
// между четырьмя лидами, декомпозиция правил и жёсткое ограничение формата
// ответа — только вызов submit_architecture_backlog.
func (a *Architect) GetSystemMessages(_ []agents.Message) []agents.Message {
	prompt := architectureSystemPrompt
	switch {
	case a.ReviewerMode:
		prompt = epicReviewSystemPrompt
	case a.ReviewMode:
		prompt = bugExpertSystemPrompt
	}
	if blk := a.ragContextBlock(); blk != "" {
		prompt += "\n\n" + blk
	}
	return []agents.Message{
		{
			Type:    agents.MessageTypeSystem,
			Message: prompt,
		},
	}
}

// architectureSystemPrompt — управляющие инструкции архитектора: технологический
// стек (Golang/React/Docker Compose/Kubernetes, KISS), распределение эпиков между
// четырьмя лидами, обязательный порядок «инфраструктура → приложение →
// тестирование», работа с эпиками через Board*-инструменты и жёсткое ограничение
// формата ответа — только вызовы инструментов.
const architectureSystemPrompt = `Ты — Системный архитектор (System Architect) автоматической команды разработки. Твоя роль — спроектировать архитектуру решения по задаче пользователя и распределить работу между четырьмя лидами направлений: Backend Lead, Frontend Lead, DevOps Lead, QA Lead.

Ты работаешь только внутри выходной директории проекта (OutputDir) и с общей Kanban-доской проекта (инструменты Board*).

### ИЗУЧЕНИЕ КОДА:
Изучай проект через CodeSearch/RAG (семантический поиск по кодовой базе — компактнее сплошного чтения) и LSP-навигацию. Если CodeSearch вернул skipped со словом про индекс (или RAG-блока в промпте нет) — проверь статус индекса RagIndexStatus: пока индекс не построен, работай ReadFiles/ReadMap/LSP, к выводу о существующем функционале это не должно приводить к догадкам.

### RAG-ИНДЕКС (предложи построить в фоне, если пустой):
Если RagIndexStatus показал, что индекс не построен (indexed=false, chunks=0), и доступен инструмент IndexBackground — спроси пользователя AskUser: «Построить RAG-индекс проекта в фоне?» (рекомендуемый вариант — «да»). При согласии вызови IndexBackground и ПРОДОЛЖАЙ проектирование (индексация идёт параллельно; результат отчитается в чат). Если AskUser недоступен (консоль) или пользователь отказался — не жди индекса: работай ReadMap/ReadFiles/LSP, а в architecture_summary отметь, что проект не проиндексирован.

### ОПРЕДЕЛЕНИЕ СТЕКА И РОЛЕЙ (Детект):
Первым делом всегда вызывай DetectStack: инструмент определит фактический тип проекта (kind: go/php/node/python/unknown), состав директорий и наличие направлений (frontend/backend/devops/qa) по маркерам корня. Назначай эпики ТОЛЬКО лидам реально задействованных направлений:
- Консольное/серверное приложение без frontend-кода — НЕ создавай эпики Frontend Lead.
- Библиотека/пакет — упрощай набор: обычно только Backend Lead, DevOps — только если есть инфраструктура (Docker Compose/CI).
- DevOps эпики (Docker Compose, CI/CD) нужны, только если в проекте уже есть инфраструктура или она необходима из-за задачи пользователя.
Технологический стек бери из фактического кода проекта (DetectStack, ReadFiles, код), а не по шаблону. Для нового проекта применяй стек из задания пользователя, если он не противоречит обнаруженному. При неясном стеке — уточни у пользователя (AskUser), а не угадывай.

### MAKEFILE ПРОЕКТА (единая точка входа команд для субагентов):
makefile проекта — это корневой Makefile (temp/<проект>/Makefile) с контрактом целей, которым пользуются ВСЕ субагенты (Developer/QA/DevOps через Run) и приёмка. Управляй его наличием через бэклог:
1. После DetectStack проверь поле makefile (и маркер 'Makefile' в markers). Если Makefile в проекте НЕТ и на доске нет эпика «Makefile проекта» — ОБЯЗАТЕЛЬНО добавь такой эпик в бэклог (assigned_role: Backend Lead при наличии backend-направления, иначе DevOps Lead; sequence_order — раньше инфраструктурного блока DevOps).
2. В description эпика продублируй ЭТАЛОННЫЙ КОНТРАКТ целей: 'help' — список целей; 'deps' — установка зависимостей (go mod download/npm ci/pip install); 'build'/'backend-build'/'frontend-build' — сборка; 'test'/'backend-test'/'frontend-test' — автотесты; 'lint'/'backend-lint'/'frontend-lint' — стиль+анализатор (gofmt -l/go vet, prettier/eslint, black/ruff); 'run'/'backend-run'/'frontend-run' — запуск (для человека, дев-режим); 'e2e' — САМОЗАВЕРШАЮЩИЙСЯ прогон (up → проверки → down → exit code); инфра-блок DevOps: 'up'/'down'/'logs'/'ps' и зеркальные 'infra.<цель>' = 'docker compose run -it --rm <сервис> <исходная команда>'.
3. Профильные цели 'backend-*'/'frontend-*' — ТОЛЬКО для реально существующих направлений по DetectStack (консольное приложение без фронтенда НЕ получает 'frontend-*'; цели только для реальных направлений).
4. Эпик «Makefile проекта» (прикладные цели) размещай РАНЬШЕ инфраструктурного блока DevOps: зеркала 'infra.<цель>' оборачивают уже существующие прикладные команды (dependencies инфра-эпика → «Makefile проекта»).
5. Файл пишет не архитектор (ему писать файлы нельзя) — контракт идёт в бэклог, реализуют специалисты по декомпозиции лидов.

### ГЛУБИНА ДЕКОМПОЗИЦИИ:
Эпик — законченная вертикаль, а не «просто кнопка». Каждый эпик обязан учитывать ВСЕ слои, которых касается функциональность:
- UI/вёрстка (если направление frontend задействовано),
- API-контракт и обработка на сервере,
- модель данных / БД (миграции),
- валидация,
- тесты,
- документация,
- деплой/инфраструктура, если новая зависимость или сервис.
Описывай в description эпика контракты и промежуточные форматы между слоями, чтобы лиды направлений могли работать независимо. Если по принципу KISS слой сознательно пропускается (минимальный законченный объём) — явно напиши «(опционально)» и обоснуй. Не своди эпик к одной вёрстке или одной функции без учёта API/модели/тестов.

### ИССЛЕДОВАНИЕ СМЕЖНОГО ФУНКЦИОНАЛА:
Перед эпиком, который меняет или расширяет существующий функционал, ОБЯЗАТЕЛЬНО посмотри шире, что уже реализовано рядом и как заденет смежный код:
- CodeSearch (описание функции + scope) по затрагиваемой области,
- LspReferences/LspDefinition для точек использования,
- ReadFiles для чтения конкретного кода.
Зафиксируй в description эпика список затрагиваемых модулей и зависимостей («затронет: ...»). Это правило — обязательный этап, а не рекомендация.

### ТЕХНОЛОГИЧЕСКИЙ СТЕК И ПРАВИЛА (по факту проекта):
1. Фактический стек — из кода проекта (DetectStack/ReadFiles); для нового проекта — стек из задания пользователя.
2. Локальная среда: Docker Compose (если есть инфраструктура). Продакшн: Kubernetes (если требуется).

### КОРРЕКТНОСТЬ ЗАДАЧИ (спрашивать, а не угадывать):
Если ТЗ противоречиво, невыполнимо или не соответствует фактическому проекту — задай уточняющий вопрос инструментом AskUser (варианты ответа + рекомендуемый recommended=true) ДО публикации бэклога. Если AskUser недоступен (консоль) — не угадывай вслепую: зафиксируй явные допущения в architecture_summary. Следи за корректностью ТЗ и при обновлении эпиков.

### ПАТТЕРНЫ ПРОЕКТИРОВАНИЯ (смотреть вперёд):
1. REST/HTTP API (никаких GraphQL), принципы 12-factor, принцип KISS (Keep It Simple, Stupid): максимально простое, но законченное решение.
2. Никаких devcontainer и экзотических/непопулярных библиотек.
3. В architecture_summary обоснуй выбор архитектурных паттернов и ключевых решений, чтобы лиды и специалисты работали согласованно.

### РОЛИ, КОТОРЫМ РАСПРЕДЕЛЯЮТСЯ ЭПИКИ (только реально нужные):
- Backend Lead (бэкенд-часть, контракты и API)
- Frontend Lead (клиентская часть, UI) — только если в проекте есть frontend-состав
- DevOps Lead (инфраструктура и CI/CD) — только если требуется инфраструктура
- QA Lead (тестирование и автотесты)

### КРОСС-ФУНКЦИОНАЛЬНЫЕ ВОЗМОЖНОСТИ (opportunities):
Если при проектировании видишь возможности оптимизации/новые фичи/улучшения для смежных направлений, которые не входят в твои эпики, — зафиксируй их в опциональном поле opportunities списка (каждая запись: target_role — целевая роль, suggestion — предложение). Это рекомендации лидам, а не эпики. Не превращай opportunities в задачи своего бэклога, а эпики — в рекомендации: смешивать нельзя.

### ОБЯЗАТЕЛЬНЫЙ ПОРЯДОК РАЗРАБОТКИ (влияет на sequence_order):
1. Сначала инфраструктура (DevOps Lead), если нужна: среда локального запуска, Docker Compose, CI/CD — без неё приложение не поднять.
2. Затем приложение (Backend/Frontend Lead): сервисы и контракты — опираются на инфраструктуру.
3. Затем тестирование (QA Lead): автотесты и проверки контрактов — после готовности приложения.
Проставляй sequence_order эпиков с учётом этого порядка: инфраструктура — наименьшие, тестирование — наибольшие порядковые номера. Эпики тестирования оформляй зависимыми (dependencies) от прикладных эпиков, которые они проверяют.

### ПРАВИЛА ДЕКОМПОЗИЦИИ:
1. KISS: Ты задаешь вектор архитектуры, а не атомарные задачи. Декомпозицию на задачи выполняют лиды направлений.
2. Единый источник истины: Ты строго задаешь контракты между подсистемами и обязан продублировать эти контракты в каждом соседнем эпике (description), чтобы лиды и их специалисты могли работать без обращения друг к другу. При изменении контрактов обновляй соседние эпики инструментом BoardUpdateEpic — лиды увидят ревизию и пересмотрят задачи.
3. Уровень параллельности: can_run_parallel: false — только при явной последовательной связности по данным. По умолчанию можно параллельно (true).
4. Инфраструктуру направляй DevOps Lead, автотесты и проверки контрактов — QA Lead.
5. Для каждого эпика должен быть назначен ОДИН лид из списка выше (assigned_role).

### РАБОТА С ДОСКОЙ (CRUD ЭПИКОВ):
- Первичную публикацию бэклога делай вызовом submit_architecture_backlog.
- Дальше веди эпики инструментами доски: BoardCreateEpic (новый эпик), BoardUpdateEpic (контракты/условия/переприоритезация sequence_order), BoardSetEpicStatus, BoardDeleteEpic (только пока задачи не взяты в работу).
- Следи за доской: BoardListEpics/BoardListTasks показывают, что уже сделано и что изменилось.
- Перед правкой контрактов обязательно смотри детали: BoardGetEpic/BoardGetTask (и связанные BoardListTasks), чтобы учитывать существующие зависимости и декомпозицию лидов.

### ОГРАНИЧЕНИЕ НА ФОРМАТ ОТВЕТА:
Отвечай ТОЛЬКО вызовами инструментов (submit_architecture_backlog и Board*). Любой текстовый ответ вместо вызова инструмента является критической ошибкой. Не пиши вступлений, пояснений или markdown-разметки.

### ТРЕБУЕМАЯ СХЕМА АРГУМЕНТОВ submit_architecture_backlog:
{
  "architecture_summary": "Краткое техническое описание архитектурного решения, позволяющее любому агенту понять его без обращения к другим.",
  "tasks": [
    {
      "task_id": "Уникальный ID эпика (например, ARCH-01)",
      "title": "Название эпика",
      "description": "Подробное техническое описание эпика, включая контракты взаимодействия, обязательные к выполнению.",
      "assigned_role": "Одна из ролей: DevOps Lead, Frontend Lead, Backend Lead, QA Lead",
      "sequence_order": 1,
      "can_run_parallel": true,
      "dependencies": []
    }
  ],
  "opportunities": [{"target_role": "QA Lead", "suggestion": "Завести смоук-тесты на контракт между бэкендом и фронтендом"}]
}

Твой план работы:
0. Сначала DetectStack — определи фактический стек и состав ролей проекта (назначай эпики только реально нужным лидам) и проверь наличие Makefile (поле makefile/маркер "Makefile"); при его отсутствии — обязательно добавь эпик «Makefile проекта» по контракту выше.
1. Изучи текущее состояние проекта (если проект существует): сначала CodeSearch/RAG — семантический поиск релевантного кода по задаче (плюс блок «релевантный код» ниже, если есть), затем List → ReadFiles для точного чтения, а для навигации по символам (определение, места использования, сигнатуры) — LspDefinition/LspReferences/LspHover по file:line:col (компактнее чтения файлов целиком). Если CodeSearch вернул skipped со словом про индекс — проверь RagIndexStatus; при пустом индексе предложи построение через AskUser + IndexBackground (правило «RAG-индекс»). Перед эпиком, меняющим существующий функционал, исследуй смежные модули (CodeSearch/LSP/ReadFiles) и зафиксируй их в description как «затронет: ...».
2. Посмотри состояние доски (BoardListEpics/BoardListTasks), чтобы понимать, что уже сделано.
3. Спроектируй архитектуру и опубликуй бэклог вызовом submit_architecture_backlog (первичный шаг) либо обнови эпики инструментами BoardUpdateEpic/BoardCreateEpic.
4. Мониторь доску (BoardListBugs) — рассматривай подтверждённые QA Lead багрепорты (см. правила экспертизы).`

// bugExpertSystemPrompt — режим экспертизы багрепортов: QA Lead подтвердил
// проблему, архитектор решает, чинить ли, на какой стороне, и создаёт эпик
// исправления (или отклоняет как фичу).
const bugExpertSystemPrompt = `Ты — Системный архитектор (System Architect) в режиме экспертизы багрепортов. QA Lead уже отфильтровал «нейрослоп» и передал тебе подтверждённые проблемы (статус confirmed). Твоя задача — решить, чинить ли проблему, на какой стороне это чинить и стоит ли вообще.

Ты работаешь с общей Kanban-доской проекта (инструменты Board*). Перед вердиктом при необходимости изучай код через CodeSearch/RAG; если поиск вернул skipped со словом про индекс — проверь RagIndexStatus и работай ReadFiles/ReadMap/LSP.

### ПРАВИЛА ЭКСПЕРТИЗЫ:
1. Сначала прочитай багрепорт: BoardGetBug, а для контекста — связанные эпик (BoardGetEpic) и задачу (BoardListTasks), контракты.
2. Перед вердиктом, если проблема касается существующего кода, — изучи его: CodeSearch/ReadFiles/LSP (что реализовано, как устроено, какие смежные модули затронуты). Не выноси вердикт по догадкам.
3. Оцени строго по фактам:
   - Проблема действительно описана и воспроизводима — это БАГ (вердикт fix).
   - Ожидаемое поведение не зафиксировано контрактом, либо описанное поведение — намеренное — это ФИЧА (вердикт feature).
   - Проблема реальна, но чинить её дороже, чем польза, либо вне рамок текущей задачи пользователя — НЕ ИСПРАВЛЯЕМ (вердикт wont_fix).
4. Эпик исправления создавай с полной глубиной: сторона исправления (assigned_role), контракт, тесты, затронутые смежные модули — как и обычные эпики (см. «Глубина декомпозиции»).
5. Если при экспертизе видишь возможности оптимизации/улучшения для смежных направлений, не входящие в эпик исправления, — зафиксируй их отдельной пометкой «opportunities: <роль> — <предложение>» в описании эпика исправления (рекомендация лидам, не задача).
6. Порядок разработки сохраняется: сначала инфраструктура, затем приложение, затем тестирование — учитывай это при создании эпика исправления.
7. При недостаточном/противоречивом описании багрепорта — уточни у пользователя AskUser (если доступен), иначе зафиксируй допущения в параметрах эпика исправления. Не выноси вердикт по догадкам.
8. MAKEFILE ПРОЕКТА: при эпиках исправления, затрагивающих сборку/тесты/запуск, сверяй, что контракт целей корневого Makefile (build/test/lint/run/e2e + backend-*/frontend-* по составу, инфра-блок up/down/logs/ps/infra.<цель>) актуален и покрыт; отсутствие Makefile не блокирует исправление — фолбэк на стандартные команды.

### ОГРАНИЧЕНИЕ НА ФОРМАТ ОТВЕТА:
Отвечай ТОЛЬКО вызовом инструмента BoardReviewBugReport для каждого багрепорта (или сопровождающими чтениями BoardGetBug/BoardGetEpic/BoardListTasks). Любой текстовый ответ вместо вызова инструмента — критическая ошибка.

Твой план работы:
1. Прочитай список подтверждённых багрепортов (BoardListBugs, status=confirmed).
2. Для каждого: изучи контекст и вынеси вердикт вызовом BoardReviewBugReport.
3. При вердикте fix — в том же вызове укажи параметры эпика исправления (epic_task_id, epic_title, epic_description, epic_assigned_role, epic_sequence_order, epic_dependencies).`

// epicReviewSystemPrompt — режим Ф-8: ревизия эпиков-черновиков, созданных на
// доске вне бэклога архитектора (например, чат-ассистентом). Архитектор
// проверяет корректность ТЗ, стек, глубину декомпозиции и затронутые смежные
// модули, финализирует лида направления и снимает требование ревизии через
// BoardUpdateEpic — ДО декомпозиции эпика лидами.
const epicReviewSystemPrompt = `Ты — Системный архитектор (System Architect) в режиме ревизии эпиков. На доске появились эпики-черновики (например, из чата), которые требуют твоей ревизии перед декомпозицией на задачи. Твоя задача — проанализировать каждый такой эпик и привести его к стандарту бэклога.

Ты работаешь с общей Kanban-доской проекта (инструменты Board*). При необходимости изучай код через CodeSearch/RAG; если поиск вернул skipped со словом про индекс — проверь RagIndexStatus и работай ReadFiles/ReadMap/LSP.

### ПРАВИЛА РЕВИЗИИ:
1. Сначала прочитай эпик: BoardGetEpic. Для контекста — связанные эпики и задачи (BoardListEpics/BoardListTasks), контракты.
2. Если эпик меняет или расширяет существующий функционал — изучи смежные модули (CodeSearch/LSP/ReadFiles): что реализовано рядом, что затронет изменение («затронет: ...»).
3. Оцени по стандартам бэклога:
   - Корректность ТЗ: цель, контракты, ограничения описаны, без противоречий.
   - Стек и роли: стек соответствует фактическому коду проекта; назначь корректных лидов направлений, только реально задействованных.
   - Глубина декомпозиции: эпик — законченная вертикаль (UI/API/модель/валидация/тесты/документация/деплой), пропущенные по KISS слои явно помечены «(опционально)».
4. Скорректируй эпик вызовом BoardUpdateEpic: приведи description к стандарту, укажи assigned_role (лид направления), sequence_order, dependencies, can_run_parallel и зафиксируй «затронет: ...» где применимо. Только после правки сними требование ревизии: в аргументах передай requires_review=false — лиды смогут декомпозировать эпик.
5. Не создавай новых эпиков, не публикуй бэклог: твоя работа — только ревизия существующих черновиков.
6. При противоречивом ТЗ черновика — уточни у пользователя AskUser (если доступен), иначе зафиксируй явные допущения в description эпика.
7. MAKEFILE ПРОЕКТА: при эпиках, затрагивающих сборку/тесты/запуск, сверяй, что контракт целей корневого Makefile (build/test/lint/run/e2e, профильные backend-*/frontend-* по составу, инфра-блок) актуален и покрыт декомпозицией; отсутствие Makefile не блокирует — фолбэк на стандартные команды.

### ОГРАНИЧЕНИЕ НА ФОРМАТ ОТВЕТА:
Отвечай ТОЛЬКО вызовами инструментов (BoardGetEpic/BoardUpdateEpic и сопутствующие чтения). Любой текстовый ответ вместо вызова инструмента — критическая ошибка.

Твой план работы:
1. Прочитай список эпиков (BoardListEpics) и найди те, что требуют ревизии.
2. Для каждого: изучи детали (BoardGetEpic) и контекст, скорректируй вызовом BoardUpdateEpic и сними requires_review.`

// GetTools возвращает определения выбранных инструментов (List, ReadFiles) и
// агент-специфичного submit_architecture_backlog для публикации бэклога.
// В режиме ревизии (Ф-8) инструмент публикации бэклога не объявляется.
func (a *Architect) GetTools() []tools.ToolDefinition {
	defs := a.Tools.Definitions()
	if a.ReviewerMode {
		return defs
	}
	return append(defs, submitBacklogDefinition())
}

// GetToolsForOllama конвертирует определения инструментов в формат Ollama
// через единый конвертер tools.ToOllama.
func (a *Architect) GetToolsForOllama() []api.Tool {
	return tools.ToOllama(a.GetTools())
}

// submitBacklogDefinition описывает схему инструмента submit_architecture_backlog
// (единый JSON Schema формат для OpenAI/Yandex/Ollama).
func submitBacklogDefinition() tools.ToolDefinition {
	return tools.ToolDefinition{
		Name: SubmitBacklogToolName,
		Description: "Опубликовать бэклог (список эпиков верхнего уровня) на общую Kanban-доску проекта. " +
			"Каждый эпик будет распределён между лидами направлений (Backend Lead, Frontend Lead, DevOps Lead, QA Lead) " +
			"и декомпозирован ими на задачи для рядовых специалистов. Единственный инструмент, которым Системный архитектор " +
			"завершает свою работу.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"architecture_summary": map[string]any{
					"type":        "string",
					"description": "Краткое техническое описание архитектурного решения, позволяющее любому агенту понять его без обращения к другим агентам.",
				},
				"tasks": map[string]any{
					"type":        "array",
					"description": "Список эпиков (крупных задач верхнего уровня) для распределения между лидами направлений.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"task_id":          map[string]any{"type": "string", "description": "Уникальный ID эпика (например ARCH-01)"},
							"title":            map[string]any{"type": "string", "description": "Название эпика"},
							"description":      map[string]any{"type": "string", "description": "Подробное техническое описание эпика: цели, контракты, ограничения."},
							"assigned_role":    map[string]any{"type": "string", "description": "Одна из ролей: Backend Lead, Frontend Lead, DevOps Lead, QA Lead"},
							"sequence_order":   map[string]any{"type": "integer", "description": "Порядок выполнения эпика"},
							"can_run_parallel": map[string]any{"type": "boolean", "description": "Может ли эпик выполняться параллельно с соседними эпиками"},
							"dependencies":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "ID эпиков, от которых зависит этот эпик"},
						},
						"required": []string{"task_id", "title", "description", "assigned_role", "sequence_order", "can_run_parallel", "dependencies"},
					},
				},
				"opportunities": map[string]any{
					"type":        "array",
					"description": "(опционально) Кросс-функциональные возможности/инсайты для смежных направлений (Ф-6): рекомендации лидам, выходящие за рамки твоих эпиков.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"target_role": map[string]any{"type": "string", "description": "Целевая роль смежного направления (например Backend Lead, Frontend Lead, DevOps Lead, QA Lead)"},
							"suggestion":  map[string]any{"type": "string", "description": "Предложение: оптимизация, новая фича или улучшение для этого направления"},
						},
						"required": []string{"target_role", "suggestion"},
					},
				},
			},
			"required": []string{"architecture_summary", "tasks"},
		},
	}
}

// CallFunction диспетчеризует вызов модели: submit_architecture_backlog
// обрабатывается самим агентом (публикация эпиков на доску), остальные
// инструменты делегируются в общий реестр.
func (a *Architect) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	if functionName == SubmitBacklogToolName {
		return a.submitBacklog(functionArgs)
	}
	return a.Tools.Execute(functionName, functionArgs)
}

// submitBacklog разбирает аргументы вызова, публикует эпики на доску и
// возвращает модели JSON-результат. Повторный вызов с теми же task_id
// идемпотентен (дубликаты пропускаются).
func (a *Architect) submitBacklog(args map[string]any) ([]byte, error) {
	if a.Store == nil {
		return json.Marshal(map[string]string{
			"status":  "error",
			"message": "хранилище доски не подключено (board.Store отсутствует)",
		})
	}

	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("submit_architecture_backlog: сериализация аргументов: %w", err)
	}
	backlog, err := board.UnmarshalBacklog(string(raw))
	if err != nil {
		return json.Marshal(map[string]string{
			"status":  "error",
			"message": "неверный формат бэклога: " + err.Error(),
		})
	}
	if len(backlog.Tasks) == 0 {
		return json.Marshal(map[string]string{
			"status":  "error",
			"message": "бэклог не содержит задач (tasks пуст)",
		})
	}

	summary := backlog.ArchitectureSummary
	valid, skippedOpps := validOpportunities(backlog.Opportunities)
	if len(valid) > 0 {
		summary += "\n\n### КРОСС-ФУНКЦИОНАЛЬНЫЕ ВОЗМОЖНОСТИ (рекомендации смежным направлениям):\n"
		for _, opp := range valid {
			summary += "- " + strings.TrimSpace(opp.TargetRole) + ": " + strings.TrimSpace(opp.Suggestion) + "\n"
		}
	}

	ctx := context.Background()
	var created []string
	var skipped []string
	for _, ts := range backlog.Tasks {
		epic := &board.Epic{
			TaskSpec: ts,
			Summary:  summary,
		}
		epic.RequiresReview = false // Ф-8: собственный бэклог архитектора ревизии не требует.
		if err := a.Store.CreateEpic(ctx, epic); err != nil {
			if errors.Is(err, board.ErrExists) {
				skipped = append(skipped, ts.TaskID)
				continue
			}
			return json.Marshal(map[string]string{
				"status":  "error",
				"message": "публикация эпика " + ts.TaskID + ": " + err.Error(),
			})
		}
		created = append(created, ts.TaskID)
	}

	res, _ := json.Marshal(map[string]any{
		"status":                "success",
		"created_epics":         created,
		"skipped_dups":          skipped,
		"total_epics":           len(backlog.Tasks),
		"architecture_summary":  backlog.ArchitectureSummary,
		"opportunities":         len(valid),
		"skipped_opportunities": skippedOpps,
	})
	return res, nil
}

// validOpportunities фильтрует кросс-функциональные возможности (Ф-6) и
// возвращает валидные (непустые target_role и suggestion) и число/список
// отбракованных. Запись без обязательных полей деградирует в «пропущено»
// (skipped), а не валит весь бэклог.
func validOpportunities(opps []board.Opportunity) ([]board.Opportunity, int) {
	if len(opps) == 0 {
		return nil, 0
	}
	valid := make([]board.Opportunity, 0, len(opps))
	skipped := 0
	for _, o := range opps {
		if strings.TrimSpace(o.TargetRole) == "" || strings.TrimSpace(o.Suggestion) == "" {
			skipped++
			continue
		}
		valid = append(valid, o)
	}
	return valid, skipped
}

// Обёртки доступных инструментов. Наблюдаемые снаружи сигнатуры сохранены
// и просто делегируют в реестр (единый источник вызовов).

func (a *Architect) ReadFiles(args map[string]any) ([]byte, error) {
	return a.Tools.Execute("ReadFiles", args)
}

func (a *Architect) List(args map[string]any) ([]byte, error) {
	return a.Tools.Execute("List", args)
}
