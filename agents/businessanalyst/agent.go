package businessanalyst

import (
	"ai/agents"
	"ai/domain"
	"ai/domain/services"
	"ai/shared/projects"
	"ai/tools"
	"os"
	"path/filepath"
)

// baToolNames — инструменты Business Analyst: чтение структуры проекта,
// работа с общей Kanban-доской (чтение/обновление эпика, смена статуса).
var baToolNames = []string{
	"ReadFiles", "List",
	tools.BoardGetEpic, tools.BoardUpdateEpic, tools.BoardSetEpicStatus,
}

// BusinessAnalyst — агент бизнес-аналитика. Уточняет требования эпика:
// добавляет бизнес-контекст, приёмочные критерии, пользовательские сценарии.
// Работает только с эпиками, задачи не создаёт.
type BusinessAnalyst struct {
	// *tools.FileOps — разделяемый контекст файловых инструментов.
	*tools.FileOps
	Prompt string
	// Tools — выбранные инструменты из общего реестра.
	Tools *tools.Set
	// Store — общая Kanban-доска проекта (Redis).
	Store services.BoardStore
}

// SetBoardStore подключает бизнес-аналитика к доске.
func (ba *BusinessAnalyst) SetBoardStore(s services.BoardStore) {
	if s == nil {
		return
	}
	ba.Store = s
	ba.Tools = tools.Select(baToolNames, tools.Deps{Board: s, FileOps: ba.FileOps})
}

// GetBoard возвращает подключённую доску (nil — standalone-режим).
func (ba *BusinessAnalyst) GetBoard() services.BoardStore { return ba.Store }

// Label возвращает человекочитаемое имя агента.
func (ba *BusinessAnalyst) Label() string { return "Бизнес-аналитик" }

// NewBusinessAnalyst создаёт бизнес-аналитика в выходной папке проекта.
func NewBusinessAnalyst(projectName, prompt string) *BusinessAnalyst {
	dir := projects.ProjectDir(projectName)
	os.MkdirAll(dir, 0755)
	return newBusinessAnalyst(dir, prompt)
}

// NewBusinessAnalystInDir создаёт бизнес-аналитика в заданной директории.
func NewBusinessAnalystInDir(prompt, dir string) (*BusinessAnalyst, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return nil, err
	}
	return newBusinessAnalyst(abs, prompt), nil
}

func newBusinessAnalyst(dir, prompt string) *BusinessAnalyst {
	ops := &tools.FileOps{OutputDir: dir}
	return &BusinessAnalyst{
		FileOps: ops,
		Prompt:  prompt,
		Tools:   tools.Select(baToolNames, tools.Deps{FileOps: ops}),
	}
}

func (ba *BusinessAnalyst) GetUserMessages() []agents.Message {
	return []agents.Message{
		{
			Type:    agents.MessageTypeHuman,
			Message: ba.Prompt,
		},
	}
}

// RequiredToolFirstRound — бизнес-аналитик может начать с изучения эпика
// (BoardGetEpic) или сразу обновить его (BoardUpdateEpic).
func (ba *BusinessAnalyst) RequiredToolFirstRound() (string, bool) {
	return "", false
}

func (ba *BusinessAnalyst) GetSystemMessages(_ []agents.Message) []agents.Message {
	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: `Ты — бизнес-аналитик (Business Analyst). Твоя задача — уточнить требования эпика из черновика, созданного Chat Assistant.

Этап: бизнес-ревью (business_review). Твой результат — эпик готов к системному анализу архитектором.

ЧТО НУЖНО СДЕЛАТЬ:
1. Прочитай черновик эпика инструментом BoardGetEpic (используй epicID из задания).
2. Уточни и структурируй требования:
   - Бизнес-контекст: зачем это нужно, какую проблему решает, для кого.
   - Пользовательские сценарии (User Stories): «Как [роль], я хочу [действие], чтобы [цель]».
   - Приёмочные критерии (Acceptance Criteria): конкретные, проверяемые утверждения. Каждый критерий — независимое условие, которое можно проверить на зелёный/красный.
   - Нефункциональные требования: производительность, безопасность, ограничения.
3. Если в черновике эпика есть технические детали — не удаляй их, но дополни бизнес-контекстом.
4. Если требований недостаточно или эпик неясен — запроси уточнения через описание (description), не переводя в system_analysis.
5. Когда требования уточнены — переведи эпик в статус system_analysis инструментом BoardSetEpicStatus (business_review → system_analysis). Используй BoardUpdateEpic для сохранения обновлённого описания.

ПРАВИЛА:
- Не пиши код, не создавай задачи — это сделает архитектор.
- Не меняй статус задачи — ты работаешь только с эпиками.
- Приёмочные критерии должны быть конкретными и проверяемыми (не «должно работать быстро», а «отклик API ≤ 200 мс при 1000 req/s»).
- Если эпик слишком широкий — разбей его на подэпики через BoardCreateEpic и переведи исходный в cancelled.
- Не используй инструменты для записи файлов — у тебя их нет.

Инструменты:
- BoardGetEpic — прочитать эпик
- BoardUpdateEpic — обновить описание, статус, назначение
- BoardSetEpicStatus — перевести в system_analysis (когда требования готовы) или cancelled (если эпик неактуален)
- ReadFiles, List — изучить код проекта для контекста`,
		},
	}
}

// GetTools возвращает определения выбранных инструментов.
func (ba *BusinessAnalyst) GetTools() []domain.ToolDefinition {
	return ba.Tools.Definitions()
}

func (ba *BusinessAnalyst) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	return ba.Tools.Execute(functionName, functionArgs)
}
