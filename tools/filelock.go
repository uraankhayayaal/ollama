package tools

// Сериализация файловых мутаций внутри одного проекта.
//
// Параллельные шаги волны (agents/planner) выполняют агентов в отдельных
// горутинах — каждый пишет в свой каталог, НО соседние шаги одной волны
// работают в одном проекте (общий OutputDir). Без координации две горутины
// могут одновременно править один файл: NoOverwrite-проверки (check-then-write)
// проходят обе, SearchReplace/PatchGoFunction читают один и тот же устаревший
// источник и взаимно затирают патчи, AppendTo перемежает содержимое.
//
// LLM-вызовы занимают 99% времени шага, файловые операции — миллисекунды,
// поэтому сериализация только мутаций практически не влияет на параллелизм.
// Ключ блокировки — корневой каталог проекта: разные проекты (и тесты с
// t.TempDir()) не мешают друг другу.

import (
	"path/filepath"
	"sync"
)

// projectLocks — пул мьютексов по корневому каталогу проекта.
var projectLocks sync.Map // key: string (clean root) -> *sync.Mutex

// withProjectLock сериализует выполнение fn для всех файловых мутаций в
// каталоге root. Блокировки пер-проектные: работа в разных каталогах
// не блокирует друг друга.
func withProjectLock(root string, fn func() error) error {
	key := filepath.Clean(root)
	v, _ := projectLocks.LoadOrStore(key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	return fn()
}
