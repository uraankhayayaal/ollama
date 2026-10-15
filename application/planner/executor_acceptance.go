package planner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ai/domain"
	"ai/domain/services"
	"ai/shared/projects"
)

// runAcceptorAgent выполняет детерминированную приёмку собранного приложения:
// определяет тип проекта, собирает его, запускает на короткое время и
// анализирует логи. Сам шаг никогда не «падает» — отрицательный вердикт
// сохраняется в отчёт и обрабатывается циклом runAcceptanceLoop.
//
// Если scope шага указывает на один подкаталог (например frontend/ или
// server/) — приёмка выполняется в нём отдельно: у фронтенда и бэкенда
// собственная сборка и проверки. Пустой scope — приёмка всего корня
// (acceptor сам найдёт подпроекты и примет каждый по отдельности).
func (e *Executor) runAcceptorAgent(ctx context.Context, step *Step, projectName string) error {
	root := projects.ProjectDir(projectName)
	dir := acceptanceDir(root, step.Scope)
	if e.acceptor == nil {
		return fmt.Errorf("приёмка: порт acceptor не подключён")
	}
	rep := e.acceptor.Accept(dir)
	e.mu.Lock()
	e.acceptReports[step.ID] = rep
	e.mu.Unlock()

	// Телеметрия стадий приёмки в чекпоинт (verdict, статусы сборки/запуска/
	// форматирования/анализа). Чистая диагностика, на вердикт не влияет.
	m := map[string]any{
		"verdict": string(rep.Verdict),
		"dir":     dir,
	}
	if rep.Build.OK {
		m["build"] = "ok"
	} else {
		m["build"] = "fail"
	}
	if rep.Build.Skipped {
		m["build"] = "skipped"
	}
	if rep.Run != nil {
		m["run"] = "fail"
		if rep.Run.OK {
			m["run"] = "ok"
		}
		if rep.Run.Skipped {
			m["run"] = "skipped"
		}
	}
	if rep.Format != nil {
		m["format"] = "fail"
		if rep.Format.OK {
			m["format"] = "ok"
		}
	}
	if rep.Analyze != nil {
		m["analyze"] = "fail"
		if rep.Analyze.OK {
			m["analyze"] = "ok"
		}
	}
	e.trackStepMetrics(ctx, step.ID, m)

	if rep.Verdict == domain.VerdictApprove {
		e.log.Infof("[приёмка] шаг %s: приёмка %q пройдена (%s)", step.ID, dir, rep.Summary)
	} else {
		e.log.Infof("[приёмка] шаг %s: приёмка %q НЕ пройдена (%s)", step.ID, dir, rep.Summary)
		for _, iss := range rep.Issues {
			loc := iss.File
			if iss.Line > 0 {
				loc = fmt.Sprintf("%s:%d", loc, iss.Line)
			}
			if loc != "" {
				e.log.Detailf("[приёмка]   - [%s] %s %s", iss.Severity, loc, iss.Text)
			} else {
				e.log.Detailf("[приёмка]   - [%s] %s", iss.Severity, iss.Text)
			}
		}
	}
	return nil
}

// acceptanceDir выбирает директорию приёмки для шага. Если scope шага сужается
// до одного существующего подкаталога (например ["frontend/"]) — приёмка идёт
// в нём; иначе — в корне проекта.
func acceptanceDir(root string, scope []string) string {
	top := topLevelScopeDir(scope)
	if top == "" {
		return root
	}
	candidate := filepath.Join(root, top)
	if st, err := os.Stat(candidate); err == nil && st.IsDir() {
		return candidate
	}
	return root
}

// topLevelScopeDir возвращает общую старшую директорию всех записей scope,
// если они все лежат в одном подкаталоге проекта ("frontend" для
// ["frontend/src/App.tsx", "frontend/package.json"]). Возвращает "" при разных
// подкаталогах, пустом scope или файле в корне.
func topLevelScopeDir(scope []string) string {
	seen := map[string]bool{}
	var dirs []string
	for _, s := range scope {
		s = strings.TrimPrefix(strings.TrimSpace(strings.ReplaceAll(s, "\\", "/")), "./")
		s = strings.Trim(strings.TrimSpace(s), "/")
		if s == "" {
			continue
		}
		first := s
		if i := strings.IndexByte(s, '/'); i >= 0 {
			first = s[:i]
		}
		if first == "." || first == ".." {
			continue
		}
		if !seen[first] {
			seen[first] = true
			dirs = append(dirs, first)
		}
	}
	if len(dirs) == 1 {
		return dirs[0]
	}
	return ""
}

// leadStepScope возвращает scope специалистов шага-декомпозиции лида.
// Планировщик даёт шагу точечный scope (конкретные новые файлы, например
// frontend/package.json), но лид декомпозирует шаг на задачи, затрагивающие
// произвольные файлы всей области (компоненты, стили, сервисы). Сужать
// специалистов до одного файла из плана нельзя — они не смогут писать свои
// файлы («вне области работы»). Поэтому специалистам выставляется корневая
// директория шага (первый сегмент: frontend/, server/), а не перечень файлов.
func leadStepScope(projectDir string, step *Step) []string {
	var scope []string
	if len(step.Scope) > 0 {
		if top := topLevelScopeDir(step.Scope); top != "" {
			scope = []string{top + "/"}
		} else {
			scope = effectiveCreationScope(projectDir, step.Scope)
		}
	}
	// PLAN.md всегда доступен специалистам: в нём полный план работ с
	// контрактами задач лидов, и каждый следующий разработчик должен его
	// прочитать, чтобы понять план и объём уже реализованного.
	scope = append(scope, planDocName)
	return scope
}

// effectiveCreationScope расширяет scope шага для СОЗДАНИЯ файлов. Если запись
// scope указывает на конкретный файл, которого ещё нет на диске, шаг создаёт
// его с нуля, и модель может выбрать фактическое имя (например, .js вместо
// .tsx из плана). Точное имя из плана в этом случае лишь зацикливает шаг на
// ошибках «вне области работы», и файлы не появляются. Поэтому для новых
// файлов scope расширяется до родительской директории; существующие файлы
// (шаги правки) остаются точечными, как задал планировщик.
func effectiveCreationScope(projectDir string, scope []string) []string {
	if len(scope) == 0 {
		return scope
	}
	out := make([]string, 0, len(scope))
	changed := false
	for _, s := range scope {
		normalized := strings.TrimSpace(strings.ReplaceAll(s, "\\", "/"))
		trimmed := strings.TrimSuffix(normalized, "/")
		// Директории (со слэшем на конце) не трогаем.
		if trimmed != normalized || trimmed == "" {
			out = append(out, s)
			continue
		}
		full := filepath.Join(projectDir, filepath.FromSlash(trimmed))
		if _, err := os.Stat(full); os.IsNotExist(err) {
			if dir := filepath.ToSlash(filepath.Dir(trimmed)); dir != "." && dir != "" {
				out = append(out, dir+"/")
				changed = true
				continue
			}
		}
		out = append(out, s)
	}
	if !changed {
		return scope
	}
	return out
}

// runAcceptanceLoop — цикл «приёмка → планировщик исправлений → приёмка».
// Пока хотя бы один шаг acceptor плана не прошёл приёмку (и не исчерпан
// бюджет раундов ACCEPT_MAX_ROUNDS): отчёт приёмки передаётся планировщику,
// тот составляет шаги исправления разработчиками backend/frontend,
// исполнитель их прогоняет и повторяет приёмку.
func (e *Executor) runAcceptanceLoop(ctx context.Context) error {
	if e.acceptor == nil {
		return nil
	}
	maxRounds := e.acceptor.MaxRounds()
	if maxRounds <= 0 {
		return nil
	}

	// Шаги приёмки плана. Исправления выполняются сразу, без добавления
	// в волны: план остаётся неизменным.
	var acceptSteps []*Step
	for i := range e.plan.Steps {
		if e.plan.Steps[i].Agent == AgentQAEngineer {
			acceptSteps = append(acceptSteps, &e.plan.Steps[i])
		}
	}
	if len(acceptSteps) == 0 {
		return nil
	}

	for round := 1; round <= maxRounds; round++ {
		var failing []*Step
		for _, s := range acceptSteps {
			switch rep := e.acceptReports[s.ID]; {
			case rep == nil:
				failing = append(failing, s)
			case rep.Verdict != domain.VerdictApprove:
				failing = append(failing, s)
			}
		}
		if len(failing) == 0 {
			e.log.Infof("[приёмка] приёмка пройдена: все проекты соответствуют требованиям")
			return nil
		}

		for _, s := range failing {
			rep := e.acceptReports[s.ID]
			// Check if report is nil before proceeding with fixes
			if rep == nil {
				e.log.Warnf("[приёмка] раунд %d/%d: отчёт приёмки отсутствует для шага %q", round, maxRounds, s.Description)
				// Try to re-run acceptor for this step
				e.log.Infof("[приёмка] раунд %d/%d: повторный запуск приёмки для шага %q", round, maxRounds, s.Description)
				if err := e.runAcceptorAgent(ctx, s, e.plan.ProjectName); err != nil {
					return fmt.Errorf("раунд приёмки %d: повторный запуск приёмки для шага %q: %w", round, s.Description, err)
				}
				// Check again if the report is now available
				rep = e.acceptReports[s.ID]
				if rep == nil {
					return fmt.Errorf("раунд приёмки %d: отчёт приёмки всё ещё отсутствует для шага %q", round, s.Description)
				}
			}

			e.log.Infof("[приёмка] раунд %d/%d: приёмка %q не пройдена — вызываю планировщик исправлений", round, maxRounds, s.Description)

			fixes, err := e.planFixSteps(ctx, rep)
			if err != nil {
				return fmt.Errorf("раунд приёмки %d: планирование исправлений: %w", round, err)
			}
			if len(fixes) == 0 {
				e.log.Warnf("[приёмка] раунд %d: планировщик не вернул шагов исправлений", round)
				return fmt.Errorf("раунд приёмки %d: планировщик не составил план исправлений для %q", round, s.Description)
			}

			// Выполняем шаги исправления (planning-результат), пропуская
			// неприменимые/лишние типы агентов.
			executed := 0
			for _, fs := range fixes {
				if fs.Agent == AgentQAEngineer {
					// Модель проигнорировала запрет: приёмку запускает цикл ниже.
					e.log.Detailf("[приёмка] раунд %d: шаг qa в плане исправлений пропущен", round)
					continue
				}
				// Фикс-шаг не должен быть лид-типа: лид декомпозирует задачи, а в
				// цикле исправлений нужен один точечный правщик кода. Модель часто
				// возвращает backendlead/frontendlead — нормализуем в developer
				// соответствующей специализации по роли/подпроекту.
				switch fs.Agent {
				case AgentFrontendLead:
					e.log.Detailf("[приёмка] раунд %d: лид-шаг %s приведён к frontend-developer", round, fs.Agent)
					fs.Agent = AgentFrontendDev
				case AgentBackendLead:
					e.log.Detailf("[приёмка] раунд %d: лид-шаг %s приведён к backend-developer", round, fs.Agent)
					fs.Agent = AgentBackendDev
				}
				step := fs
				step.ID = fixStepID(e, round, executed)
				// Обновлённая область видимости: используем файлы из отчёта
				// приёмки, чтобы фикс-агента не заблокировала запись нужных
				// исходников. Если планировщик не указал scope — подставляем
				// файлы из отчёта.
				// Если приёмка вовсе не смогла указать конкретные файлы
				// (например, тип проекта ещё не определён — нет go.mod или
				// package.json, замечания без файлов) — scope принудительно
				// обнуляем: сужать область догадками планировщика нельзя,
				// иначе исправление упрётся в «вне области работы» и не
				// сможет записать нужные файлы.
				if files := rep.IssueFiles(); len(files) == 0 {
					step.Scope = nil
				} else if len(step.Scope) == 0 {
					step.Scope = files
				}
				// Переланировка добавляет задачу исправления в план: она
				// становится частью плана (findStep/чекпоинт/resume), а её
				// scope — обновлённый (файлы из отчёта приёмки).
				e.plan.Steps = append(e.plan.Steps, step)
				e.log.Infof("[%s] раунд %d: шаг исправления %s: %s", agentLabel(step.Agent, step.Role), round, step.ID, step.Description)
				e.markRunning(ctx, step.ID)
				if err := e.executeStep(ctx, &step); err != nil {
					e.markFailed(ctx, step.ID)
					return fmt.Errorf("раунд приёмки %d, шаг исправления %q: %w", round, step.ID, err)
				}
				e.markDone(ctx, step.ID)
				executed++
			}
			if executed == 0 {
				return fmt.Errorf("раунд приёмки %d: планировщик не дал применимых шагов исправлений", round)
			}

			// Повторная приёмка после исправлений.
			e.log.Infof("[приёмка] раунд %d: повторная приёмка после исправлений", round)
			if err := e.runAcceptorAgent(ctx, s, e.plan.ProjectName); err != nil {
				return err
			}
		}
	}

	e.log.Warnf("[приёмка] исчерпан бюджет раундов приёмки (%d) — остались неисправленные замечания", maxRounds)
	return fmt.Errorf("приёмка не пройдена после %d раундов исправлений, см. лог приёмки", maxRounds)
}

// planFixSteps отдаёт планировщику отчёт приёмки и получает шаги исправления.
// Формат — тот же JSON-план, но планировщику запрещено добавлять acceptor:
// повторную приёмку запускает цикл приёмки исполнителя.
func (e *Executor) planFixSteps(ctx context.Context, rep *domain.Report) ([]Step, error) {
	// Обновлённая область видимости для задач исправления: исходит из тех
	// файлов, на которые реально указывает приёмка (acceptor не ограничен
	// областью видимости и видит весь модуль). Эти локации передаём
	// планировщику, чтобы scope шага покрывал файлы, которые придётся менять,
	// а не сузился до произвольного (например, [go.mod]) и не заблокировал
	// фикс-агента на записи нужных файлов.
	issueFiles := []string{}
	if rep != nil {
		issueFiles = uniqueSlash(rep.IssueFiles())
	}
	prompt := fmt.Sprintf(`Приёмка собранного приложения не пройдена.

%s

Карта уже реализованного кода проекта (существующие пакеты, типы и сигнатуры — НЕ дублируй и НЕ переписывай их, исправляй точечно):

%s

Составь план исправлений этих ошибок.
Требования:
- Все шаги — только агенты-разработчики backend или frontend, выбранные по принадлежности файлов к подпроекту (server/→backend, frontend/→frontend).
- НЕ добавляй шаг qa — повторную сборку, тестирование и приёмку запустит исполнитель.
- scope каждого шага — ТОЛЬКО файлы, реально требующие правки, обязательно включая:
%s
  (а не весь проект и не узкую догадку вроде только [go.mod], если правка нужна в исходниках).
- Каждый шаг — одно конкретное исправление.`, rep.FixPrompt(), BuildProjectMap(rep.Project), bullet(issueFiles))

	pa := NewPlanner(rep.Project, prompt)
	resp, err := e.provider.Generate(ctx, pa)
	if err != nil {
		return nil, err
	}
	fixPlan, err := ParsePlan(resp.Content)
	if err != nil {
		e.log.Warnf("[планировщик] не удалось разобрать план исправлений: %v", err)
		e.log.Detailf("[планировщик] Ответ планировщика:\n%s", resp.Content)
		return nil, err
	}
	return fixPlan.Steps, nil
}

// leadWithHint оборачивает агента-лида для повторного запроса декомпозиции:
// одна попытка на тот же промпт, но с подсказкой, что предыдущий ответ не был
// JSON. Новый контекст (чистый), поэтому подсказка кладётся в системные
// сообщения, чтобы модель сразу её видела.
func leadWithHint(lead services.Agent, prevContent string) services.Agent {
	hint := "Ваш предыдущий ответ не был распознан как JSON-декомпозиция задач.\n" +
		"Верните ТОЛЬКО допустимый JSON — массив объектов вида:\n" +
		`[{"title":"...","description":"...","role":"backend|frontend","files":["..."]}]` + "\n" +
		"без пояснений, markdown-обёрток и путей. Роль — одна из: backend, frontend, devops." +
		"\n\nВаш предыдущий ответ, чтобы не повторять его ошибки:\n" + prevContent
	return &leadHintAgent{inner: lead, hint: hint}
}

// leadHintAgent — декоратор services.Agent, добавляющий подсказку к системным
// сообщениям лида (остальное делегирует внутреннему агенту).
type leadHintAgent struct {
	inner services.Agent
	hint  string
}

func (h *leadHintAgent) GetSystemMessages(text []domain.AgentMessage) []domain.AgentMessage {
	base := h.inner.GetSystemMessages(text)
	return append(base, domain.AgentMessage{Type: domain.MessageTypeSystem, Message: h.hint})
}

func (h *leadHintAgent) GetUserMessages() []domain.AgentMessage { return h.inner.GetUserMessages() }

func (h *leadHintAgent) GetTools() []domain.ToolDefinition { return h.inner.GetTools() }

func (h *leadHintAgent) CallFunction(name string, args map[string]any) ([]byte, error) {
	return h.inner.CallFunction(name, args)
}

// fixStepID генерирует уникальный ID шага исправления приёмки, чтобы не
// пересекаться с ID шагов основного плана и других раундов.
func fixStepID(e *Executor, round, idx int) string {
	base := fmt.Sprintf("accept-r%d-%d", round, idx)
	if e.findStep(base) == nil {
		return base
	}
	for n := 1; ; n++ {
		id := fmt.Sprintf("accept-r%d-%d-%d", round, idx, n)
		if e.findStep(id) == nil {
			return id
		}
	}
}

// uniqueSlash возвращает уникальные элементы списка, нормализуя пути к
// slash-формату.
func uniqueSlash(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		it = strings.TrimSpace(strings.ReplaceAll(it, "\\", "/"))
		if it == "" || seen[it] {
			continue
		}
		seen[it] = true
		out = append(out, it)
	}
	return out
}

// bullet форматирует список как маркированный отступ (пустой список → «—»).
func bullet(items []string) string {
	if len(items) == 0 {
		return "  - (файлы из отчёта приёмки)"
	}
	var b strings.Builder
	for _, it := range items {
		b.WriteString("  - ")
		b.WriteString(it)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// findStep находит шаг по ID.
func (e *Executor) findStep(id string) *Step {
	for i := range e.plan.Steps {
		if e.plan.Steps[i].ID == id {
			return &e.plan.Steps[i]
		}
	}
	return nil
}

// chatNameForSpec возвращает имя агента для чата по типу агента и роли задачи.
func chatNameForSpec(spec services.Agent, role string) string {
	type labler interface{ Label() string }
	if l, ok := spec.(labler); ok && l.Label() != "" {
		return l.Label()
	}
	r := strings.ToLower(role)
	switch {
	case strings.Contains(r, "qa") && strings.Contains(r, "lead"):
		return "QA-лид"
	case strings.Contains(r, "qa"), strings.Contains(r, "тест"), strings.Contains(r, "testing"):
		return "QA-инженер"
	case strings.Contains(r, "devopslead") || (strings.Contains(r, "lead") && (strings.Contains(r, "devops") || strings.Contains(r, "инфра") || strings.Contains(r, "infra"))):
		return "DevOps-лид"
	case strings.Contains(r, "devops"), strings.Contains(r, "инфра"), strings.Contains(r, "infra"), strings.Contains(r, "sre"), strings.Contains(r, "ci"):
		return "DevOps-инженер"
	case strings.Contains(r, "frontendlead") || (strings.Contains(r, "lead") && (strings.Contains(r, "front") || strings.Contains(r, "фронт") || strings.Contains(r, "ui"))):
		return "Frontend-лид"
	case strings.Contains(r, "backendlead") || (strings.Contains(r, "lead") && (strings.Contains(r, "back") || strings.Contains(r, "бэк"))):
		return "Backend-лид"
	case strings.Contains(r, "architect"):
		return "архитектор"
	case strings.Contains(r, "front"), strings.Contains(r, "react"), strings.Contains(r, "vue"), strings.Contains(r, "next"), strings.Contains(r, "ui"), strings.Contains(r, "фронт"):
		return "фронтенд-разработчик"
	case strings.Contains(r, "back"), strings.Contains(r, "бэк"), strings.Contains(r, "backend"), strings.Contains(r, "go"), strings.Contains(r, "python"), strings.Contains(r, "java"), strings.Contains(r, "node"):
		return "бэкенд-разработчик"
	}
	return role
}
