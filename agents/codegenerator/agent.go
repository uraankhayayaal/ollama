package codegenerator

// Board required store imports.
import (
	"ai/agents"
	"ai/agents/codereviewer"
	"ai/board"
	"ai/forges"
	"ai/logging"
	"ai/tools"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ollama/ollama/api"
)

// codegenToolNames — инструменты, которые генератор выбирает из общего реестра
// tools. Каждый агент сам решает, с какими инструментами работать; сами
// реализации (WriteFiles, ReadFiles, ...) живут в пакете tools и разделяются
// между агентами.
var codegenToolNames = []string{
	"WriteFiles", "ReadFiles", "DeleteFiles", "Run", "List", "AppendFile",
}

// codegenBoardToolNames — инструменты общей Kanban-доски, добавляемые
// генератору, когда оркестратор Kanban подключает доску (SetBoardStore):
// чтение своей задачи и смена её статуса.
var codegenBoardToolNames = []string{
	tools.BoardGetTask, tools.BoardSetTaskStatus,
}

type Codegenerator struct {
	// *tools.FileOps — разделяемый контекст файловых инструментов
	// (OutputDir, MaxFiles, NoOverwrite). Поля и методы FileOps промотируются:
	// cg.OutputDir, cg.Write(..) и т.п. доступны напрямую.
	*tools.FileOps
	Prompt string
	Config Config
	// Tools — выбранные генератором инструменты из общего реестра
	// (единый источник для GetTools/GetToolsForOllama и диспетчеризации вызовов).
	Tools *tools.Set
	// Store — общая Kanban-доска проекта (Redis). Подключается оркестратором
	// Kanban через SetBoardStore; в standalone-режиме (CLI) — nil.
	Store *board.Store
}

// SetBoardStore подключает генератор к общей Kanban-доске проекта: добавляет
// инструменты смены статуса задачи (Board*), передаёт Board-контекст в реестр.
// Вызывается оркестратором Kanban при построении агента специалиста.
func (cg *Codegenerator) SetBoardStore(s *board.Store) {
	if s == nil {
		return
	}
	cg.Store = s
	names := append(append([]string{}, codegenToolNames...), codegenBoardToolNames...)
	cg.Tools = tools.Select(names, tools.Deps{FileOps: cg.FileOps, Board: s})
}

// NewCodegenerator создаёт генератор в общей для всех агентов выходной папке
// temp/<projectName> в корне модуля (там же, где рефактор ищет существующие
// проекты). projectName — обязательное имя проекта, задаётся пользователем.
// prompt — текст задания для модели (может быть пустым — тогда используется
// задание по умолчанию).
func NewCodegenerator(projectName, prompt string) *Codegenerator {
	dir := ProjectDir(projectName)
	os.MkdirAll(dir, 0755)
	return newCG(dir, prompt, LoadConfig())
}

// ProjectDir возвращает путь к выходной директории проекта
// temp/<projectName> в корне модуля. Единый источник пути для конструкторов
// генератора и рефактора: рефактор гарантированно найдёт проект, созданный
// генератором, независимо от рабочей директории запуска.
func ProjectDir(projectName string) string {
	return filepath.Join(moduleRoot(), "temp", projectName)
}

// moduleRoot находит корень модуля — директорию с go.mod, поднимаясь вверх от
// рабочей директории. Если go.mod не найден, возвращает рабочую директорию
// (запуск вне модуля): temp/ тогда окажется рядом с CWD и для генератора,
// и для рефактора.
func moduleRoot() string {
	wd, _ := os.Getwd()
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return wd
		}
		dir = parent
	}
}

// newCG создаёт генератор в заданной директории: собирает *tools.FileOps
// (лимиты записи берутся из конфига) и выбирает из реестра инструменты
// генератора кода. Используется всеми конструкторами.
func newCG(dir, prompt string, cfg Config) *Codegenerator {
	ops := &tools.FileOps{OutputDir: dir, MaxFiles: cfg.MaxFiles, NoOverwrite: cfg.NoOverwrite}
	return &Codegenerator{
		FileOps: ops,
		Prompt:  prompt,
		Config:  cfg,
		Tools:   tools.Select(codegenToolNames, tools.Deps{FileOps: ops}),
	}
}

func (cg Codegenerator) GetUserMessages() []agents.Message {
	return []agents.Message{
		{
			Type:    agents.MessageTypeHuman,
			Message: cg.Prompt,
		},
	}
}

// RequiredToolFirstRound требует, чтобы в первом раунде модель обязательно
// вызвала WriteFiles (создание файлов проекта), а не ответила текстом.
// Раннер при необходимости подскажет модели и повторит запрос.
func (cg Codegenerator) RequiredToolFirstRound() (string, bool) {
	return "WriteFiles", true
}

func (cg Codegenerator) GetSystemMessages(text []agents.Message) []agents.Message {
	lang := cg.Config.Language
	if lang == "" {
		lang = "Go"
	}

	var moduleInstruction string
	if cg.Config.Module != "" {
		moduleInstruction = fmt.Sprintf("Используй имя модуля Go %q в файле go.mod.", cg.Config.Module)
	}

	overwriteRule := "Ты можешь перезаписывать файлы инструментами WriteFiles."
	if cg.Config.NoOverwrite {
		overwriteRule = "Включена защита от перезаписи: инструменты вернут ошибку, если ты попытаешься перезаписать уже существующий файл. Перезаписывай файлы только при необходимости."
	}

	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: fmt.Sprintf(`Ты - опытный разработчик на языке %s и архитектор, который пишет аккуратный, рабочий код и соблюдает архитектурные слои и обязанности каждого участка кода.
Ты работаешь только внутри выходной директории проекта (OutputDir).
%s
%s

Твой план работы:
1. Создай все нужные файлы (включая go.mod, если требуется) инструментами WriteFiles.
2. Проверь, что проект компилируется и проходит проверки, запустив инструмент Run с командами: "go build ./...", "go vet ./..." и (если есть тесты) "go test ./...".
3. Если компиляция или проверки падают, исправь код (WriteFiles перезапишет файл, а для точечных добавлений используй AppendFile) и запускай Run снова, пока всё не станет зелёным.
4. После успешного build продумай краткую проверку главного сценария работы (вызов программы).
5. Перед чтением или правкой файлов узнай, что уже создано, инструментом List.

Правила:
- Нельзя отвечать текстом-рассуждением вместо действий. Используй инструменты.
- Передавай все файлы списком в свойстве files в одном вызове инструмента WriteFiles.
- В конце приведи файл readme.md с инструкцией запуска и использования.
- Нельзя писать файлы вне OutputDir (инструменты сами это заблокируют).`, lang, moduleInstruction, overwriteRule),
		},
	}
}

// GetTools возвращает определения выбранных инструментов (единый JSON Schema
// формат для OpenAI/Yandex). Источник схем — реестр tools.
func (cg Codegenerator) GetTools() []tools.ToolDefinition {
	return cg.Tools.Definitions()
}

// GetToolsForOllama конвертирует определения инструментов в формат Ollama
// через единый конвертер tools.ToOllama (без ручного дублирования схем).
func (cr Codegenerator) GetToolsForOllama() []api.Tool {
	return tools.ToOllama(cr.GetTools())
}

func (cg *Codegenerator) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	return cg.Tools.Execute(functionName, functionArgs)
}

// Обёртки выбранных файловых инструментов. Тело живёт в tools.FileOps;
// наблюдаемые снаружи сигнатуры сохранены (используются тестами и
// программными вызовами) и просто делегируют в реестр.

func (cg *Codegenerator) WriteFiles(args map[string]any) ([]byte, error) {
	return cg.Tools.Execute("WriteFiles", args)
}
func (cg *Codegenerator) ReadFiles(args map[string]any) ([]byte, error) {
	return cg.Tools.Execute("ReadFiles", args)
}
func (cg *Codegenerator) DeleteFiles(args map[string]any) ([]byte, error) {
	return cg.Tools.Execute("DeleteFiles", args)
}
func (cg *Codegenerator) Run(args map[string]any) ([]byte, error) {
	return cg.Tools.Execute("Run", args)
}
func (cg *Codegenerator) List(args map[string]any) ([]byte, error) {
	return cg.Tools.Execute("List", args)
}
func (cg *Codegenerator) AppendFile(args map[string]any) ([]byte, error) {
	return cg.Tools.Execute("AppendFile", args)
}

// Обёртки внутренних помощников FileOps. Промотированные методы другого
// пакета невидимы для агента, поэтому дублируем тонкий слой делегирования.

func (cg *Codegenerator) resolvePath(name string) (string, error) {
	return cg.FileOps.ResolvePath(name)
}
func (cg *Codegenerator) write(name, content string) error { return cg.FileOps.Write(name, content) }
func (cg *Codegenerator) readResult(name string) map[string]string {
	return cg.FileOps.ReadResult(name)
}
func (cg *Codegenerator) remove(name string) (map[string]string, error) {
	return cg.FileOps.Remove(name)
}

// SelfReviewDir возвращает путь к директории сгенерированного кода.
// Используется main.go для цикла self-repair: локальное ревью + исправление.
func (cg Codegenerator) SelfReviewDir() string {
	return cg.OutputDir
}

// NewReviewAgentFor создаёт агента код-ревью, привязанного к локальной
// директории с кодом (через forges.LocalForge). Позволяет прогнать написанный
// код через логику codereviewer без сети. focus — необязательная цель ревью
// (например "безопасность"). Возвращает агента и его forge, чтобы вызывающий
// мог извлечь собранные замечания (lf.Published).
func (cg Codegenerator) NewReviewAgentFor(dir string, focus string) (agents.Agent, forges.Forge, error) {
	lf, err := forges.NewLocalForge(dir)
	if err != nil {
		return nil, nil, err
	}
	return codereviewer.NewCodereviewerWithForge(lf, focus), lf, nil
}

// FixPromptFor формирует новое задание для исправления сгенерированного кода:
// берет исходное задание и дополняет его найденными при ревью замечаниями.
func (cg Codegenerator) FixPromptFor(original string, comments []forges.ReviewComment) string {
	if len(comments) == 0 {
		return original + "\n\nКод прошёл локальное ревью без замечаний — финальный прогон: убедись, что всё компилируется и работает."
	}

	var b strings.Builder
	b.WriteString(original)
	b.WriteString("\n\nИсправь уже сгенерированный код в OutputDir по следующим замечаниям ревью (не ломай остальной функционал, затем снова загони go build и go vet через Run):\n")
	for _, c := range comments {
		fmt.Fprintf(&b, "- %s:%d %s\n", c.FilePath, c.Line, c.Text)
	}
	return b.String()
}

// NewCodegeneratorInDir создаёт генератор в заданной директории (а не в новой
// temp/). Используется для повторных раундов self-repair: модель исправляет
// уже существующий вывод на основе замечаний ревью.
func NewCodegeneratorInDir(prompt, dir string) (*Codegenerator, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return nil, err
	}
	return newCG(abs, prompt, LoadConfig()), nil
}

// Finalize пишет в OutputDir файл-отчёт SUMMARY.md (если включено конфигом)
// со структурой сгенерированного проекта. Вызывается из main.go после цикла
// генерации, заполняя отчёт реально созданными файлами.
func (cg Codegenerator) Finalize() {
	if cg.Config.SummaryFile == "" {
		return
	}

	full, err := cg.resolvePath(cg.Config.SummaryFile)
	if err != nil {
		return
	}

	lines := []string{
		"# Сгенерированный проект",
		"",
		"- Язык: " + cg.Config.Language,
		"- Задание: " + cg.Prompt,
		"",
		"## Файлы",
		"",
	}

	_ = filepath.Walk(cg.OutputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(cg.OutputDir, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			// Не спускаемся в зависимости/билды/кеши (node_modules и т.п.).
			if rel != "." && (strings.HasPrefix(info.Name(), ".") || forges.IsIgnoredDir(info.Name())) {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == cg.Config.SummaryFile {
			return nil
		}
		lines = append(lines, "- "+rel)
		return nil
	})

	content := strings.Join(lines, "\n") + "\n"
	_ = os.WriteFile(full, []byte(content), 0644)
	logging.Infof("[Finalize] написан отчёт: %s", full)

	// Гарантируем наличие README.md с инструкциями по установке, запуску и
	// использованию. Если модель уже создала его — не трогаем (не перезапишем).
	cg.EnsureREADME()
}

// EnsureREADME создаёт README.md в OutputDir, если он ещё не существует,
// с инструкцией по установке, запуску и использованию, собранной из
// реально сгенерированных файлов. Если README уже есть (его написала
// модель) — файл не перезаписывается, чтобы не портить авторский текст.
func (cg Codegenerator) EnsureREADME() {
	const name = "README.md"

	full, err := cg.resolvePath(name)
	if err != nil {
		return
	}
	if _, err := os.Stat(full); err == nil {
		// README уже есть (например, его создала модель) — не трогаем.
		logging.Detailf("[EnsureREADME] %s уже существует, пропускаю", full)
		return
	}

	files := cg.listProjectFiles()
	content := buildReadme(cg, files)
	if content == "" {
		return
	}
	_ = os.WriteFile(full, []byte(content), 0644)
	logging.Infof("[EnsureREADME] создан: %s", full)
}

// listProjectFiles возвращает относительные пути всех файлов OutputDir
// (рекурсивно), кроме собственного README/SUMMARY, отсортированные.
func (cg Codegenerator) listProjectFiles() []string {
	var out []string
	_ = filepath.Walk(cg.OutputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(cg.OutputDir, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			// Пропускаем зависимости/билды/кеши (node_modules и т.п.).
			if rel != "." && (strings.HasPrefix(info.Name(), ".") || forges.IsIgnoredDir(info.Name())) {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == "README.md" || rel == cg.Config.SummaryFile {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out
}

// buildReadme собирает текст README.md из имеющихся файлов: определяет
// модуль и точку входа (main.go, cmd/), Go-версию, наличие тестов и
// формирует разделы "Установка", "Запуск", "Использование".
func buildReadme(cg Codegenerator, files []string) string {
	if len(files) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("# Сгенерированный проект\n\n")
	b.WriteString("Проект создан агентом-генератором кода.\n\n")

	module := cg.Config.Module
	goVersion := goVersionFromFiles(cg.OutputDir, files)
	hasTests := hasGoTests(files)

	if module != "" {
		fmt.Fprintf(&b, "- Модуль: `%s`\n", module)
	}
	if goVersion != "" {
		fmt.Fprintf(&b, "- Go: `%s`\n", goVersion)
	}
	if hasTests {
		b.WriteString("- Тесты: есть\n")
	}

	b.WriteString("\n## Установка\n\n")
	if module != "" {
		fmt.Fprintf(&b, "```bash\ngo mod download\n```\n\n")
	}
	if goVersion != "" {
		fmt.Fprintf(&b, "Требуется Go %s или новее.\n\n", goVersion)
	}

	b.WriteString("## Запуск\n\n")
	if mainPath := findMainGo(files); mainPath != "" {
		fmt.Fprintf(&b, "```bash\ngo run %s\n```\n\n", mainPath)
	} else {
		b.WriteString("```bash\ngo run .\n```\n\n")
	}

	if hasTests {
		b.WriteString("## Тесты\n\n```bash\ngo test ./...\n```\n\n")
	}

	b.WriteString("## Использование\n\n")
	if mainPath := findMainGo(files); mainPath != "" {
		fmt.Fprintf(&b, "После запуска (`go run %s`) программа выполнит основной сценарий из задания.\n", mainPath)
	} else {
		b.WriteString("Публичные пакеты/функции проекта используются через импорт: `import \"%s/…\"`.\n")
	}

	b.WriteString("\n## Структура\n\n```\n")
	for _, f := range files {
		fmt.Fprintf(&b, "%s\n", f)
	}
	b.WriteString("```\n")

	return b.String()
}

// findMainGo возвращает путь к файлу point-of-entry (main.go или первый
// файл под cmd/), подходящий для команды "go run". Иначе "".
func findMainGo(files []string) string {
	if len(files) == 0 {
		return ""
	}
	// Отдаём предпочтение корневому main.go.
	for _, f := range files {
		if f == "main.go" || f == "./main.go" {
			return "main.go"
		}
	}
	for _, f := range files {
		if filepath.Base(f) == "main.go" {
			return f
		}
	}
	return ""
}

// hasGoTests сообщает, содержит ли список файлов Go-тесты (*_test.go).
func hasGoTests(files []string) bool {
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			return true
		}
	}
	return false
}

// goVersionFromFiles ищет строку "go x.y.z" в go.mod и возвращает её,
// иначе "".
func goVersionFromFiles(dir string, files []string) string {
	for _, f := range files {
		if filepath.Base(f) != "go.mod" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "go ") {
				return strings.TrimPrefix(line, "go ")
			}
		}
	}
	return ""
}
