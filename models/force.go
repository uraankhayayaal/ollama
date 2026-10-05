package models

// Требование «работай на сильной модели», переданное ЧЕРЕЗ контекст.
//
// Зачем это не в агенте и не в поле провайдера: эскалация модели должна
// переживать вызов Generate. Внутри одного цикла её решал LayeredProvider
// (small → large при зацикливании), но после рестарта сервера или после
// паузы задача снова начинала с дешёвой модели. Оркестратор записывает
// требование в запись задачи (board.Task.ModelTier) и на каждый её прогон
// оборачивает контекст этой функцией — тогда «эта задача тяжёлая» действует
// столько, сколько нужно, и не зависит от того, кто именно вызывает модель.

import "context"

// heavyModelKey — ключ значения в контексте (неэкспортируемый тип, чтобы
// случайно не пересечься с чужими ключами).
type heavyModelKey struct{}

// WithHeavyModel помечает контекст требованием использовать большой слой
// (LayeredProvider). Требование наследуется вложенными вызовами.
func WithHeavyModel(ctx context.Context) context.Context {
	return context.WithValue(ctx, heavyModelKey{}, true)
}

// HeavyModelFor сообщает, помечен ли контекст требованием сильной модели.
func HeavyModelFor(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(heavyModelKey{}).(bool)
	return v
}
