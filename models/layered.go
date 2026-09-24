package models

import (
	"ai/agents"
	"ai/logging"
	"ai/runner"
	"context"
	"fmt"
	"os"
	"strings"
)

// Небольшой/обычный агент (разработчик, инфраструктура, QA-инженер) выполняет
// точечные правки и читает уже готовые контракты — для него дешевая модель
// достаточна. Тяжёлые агенты (лиды декомпозиции, ревью) получают большую
// модель, чтобы качество маршрутизировалось по сложности задачи.
//
// Настройка: LayeredProvider оборачивает два провайдера — small (быстрый и
// дешёвый) и large (качественный). Тяжёлые шаги/агенты сразу идут на large;
// для остальных первый проход выполняет small, а при финальной ошибке или
// срыве по лимиту раундов происходит эскалация: весь цикл повторяется на
// large (один раз, чтобы не растить токен-бюджет).
//
// Управление переменными окружения:
//   - LLM_ALWAYS_HEAVY=1 — все шаги выполняются только на большой модели;
//   - LLM_MODEL_SMALL/LLM_MODEL_LARGE задаются при создании (main.go).
type LayeredProvider struct {
	Small LLMProvider
	Large LLMProvider
}

// NeedsHeavyModel — опциональный интерфейс агента: агент сам объявляет,
// что ему нужна большая модель (например, лид: декомпозиция эпиков).
type NeedsHeavyModel interface {
	NeedsHeavyModel() bool
}

// llmAlwaysHeavy — принудительно тяжёлый режим (LLM_ALWAYS_HEAVY=1).
func llmAlwaysHeavy() bool {
	return strings.TrimSpace(os.Getenv("LLM_ALWAYS_HEAVY")) == "1"
}

// NewLayeredProvider собирает двухслойный провайдер. Если large не задан —
// возвращается small без обёртки (поведение не меняется).
func NewLayeredProvider(small, large LLMProvider) LLMProvider {
	if small == nil {
		return large
	}
	if large == nil {
		return small
	}
	return &LayeredProvider{Small: small, Large: large}
}

// heavyFor решает, какой слой нужен агенту: явное требование агента
// (NeedsHeavyModel), эвристика по типу (лиды) или принудительный режим.
func (l *LayeredProvider) heavyFor(agent agents.Agent) bool {
	if llmAlwaysHeavy() {
		return true
	}
	if h, ok := agent.(NeedsHeavyModel); ok && h.NeedsHeavyModel() {
		return true
	}
	// Эвристика: лиды-декомпозиторы получают большую модель по умолчанию
	// (agentName уже в нижнем регистре, например "backendlead").
	return strings.Contains(agentName(agent), "lead")
}

// agentName возвращает имя конкретного типа агента без пакета и звёздочки
// (например "*backendlead.BackendLead" -> "backendlead").
func agentName(agent agents.Agent) string {
	name := strings.ToLower(fmt.Sprintf("%T", agent))
	name = strings.TrimPrefix(name, "*")
	if i := strings.Index(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// Generate выполняет полный агентский цикл на выбранном слое с эскалацией:
// обычный агент пробует small, при ошибке или зацикленном/обрезанном ответе
// один раз повторяет цикл на large. Ответ large считается финальным.
func (l *LayeredProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	if l.heavyFor(agent) {
		logging.Detailf("[Layered] агент %T: тяжёлая модель (LARGE)", agent)
		return l.Large.Generate(ctx, agent)
	}

	resp, err := l.Small.Generate(ctx, agent)
	if err == nil && resp != nil && !resp.Truncated {
		return resp, nil
	}

	// Эскалация: сильная модель получает свежий шанс (без resume-истории —
	// бюджет раундов начинается заново). На повторном цикле тяжелый слой
	// пробует ещё раз; если и он не смог — возвращаем его вердикт.
	logging.Warnf("[Layered] агент %T: малый слой не справился (error=%v, truncated=%v) — эскалация на LARGE",
		agent, err, resp != nil && resp.Truncated)
	heavResp, heavErr := l.Large.Generate(ctx, agent)
	if heavErr != nil {
		if err != nil {
			return nil, err
		}
		return nil, heavErr
	}
	return heavResp, nil
}

// ModelLimits сообщает лимиты слоёв маршрутизации: входное окно — по самому
// маленькому из слоёв (история должна влезать в обе модели), вывод/thinking —
// по максимуму (резерв под худший случай). Generate делегирует раунды
// конкретному слою, и раннер видит лимиты именно его — этот метод страховка
// для прямого использования LayeredProvider как ChatProvider.
func (l *LayeredProvider) ModelLimits() runner.ModelLimits {
	return mergeModelLimits(l.Small, l.Large)
}

// mergeModelLimits объединяет лимиты двух слоёв консервативно: входное окно —
// минимальное из ненулевых, вывод/thinking — максимальное из двух.
func mergeModelLimits(small, large LLMProvider) runner.ModelLimits {
	var out runner.ModelLimits
	take := func(p LLMProvider) {
		mp, ok := p.(runner.ModelLimitsProvider)
		if !ok {
			return
		}
		ml := mp.ModelLimits()
		if ml.InputTokens > 0 && (out.InputTokens == 0 || ml.InputTokens < out.InputTokens) {
			out.InputTokens = ml.InputTokens
		}
		if ml.OutputTokens > out.OutputTokens {
			out.OutputTokens = ml.OutputTokens
		}
		if ml.ThinkTokens > out.ThinkTokens {
			out.ThinkTokens = ml.ThinkTokens
		}
	}
	take(small)
	take(large)
	return out
}

// ChatOnce маршрутизирует отдельный запрос по тому же правилу слоёв (без
// эскалации раунда — финальные решения принимает Generate). Используется,
// когда провайдера зовут напрямую как ChatProvider.
func (l *LayeredProvider) ChatOnce(ctx context.Context, agent agents.Agent, msgs []runner.Message) (*runner.ModelReply, error) {
	layer := l.Small
	if l.heavyFor(agent) {
		layer = l.Large
	}
	cp, ok := layer.(runner.ChatProvider)
	if !ok {
		return nil, fmt.Errorf("слой %T не является ChatProvider", layer)
	}
	return cp.ChatOnce(ctx, agent, msgs)
}

// ChatStream — потоковая версия ChatOnce для слоёв: делегирует выбранному слою,
// если тот поддерживает стриминг, иначе fallback на разовый ChatOnce.
func (l *LayeredProvider) ChatStream(ctx context.Context, agent agents.Agent, msgs []runner.Message, onChunk func(runner.StreamChunk)) (*runner.ModelReply, error) {
	layer := l.Small
	if l.heavyFor(agent) {
		layer = l.Large
	}
	if sp, ok := layer.(runner.StreamChatProvider); ok {
		return sp.ChatStream(ctx, agent, msgs, onChunk)
	}
	cp, ok := layer.(runner.ChatProvider)
	if !ok {
		return nil, fmt.Errorf("слой %T не является ChatProvider", layer)
	}
	return cp.ChatOnce(ctx, agent, msgs)
}