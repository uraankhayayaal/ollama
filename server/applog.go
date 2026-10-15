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
	"time"

	applogs "ai/application/logs"
	"ai/infrastructure/runevents"
	"ai/shared/projects"
	"ai/tools"
)

// Кольцевой буфер строк логов рантайма проекта и его границы вынесены в
// application/logs (RingBuffer/BufferLines/DefaultTail/MaxTail). Ниже —
// алиасы под прежние серверные имена, чтобы код сессии и REST-хендлер не
// менялись.
const (
	appLogBufferLines = applogs.BufferLines
	appLogDefaultTail = applogs.DefaultTail
	appLogMaxTail     = applogs.MaxTail
	// appLogIdleFlush — период сброса накопленных строк в лог проекта.
	// Писать в файл на каждую строку значило бы захлёбывать диск логами от
	// рантайма; копим и пишем пачками.
	appLogIdleFlush = 2 * time.Second
)

// appLogLine — строка лога рантайма в API.
type appLogLine = applogs.Line

// appLogBuffer — обёртка над application/logs.RingBuffer с сохранением прежних
// серверных имён методов (push/tail/takeDirty).
type appLogBuffer struct{ applogs.RingBuffer }

func (b *appLogBuffer) push(source, line string) { b.Push(source, line) }

func (b *appLogBuffer) tail(n int) []appLogLine { return b.Tail(n) }

func (b *appLogBuffer) takeDirty() bool { return b.TakeDirty() }

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
