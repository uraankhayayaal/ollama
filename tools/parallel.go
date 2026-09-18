package tools

// IsParallelSafe сообщает, можно ли выполнять инструмент одновременно с
// другими вызовами того же раунда (parallel tool calling).
//
// Классификация консервативная: параллельно запускаются ТОЛЬКО read-only
// инструменты без побочных эффектов и без общей мутируемой памяти —
// чтение файлов/карт кода (List/ReadFiles/ReadMap) и чтение Kanban-доски
// (BoardList*/BoardGet*; клиент Redis потокобезопасен). Это главный
// боттлнек агента: модель часто просит прочитать десяток файлов — раньше
// они читались строго по очереди.
//
// Все остальные инструменты — запись файлов (WriteFiles/AppendFile/
// DeleteFiles/SearchReplace/PatchGoFunction), запуск команд (Run),
// публикация в MR (ReviewMr/ApproveMr/NextChunk, состояние цикла ревью
// мутируется внутри сеанса) и записи в доску — выполняются последовательно
// в порядке вызова, сохраняя прежнюю семантику и порядок применения.
func IsParallelSafe(name string) bool {
	switch name {
	case "List", "ReadFiles", "ReadMap":
		return true
	case BoardListEpics, BoardGetEpic, BoardListTasks, BoardGetTask, BoardListBugs, BoardGetBug:
		return true
	}
	return false
}
