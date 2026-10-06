package sandbox

// registry.go — реестр активных контейнеров сессий (п. 1.5).
//
// Один процесс сервера ведёт несколько проектов, у каждого — свой контейнер.
// Потребители (FileOps.Run, acceptor) приходят сюда с путём рабочего каталога
// и (когда знают) именем проекта, чтобы получить активный Workspace.
//
// Почему матчинг не по одному пути: worktree задачи — СОСЕД клона
// (.wt-task-<проект>-…, .resolve-…, .conflict-… в каталоге рядом с Root), а
// для temp-проектов сосед — это общий корень temp/, куда входят и другие
// проекты. Поэтому контейнер монтирует родительский каталог (иначе worktree
// не попадёт внутрь), но матчинг идёт строго: каталог проекта, либо его
// dot-сосед, названный именем этого проекта. Чужой проект в контейнер чужой
// сессии не попадает — при разрешении по одному пути temp/ обе сессии
// совпадали бы одинаково.

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// entry — запись реестра: контейнер сессии и что он покрывает на хосте.
type entry struct {
	// Proj — имя проекта-владельца: нужно для матчинга worktree (см. coverLen).
	Proj string
	// Dir — корень проекта (inf.Root): каталог, в котором работает агент.
	Dir string
	// Mounts — смонтированные в контейнер каталоги (для проверки покрытия
	// используется Dir; mounts — справочно и для тестов).
	Mounts []string
	WS     Workspace
}

var (
	registryMu sync.RWMutex
	registry   = map[string]entry{} // проект → запись
)

// Activate регистрирует контейнер сессии проекта. Повторная активация
// заменяет запись: предыдущий Workspace остаётся ответственностью вызывающего
// (StartSession закрывает остатки сам, StopSession — типовой путь).
func Activate(project string, ws Workspace, dir string, mounts []string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[project] = entry{Proj: project, Dir: filepath.Clean(dir), Mounts: mounts, WS: ws}
}

// Deactivate убирает запись сессии и возвращает прежний Workspace (nil, если
// сессии не было). Закрытие — обязанность вызывающего (StopSession).
func Deactivate(project string) Workspace {
	registryMu.Lock()
	defer registryMu.Unlock()
	e, ok := registry[project]
	if !ok {
		return nil
	}
	delete(registry, project)
	return e.WS
}

// Lookup возвращает активный контейнер сессии для каталога dir.
// project — имя проекта, когда оно известно (может быть пустым): точное
// совпадение имени важнее совпадения по пути. Если проект в реестре ЕСТЬ, но
// каталог ему не принадлежит — nil: чужая сессия не должна подхватить чужую
// команду даже при совпадении путей. Для неизвестного имени (и для пустого —
// как у приёмщика) — поиск наилучшего покрытия по пути. Пустой результат —
// контейнера для этого каталога нет (вызывающий решает: хост или отказ).
func Lookup(project, dir string) Workspace {
	registryMu.RLock()
	defer registryMu.RUnlock()
	dir = filepath.Clean(dir)
	if project != "" {
		if e, ok := registry[project]; ok {
			if e.covers(dir) {
				return e.WS
			}
			return nil
		}
	}
	// Наиболее точное покрытие среди всех сессий (стабильно при равенстве:
	// карта обходится в произвольном порядке — поэтому строгое «>», а
	// не «>=», и проектная запись выше уже обработана).
	var best Workspace
	bestLen := -1
	for _, e := range registry {
		if l := e.coverLen(dir); l > bestLen {
			best, bestLen = e.WS, l
		}
	}
	return best
}

// StopSession закрывает и снимает сессию проекта. Нет сессии — no-op.
//
// Контекст закрытия принудительно отвязывается от вызывающего: defer в
// горутине-раннере выполняется уже после отмены оркестрации, и
// остановленный по таймауту StopContainer оставил бы брошенный контейнер
// (п. 1.7 требует, чтобы контейнер умирал вместе с сессией).
func StopSession(ctx context.Context, project string) error {
	ws := Deactivate(project)
	if ws == nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	return ws.Close(cctx)
}

// ActiveProjects — имена проектов с активными сессиями (диагностика, тесты).
func ActiveProjects() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for p := range registry {
		out = append(out, p)
	}
	return out
}

// covers — относится ли dir к проекту сессии.
func (e entry) covers(dir string) bool { return e.coverLen(dir) >= 0 }

// coverLen — длина matched-префикса покрытия или -1. Правила:
//  1. dir внутри корня проекта — всегда покрыто;
//  2. dir — dot-сосед корня (worktree задачи/эпика/конфликта), чье имя
//     содержит имя проекта, — покрыто. Без проверки имени точка (2)
//     захватила бы worktree ЧУЖОГО проекта в общем temp/.
func (e entry) coverLen(dir string) int {
	if e.Dir == "" {
		return -1
	}
	if dir == e.Dir || strings.HasPrefix(dir, e.Dir+string(filepath.Separator)) {
		return len(e.Dir)
	}
	parent := filepath.Dir(e.Dir)
	if filepath.Dir(dir) != parent {
		return -1
	}
	name := filepath.Base(dir)
	if !strings.HasPrefix(name, ".") {
		return -1
	}
	if e.Proj != "" && !strings.Contains(name, e.Proj) {
		return -1
	}
	return len(parent)
}
