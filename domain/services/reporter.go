package services

import "context"

// Reporter — порт трансляции live-событий агентского цикла (Web UI) плюс
// деривация клонов с агентом/scope'ом через контекст. Реализуется
// runevents.Router (инфраструктура); порт вынесен из runevents в домен
// (Этап 8, H5d), чтобы application/planner зависел только от доменного
// порта, а не от outer-пакета.
//
// Контекст-хелперы (WithReporter/ReporterFromContext/WithScope) живут здесь,
// чтобы ключ контекста был единым: сервер кладёт репортёр (runevents.WithReporter
// делегирует сюда), а оркестратор читает его через ReporterFromContext.
type Reporter interface {
	// WithAgent — клон с установленным именем агента (общий sink).
	WithAgent(name string) Reporter
	// WithScope — клон с scope'ом единицы работы (task:<id>, epic:<id>, …);
	// атрибуция токенов раундов. Пустой scope — расход без атрибуции.
	WithScope(scope string) Reporter
	// OnMessage — полный текст ответа модели (финальный).
	OnMessage(role, content string, truncated bool)
	// OnMessageDelta — потоковый фрагмент ответа модели (стриминг).
	OnMessageDelta(streamID, content string)
	OnToolStart(tool, args string)
	OnToolResult(tool, result string, ok bool)
	// OnTokens — потребление токенов одного раунда модели (вход/выход).
	// tps — реальная скорость генерации выхода (ток/с) или 0, если неизвестна.
	OnTokens(in, out int64, tps float64)
	// OnAppLog — строка лога рантайма приложения (source — local/docker).
	OnAppLog(source, content string)
}

// reporterKey — тип ключа контекста для передачи Reporter.
type reporterKey struct{}

// WithReporter помещает Reporter в контекст.
func WithReporter(ctx context.Context, r Reporter) context.Context {
	return context.WithValue(ctx, reporterKey{}, r)
}

// ReporterFromContext извлекает Reporter из контекста. nil, если не установлен.
func ReporterFromContext(ctx context.Context) Reporter {
	if r, ok := ctx.Value(reporterKey{}).(Reporter); ok {
		return r
	}
	return nil
}

// WithScope помечает атрибуцию токенов в контексте вызова Generate: вложенный
// контекст несёт клон репортёра с заданным scope. Если репортёра в контексте
// нет (консольный режим), контекст возвращается без изменений.
func WithScope(ctx context.Context, scope string) context.Context {
	if r := ReporterFromContext(ctx); r != nil {
		return WithReporter(ctx, r.WithScope(scope))
	}
	return ctx
}
