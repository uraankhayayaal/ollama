package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ai/agents"
	"ai/board"
	"ai/runevents"
	"ai/runner"
	"ai/tokens"
)

// TestGetTokensStartsAtZero проверяет REST-эндпоинт счётчика токенов:
// новый проект возвращает нули.
func TestGetTokensStartsAtZero(t *testing.T) {
	_, handler, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/projects/tok-proj/tokens", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET tokens: %d, body: %s", rec.Code, rec.Body.String())
	}
	var ev tokenEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Input != 0 || ev.Output != 0 {
		t.Fatalf("tokens = %d/%d, want 0/0", ev.Input, ev.Output)
	}
}

// TestTokensAccumulatePerProject проверяет накопление токенов через сессию
// (addTokens — путь WS-событий) и чтение REST-эндпоинтом: суммы хранятся в
// Redis за время жизни проекта и не смешиваются между проектами.
func TestTokensAccumulatePerProject(t *testing.T) {
	srv, handler, _ := newTestServer(t)

	sess, _, err := srv.getOrCreate("tok-a")
	if err != nil {
		t.Fatalf("getOrCreate tok-a: %v", err)
	}
	_, _, err = srv.getOrCreate("tok-b")
	if err != nil {
		t.Fatalf("getOrCreate tok-b: %v", err)
	}

	sess.addTokens(100, 40, 12.5, "")
	sess.addTokens(50, 60, 0, "")

	// tok-a накопил 150/100.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/projects/tok-a/tokens", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET tokens tok-a: %d", rec.Code)
	}
	var ev tokenEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Input != 150 || ev.Output != 100 {
		t.Fatalf("tok-a tokens = %d/%d, want 150/100", ev.Input, ev.Output)
	}
	// Вторая порция не имела скорости генерации — сохраняется последняя (12.5).
	if ev.TPS != 12.5 {
		t.Fatalf("tok-a tps = %v, want 12.5", ev.TPS)
	}

	// tok-b остался пустым.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/projects/tok-b/tokens", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET tokens tok-b: %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Input != 0 || ev.Output != 0 {
		t.Fatalf("tok-b tokens = %d/%d, want 0/0", ev.Input, ev.Output)
	}
}

// TestTokensAccumulateByScope проверяет атрибуцию расхода по единицам работы
// (Ф-2): порция со scope'ом попадает и в общий счётчик проекта, и в счётчик
// scope'а (tokens:<project>:scoped:<scope>), откуда оркестратор берёт факт для
// задачи/эпика. Порция без scope остаётся только в счётчике проекта.
func TestTokensAccumulateByScope(t *testing.T) {
	srv, _, _ := newTestServer(t)
	sess, _, err := srv.getOrCreate("tok-scope")
	if err != nil {
		t.Fatalf("getOrCreate tok-scope: %v", err)
	}
	ctx := context.Background()

	sess.addTokens(100, 10, 0, tokens.ScopeTask("T-1"))
	sess.addTokens(40, 5, 0, tokens.ScopeTask("T-1"))
	sess.addTokens(7, 1, 0, tokens.ScopeEpic("epic-1"))
	// Неизвестный scope — тихо игнорируем (без мусора в Redis).
	sess.addTokens(3, 3, 0, "мусор")
	// Без scope — только проект.
	sess.addTokens(1, 1, 0, "")

	in, out, err := sess.tok.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if in != 151 || out != 20 {
		t.Fatalf("счётчик проекта = %d/%d, want 151/20", in, out)
	}

	in, out, err = sess.tok.GetScoped(ctx, tokens.ScopeTask("T-1"))
	if err != nil {
		t.Fatal(err)
	}
	if in != 140 || out != 15 {
		t.Fatalf("scope task:T-1 = %d/%d, want 140/15", in, out)
	}

	in, out, err = sess.tok.GetScoped(ctx, tokens.ScopeEpic("epic-1"))
	if err != nil {
		t.Fatal(err)
	}
	if in != 7 || out != 1 {
		t.Fatalf("scope epic:epic-1 = %d/%d, want 7/1", in, out)
	}

	// Сброс после фиксации итога в доске.
	if err := sess.tok.ResetScoped(ctx, tokens.ScopeTask("T-1")); err != nil {
		t.Fatal(err)
	}
	if in, out, err = sess.tok.GetScoped(ctx, tokens.ScopeTask("T-1")); err != nil {
		t.Fatal(err)
	}
	if in != 0 || out != 0 {
		t.Fatalf("scope task:T-1 после ResetScoped = %d/%d, want 0/0", in, out)
	}
}

// wiredReporterProvider — тестовый LLM-провайдер: проверяет, что сессия
// внедрила репортёр в контекст исполнения (иначе раунд не будет считать
// токены), эмитит OnTokens и тут же завершает оркестрацию ошибкой, чтобы
// Kanban-раннер не ушёл в бесконечный цикл.
type wiredReporterProvider struct{ t *testing.T }

func (p *wiredReporterProvider) Generate(ctx context.Context, _ agents.Agent) (*runner.AgentResponse, error) {
	rep := runevents.ReporterFromContext(ctx)
	if rep == nil {
		p.t.Fatal("сессия не внедрила репортёр в контекст провайдера — токены не будут считаться")
	}
	rep.OnTokens(7, 3, 0)
	return nil, errors.New("стоп: тестовый провайдер")
}

// TestReporterWiredIntoSession проверяет, что sess.start внедряет репортёр
// в контекст оркестрации: эмитированный раундом вход/выход токенов накапливается
// в Redis-счётчике проекта (это и есть живое обновление счётчика в Web UI по
// WS type=tokens). Раньше репортёр нигде в контекст не встраивался — токены
// всегда оставались нулевыми.
func TestReporterWiredIntoSession(t *testing.T) {
	srv, _, _ := newTestServer(t)
	sess, _, err := srv.getOrCreate("tok-wired")
	if err != nil {
		t.Fatalf("getOrCreate tok-wired: %v", err)
	}

	// Доска с эпиком и незакрытым багрепортом: задача не «решена» (AllDone
	// false), раннер в board-only доходит до фазы триажа багрепортов и
	// вызывает провайдера хотя бы раз.
	ctx := context.Background()
	if err := sess.board.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "эпик", Description: "описание"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := sess.board.CreateBugReport(ctx, &board.BugReport{
		BugID: "bug-1", Title: "баг", Description: "описание", EpicID: "epic-1",
	}); err != nil {
		t.Fatal(err)
	}

	if err := sess.start(ctx, "задача", &wiredReporterProvider{t: t}, true); err != nil {
		t.Fatal(err)
	}

	// Ждём завершения оркестрации (тестовый провайдер останавливает её ошибкой).
	deadline := time.Now().Add(5 * time.Second)
	for {
		sess.mu.Lock()
		running := sess.running
		sess.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("оркестрация не завершилась за 5 с")
		}
		time.Sleep(10 * time.Millisecond)
	}

	in, out, err := sess.tok.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if in != 7 || out != 3 {
		t.Fatalf("tokens = %d/%d, want 7/3", in, out)
	}
}
