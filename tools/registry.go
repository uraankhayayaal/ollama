package tools

import (
	"fmt"

	"ai/tools/git"
	"ai/tools/rag"
)

// ResolveGitConflicts — имя инструмента в реестре; каноническое значение
// и реализация вынесены в подпакет ai/tools/git (E6e1).
const ResolveGitConflicts = git.ResolveToolName

// CodeSearch и RagIndexStatus — имена RAG-инструментов; канонические значения и
// реализация вынесены в подпакет ai/tools/rag (E6b).
const (
	CodeSearch     = rag.CodeSearch
	RagIndexStatus = rag.IndexStatus
)

// ragCtx приводит файловый контекст к rag.Context (nil-безопасно: nil FileOps →
// nil интерфейс, а не «typed nil», чтобы инструменты корректно деградировали).
func ragCtx(ops *FileOps) rag.Context {
	if ops == nil {
		return nil
	}
	return ops
}

// Set — набор инструментов, выбранных агентом из общего реестра.
// Агент держит его в себе и использует для трёх вещей:
// отдачи определений провайдерам (GetTools)
// и диспетчеризации вызовов (CallFunction).
type Set struct {
	byName map[string]Tool
	order  []string
}

// Select собирает набор инструментов по именам из общего реестра.
func Select(names []string, deps Deps) *Set {
	s := &Set{byName: make(map[string]Tool, len(names))}
	for _, n := range names {
		s.byName[n] = newTool(n, deps)
		s.order = append(s.order, n)
	}
	return s
}

// Add дополняет набор инструментами вне общего реестра (например,
// мосты-инструменты к серверным действиям, Ф-3: server не может объявить их в
// реестре этого пакета из-за цикла импортов). Уже присутствующие имена не
// дублируются, соответствие «инструмент не из реестра, вызван в Execute» —
// обычный поимённый диспетч.
func (s *Set) Add(tools ...Tool) {
	if s.byName == nil {
		s.byName = make(map[string]Tool, len(tools))
	}
	for _, t := range tools {
		name := t.Name()
		if _, ok := s.byName[name]; ok {
			continue
		}
		s.byName[name] = t
		s.order = append(s.order, name)
	}
}

// Get возвращает инструмент по имени и признак его наличия в наборе.
func (s *Set) Get(name string) (Tool, bool) {
	t, ok := s.byName[name]
	return t, ok
}

// Has сообщает, выдан ли инструмент агенту в этом наборе. В отличие от Get не
// отдаёт сам инструмент — достаточно факта наличия (проверка прав до вызова).
func (s *Set) Has(name string) bool {
	_, ok := s.byName[name]
	return ok
}

// Remove исключает инструмент из набора по имени.
func (s *Set) Remove(name string) {
	if s.byName == nil {
		return
	}
	delete(s.byName, name)
	for i, n := range s.order {
		if n == name {
			s.order = append(s.order[:i], s.order[i+1:]...)
			return
		}
	}
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
	case "ReadMap":
		return &readMapTool{ops: deps.FileOps}
	case "DeleteFiles":
		return &deleteFilesTool{ops: deps.FileOps}
	case "Run":
		return &runTool{ops: deps.FileOps}
	case ReadAppLogs:
		return &readAppLogsTool{ops: deps.FileOps}
	case "List":
		return &listTool{ops: deps.FileOps}
	case "AppendFile":
		return &appendFileTool{ops: deps.FileOps}
	case "PatchGoFunction":
		return &patchGoFunctionTool{ops: deps.FileOps}
	case "PatchFunction":
		return &patchFunctionTool{ops: deps.FileOps}
	case "SearchReplace":
		return &searchReplaceTool{ops: deps.FileOps}
	case ResolveGitConflicts:
		var dir string
		var ops git.Ops
		if deps.FileOps != nil {
			dir, ops = deps.FileOps.OutputDir, deps.FileOps
		}
		return &git.ResolveTool{Dir: dir, Ops: ops, Ex: deps.GitExec}
	case LspCheck:
		return &lspCheckTool{ops: deps.FileOps}
	case LspDefinition:
		return &lspDefinitionTool{ops: deps.FileOps}
	case LspReferences:
		return &lspReferencesTool{ops: deps.FileOps}
	case LspHover:
		return &lspHoverTool{ops: deps.FileOps}
	case CodeSearch:
		return &rag.SearchTool{Ctx: ragCtx(deps.FileOps), Searcher: deps.RAG}
	case RagIndexStatus:
		return &rag.StatusTool{Ctx: ragCtx(deps.FileOps), Searcher: deps.RAG}
	case DetectStack:
		return &stackTool{ops: deps.FileOps}
	case WebSearch:
		return &webSearchTool{client: nil, endpoint: ""}
	case "ReviewMr":
		return &reviewMrTool{ses: deps.Session}
	case "ApproveMr":
		return &approveMrTool{ses: deps.Session}
	case "NextChunk":
		return &nextChunkTool{ses: deps.Session}
	}
	panic(fmt.Sprintf("tools: инструмент %q не зарегистрирован в реестре", name))
}
