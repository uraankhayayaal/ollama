package devops

import (
	"ai/agents"
	"ai/board"
	"ai/projects"
	"ai/tools"
	"os"
	"path/filepath"

	"github.com/ollama/ollama/api"
)

// devopsToolNames — инструменты DevOps-инженера, выбираемые из общего реестра
// tools. Агент пишет инфраструктурный код (Docker Compose, Kubernetes,
// конфигурации CI/CD, Dockerfile) и проверяет его запуском команд.
var devopsToolNames = []string{
	"WriteFiles", "ReadFiles", "DeleteFiles", "AppendFile", "List", "Run",
}

// devopsBoardToolNames — инструменты общей Kanban-доски, добавляемые
// DevOps-инженеру, когда оркестратор Kanban подключает доску (SetBoardStore):
// чтение своей задачи и смена её статуса.
var devopsBoardToolNames = []string{
	tools.BoardGetTask, tools.BoardSetTaskStatus,
}

// Devops — агент DevOps Engineer. Пишет и оптимизирует инфраструктурный код
// в выходной директории OutputDir: локально — Docker Compose, для прода —
// манифесты Kubernetes, и настраивает прозрачный CI/CD, вызывающий точную
// команду автотестов QA. Инструменты разделяются с остальными агентами через
// общий реестр tools.
type Devops struct {
	// *tools.FileOps — разделяемый контекст файловых инструментов
	// (OutputDir, MaxFiles, NoOverwrite). Поля и методы FileOps промотируются:
	// d.OutputDir, d.Write(..) и т.п. доступны напрямую.
	*tools.FileOps
	Prompt string
	Config Config
	// Tools — выбранные агентом инструменты из общего реестра
	// (единый источник для GetTools/GetToolsForOllama и диспетчеризации вызовов).
	Tools *tools.Set
	// Store — общая Kanban-доска проекта (Redis). Подключается оркестратором
	// Kanban через SetBoardStore; в standalone-режиме (CLI) — nil.
	Store *board.Store
}

// SetBoardStore подключает DevOps-инженера к общей Kanban-доске проекта:
// добавляет инструменты статуса задачи (Board*), передаёт Board-контекст в
// реестр. Вызывается оркестратором Kanban при построении агента специалиста.
func (d *Devops) SetBoardStore(s *board.Store) {
	if s == nil {
		return
	}
	d.Store = s
	names := append(append([]string{}, devopsToolNames...), devopsBoardToolNames...)
	d.Tools = tools.Select(names, tools.Deps{FileOps: d.FileOps, Board: s})
}

// NewDevops создаёт DevOps-агента в общей для всех агентов выходной папке
// temp/<projectName> в корне модуля (единая точка для генератора и рефактора,
// чтобы агент работал с тем же проектом). projectName — имя проекта, задаётся
// пользователем. prompt — текст задания для модели (может быть пустым — тогда
// используется задание по умолчанию).
func NewDevops(projectName, prompt string) *Devops {
	dir := projects.ProjectDir(projectName)
	os.MkdirAll(dir, 0755)
	return newDevops(dir, prompt, LoadConfig())
}

// NewDevopsInDir создаёт DevOps-агента в заданной директории (а не в новой
// temp/). Используется, когда нужно работать с уже существующей директорией
// проекта, где уже лежит код.
func NewDevopsInDir(prompt, dir string) (*Devops, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return nil, err
	}
	return newDevops(abs, prompt, LoadConfig()), nil
}

// newDevops создаёт агента в заданной директории: собирает *tools.FileOps
// (лимиты записи берутся из конфига) и выбирает из реестра инструменты
// DevOps-инженера. Используется всеми конструкторами.
func newDevops(dir, prompt string, cfg Config) *Devops {
	ops := &tools.FileOps{OutputDir: dir, MaxFiles: cfg.MaxFiles, NoOverwrite: cfg.NoOverwrite}
	return &Devops{
		FileOps: ops,
		Prompt:  prompt,
		Config:  cfg,
		Tools:   tools.Select(devopsToolNames, tools.Deps{FileOps: ops}),
	}
}

func (d *Devops) GetUserMessages() []agents.Message {
	return []agents.Message{
		{
			Type:    agents.MessageTypeHuman,
			Message: d.Prompt,
		},
	}
}

// RequiredToolFirstRound — DevOps-инженер не обязан обязательно вызывать
// конкретный инструмент в первом раунде: модель может начать с изучения
// существующих манифестов (List/ReadFiles) или сразу записать новый код.
func (d *Devops) RequiredToolFirstRound() (string, bool) {
	return "", false
}

func (d *Devops) GetSystemMessages(_ []agents.Message) []agents.Message {
	overwriteRule := "Ты можешь перезаписывать файлы инструментами WriteFiles."
	if d.Config.NoOverwrite {
		overwriteRule = "Включена защита от перезаписи: инструменты вернут ошибку, если ты попытаешься перезаписать уже существующий файл. Перезаписывай файлы только при необходимости."
	}

	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: `Ты — опытный DevOps Engineer. Твоя цель — выполнять инфраструктурные задачи, поставленные DevOps Lead, обеспечивая стабильность локального окружения и деплоя на прод.

Ты работаешь только внутри выходной директории проекта (OutputDir).
` + overwriteRule + `

ПРАВИЛА РАБОТЫ:
1. Строго по стеку: Локально пиши и оптимизируй Docker Compose. Для прода готовь манифесты Kubernetes. Никаких devcontainer и сторонних оркестраторов.
2. Простота кода (KISS): Избегай сложных, трудночитаемых bash-скриптов внутри пайплайнов. Конфигурация CI/CD должна быть прозрачной и легко поддерживаемой.
3. Поддержка QA: Настраивай CI/CD пайплайн так, чтобы в нем вызывалась точная консольная команда автотестирования. Убедись, что локальный Docker Compose содержит все необходимые сервисы и моки для корректной работы тестов QA.

ОГРАНИЧЕНИЕ НА ФОРМАТ ОТВЕТА:
Выдавай ТОЛЬКО готовый инфраструктурный код (YAML-манифесты, конфигурации CI/CD, Dockerfile). Минимизируй сопутствующий текст, пиши только комментарии к коду, если это необходимо для его работы.

Твой план работы:
1. Изучи текущее состояние проекта: используй List для просмотра структуры, затем ReadFiles для чтения существующих манифестов и конфигов (compose, Dockerfile, k8s, CI/CD).
2. Создай или обнови все нужные инфраструктурные файлы инструментами WriteFiles (для точечных правок используй AppendFile, для удаления — DeleteFiles).
3. Проверь корректность написанного: запускай проверки через Run, например "docker compose config", "kubectl apply --dry-run=client -f <манифест>", либо синтаксическую проверку YAML. Исправляй, пока всё не станет валидным.
4. Убедись, что локальный Docker Compose поднимает все сервисы и моки, необходимые QA, и что в CI/CD указана точная команда автотестов (например "go test ./...", "npm test" и т.п.).

Правила:
- Нельзя отвечать текстом-рассуждением вместо действий. Используй инструменты.
- Нельзя писать файлы вне OutputDir (инструменты сами это заблокируют).`,
		},
	}
}

// GetTools возвращает определения выбранных инструментов (единый JSON Schema
// формат для OpenAI/Yandex). Источник схем — реестр tools.
func (d *Devops) GetTools() []tools.ToolDefinition {
	return d.Tools.Definitions()
}

// GetToolsForOllama конвертирует определения инструментов в формат Ollama
// через единый конвертер tools.ToOllama.
func (d *Devops) GetToolsForOllama() []api.Tool {
	return tools.ToOllama(d.GetTools())
}

func (d *Devops) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	return d.Tools.Execute(functionName, functionArgs)
}

// Обёртки выбранных файловых инструментов. Наблюдаемые снаружи сигнатуры
// сохранены и просто делегируют в реестр (единый источник вызовов).

func (d *Devops) WriteFiles(args map[string]any) ([]byte, error) {
	return d.Tools.Execute("WriteFiles", args)
}
func (d *Devops) ReadFiles(args map[string]any) ([]byte, error) {
	return d.Tools.Execute("ReadFiles", args)
}
func (d *Devops) DeleteFiles(args map[string]any) ([]byte, error) {
	return d.Tools.Execute("DeleteFiles", args)
}
func (d *Devops) AppendFile(args map[string]any) ([]byte, error) {
	return d.Tools.Execute("AppendFile", args)
}
func (d *Devops) List(args map[string]any) ([]byte, error) {
	return d.Tools.Execute("List", args)
}
func (d *Devops) Run(args map[string]any) ([]byte, error) {
	return d.Tools.Execute("Run", args)
}
