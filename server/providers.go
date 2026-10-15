package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"ai/application/usecases"
	"ai/infrastructure/llm"
)

// handleGetProviders отдаёт список провайдеров из providers.json и ТЕКУЩИЙ
// выбор (провайдер + основная и большая модели), чтобы переключатель в UI
// показывал реально работающую модель, а не первую попавшуюся.
//
// Список провайдеров отсортирован: обход map в Go неупорядочен, и без сортировки
// UI прыгал бы между провайдерами при каждом обновлении страницы.
func (s *Server) handleGetProviders(w http.ResponseWriter, r *http.Request) {
	cfg, err := llm.LoadProvidersConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "providers.json недоступен: "+err.Error())
		return
	}

	type providerInfo struct {
		Name         string   `json:"name"`
		Models       []string `json:"models"`
		DefaultModel string   `json:"default_model"`
		LargeModel   string   `json:"large_model"`
	}

	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)

	providers := make([]providerInfo, 0, len(names))
	for _, name := range names {
		providers = append(providers, providerInfo{
			Name:         name,
			Models:       selectableModels(cfg.Providers[name]),
			DefaultModel: cfg.Providers[name].DefaultModel,
			LargeModel:   cfg.Providers[name].LargeModel,
		})
	}

	sel := s.prov.selection()
	out := map[string]any{
		"providers":           providers,
		"current_provider":    string(sel.Provider),
		"current_model":       sel.Model,
		"current_large_model": sel.LargeModel,
		"override":            s.prov.isOverride(),
	}
	// Фактические модели (после дефолтов providers.json) и ошибка текущего
	// выбора: без них UI не может показать «large=qwen3.6:35b» или «провайдер
	// не настроен».
	if res, rerr := s.prov.resolved(); res != nil {
		out["current_model"] = res.Model
		out["current_large_model"] = res.LargeModel
		if rerr != nil {
			out["error"] = rerr.Error()
		}
	} else if rerr != nil {
		out["error"] = rerr.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

// selectableModels — модели, доступные для выбора: объединение models,
// default_model и large_model. Последние два могут отсутствовать в models
// (особенно large_model), и без объединения действующую модель нельзя было бы
// выбрать в UI.
func selectableModels(cfg llm.ProviderConfig) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(cfg.Models)+2)
	for _, m := range append(append([]string{}, cfg.Models...), cfg.DefaultModel, cfg.LargeModel) {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// handleSelectProvider переключает провайдера/модели на лету. Провайдер
// создаётся сразу — если он непригоден (битый base_url, пустая модель), клиент
// получает ошибку сейчас, а не на первом запросе к модели. Уже идущая
// оркестрация продолжает на прежней модели (провайдер переиспользуется до
// конца запуска) — об этом сказано в ответе и в логе.
func (s *Server) handleSelectProvider(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		// LargeModel — необязательная большая модель для «тяжёлых» агентов
		// (лиды, архитектор, ревьюер); "" — слой не включается.
		LargeModel string `json:"large_model"`
		// Reset=true — вернуть выбор из окружения (кнопка «сбросить»).
		Reset bool `json:"reset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if body.Reset {
		res, err := s.prov.clearOverride()
		s.writeSelectionResult(w, res, err)
		return
	}

	cfg, err := llm.LoadProvidersConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "providers.json недоступен: "+err.Error())
		return
	}

	providerCfg, ok := cfg.Providers[strings.TrimSpace(body.Provider)]
	if !ok {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("провайдер %q не найден в providers.json", body.Provider))
		return
	}

	allowed := selectableModels(providerCfg)
	model := strings.TrimSpace(body.Model)
	if model == "" {
		model = providerCfg.ResolveModel("")
	}
	if !contains(allowed, model) {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("модель %q недоступна у провайдера %s (доступны: %s)",
			model, body.Provider, strings.Join(allowed, ", ")))
		return
	}
	large := strings.TrimSpace(body.LargeModel)
	if large != "" && !contains(allowed, large) {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("модель %q недоступна у провайдера %s (доступны: %s)",
			large, body.Provider, strings.Join(allowed, ", ")))
		return
	}

	res, err := s.prov.setOverride(llm.Selection{
		Provider:   llm.ProviderName(strings.TrimSpace(body.Provider)),
		Model:      model,
		LargeModel: large,
	})
	s.writeSelectionResult(w, res, err)
}

// writeSelectionResult отвечает на смену выбора: 200 с фактическими моделями
// либо 502 с причиной (провайдер не собрался — выбор уже применён, но не
// работает; UI покажет текст ошибки из лога/ответа).
func (s *Server) writeSelectionResult(w http.ResponseWriter, res *llm.Resolved, err error) {
	if err != nil {
		msg := err.Error()
		// Ошибка конфигурации (providers.json, base_url, модель) — это ответ
		// на запрос «сделай так», а не отказ сервера: 400 информативнее 502.
		code := http.StatusBadGateway
		if res == nil || strings.Contains(msg, "providers.json") {
			code = http.StatusBadRequest
		}
		writeErr(w, code, "не удалось применить выбор модели: "+msg)
		return
	}
	// Идущая оркестрация держит старый провайдер до конца запуска — честно
	// предупреждаем, чтобы смена модели не выглядела «не сработавшей».
	inFlight := s.anySessionRunning()

	out := map[string]any{
		"ok":                 true,
		"provider":           string(res.Name),
		"model":              res.Model,
		"large_model":        res.LargeModel,
		"describe":           res.Describe(),
		"applies_to_running": inFlight,
	}
	if inFlight {
		out["message"] = "текущий запуск продолжится на прежней модели; выбор применится со следующего"
	}
	writeJSON(w, http.StatusOK, out)
}

// provider возвращает цикл агента (ленивая инициализация). Возвращает
// ошибку, если провайдер не настроен (LLM_PROVIDER).
func (s *Server) provider() (usecases.FullCycle, error) {
	p, _, err := s.prov.get()
	if err != nil {
		return nil, err
	}
	return p, nil
}

// sessionFlags возвращает флаги состояния сессии проекта (running/gating/
// standby). Используется там, где статус читается из projectMeta без снапшота:
// list/meta проектов по REST.
func (s *Server) sessionFlags(project string) (running, gating, standby bool) {
	sess := s.session(project)
	if sess == nil {
		return false, false, false
	}
	sess.mu.Lock()
	running, standby = sess.running, sess.standby
	sess.mu.Unlock()
	gating, _, _ = sess.gateOf().State()
	return running, gating, standby
}

// anySessionRunning сообщает, идёт ли где-нибудь оркестрация. Порядок
// блокировок тот же, что в sessionFlags (s.mu → sess.mu).
func (s *Server) anySessionRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		sess.mu.Lock()
		run := sess.running
		sess.mu.Unlock()
		if run {
			return true
		}
	}
	return false
}
