package tools

import "fmt"

// Set — набор инструментов, выбранных агентом из общего реестра.
// Агент держит его в себе и использует для трёх вещей:
// отдачи определений провайдерам (GetTools/GetToolsForOllama)
// и диспетчеризации вызовов (CallFunction).
type Set struct {
	byName map[string]Tool
	order  []string
	// boardConnected — был ли board.Store подключён при построении набора
	// (deps.Board != nil). Позволяет различить в Execute два случая вызова
	// board-инструмента, отсутствующего в наборе: доска не подключена
	// (standalone/plan — мягкая ошибка «доска не подключена») и доска
	// подключена, но инструмент сознательно не выдан (read-only участник —
	// жёсткая ошибка not in tool set).
	boardConnected bool
}

// Select собирает набор инструментов по именам из общего реестра.
// Каждый агент указывает только те имена, с которыми собирается работать;
// сам каталог инструментов живёт в этом пакете и переиспользуется агентами.
//
// deps передаёт агенту-контекст: инструменты генератора кода получают
// *FileOps, инструменты ревью — *ReviewSession (разделяемое состояние цикла).
// Инструменты Kanban-доски (Board*) включаются только при подключённом
// board.Store: без доски их вызов заведомо завершится «доска не подключена»,
// а модель бесполезно будет жечь раунды, пробуя доступные, но мёртвые
// инструменты (например, лид в standalone-режиме plan).
func Select(names []string, deps Deps) *Set {
	s := &Set{byName: make(map[string]Tool, len(names)), boardConnected: deps.Board != nil}
	for _, n := range names {
		if IsBoardTool(n) && deps.Board == nil {
			continue
		}
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
		// Известный инструмент доски, но доска не подключена (standalone/plan):
		// Select его не включил, а модель позвала (промпт лидов упоминает Board*).
		// Возвращаем мягкую ошибку «доска не подключена» — по промпту лид в этом
		// случае переходит к JSON-декомпозиции. При подключённой доске вызов
		// инструмента, сознательно не выданного агенту (read-only участник),
		// — по-прежнему жёсткая ошибка not in tool set.
		if IsBoardTool(name) && !s.boardConnected {
			return boardErr(name, fmt.Errorf("доска не подключена"))
		}
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
	case "ReadMap":
		return &readMapTool{ops: deps.FileOps}
	case "DeleteFiles":
		return &deleteFilesTool{ops: deps.FileOps}
	case "Run":
		return &runTool{ops: deps.FileOps}
	case "List":
		return &listTool{ops: deps.FileOps}
	case "AppendFile":
		return &appendFileTool{ops: deps.FileOps}
	case "PatchGoFunction":
		return &patchGoFunctionTool{ops: deps.FileOps}
	case "SearchReplace":
		return &searchReplaceTool{ops: deps.FileOps}
	case LspCheck:
		return &lspCheckTool{ops: deps.FileOps}
	case "ReviewMr":
		return &reviewMrTool{ses: deps.Session}
	case "ApproveMr":
		return &approveMrTool{ses: deps.Session}
	case "NextChunk":
		return &nextChunkTool{ses: deps.Session}
	case BoardListEpics:
		return &boardListEpicsTool{b: deps.Board}
	case BoardGetEpic:
		return &boardGetEpicTool{b: deps.Board}
	case BoardListTasks:
		return &boardListTasksTool{b: deps.Board}
	case BoardGetTask:
		return &boardGetTaskTool{b: deps.Board}
	case BoardListBugs:
		return &boardListBugsTool{b: deps.Board}
	case BoardGetBug:
		return &boardGetBugTool{b: deps.Board}
	case BoardCreateEpic:
		return &boardCreateEpicTool{b: deps.Board}
	case BoardUpdateEpic:
		return &boardUpdateEpicTool{b: deps.Board}
	case BoardDeleteEpic:
		return &boardDeleteEpicTool{b: deps.Board}
	case BoardSetEpicStatus:
		return &boardSetEpicStatusTool{b: deps.Board}
	case BoardCreateTask:
		return &boardCreateTaskTool{b: deps.Board}
	case BoardUpdateTask:
		return &boardUpdateTaskTool{b: deps.Board}
	case BoardDeleteTask:
		return &boardDeleteTaskTool{b: deps.Board}
	case BoardSetTaskStatus:
		return &boardSetTaskStatusTool{b: deps.Board}
	case BoardCreateBug:
		return &boardCreateBugReportTool{b: deps.Board}
	case BoardSetBugStatus:
		return &boardSetBugStatusTool{b: deps.Board}
	case BoardReviewBug:
		return &boardReviewBugReportTool{b: deps.Board}
	}
	// Неизвестное имя — ошибка на этапе конструирования агента (fail-fast).
	panic(fmt.Sprintf("tools: инструмент %q не зарегистрирован в реестре", name))
}
