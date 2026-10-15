package usecases

import (
	"ai/domain"
	"ai/domain/services"
	"ai/logging"
	"context"
	"fmt"
	"os"
	"strings"
)

// FullCycle — порт полного агентского цикла для оркестратора, чат-ассистента и
// CLI: выполняет диалог агента с инструментами. Реализуется над ChatProvider
// (см. NewCycle/NewEscalating): провайдеры моделей больше не знают про цикл, а
// application собирает политику запуска сам. Определение порта — в
// domain/services (services.AgentCycle), здесь лишь удобный алиас.
type FullCycle = services.AgentCycle

// singleCycle — полный цикл на одном провайдере без эскалации.
type singleCycle struct{ provider ChatProvider }

// NewCycle собирает цикл на единственном провайдере (эскалации нет).
func NewCycle(provider ChatProvider) FullCycle { return singleCycle{provider: provider} }

func (c singleCycle) Generate(ctx context.Context, agent Agent) (*domain.AgentResponse, error) {
	return RunAgent(ctx, c.provider, agent)
}

// escalatingCycle — двухслойный цикл с эскалацией: обычный агент пробует
// малый слой, при ошибке/зацикливании/обрезке один раз повторяет на большом.
//
// Небольшой/обычный агент (разработчик, инфраструктура, QA-инженер) выполняет
// точечные правки и читает готовые контракты — ему дешевая модель достаточна.
// Тяжёлые агенты (лиды декомпозиции, ревью) получают большую модель, чтобы
// качество маршрутизировалось по сложности задачи.
type escalatingCycle struct{ small, large ChatProvider }

// NewEscalating собирает двухслойный цикл. Если большой слой не задан —
// возвращается цикл на малом (поведение не меняется).
func NewEscalating(small, large ChatProvider) FullCycle {
	if large == nil {
		return NewCycle(small)
	}
	return escalatingCycle{small: small, large: large}
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

// heavyForContext — требование сильной модели из контекста
// (domain.WithHeavyModel). Источник — запись задачи доски (model_tier),
// накопленная за прошлые прогоны: внутри одного вызова Generate её нет, а
// переживать рестарт она обязана.
func heavyForContext(ctx context.Context) bool {
	return domain.HeavyModelFor(ctx)
}

// heavyFor решает, какой слой нужен агенту: явное требование агента
// (NeedsHeavyModel), эвристика по типу (лиды) или принудительный режим.
func heavyFor(agent Agent) bool {
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
func agentName(agent Agent) string {
	name := strings.ToLower(fmt.Sprintf("%T", agent))
	name = strings.TrimPrefix(name, "*")
	if i := strings.Index(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// Generate выполняет полный цикл на выбранном слое с эскалацией: обычный агент
// пробует small, при ошибке, зацикливании или обрезанном ответе один раз
// повторяет цикл на large. Ответ large считается финальным.
//
// Зацикливание (AgentResponse.Looped) — такой же повод для эскалации, как
// срыв по лимиту раундов: разрыв петли не помог, модель не поняла свою ошибку
// и повторяет то же самое. Задачу с другой стороны выполняет БОЛЬШАЯ модель —
// с чистой историей (WithoutResumeState), потому что продолжение того же
// застрявшего диалога воспроизвело бы ту же петлю.
func (c escalatingCycle) Generate(ctx context.Context, agent Agent) (*domain.AgentResponse, error) {
	// Требование задачи из контекста сильнее эвристики по агенту: если доска
	// помнит, что задача не осилилась на дешёвой модели, берём большой слой с
	// первого раунда, а не тратим цикл на заведомо слабую попытку.
	if heavyFor(agent) || heavyForContext(ctx) {
		logging.Detailf("[Layered] агент %T: тяжёлая модель (LARGE)", agent)
		return RunAgent(ctx, c.large, agent)
	}

	resp, err := RunAgent(ctx, c.small, agent)
	if err == nil && resp != nil && !resp.Truncated && !resp.Looped {
		return resp, nil
	}

	// Эскалация: сильная модель получает свежий шанс (без resume-истории —
	// бюджет раундов начинается заново). На повторном цикле тяжелый слой
	// пробует ещё раз; если и он не смог — возвращаем его вердикт.
	logging.Warnf("[Layered] агент %T: малый слой не справился (error=%v, truncated=%v, looped=%v%s) — эскалация на LARGE",
		agent, err, resp != nil && resp.Truncated, resp != nil && resp.Looped, loopDetail(resp))
	heavResp, heavErr := RunAgent(WithoutResumeState(ctx), c.large, agent)
	if heavErr != nil {
		if err != nil {
			return nil, err
		}
		return nil, heavErr
	}
	return heavResp, nil
}

// loopDetail дополняет лог эскалации причиной зацикливания (если цикл был
// прерван петлёй), чтобы в логе было видно, ЧТО именно не получилось.
func loopDetail(resp *domain.AgentResponse) string {
	if resp == nil || !resp.Looped {
		return ""
	}
	return ", причина: " + resp.LoopReason
}
