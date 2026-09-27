package server

// Логи рантайма приложения (Ф-2 PLAN-2026-09-19-todo-owerview-for-prom.md):
// приём строк от tools.ReadAppLogs, кольцевой буфер, трансляция в Web UI и
// REST-точка для чтения хвоста.
//
// Зачем это отдельно от существующей «панели логов»: там лежит журнал работы
// агентов (logs/<проект>.log) — что модель вызвала и с каким исходом. Логов
// САМОГО приложения там не было, поэтому падение сервера на строке 87 было
// видно только через «тесты зелёные, значит всё хорошо» — то есть никак.
//
// Поток данных:
//
//	tools.ReadAppLogs ─(SetAppLogSink, по каталогу проекта)→ sess.appendAppLog
//	                  ─(runevents.TypeAppLog)────────────────→ hub.publish("applog")
//	                  ─(кольцевой буфер)──────────────────────→ GET /api/projects/{id}/applog
//
// Инструмент отдаёт строки и напрямую (в свой хвост для самокоррекции), и через
// подписку: путь через события — для UI, путь через хвост — для модели.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai/projects"
	"ai/runevents"
	"ai/tools"
)

// Границы кольцевого буфера логов рантайма. Меньше — потеряем причину падения
// (она всегда в конце), больше — распухает память на проектах с болтливым
// выводом вроде сборки фронтенда.
const (
	appLogBufferLines = 2000
	appLogDefaultTail = 300
	appLogMaxTail     = 2000
	// appLogIdleFlush — период сброса накопленных строк в лог проекта.
	// Писать в файл на каждую строку значило бы захлёбывать диск логами от
	// рантайма; копим и пишем пачками.
	appLogIdleFlush = 2 * time.Second
)

// appLogLine — строка лога рантайма в API.
type appLogLine struct {
	// Time — момент получения строки (не время приложения): логи сторонних
	// приложений часто вообще без таймстемпов.
	Time time.Time `json:"time"`
	// Source — откуда строка: local (процесс) или docker (сервис compose).
	Source string `json:"source,omitempty"`
	// Line — сама строка.
	Line string `json:"line"`
}

// appLogBuffer — кольцевой буфер строк логов рантайма проекта.
type appLogBuffer struct {
	mu    sync.Mutex
	lines []appLogLine
	// dirty — есть несохранённые строки (для лога проекта).
	dirty int
}

func (b *appLogBuffer) push(source, line string) {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, appLogLine{Time: now, Source: source, Line: line})
	if len(b.lines) > appLogBufferLines {
		n := copy(b.lines, b.lines[len(b.lines)-appLogBufferLines:])
		b.lines = b.lines[:n]
	}
	b.dirty++
}

// tail возвращает последние n строк (n <= 0 — умолчание).
func (b *appLogBuffer) tail(n int) []appLogLine {
	if n <= 0 {
		n = appLogDefaultTail
	}
	if n > appLogMaxTail {
		n = appLogMaxTail
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > len(b.lines) {
		n = len(b.lines)
	}
	out := make([]appLogLine, n)
	copy(out, b.lines[len(b.lines)-n:])
	return out
}

// takeDirty забирает и обнуляет счётчик несохранённых строк: вернёт true, если
// в лог проекта есть что дописать.
func (b *appLogBuffer) takeDirty() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dirty == 0 {
		return false
	}
	b.dirty = 0
	return true
}

// appLogFlusherLoop дописывает накопленные строки в лог проекта пачками и
// подмешивает хвост в цикл самокоррекции агента.
func (sess *Session) appLogFlusherLoop(ctx context.Context) {
	t := time.NewTicker(appLogIdleFlush)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !sess.appLog.takeDirty() {
				continue
			}
			for _, l := range sess.appLog.tail(appLogDefaultTail) {
				sess.log.Detailf("[рантайм:%s] %s", l.Source, l.Line)
			}
		}
	}
}

// registerAppLogSink подписывает сессию на строки логов рантайма для
// каталога проекта. Инструмент живёт в пакете tools и не имеет доступа к
// контексту сессии, поэтому связь односторонняя: инструмент → подписка.
//
// Подписка снимается на завершении сессии: иначе проект, удалённый и
// пересозданный, держал бы в реестре инструментов ссылку на старую сессию.
func (sess *Session) registerAppLogSink(ctx context.Context) {
	dir := projects.ProjectDir(sess.project)
	cancel := tools.SetAppLogSink(dir, sess.appendAppLog)
	if cancel == nil {
		return
	}
	go func() {
		<-ctx.Done()
		cancel()
	}()
}

// appendAppLog — обработчик строки лога рантайма из инструмента.
func (sess *Session) appendAppLog(source, line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	sess.appLog.push(source, line)
	sess.srv.hub.publish(sess.project, "applog", runevents.Event{
		Type:    runevents.TypeAppLog,
		Source:  source,
		Content: line,
		Time:    time.Now().UTC().Truncate(time.Millisecond),
	})
}

// onRunAppLog — приём строки лога рантайма из шины агентского цикла
// (runevents.TypeAppLog). Инструмент шлёт их напрямую подпиской, но тот же тип
// события может прийти и от репортёра цикла — тогда нужен путь через шину.
func (sess *Session) onRunAppLog(ev runevents.Event) {
	sess.appLog.push(ev.Source, ev.Content)
	sess.srv.hub.publish(sess.project, "applog", ev)
}

// handleAppLog отдаёт хвост логов рантайма проекта: GET
// /api/projects/{id}/applog?lines=300. Пустой буфер — не ошибка, а status
// "skipped" с подсказкой (деградация по правилам репозитория): рантайм ещё не
// запускали, и это нормальное состояние до первого вызова ReadAppLogs.
func (s *Server) handleAppLog(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	sess := s.session(project)
	if sess == nil || sess.appLog == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "skipped",
			"hint":   "сессия проекта не запущена — логи рантайма появятся после старта",
			"lines":  []appLogLine{},
		})
		return
	}
	n := 0
	if v := r.URL.Query().Get("lines"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			n = parsed
		}
	}
	lines := sess.appLog.tail(n)
	if len(lines) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "skipped",
			"hint": "рантайм ещё не запускался: попроси агента прочитать логи приложения " +
				"(ReadAppLogs), чтобы они появились здесь",
			"lines": []appLogLine{},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"lines":  lines,
		"count":  len(lines),
	})
}
