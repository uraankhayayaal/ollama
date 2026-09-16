package server

import (
	"ai/models"
	"fmt"
	"sync"
)

// providerResolve кэширует провайдер LLM. Вызывается при первом POST /chat;
// позволяет серверу стартовать без настроенного LLM (только доска и чат).
type providerResolve struct {
	mu   sync.Mutex
	prov models.LLMProvider
	name models.ProviderName
	err  error
	done bool
}

// get возвращает провайдер, инициируя ResolveProvider при первом вызове.
func (p *providerResolve) get() (models.LLMProvider, models.ProviderName, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return p.prov, p.name, p.err
	}
	p.done = true
	prov, name, err := models.ResolveProvider()
	if err != nil {
		err = fmt.Errorf("инициализация провайдера LLM: %w", err)
	}
	p.prov, p.name, p.err = prov, name, err
	return p.prov, p.name, p.err
}

// ok возвращает true, если провайдер уже был успешно получен.
func (p *providerResolve) ok() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done && p.err == nil && p.prov != nil
}
