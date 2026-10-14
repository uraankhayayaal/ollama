package server

import (
	"context"

	"ai/application/logtail"
)

// logBroker — перематывает лог-файлы проектов и транслирует новые строки
// в WebSocket-хаб (pub/sub per project, type="log").
//
// Логика инкрементального чтения (позиция/remainder/отпечаток, ротация,
// рескан каталогов) живёт в application/logtail; здесь — только адаптер,
// публикующий строки в хаб.
//
// Поток данных:
//
//	appender ─(Subscribe, по каталогу)→ logtail.Broker
//	logtail ─(Publish)→ logBroker.publish → hub.publish(project, "log")

// logMessage — полезная нагрузка события "log" в WebSocket-хабе.
type logMessage struct {
	Project string `json:"project"`
	File    string `json:"file"`
	Line    string `json:"line"`
}

// logBroker — тонкая обёртка над logtail.Broker: строки публикуются в хаб.
type logBroker struct{ b *logtail.Broker }

func newLogBroker(h *Hub) *logBroker {
	return &logBroker{b: logtail.NewBroker(func(project string, l logtail.Line) {
		h.publish(project, "log", logMessage{
			Project: l.Project,
			File:    l.File,
			Line:    l.Line,
		})
	})}
}

// Run запускает фоновое сканирование до ctx.Done.
func (b *logBroker) Run(ctx context.Context) { b.b.Run(ctx) }

// Subscribe регистрирует проект и добирает отслеживаемые файлы.
func (b *logBroker) Subscribe(project string, dirs []string, files []string) {
	b.b.Subscribe(project, dirs, files)
}

// Broadcast опрашивает подписки и публикует появившиеся строки.
func (b *logBroker) broadcast() { b.b.Broadcast() }

// listLogFiles отбирает имена *.log файлов (логика — в application/logtail).
func listLogFiles(dirs []string) []string { return logtail.ListLogFiles(dirs) }
