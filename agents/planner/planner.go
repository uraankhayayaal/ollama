package planner

import (
	"ai/agents"
	"ai/agents/codegenerator"
	"ai/tools"
	"fmt"

	"github.com/ollama/ollama/api"
)

// plannerToolNames — минимальный набор инструментов для анализа состояния проекта.
var plannerToolNames = []string{"List", "ReadFiles"}

// Planner — агент-планировщик, разбивает запрос пользователя на поэтапный план.
// Использует минимальный набор инструментов (List, ReadFiles) для анализа
// текущего состояния проекта перед составлением плана.
type Planner struct {
	Prompt      string
	ProjectName string
	// ProjectMap — компактная карта проекта (структура, размеры, языки),
	// собирается детерминированно до вызова модели. Нужна для декомпозиции
	// задачи на шаги с точным scope.
	ProjectMap string
	Tools      *tools.Set
}

// NewPlanner создаёт планировщик для заданного проекта.
// Инструменты планировщика указывают на директорию проекта temp/<projectName>,
// чтобы модель могла изучить существующий код перед составлением плана.
func NewPlanner(projectName, prompt string) *Planner {
	dir := codegenerator.ProjectDir(projectName)
	ops := &tools.FileOps{OutputDir: dir}
	return &Planner{
		Prompt:      prompt,
		ProjectName: projectName,
		ProjectMap:  BuildProjectMap(projectName),
		Tools:       tools.Select(plannerToolNames, tools.Deps{FileOps: ops}),
	}
}

func (p *Planner) GetUserMessages() []agents.Message {
	return []agents.Message{
		{Type: agents.MessageTypeHuman, Message: p.Prompt},
	}
}

// RequiredToolFirstRound — планировщик не требует обязательного инструмента:
// модель может сразу выдать план, если контекста достаточно.
func (p *Planner) RequiredToolFirstRound() (string, bool) {
	return "", false
}

func (p *Planner) GetSystemMessages(_ []agents.Message) []agents.Message {
	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: fmt.Sprintf(`Ты — архитектор-планировщик. Твоя задача — разбить запрос пользователя на поэтапный план из чётких инструкций.

Проект: %s

%s

Доступные агенты для делегации:
- codegenerator: создание нового проекта/файлов с нуля
- refactor: доработка/рефакторинг существующего проекта
- codereviewer: ревью написанного кода

Твой план:
1. Если проект уже существует — изучи его структуру через List и ключевые файлы через ReadFiles. Карта проекта выше уже даёт структуру и размеры файлов: используй её, чтобы точно определить, какие файлы/пакеты затронет задача.
2. Проанализируй запрос пользователя и определи, какие шаги нужны.
3. Составь план в формате JSON.

Формат ответа — ТОЛЬКО JSON без markdown-обёрток:
{
  "project_name": "имя_проекта",
  "summary": "краткое описание плана",
  "steps": [
    {
      "id": "step1",
      "agent": "codegenerator|refactor|codereviewer",
      "prompt": "конкретная инструкция для агента",
      "project_name": "имя_проекта",
      "depends_on": [],
      "description": "что делает этот шаг",
      "scope": ["src/main.go", "internal/order/"]
    }
  ]
}

Декомпозиция задач (обязательно):
- Разбивай крупную работу на НЕСКОЛЬКО небольших шагов, каждый — про одно
  конкретное изменение. Не пиши один гигантский шаг «сделай всё».
- Ориентир: scope одного шага — не больше 5–10 файлов. Если изменение
  затрагивает больше файлов — разбей на несколько последовательных шагов
  (шаг N меняет пакет A, шаг N+1 — пакет B).
- Большие проекты всегда начинай с узких шагов по одному пакету/каталогу,
  а не с шага «отрефактори весь проект».
- Каждый refactor-шаг должен читать и менять минимум файлов: только те,
  которые реально относятся к задаче.

Правила:
- Каждый шаг — минимально достаточный контекст для агента.
- Не дублируй информацию между шагами.
- Шаги идут в порядке выполнения (depends_on указывает зависимости).
- Для нового проекта: сначала codegenerator, затем codereviewer.
- Для рефакторинга: сначала refactor, затем codereviewer.
- Последний шаг — codereviewer для проверки качества.
- Пиши промпты для агентов максимально конкретно и кратко.

Область работы (scope):
- В scope каждого шага перечисляй ТОЛЬКО те файлы и директории, которые этот
  шаг создаёт, читает или изменяет. Примеры: "src/main.go", "internal/order/".
- Агент работает строго в рамках своего scope: он не может читать, писать или
  удалять файлы вне него, а ревьювер видит в диффе только файлы из scope.
- Для codegenerator (новые файлы) scope = создаваемые файлы/пакеты.
- Для refactor scope = файлы, которые нужно изменить (не весь проект).
- Для codereviewer scope = те же файлы, что создали/изменили предыдущие шаги
  (ни в коем случае не весь проект) — это предотвращает лавину замечаний
  на несвязанный код.
- Если шагу действительно нужен весь проект (например, глобальная проверка
  сборки) — оставляй scope пустым, но делай это осознанно и редко.`, p.ProjectName, p.ProjectMap),
		},
	}
}

func (p *Planner) GetTools() []tools.ToolDefinition {
	return p.Tools.Definitions()
}

func (p *Planner) GetToolsForOllama() []api.Tool {
	return tools.ToOllama(p.GetTools())
}

func (p *Planner) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	return p.Tools.Execute(functionName, functionArgs)
}
