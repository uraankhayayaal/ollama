package tools

import "testing"

// Read-only инструменты можно выполнять параллельно (без shared мутируемого
// состояния и побочных эффектов).
func TestIsParallelSafeReadOnly(t *testing.T) {
	safe := []string{
		"List",
		"ReadFiles",
		"ReadMap",
		BoardListEpics, BoardGetEpic,
		BoardListTasks, BoardGetTask,
		BoardListBugs, BoardGetBug,
	}
	for _, name := range safe {
		if !IsParallelSafe(name) {
			t.Errorf("IsParallelSafe(%q) = false, ожидали true (read-only)", name)
		}
	}
}

// Пишущие/мутирующие инструменты и запуск команд должны оставаться
// последовательными: параллельное выполнение привело бы к гонкам за общее
// состояние (файлы, доска, сеанс ревью) и к недоопределённому порядку.
func TestIsParallelSafeMutating(t *testing.T) {
	unsafe := []string{
		"WriteFiles",
		"AppendFile",
		"DeleteFiles",
		"Run",
		"PatchGoFunction",
		"SearchReplace",
		"ReviewMr",
		"ApproveMr",
		"NextChunk",
		BoardCreateEpic, BoardUpdateEpic, BoardDeleteEpic, BoardSetEpicStatus,
		BoardCreateTask, BoardUpdateTask, BoardDeleteTask, BoardSetTaskStatus,
		BoardCreateBug, BoardSetBugStatus, BoardReviewBug,
		"SomeUnknownTool",
	}
	for _, name := range unsafe {
		if IsParallelSafe(name) {
			t.Errorf("IsParallelSafe(%q) = true, ожидали false (мутирующий)", name)
		}
	}
}
