package server

import (
	"ai/models"
	"fmt"
	"os"
	"sync"
)

type providerResolve struct {
	mu             sync.Mutex
	prov           models.LLMProvider
	name           models.ProviderName
	err            error
	done           bool
	activeProvider string
	activeModel    string
}

func (p *providerResolve) get() (models.LLMProvider, models.ProviderName, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return p.prov, p.name, p.err
	}
	p.done = true

	provider := models.ProviderName(os.Getenv("LLM_PROVIDER"))
	model := os.Getenv("MODEL")

	if p.activeProvider != "" {
		provider = models.ProviderName(p.activeProvider)
	}
	if p.activeModel != "" {
		model = p.activeModel
	}

	os.Setenv("LLM_PROVIDER", string(provider))
	if model != "" {
		os.Setenv("MODEL", model)
	}

	prov, name, err := models.ResolveProvider()
	if err != nil {
		err = fmt.Errorf("инициализация провайдера LLM: %w", err)
	}
	p.prov, p.name, p.err = prov, name, err
	return p.prov, p.name, p.err
}

func (p *providerResolve) setActive(provider, model string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.activeProvider = provider
	p.activeModel = model
}

func (p *providerResolve) ok() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done && p.err == nil && p.prov != nil
}
