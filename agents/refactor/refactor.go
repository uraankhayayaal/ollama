package refactor

import (
	"ai/agents"
	"ai/agents/codegenerator"
	"fmt"
	"os"
)

// RefactorAgent — отдельный агент для работы с уже существующим проектом
// (рефакторинг, доработка). Отличается от codegenerator.Codegenerator двумя
// вещами: не требует обязательного вызова WriteFiles в первом раунде (модель
// начинает с изучения кода) и использует рефакторинговый системный промпт
// (управление промптами генерации остаётся в codegenerator, общий механизм
// файловых инструментов — в tools).
type RefactorAgent struct {
	*codegenerator.Codegenerator
}

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

	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: fmt.Sprintf(`Ты — опытный разработчик на языке %s и архитектор. Твоя задача — провести рефакторинг или доработку уже существующего проекта.
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
- Нельзя писать файлы вне OutputDir (инструменты сами это заблокируют).`, lang, moduleInstruction, overwriteRule),
		},
	}
}
