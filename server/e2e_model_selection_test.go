//go:build e2e

package server

// Живой E2E выбора модели в Web UI: настоящий HTTP, настоящая Ollama.
//
// Исключение из правила «тесты не трогают внешние сервисы»
// (docs/40-operations/testing.md): тест намеренно вынесен за build-тег, поэтому
// в рабочий набор и в CI не попадает. Доска в тесте — miniredis, как и в
// остальных тестах пакета, живой нужен только Ollama.
//
// Запуск:
//
//	E2E_LIVE=1 go test -tags e2e -run TestE2EModelSelection ./server/ -timeout 10m
//
// Модели выбираются автоматически из /api/tags: нужны две, умеющие tool-calls
// (по ним проверяется, что чат реально пошёл в выбранную модель). Можно
// задать явно: E2E_MODEL_SMALL, E2E_MODEL_SECOND, E2E_MODEL_LARGE.

import (
	"ai/chat"
	"ai/logging"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// e2eLLMBase — адрес Ollama для живого E2E.
func e2eLLMBase() string {
	if v := strings.TrimSpace(os.Getenv("OLLAMA_BASE_URL")); v != "" {
		return v
	}
	return "http://127.0.0.1:11434"
}

func e2eEnvOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// e2eJSON — один запрос к JSON-эндпоинту (база + путь), возвращает код и тело.
func e2eJSON(t *testing.T, method, url string, body any, timeout time.Duration) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s %s: %v", method, url, err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("новый запрос: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req.WithContext(t.Context()))
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// e2eCheck — независимая проверка: продолжаем дальше, чтобы увидеть все дефекты.
func e2eCheck(t *testing.T, name string, cond bool, detail string) {
	t.Helper()
	if !cond {
		t.Errorf("E2E: %s — %s", name, detail)
	}
}

// e2eModelNames — модели, которые умеют отвечать на tool-calls (через
// тестовый запрос с инструментом). Без этого ассистенту не на чем работать,
// и проверка «чата на выбранной модели» ничего не докажет.
func e2eModelNames(t *testing.T) []string {
	t.Helper()
	code, raw := e2eJSON(t, "GET", e2eLLMBase()+"/api/tags", nil, 15*time.Second)
	if code != 0 && code != http.StatusOK {
		t.Skipf("Ollama недоступна (%s → %d) — живой E2E пропущен", e2eLLMBase(), code)
	}
	if code == 0 {
		t.Skipf("Ollama недоступна (%s) — живой E2E пропущен", e2eLLMBase())
	}
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("разбор /api/tags: %v", err)
	}
	names := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		if m.Name == "" || strings.Contains(m.Name, "embed") {
			continue
		}
		names = append(names, m.Name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Skip("в Ollama нет моделей — живой E2E пропущен")
	}
	return names
}

// e2eHasTools — умеет ли модель вызывать инструменты (тестовый вызов).
func e2eHasTools(t *testing.T, model string) bool {
	t.Helper()
	body := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "Позвони в погодную службу и узнай погоду в Париже"},
		},
		"tools": []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name":        "get_weather",
					"description": "Узнать погоду в городе",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"city": map[string]string{"type": "string"},
						},
						"required": []string{"city"},
					},
				},
			},
		},
		"stream":  false,
		"options": map[string]any{"num_predict": 128},
	}
	code, raw := e2eJSON(t, "POST", e2eLLMBase()+"/api/chat", body, 180*time.Second)
	if code != http.StatusOK {
		return false
	}
	var out struct {
		Message struct {
			ToolCalls []json.RawMessage `json:"tool_calls"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return false
	}
	return len(out.Message.ToolCalls) > 0
}

// e2ePickModel — модель с tool-calls, не совпадающая с alreadyUsed.
func e2ePickModel(t *testing.T, candidates []string, alreadyUsed ...string) string {
	t.Helper()
	used := map[string]bool{}
	for _, u := range alreadyUsed {
		used[u] = true
	}
	const limit = 6 // не гоняем все модели на медленной машине
	probes := 0
	for _, name := range candidates {
		if used[name] {
			continue
		}
		if probes >= limit {
			break
		}
		probes++
		if e2eHasTools(t, name) {
			return name
		}
	}
	return ""
}

// e2eLoaded — модели, загруженные в Ollama сейчас (/api/ps).
func e2eLoaded(t *testing.T) []string {
	t.Helper()
	_, raw := e2eJSON(t, "GET", e2eLLMBase()+"/api/ps", nil, 15*time.Second)
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	names := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		names = append(names, m.Name)
	}
	sort.Strings(names)
	return names
}

// e2eUnload — выгружает всё из Ollama: иначе /api/ps не докажет, какую модель
// реально спросило приложение.
func e2eUnload(t *testing.T) {
	t.Helper()
	for _, name := range e2eLoaded(t) {
		body := map[string]any{"model": name, "keep_alive": 0}
		e2eJSON(t, "POST", e2eLLMBase()+"/api/generate", body, 60*time.Second)
	}
	time.Sleep(time.Second)
}

func writeE2EConfig(t *testing.T, path string, cfg map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal конфига: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("запись %s: %v", path, err)
	}
}

// TestE2EModelSelection — полный путь «переключил модель в UI → работает
// выбранная модель» на живой Ollama.
//
// Ключевое доказательство — /api/ps: до запроса модели выгружены, поэтому
// единственная загруженная модель доказывает, что приложение спросило именно
// её. Логи проверяются отдельно: без них в проде не видно, на чём работает.
func TestE2EModelSelection(t *testing.T) {
	if os.Getenv("E2E_LIVE") == "" {
		t.Skip("живой E2E: нужен E2E_LIVE=1 и запущенная Ollama (см. docs/40-operations/testing.md)")
	}

	// --- модели: две с tool-calls + любая для крупного слоя ---------------
	candidates := e2eModelNames(t)
	modelA := strings.TrimSpace(os.Getenv("E2E_MODEL_SMALL"))
	if modelA == "" {
		modelA = e2ePickModel(t, candidates)
	}
	if modelA == "" {
		t.Skip("среди моделей Ollama не нашлось ни одной с tool-calls")
	}
	modelB := strings.TrimSpace(os.Getenv("E2E_MODEL_SECOND"))
	if modelB == "" {
		modelB = e2ePickModel(t, candidates, modelA)
	}
	if modelB == "" {
		t.Skipf("нет второй модели с tool-calls (первая: %s) — переключение проверить не на чем", modelA)
	}
	if modelB == modelA {
		t.Fatalf("E2E_MODEL_SECOND совпадает с основной моделью (%s)", modelA)
	}
	large := strings.TrimSpace(os.Getenv("E2E_MODEL_LARGE"))
	if large == "" {
		for _, name := range candidates {
			if name != modelA && name != modelB {
				large = name
				break
			}
		}
	}
	t.Logf("E2E модели: основная=%s, вторая=%s, крупная=%s", modelA, modelB, large)

	// --- логи: отдельный каталог на тест, чтобы не писать в logs/ проекта ---
	logDir := t.TempDir()
	t.Setenv("LOG_DIR", logDir)
	logging.Setup("server")
	t.Cleanup(logging.Close)

	// --- конфиг и выбор по умолчанию ---------------------------------------
	cfgPath := filepath.Join(t.TempDir(), "providers.json")
	t.Setenv("PROVIDERS_CONFIG", cfgPath)
	t.Setenv("LLM_PROVIDER", "ollama")
	t.Setenv("MODEL", modelA)
	t.Setenv("MODEL_LARGE", "")
	// RAG в живом E2E не нужен — без QDRANT_ADDR/EMBEDDING_MODEL ассистент
	// работает без блока «релевантный код» (путь деградации, он же детерминирован).
	t.Setenv("QDRANT_ADDR", "")
	t.Setenv("EMBEDDING_MODEL", "")

	setE2EModels(t, cfgPath, []string{modelA, modelB, large}, modelA, "")

	srv, handler, _ := newTestServer(t)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	e2eUnload(t)

	// --- 1. старт: выбор из окружения --------------------------------------
	code, raw := e2eJSON(t, "GET", ts.URL+"/api/providers", nil, 20*time.Second)
	if code != http.StatusOK {
		t.Fatalf("GET /api/providers → %d: %s", code, raw)
	}
	var startup struct {
		Providers []struct {
			Name   string   `json:"name"`
			Models []string `json:"models"`
		} `json:"providers"`
		CurrentProvider string `json:"current_provider"`
		CurrentModel    string `json:"current_model"`
		Override        bool   `json:"override"`
		Error           string `json:"error"`
	}
	if err := json.Unmarshal(raw, &startup); err != nil {
		t.Fatalf("разбор GET /api/providers: %v", err)
	}
	e2eCheck(t, "current_provider = ollama", startup.CurrentProvider == "ollama", startup.CurrentProvider)
	e2eCheck(t, "current_model = MODEL из окружения", startup.CurrentModel == modelA, startup.CurrentModel)
	e2eCheck(t, "override = false до выбора в UI", !startup.Override, fmt.Sprint(startup.Override))
	e2eCheck(t, "ошибок конфигурации нет", startup.Error == "", startup.Error)
	names := make([]string, 0, len(startup.Providers))
	for _, p := range startup.Providers {
		names = append(names, p.Name)
	}
	e2eCheck(t, "провайдеры отсортированы", equalStrings(names, sortedCopy(names)), strings.Join(names, ","))
	var ollamaProv struct {
		Models []string `json:"models"`
	}
	for _, p := range startup.Providers {
		if p.Name == "ollama" {
			ollamaProv.Models = p.Models
		}
	}
	e2eCheck(t, "список моделей совпал с providers.json",
		containsAll(ollamaProv.Models, modelA, modelB, large), strings.Join(ollamaProv.Models, ","))

	// --- 2. переключение ----------------------------------------------------
	code, raw = e2eJSON(t, "POST", ts.URL+"/api/providers/select",
		map[string]any{"provider": "ollama", "model": modelB}, 60*time.Second)
	if code != http.StatusOK {
		t.Fatalf("POST /api/providers/select → %d: %s", code, raw)
	}
	var selected struct {
		OK       bool   `json:"ok"`
		Model    string `json:"model"`
		Describe string `json:"describe"`
	}
	if err := json.Unmarshal(raw, &selected); err != nil {
		t.Fatalf("разбор ответа select: %v", err)
	}
	e2eCheck(t, "select вернул ok", selected.OK, string(raw))
	e2eCheck(t, "select назвал выбранную модель", selected.Model == modelB, selected.Model)
	e2eCheck(t, "describe называет провайдер/модель",
		strings.Contains(selected.Describe, "ollama/"+modelB), selected.Describe)

	code, raw = e2eJSON(t, "GET", ts.URL+"/api/providers", nil, 20*time.Second)
	if err := json.Unmarshal(raw, &startup); err != nil {
		t.Fatalf("разбор GET после select: %v", err)
	}
	// Главный регресс: без инвалидации кэша GET показывал бы прежнюю модель.
	e2eCheck(t, "GET подтверждает переключение (регресс кэша провайдера)",
		startup.CurrentModel == modelB, startup.CurrentModel)
	e2eCheck(t, "override = true после выбора в UI", startup.Override, fmt.Sprint(startup.Override))

	// --- 3. реальный запрос чата --------------------------------------------
	dir := registerTestDir(t, srv, "e2e-model")
	writeTestFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	code, raw = e2eJSON(t, "POST", ts.URL+"/api/projects",
		map[string]any{"path_or_git": dir}, 30*time.Second)
	if code != http.StatusOK {
		t.Fatalf("POST /api/projects → %d: %s", code, raw)
	}
	var opened struct {
		ProjectName string `json:"project_name"`
	}
	if err := json.Unmarshal(raw, &opened); err != nil || opened.ProjectName == "" {
		t.Fatalf("открытие проекта: %s (%v)", raw, err)
	}
	project := opened.ProjectName

	code, raw = e2eJSON(t, "POST", ts.URL+"/api/projects/"+project+"/chat",
		map[string]any{"message": "Привет! Ответь строго одним словом."}, 30*time.Second)
	if code != http.StatusOK {
		t.Fatalf("POST chat → %d: %s", code, raw)
	}
	sess, _, err := srv.getOrCreate(project)
	if err != nil {
		t.Fatalf("сессия проекта: %v", err)
	}
	reply := waitChatRole(t, sess, chat.RoleAssistant, 4*time.Minute)
	t.Logf("ответ ассистента: %.120s", reply.Content)

	// Независимое доказательство: до запроса все модели были выгружены,
	// поэтому загружена ровно та, которую выбрал пользователь.
	loaded := e2eLoaded(t)
	e2eCheck(t, "Ollama грузил только выбранную модель",
		len(loaded) == 1 && loaded[0] == modelB, strings.Join(loaded, ","))

	// --- 4. логи сообщают, какая модель работает ------------------------------
	serverLog := readFileIfExists(t, filepath.Join(logDir, "server.log"))
	projectLog := readFileIfExists(t, filepath.Join(logDir, logging.ProjectLogName(project)+".log"))
	e2eCheck(t, "server.log: модель на старте",
		strings.Contains(serverLog, "server: LLM: ollama/"+modelA), tailOf(serverLog, 400))
	e2eCheck(t, "server.log: модель после переключения",
		strings.Contains(serverLog, "server: LLM: ollama/"+modelB), tailOf(serverLog, 400))
	e2eCheck(t, "лог проекта: [llm] с именем выбранной модели",
		strings.Contains(projectLog, "[llm]") && strings.Contains(projectLog, modelB),
		tailOf(projectLog, 400))

	// --- 5. невалидный выбор --------------------------------------------------
	code, _ = e2eJSON(t, "POST", ts.URL+"/api/providers/select",
		map[string]any{"provider": "ollama", "model": "gpt-4o"}, 30*time.Second)
	e2eCheck(t, "неизвестная модель → 400", code == http.StatusBadRequest, fmt.Sprint(code))
	code, _ = e2eJSON(t, "POST", ts.URL+"/api/providers/select",
		map[string]any{"provider": "openai", "model": modelA}, 30*time.Second)
	e2eCheck(t, "неизвестный провайдер → 400", code == http.StatusBadRequest, fmt.Sprint(code))
	code, raw = e2eJSON(t, "GET", ts.URL+"/api/providers", nil, 20*time.Second)
	if err := json.Unmarshal(raw, &startup); err != nil {
		t.Fatalf("разбор GET после отказа: %v", err)
	}
	e2eCheck(t, "после отказа выбор не изменился",
		startup.CurrentModel == modelB, startup.CurrentModel)

	// --- 6. крупная модель -----------------------------------------------------
	largeOK := large != ""
	if largeOK {
		code, raw = e2eJSON(t, "POST", ts.URL+"/api/providers/select",
			map[string]any{"provider": "ollama", "model": modelA, "large_model": large}, 60*time.Second)
		if code != http.StatusOK {
			t.Errorf("select с large_model → %d: %s", code, raw)
		} else {
			var withLarge struct {
				Describe string `json:"describe"`
			}
			_ = json.Unmarshal(raw, &withLarge)
			e2eCheck(t, "describe показывает слой large",
				strings.Contains(withLarge.Describe, "(large="+large+")"), withLarge.Describe)
		}
	} else {
		t.Log("E2E: третья модель не нашлась — крупный слой не проверялся")
	}

	// --- 7. сброс к выбору из окружения -----------------------------------------
	code, _ = e2eJSON(t, "POST", ts.URL+"/api/providers/select",
		map[string]any{"reset": true}, 60*time.Second)
	e2eCheck(t, "reset → 200", code == http.StatusOK, fmt.Sprint(code))
	code, raw = e2eJSON(t, "GET", ts.URL+"/api/providers", nil, 20*time.Second)
	if err := json.Unmarshal(raw, &startup); err != nil {
		t.Fatalf("разбор GET после сброса: %v", err)
	}
	e2eCheck(t, "после сброса override = false", !startup.Override, fmt.Sprint(startup.Override))
	e2eCheck(t, "после сброса вернулась модель из окружения",
		startup.CurrentModel == modelA, startup.CurrentModel)

	// --- 8. битый base_url виден сразу, а не на первом запросе к модели ----------
	broken := filepath.Join(filepath.Dir(cfgPath), "providers-broken.json")
	setE2EModels(t, broken, []string{modelA, modelB, large}, modelA, "")
	cfgBytes, err := os.ReadFile(broken)
	if err != nil {
		t.Fatal(err)
	}
	var brokenCfg map[string]any
	if err := json.Unmarshal(cfgBytes, &brokenCfg); err != nil {
		t.Fatal(err)
	}
	ollamaMap := brokenCfg["providers"].(map[string]any)["ollama"].(map[string]any)
	ollamaMap["base_url"] = "://не-url"
	writeE2EConfig(t, broken, brokenCfg)
	t.Setenv("PROVIDERS_CONFIG", broken)

	code, _ = e2eJSON(t, "POST", ts.URL+"/api/providers/select",
		map[string]any{"provider": "ollama", "model": modelA}, 60*time.Second)
	e2eCheck(t, "битый base_url → отказ сразу", code >= 400, fmt.Sprint(code))
	code, raw = e2eJSON(t, "GET", ts.URL+"/api/providers", nil, 20*time.Second)
	var withErr struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &withErr); err == nil {
		e2eCheck(t, "GET сообщает ошибку текущего выбора", withErr.Error != "", withErr.Error)
	}
}

// setE2EModels — записывает providers.json с двумя провайдерами (второй —
// бутафорский, нужен для проверки сортировки списка в GET); ollama-секции
// передаются models/default_model/large_model.
func setE2EModels(t *testing.T, path string, models []string, def, large string) {
	t.Helper()
	cfg := map[string]any{
		"providers": map[string]any{
			"reg": map[string]any{
				"base_url":      "https://ai.reg.cloud/v1",
				"api_key":       "unused",
				"models":        []string{"qwen-3.8-27b"},
				"default_model": "qwen-3.8-27b",
				"settings":      map[string]any{},
			},
			"ollama": map[string]any{
				"base_url":      e2eLLMBase(),
				"api_key":       "",
				"models":        cleanEmpty(models),
				"default_model": def,
				"large_model":   large,
				"settings":      map[string]any{"think_tokens": 0, "input_tokens": 32000, "output_tokens": 4096},
			},
		},
	}
	writeE2EConfig(t, path, cfg)
}

func cleanEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func readFileIfExists(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func containsAll(list []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, l := range list {
			if l == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
