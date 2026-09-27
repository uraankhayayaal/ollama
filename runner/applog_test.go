package runner

// Тесты подмешивания логов рантайма в цикл (Ф-2).
//
// Hermetic: провайдер-заглушка отдаёт заранее заданные ответы модели, а агент
// реализует RuntimeLogger очередью строк. Проверяем ровно то, что влияет на
// поведение: когда лог подмешивается, когда нет, и что подмешивается один раз.

import (
	"strings"
	"testing"

	"ai/tools"
)

// fakeRuntimeAgent — агент с накопленными логами рантайма. Встраивает
// fakeAgent из runner_test.go: он уже реализует agents.Agent целиком.
type fakeRuntimeAgent struct {
	fakeAgent
	pending []string
	source  string
}

func (f *fakeRuntimeAgent) TakeAppLogTail() (string, []string) {
	if len(f.pending) == 0 {
		return "", nil
	}
	lines := f.pending
	f.pending = nil
	return f.source, lines
}

func TestAppLogFeedMessageMentionsRuntimeAndNotError(t *testing.T) {
	msg := appLogFeedMessage("local", []string{"panic: boom"}, 1, 2)
	// Подсказка не должна читаться как «твой код сломан» — иначе модель
	// начнёт чинить то, что не сломано. И обязана содержать сами строки.
	if !strings.Contains(msg, "panic: boom") {
		t.Fatalf("в подсказке нет самих строк лога:\n%s", msg)
	}
	if !strings.Contains(msg, "не сообщение об ошибке твоей работы") {
		t.Fatalf("подсказка должна отличать лог рантайма от ошибки агента:\n%s", msg)
	}
	if !strings.Contains(msg, "local") {
		t.Fatalf("в подсказке нет источника:\n%s", msg)
	}
	if !strings.Contains(msg, "1 из 2") {
		t.Fatalf("в подсказке нет счётчика итераций:\n%s", msg)
	}
}

func TestTrimAppLogLinesKeepsTailAndCapsChars(t *testing.T) {
	// Много строк — берём последние: причина падения в конце.
	many := make([]string, 0, appLogFeedMaxLines+20)
	for i := 0; i < appLogFeedMaxLines+20; i++ {
		many = append(many, "line")
	}
	got := trimAppLogLines(many)
	if len(got) != appLogFeedMaxLines {
		t.Fatalf("строк: %d, ждём %d", len(got), appLogFeedMaxLines)
	}
	// Одна гигантская строка (трейс с locals) не должна утянуть за собой весь
	// бюджет: остаётся только она.
	huge := strings.Repeat("x", appLogFeedMaxChars*2)
	got = trimAppLogLines([]string{"short", huge})
	if len(got) != 1 || got[0] != huge {
		t.Fatalf("обрезка по символам не сработала: %d строк", len(got))
	}
}

func TestAppLogFeedRespectsMaxRounds(t *testing.T) {
	t.Setenv(appLogMaxFeedRoundsEnv, "1")
	if n := appLogMaxFeedRounds(); n != 1 {
		t.Fatalf("лимит: %d, ждём 1", n)
	}
	// Мусор в переменной — дефолт, а не «ноль подмешиваний».
	t.Setenv(appLogMaxFeedRoundsEnv, "мусор")
	if n := appLogMaxFeedRounds(); n != defaultAppLogMaxFeedRounds {
		t.Fatalf("лимит для некорректного значения: %d, ждём дефолт %d", n, defaultAppLogMaxFeedRounds)
	}
	t.Setenv(appLogMaxFeedRoundsEnv, "0")
	if n := appLogMaxFeedRounds(); n != defaultAppLogMaxFeedRounds {
		t.Fatalf("лимит для нуля: %d, ждём дефолт %d", n, defaultAppLogMaxFeedRounds)
	}
}

func TestAppLogFeedToggle(t *testing.T) {
	if !appLogFeedEnabled() {
		t.Fatalf("по умолчанию подмешивание должно быть включено")
	}
	for _, off := range []string{"0", "false", "off", "NO"} {
		t.Setenv(appLogFeedOnEnv, off)
		if appLogFeedEnabled() {
			t.Fatalf("%s должно выключать подмешивание", off)
		}
	}
	t.Setenv(appLogFeedOnEnv, "  1 ")
	if !appLogFeedEnabled() {
		t.Fatalf("явное включение должно работать")
	}
}

// TestRuntimeAgentSatisfiesInterface — контракт с инструментом: *tools.FileOps
// реализует TakeAppLogTail, и агент-разработчик получает интерфейс через
// внедрённый FileOps. Здесь проверяем сам интерфейс на фейке, чтобы падение
// сигнатуры не дошло до рабочего кода.
func TestRuntimeAgentSatisfiesInterface(t *testing.T) {
	var rl RuntimeLogger = &fakeRuntimeAgent{source: "local", pending: []string{"a"}}
	src, lines := rl.TakeAppLogTail()
	if src != "local" || len(lines) != 1 {
		t.Fatalf("интерфейс вернул %q %+v", src, lines)
	}
	if _, again := rl.TakeAppLogTail(); len(again) != 0 {
		t.Fatalf("хвост должен забираться один раз, второй вернул %+v", again)
	}
}

// TestAppLogFeedGoesToModelAsUserMessage — сквозной сценарий цикла: в раунде
// модель зовет инструмент, инструмент оставляет строки в хвосте, и раннер
// подмешивает их СЛЕДУЮЩИМ раундом отдельным user-сообщением, которое модель
// реально видит в своей истории.
func TestAppLogFeedGoesToModelAsUserMessage(t *testing.T) {
	t.Setenv(appLogFeedOnEnv, "1")
	t.Setenv(appLogMaxFeedRoundsEnv, "2")

	agent := &fakeRuntimeAgent{pending: []string{"listen tcp :8080: bind: address already in use"}}
	// Модель: раунд 1 — вызов инструмента, раунд 2 — финальный ответ.
	provider := &fakeChatProvider{replies: []*ModelReply{
		{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: "{}"}}},
		{Content: "готово"},
	}}
	resp := testGenerate(t, agent, provider)

	if resp == nil || len(resp.Messages) == 0 {
		t.Fatalf("нет истории диалога: %+v", resp)
	}
	found := false
	for _, m := range resp.Messages {
		if strings.Contains(m.Content, "address already in use") {
			found = true
			if m.Role != "user" {
				t.Fatalf("лог подмешан не как user-сообщение: role=%q", m.Role)
			}
		}
	}
	if !found {
		t.Fatalf("строка лога рантайма не попала в историю модели: %d сообщений", len(resp.Messages))
	}
}
