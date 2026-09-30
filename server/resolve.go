package server

import (
	"ai/logging"
	"ai/models"
	"fmt"
	"sync"
)

// providerResolve хранит выбор провайдера LLM и кэширует созданный клиент.
//
// Выбор двухуровневый: по умолчанию берётся из окружения (LLM_PROVIDER/MODEL/
// MODEL_LARGE), но пользователь может переопределить его из Web UI
// (POST /api/providers/select) — тогда используется override, пока он не
// сброшен. Это единственное место, где живёт состояние выбора: Server и
// обработчики читают его через selection()/describe(), поэтому гонок нет
// (всё под mu).
//
// Кэш сбрасывается при смене выбора — иначе переключение модели в UI было бы
// без эффекта после первого же запроса к модели.
type providerResolve struct {
	mu   sync.Mutex
	sel  models.Selection
	over bool // true — выбор сделан пользователем (UI), false — из окружения

	cached *models.Resolved
	err    error
	done   bool
}

// selection возвращает действующий выбор: override из UI, если он задан,
// иначе выбор из окружения. Блокировка не нужна для чтения неизменяемого
// override, но берём mu ради единообразия и возможной смены реализации.
func (p *providerResolve) selection() models.Selection {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.over {
		return p.sel
	}
	return models.EnvSelection()
}

// isOverride сообщает, что выбор сделан пользователем в UI (а не взят из
// окружения) — этим помечается текущий выбор в UI.
func (p *providerResolve) isOverride() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.over
}

// setOverride запоминает выбор пользователя, сбрасывает кэш и сразу создаёт
// провайдера: ошибка конфигурации (неизвестный base_url, пустая модель)
// возвращается в REST, а не всплывает позже на первом же запросе к модели.
// Фактическую модель пишет resolveLocked — единственное место логирования.
func (p *providerResolve) setOverride(sel models.Selection) (*models.Resolved, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sel, p.over, p.done = sel, true, false
	p.cached, p.err = nil, nil
	return p.resolveLocked()
}

// clearOverride возвращает выбор из окружения (кнопка «сбросить» в UI).
func (p *providerResolve) clearOverride() (*models.Resolved, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.over, p.done = false, false
	p.cached, p.err = nil, nil
	return p.resolveLocked()
}

// get возвращает провайдера, инициируя ResolveSelection при первом вызове.
// Фактически выбранные модели логируются: без этого в логах не видно, на
// какой модели реально идёт работа.
func (p *providerResolve) get() (models.LLMProvider, models.ProviderName, error) {
	res, err := p.resolved()
	if err != nil {
		name := p.selection().Provider
		if res != nil {
			name = res.Name
		}
		return nil, name, err
	}
	return res.Provider, res.Name, nil
}

// resolved возвращает кэшированный (или только что созданный) провайдера
// вместе с фактическими моделями — для логов и ответов REST.
func (p *providerResolve) resolved() (*models.Resolved, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resolveLocked()
}

// resolveLocked создаёт провайдера, если кэш пуст. Вызывается под mu.
func (p *providerResolve) resolveLocked() (*models.Resolved, error) {
	if p.done {
		return p.cached, p.err
	}
	p.done = true

	sel := models.EnvSelection()
	if p.over {
		sel = p.sel
	}
	res, err := models.ResolveSelection(sel)
	if err != nil {
		err = fmt.Errorf("инициализация провайдера LLM: %w", err)
		logging.Warnf("server: LLM: %v", err)
		p.cached, p.err = res, err
		return p.cached, p.err
	}
	logging.Infof("server: LLM: %s", res.Describe())
	p.cached, p.err = res, nil
	return p.cached, p.err
}

// describe возвращает описание работающей модели для логов проекта и UI.
// Не создаёт клиент: если провайдер ещё не трогали, описание собирается из
// выбора и providers.json.
func (p *providerResolve) describe() string {
	res, err := p.resolved()
	if err != nil {
		return describeSelection(p.selection()) + " (ошибка: " + err.Error() + ")"
	}
	return res.Describe()
}

// describeSelection описывает выбор без создания клиента (ошибки провайдера
// не показываем — они всплывут при реальном резолве).
func describeSelection(sel models.Selection) string {
	name := string(sel.Provider)
	if name == "" {
		name = "не задан"
	}
	out := name + "/" + sel.Model
	if sel.Model == "" {
		if cfg, err := models.LoadProvidersConfig(); err == nil {
			if pc, err := cfg.GetProviderConfig(sel.Provider); err == nil {
				out = name + "/" + pc.ResolveModel("")
			}
		}
	}
	if sel.LargeModel != "" {
		out += " (large=" + sel.LargeModel + ")"
	}
	return out
}
