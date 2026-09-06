package tools

import "fmt"

// Set — набор инструментов, выбранных агентом из общего реестра.
// Агент держит его в себе и использует для трёх вещей:
// отдачи определений провайдерам (GetTools/GetToolsForOllama)
// и диспетчеризации вызовов (CallFunction).
type Set struct {
	byName map[string]Tool
	order  []string
}

// Select собирает набор инструментов по именам из общего реестра.
// Каждый агент указывает только те имена, с которыми собирается работать;
// сам каталог инструментов живёт в этом пакете и переиспользуется агентами.
//
// deps передаёт агенту-контекст: инструменты генератора кода получают
// *FileOps, инструменты ревью — *ReviewSession (разделяемое состояние цикла).
func Select(names []string, deps Deps) *Set {
	s := &Set{byName: make(map[string]Tool, len(names))}
	for _, n := range names {
		s.byName[n] = newTool(n, deps)
		s.order = append(s.order, n)
	}
	return s
}

// Get возвращает инструмент по имени и признак его наличия в наборе.
func (s *Set) Get(name string) (Tool, bool) {
	t, ok := s.byName[name]
	return t, ok
}

// Execute диспетчеризует вызов модели к инструменту по имени.
func (s *Set) Execute(name string, args map[string]any) ([]byte, error) {
	t, ok := s.byName[name]
	if !ok {
		return nil, fmt.Errorf("function %s not in tool set", name)
	}
	return t.Execute(args)
}

// Definitions возвращает определения инструментов в порядке их выбора.
// Используется агентами в GetTools (OpenAI/Yandex формат).
func (s *Set) Definitions() []ToolDefinition {
	defs := make([]ToolDefinition, 0, len(s.order))
	for _, n := range s.order {
		defs = append(defs, s.byName[n].Definition())
	}
	return defs
}

// newTool — реестр: фабрика инструментов по имени. Зарегистрированные здесь
// инструменты доступны любому агенту через Select.
func newTool(name string, deps Deps) Tool {
	switch name {
	case "WriteFiles":
		return &writeFilesTool{ops: deps.FileOps}
	case "ReadFiles":
		return &readFilesTool{ops: deps.FileOps}
	case "DeleteFiles":
		return &deleteFilesTool{ops: deps.FileOps}
	case "Run":
		return &runTool{ops: deps.FileOps}
	case "List":
		return &listTool{ops: deps.FileOps}
	case "AppendFile":
		return &appendFileTool{ops: deps.FileOps}
	case "ReviewMr":
		return &reviewMrTool{ses: deps.Session}
	case "ApproveMr":
		return &approveMrTool{ses: deps.Session}
	case "NextChunk":
		return &nextChunkTool{ses: deps.Session}
	}
	// Неизвестное имя — ошибка на этапе конструирования агента (fail-fast).
	panic(fmt.Sprintf("tools: инструмент %q не зарегистрирован в реестре", name))
}
