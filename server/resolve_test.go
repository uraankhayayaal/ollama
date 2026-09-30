package server

import (
	"ai/models"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubProviderResolve подставляет провайдера в srv.prov без чтения
// providers.json: тесты не должны зависеть от конфигурации окружения.
// nil — «провайдер настроен, но не создан» (используется в тестах
// недоступного LLM).
func stubProviderResolve(prov models.LLMProvider) providerResolve {
	return providerResolve{
		cached: &models.Resolved{Provider: prov, Name: "stub", Model: "stub-model"},
		done:   true,
	}
}

// testProvidersConfig пишет providers.json во временный каталог и указывает
// на него PROVIDERS_CONFIG. Отдельный файл на каждый тест — кэш конфига в
// models keyed по пути и отпечатку файла, поэтому тесты не мешают друг другу.
func testProvidersConfig(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDERS_CONFIG", path)
}

// twoModelsConfig — ollama с двумя моделями (тег с двоеточием, как в жизни),
// без крупной модели: все агенты работают на выбранной.
const twoModelsConfig = `{
  "providers": {
    "ollama": {
      "base_url": "http://127.0.0.1:11434",
      "models": ["qwen3-coder:30b", "qwen2.5-coder:7b"],
      "default_model": "qwen3-coder:30b",
      "large_model": "",
      "settings": {"input_tokens": 32000, "output_tokens": 4096}
    }
  }
}`

// TestSelectProviderAppliesAfterFirstResolve — регресс на главный дефект
// переключателя модели: провайдер кэшируется при ПЕРВОМ использовании, и
// прежняя реализация setActive кэш не сбрасывала — выбор из UI не действовал
// до перезапуска процесса.
func TestSelectProviderAppliesAfterFirstResolve(t *testing.T) {
	testProvidersConfig(t, twoModelsConfig)
	t.Setenv("LLM_PROVIDER", "ollama")
	srv, _, _ := newTestServer(t)

	// Первый резолв (как при первом чате) фиксирует модель по умолчанию.
	if _, _, err := srv.prov.get(); err != nil {
		t.Fatalf("первый резолв: %v", err)
	}
	res, err := srv.prov.resolved()
	if err != nil {
		t.Fatalf("resolved: %v", err)
	}
	if res.Model != "qwen3-coder:30b" {
		t.Fatalf("модель по умолчанию = %q, want qwen3-coder:30b", res.Model)
	}

	// Пользователь выбирает другую модель в UI.
	if _, err := srv.prov.setOverride(models.Selection{
		Provider: models.ProviderOllama,
		Model:    "qwen2.5-coder:7b",
	}); err != nil {
		t.Fatalf("setOverride: %v", err)
	}

	res, err = srv.prov.resolved()
	if err != nil {
		t.Fatalf("resolved после смены: %v", err)
	}
	if res.Model != "qwen2.5-coder:7b" {
		t.Fatalf("после смены работает модель %q, want qwen2.5-coder:7b", res.Model)
	}
	if !strings.Contains(res.Describe(), "qwen2.5-coder:7b") {
		t.Fatalf("Describe() = %q, нет имени модели", res.Describe())
	}
	// Крупная модель была равна основной → слой large выключен (не дублируется
	// в описании).
	if strings.Contains(res.Describe(), "large=") {
		t.Fatalf("Describe() = %q: large-слой не должен дублировать основную модель", res.Describe())
	}
}

// TestSelectProviderKeepsEnvModelAsDefault — до явного выбора из UI действует
// выбор из окружения (LLM_PROVIDER/MODEL), и сброс возвращает его обратно.
func TestSelectProviderKeepsEnvModelAsDefault(t *testing.T) {
	testProvidersConfig(t, twoModelsConfig)
	t.Setenv("LLM_PROVIDER", "ollama")
	t.Setenv("MODEL", "qwen2.5-coder:7b")

	srv, _, _ := newTestServer(t)
	if sel := srv.prov.selection(); sel.Model != "qwen2.5-coder:7b" {
		t.Fatalf("selection() = %+v, want MODEL=qwen2.5-coder:7b", sel)
	}
	if srv.prov.isOverride() {
		t.Fatal("до выбора из UI override должен быть false")
	}

	if _, err := srv.prov.setOverride(models.Selection{Provider: "ollama", Model: "qwen3-coder:30b"}); err != nil {
		t.Fatalf("setOverride: %v", err)
	}
	if sel := srv.prov.selection(); sel.Model != "qwen3-coder:30b" {
		t.Fatalf("после выбора selection() = %+v, want qwen3-coder:30b", sel)
	}

	res, err := srv.prov.clearOverride()
	if err != nil {
		t.Fatalf("clearOverride: %v", err)
	}
	if res.Model != "qwen2.5-coder:7b" {
		t.Fatalf("после сброса модель %q, want qwen2.5-coder:7b (из MODEL)", res.Model)
	}
}

// TestRESTProvidersReportsEnvSelection — GET /api/providers до первого чата
// отдаёт выбор из окружения, чтобы UI показал работающую модель, а не первую
// попавшуюся из providers.json.
func TestRESTProvidersReportsEnvSelection(t *testing.T) {
	testProvidersConfig(t, twoModelsConfig)
	t.Setenv("LLM_PROVIDER", "ollama")
	t.Setenv("MODEL", "qwen2.5-coder:7b")

	_, handler, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/providers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET providers: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Providers []struct {
			Name   string   `json:"name"`
			Models []string `json:"models"`
		} `json:"providers"`
		CurrentProvider string `json:"current_provider"`
		CurrentModel    string `json:"current_model"`
		CurrentLarge    string `json:"current_large_model"`
		Override        bool   `json:"override"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.CurrentProvider != "ollama" || out.CurrentModel != "qwen2.5-coder:7b" {
		t.Fatalf("current = %s/%s, want ollama/qwen2.5-coder:7b", out.CurrentProvider, out.CurrentModel)
	}
	if out.Override {
		t.Fatal("override = true, до выбора из UI должен быть false")
	}
	if len(out.Providers) != 1 || out.Providers[0].Name != "ollama" {
		t.Fatalf("providers = %+v", out.Providers)
	}
	// Модели в UI-выпадающем списке: обе из providers.json (двоеточие в теге
	// не должно ничего ломать).
	if len(out.Providers[0].Models) != 2 {
		t.Fatalf("models = %v, want 2 модели", out.Providers[0].Models)
	}
}

// TestRESTSelectProviderSwitchesModel — полный путь UI: POST переключает
// модель, GET подтверждает, что переключение применилось (а не осталось
// декларацией в Server-полях, как в первой реализации).
func TestRESTSelectProviderSwitchesModel(t *testing.T) {
	testProvidersConfig(t, twoModelsConfig)
	t.Setenv("LLM_PROVIDER", "ollama")
	t.Setenv("MODEL", "qwen3-coder:30b")

	srv, handler, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/providers/select",
		strings.NewReader(`{"provider":"ollama","model":"qwen2.5-coder:7b"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST select: %d, body: %s", rec.Code, rec.Body.String())
	}
	var sel struct {
		OK      bool   `json:"ok"`
		Model   string `json:"model"`
		Desc    string `json:"describe"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sel); err != nil {
		t.Fatal(err)
	}
	if !sel.OK || sel.Model != "qwen2.5-coder:7b" {
		t.Fatalf("ответ select = %+v", sel)
	}
	if !strings.Contains(sel.Desc, "qwen2.5-coder:7b") {
		t.Fatalf("describe = %q", sel.Desc)
	}
	// Ничего не запущено — предупреждения о «текущем запуске» быть не должно.
	if sel.Message != "" {
		t.Fatalf("message = %q, при неработающей оркестрации должен быть пуст", sel.Message)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/providers", nil))
	var out struct {
		CurrentModel string `json:"current_model"`
		Override     bool   `json:"override"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.CurrentModel != "qwen2.5-coder:7b" || !out.Override {
		t.Fatalf("после select: current_model=%q override=%v", out.CurrentModel, out.Override)
	}
	// И клиент, отданный оркестрации, теперь другой:
	res, err := srv.prov.resolved()
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "qwen2.5-coder:7b" {
		t.Fatalf("провайдер работает на %q, want qwen2.5-coder:7b", res.Model)
	}
}

// TestRESTSelectProviderLargeModel — крупная модель выбирается отдельно и
// попадает в описание (её используют лиды/архитектор/ревьюер).
func TestRESTSelectProviderLargeModel(t *testing.T) {
	testProvidersConfig(t, `{
      "providers": {
        "ollama": {
          "base_url": "http://127.0.0.1:11434",
          "models": ["qwen3-coder:30b", "qwen2.5-coder:7b"],
          "default_model": "qwen3-coder:30b",
          "settings": {"input_tokens": 32000, "output_tokens": 4096}
        }
      }
    }`)
	srv, handler, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/providers/select",
		strings.NewReader(`{"provider":"ollama","model":"qwen2.5-coder:7b","large_model":"qwen3-coder:30b"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST select: %d, body: %s", rec.Code, rec.Body.String())
	}
	res, err := srv.prov.resolved()
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "qwen2.5-coder:7b" || res.LargeModel != "qwen3-coder:30b" {
		t.Fatalf("resolved = %s/%s (large %s)", res.Name, res.Model, res.LargeModel)
	}
	if want := "ollama/qwen2.5-coder:7b (large=qwen3-coder:30b)"; res.Describe() != want {
		t.Fatalf("Describe() = %q, want %q", res.Describe(), want)
	}
}

// TestRESTSelectProviderRejectsUnknown — выбор вне providers.json отклоняется
// с внятным сообщением, и провайдер продолжает работать на прежней модели.
func TestRESTSelectProviderRejectsUnknown(t *testing.T) {
	testProvidersConfig(t, twoModelsConfig)
	srv, handler, _ := newTestServer(t)

	for _, tc := range []struct{ name, body string }{
		{"неизвестная модель", `{"provider":"ollama","model":"gpt-4o"}`},
		{"неизвестный провайдер", `{"provider":"openai","model":"gpt-4o"}`},
		{"крупная модель не из списка", `{"provider":"ollama","model":"qwen3-coder:30b","large_model":"gpt-4o"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/providers/select", strings.NewReader(tc.body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("код = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
			}
			var out struct{ Message string }
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.Message, "недоступна") && !strings.Contains(out.Message, "не найден") {
				t.Fatalf("сообщение = %q", out.Message)
			}
			// Предыдущий выбор не пострадал.
			if srv.prov.isOverride() {
				t.Fatal("отклонённый выбор не должен был применяться")
			}
		})
	}
}

// TestRESTSelectProviderBrokenBaseURL — непригодный выбор отвечает ошибкой
// сразу (а не на первом запросе к модели) и попадает в GET как error.
func TestRESTSelectProviderBrokenBaseURL(t *testing.T) {
	testProvidersConfig(t, `{
      "providers": {
        "ollama": {
          "base_url": "://не-url",
          "models": ["qwen3-coder:30b"],
          "default_model": "qwen3-coder:30b",
          "settings": {}
        }
      }
    }`)
	_, handler, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/providers/select",
		strings.NewReader(`{"provider":"ollama","model":"qwen3-coder:30b"}`)))
	if rec.Code == http.StatusOK {
		t.Fatalf("битый base_url принят как валидный: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/providers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET providers: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Error == "" {
		t.Fatalf("в ответе GET нет error: %s", rec.Body.String())
	}
}

// TestSelectProviderLargeModelFallsBackToConfig — если крупная модель не
// выбрана в UI, берётся large_model из providers.json (двухслойная
// маршрутизация лидов/ревьюеров не должна молча отключаться).
func TestSelectProviderLargeModelFallsBackToConfig(t *testing.T) {
	testProvidersConfig(t, `{
      "providers": {
        "ollama": {
          "base_url": "http://127.0.0.1:11434",
          "models": ["qwen3-coder:30b", "qwen2.5-coder:7b"],
          "default_model": "qwen3-coder:30b",
          "large_model": "qwen3-coder:30b",
          "settings": {"input_tokens": 32000, "output_tokens": 4096}
        }
      }
    }`)
	srv, _, _ := newTestServer(t)

	if _, err := srv.prov.setOverride(models.Selection{Provider: "ollama", Model: "qwen2.5-coder:7b"}); err != nil {
		t.Fatalf("setOverride: %v", err)
	}
	res, err := srv.prov.resolved()
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "qwen2.5-coder:7b" || res.LargeModel != "qwen3-coder:30b" {
		t.Fatalf("resolved = %s/%s (large %s), want qwen2.5-coder:7b (large qwen3-coder:30b)",
			res.Name, res.Model, res.LargeModel)
	}
}

// TestProvidersConfigReloadsOnChange — правка providers.json подхватывается без
// перезапуска процесса (иначе добавленную модель нельзя было бы выбрать).
func TestProvidersConfigReloadsOnChange(t *testing.T) {
	testProvidersConfig(t, twoModelsConfig)
	if cfg, err := models.LoadProvidersConfig(); err != nil || len(cfg.Providers["ollama"].Models) != 2 {
		t.Fatalf("первая загрузка: %v, %+v", err, cfg)
	}

	// Дописываем модель; mtime меняется — кэш должен перечитать файл.
	path := os.Getenv("PROVIDERS_CONFIG")
	body := strings.Replace(twoModelsConfig, `"qwen2.5-coder:7b"`, `"qwen2.5-coder:7b", "llama3.2"`, 1)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := models.LoadProvidersConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(cfg.Providers["ollama"].Models); got != 3 {
		t.Fatalf("после правки моделей %d, want 3", got)
	}
}

// TestRESTSelectProviderWarnsAboutRunningOrchestration — смена модели во время
// оркестрации применяется, но честно предупреждает, что текущий запуск
// продолжится на прежней модели (иначе UI выглядел бы «не сработавшим»).
func TestRESTSelectProviderWarnsAboutRunningOrchestration(t *testing.T) {
	testProvidersConfig(t, twoModelsConfig)
	srv, handler, _ := newTestServer(t)
	registerTestDir(t, srv, "prov-model-run")

	sess, _, err := srv.getOrCreate("prov-model-run")
	if err != nil {
		t.Fatal(err)
	}
	sess.mu.Lock()
	sess.running = true
	sess.mu.Unlock()
	defer func() {
		sess.mu.Lock()
		sess.running = false
		sess.mu.Unlock()
	}()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/providers/select",
		strings.NewReader(`{"provider":"ollama","model":"qwen2.5-coder:7b"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST select: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		OK               bool   `json:"ok"`
		Model            string `json:"model"`
		AppliesToRunning bool   `json:"applies_to_running"`
		Message          string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Model != "qwen2.5-coder:7b" {
		t.Fatalf("ответ = %+v: выбор должен применяться", out)
	}
	if !out.AppliesToRunning || out.Message == "" {
		t.Fatalf("applies_to_running=%v message=%q, при идущей оркестрации нужно предупреждение",
			out.AppliesToRunning, out.Message)
	}
}
