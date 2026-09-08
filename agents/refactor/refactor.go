package refactor

import (
	"ai/agents"
	"ai/agents/codegenerator"
	"fmt"
	"os"
	"strings"
)

// RefactorAgent — отдельный агент для работы с уже существующим проектом
// (рефакторинг, доработка). Отличается от codegenerator.Codegenerator двумя
// вещами: не требует обязательного вызова WriteFiles в первом раунде (модель
// начинает с изучения кода) и использует рефакторинговый системный промпт
// (управление промптами генерации остаётся в codegenerator, общий механизм
// файловых инструментов — в tools).
type RefactorAgent struct {
	*codegenerator.Codegenerator
	Role Role
}

// Role — роль разработчика (frontend/backend). Влияет только на системный
// промпт, чтобы агент не выходил за пределы своей части монорепозитория:
// фронтендер работает с frontend/, бэкендер — с server/.
type Role string

const (
	RoleFrontend Role = "frontend"
	RoleBackend  Role = "backend"
)

// NewRefactorAgent создаёт агента-рефактора для существующего проекта в
// temp/<projectName>. В отличие от генератора, проект должен уже существовать:
// модель читает текущий код и вносит целенаправленные правки, а не создаёт
// файлы с нуля.
func NewRefactorAgent(prompt, projectName string) (*RefactorAgent, error) {
	dir := codegenerator.ProjectDir(projectName)
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("проект не найден: %s (путь: %s)", projectName, dir)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("путь не является директорией: %s", dir)
	}
	cg, err := codegenerator.NewCodegeneratorInDir(prompt, dir)
	if err != nil {
		return nil, err
	}
	return &RefactorAgent{Codegenerator: cg}, nil
}

// RequiredToolFirstRound возвращает ("", false) — модель не обязана вызывать
// WriteFiles в первом раунде, она может начать с List/ReadFiles.
func (ra *RefactorAgent) RequiredToolFirstRound() (string, bool) {
	return "", false
}

// SetRole назначает роль разработчика (frontend/backend). Планировщик вызывает
// его перед запуском шага, чтобы агент знал свою область и стек. Строковый
// аргумент позволяет планировщику передавать сырое значение из JSON плана.
func (ra *RefactorAgent) SetRole(role string) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "backend", "back", "server", "api", "бэкенд", "бэк":
		ra.Role = RoleBackend
	case "frontend", "front", "client", "ui", "фронтенд", "фронт":
		ra.Role = RoleFrontend
	default:
		ra.Role = Role(role)
	}
}

// GetSystemMessages использует рефакторинговый системный промпт вместо
// генераторного (без требования "создай все файлы с нуля").
func (ra *RefactorAgent) GetSystemMessages(text []agents.Message) []agents.Message {
	lang := ra.Config.Language
	if lang == "" {
		lang = "Go"
	}

	var moduleInstruction string
	if ra.Config.Module != "" {
		moduleInstruction = fmt.Sprintf("Модуль проекта: %q. При необходимости обнови go.mod.", ra.Config.Module)
	}

	overwriteRule := "Ты можешь перезаписывать файлы инструментами WriteFiles."
	if ra.Config.NoOverwrite {
		overwriteRule = "Включена защита от перезаписи: инструменты вернут ошибку, если ты попытаешься перезаписать уже существующий файл."
	}

	var roleInstruction string
	switch ra.Role {
	case RoleFrontend:
		roleInstruction = `Твоя роль — ФРОНТЕНД-РАЗРАБОТЧИК. Ты работаешь ТОЛЬКО с клиентской частью
приложения (фронтендом) — обычно это подкаталог frontend/ (папка Node.js/React/Vue/etc).
Твоя область:
- Только файлы фронтенда: frontend/, client/, ui/ и т.п. Не трогай код бэкенда (server/, backend/, internal/).
- UI-компоненты, страницы, стили, клиентская логика, обращение к API.
- package.json и зависимости фронтенда (Vite/React/Vue/Next.js и т.п.).
Запрещено:
- Менять бэкенд: серверную логику, API-роуты, базы данных, Go/Python-код серверной части.
- Создавать серверные точки входа или переносить серверный код в фронтенд.
Если задача требует правок на бэкенде — сообщи это и работай только со своей частью.`
	case RoleBackend:
		roleInstruction = `Твоя роль — БЭКЕНД-РАЗРАБОТЧИК. Ты работаешь ТОЛЬКО с серверной частью
приложения (бэкендом) — обычно это подкаталог server/backend/ (Go/Python/Node-сервер, API, БД).
Твоя область:
- Только файлы бэкенда: server/, backend/, cmd/, internal/, api/ (серверная часть) и т.п.
- API-роуты, бизнес-логика, работа с БД, модели данных, сервисы.
- go.mod / requirements.txt / package.json серверной части.
Запрещено:
- Менять фронтенд: UI-компоненты, страницы, стили, клиентскую логику.
- Переписывать публичный интерфейс API без согласования с фронтендом.
Если задача требует правок на фронтенде — сообщи это и работай только со своей частью.`
	default:
		roleInstruction = `Ты работаешь со всей кодобазой проекта.`
	}

	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: fmt.Sprintf(`Ты — опытный разработчик на языке %s и архитектор. Твоя задача — провести рефакторинг или доработку уже существующего проекта.
%s
%s

Ты работаешь только внутри выходной директории проекта (OutputDir).
%s

Твой план работы:
1. Сначала изучи текущее состояние проекта: используй List для просмотра структуры, затем ReadFiles для чтения ключевых файлов.
2. Проанализируй код и определи, какие изменения необходимы для выполнения задания.
3. Вноси изменения: для больших файлов используй WriteFiles (полная перезапись), для точечных правок — AppendFile.
4. После каждого набора изменений проверяй, что проект компилируется: запускай "go build ./...", "go vet ./..." и (при наличии тестов) "go test ./..." через Run.
5. Если компиляция или проверки падают — исправляй код и запускай проверки снова, пока не станет зелёным.
6. При необходимости обнови README.md отражением изменений.

Правила:
- Нельзя отвечать текстом-рассуждением вместо действий. Используй инструменты.
- Начинай с изучения существующего кода, не переписывай всё без анализа.
- Сохраняй существующую архитектуру и стиль кода проекта.
- Не ломай существующий функционал, который не затрагивается заданием.
- Нельзя писать файлы вне OutputDir (инструменты сами это заблокируют).`, lang, moduleInstruction, roleInstruction, overwriteRule),
		},
	}
}
