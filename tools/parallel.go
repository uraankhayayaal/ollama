package tools

// IsParallelSafe сообщает, можно ли выполнять инструмент одновременно с
// другими вызовами того же раунда (parallel tool calling).
//
// Классификация консервативная: параллельно запускаются ТОЛЬКО read-only
// инструменты без побочных эффектов и без общей мутируемой памяти —
// чтение файлов/карт кода (List/ReadFiles/ReadMap). Это главный
// боттлнек агента: модель часто просит прочитать десяток файлов — раньше
// они читались строго по очереди.
func IsParallelSafe(name string) bool {
	switch name {
	case "List", "ReadFiles", "ReadMap":
		return true
	}
	return false
}
