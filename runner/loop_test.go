package runner

import (
	"ai/tools"
	"context"
	"strings"
	"testing"
)

// Вызов инструмента для детектора: успешный по умолчанию.
func okCall(name, args string) loopRoundCall {
	return loopRoundCall{name: name, sig: callSignature(name, map[string]any{"k": args}), result: "ok"}
}

func failCall(name, args string) loopRoundCall {
	return loopRoundCall{name: name, sig: callSignature(name, map[string]any{"k": args}), failed: true, result: `{"status":"error","message":"boom"}`}
}

// Один и тот же вызов (имя+аргументы) три раза подряд — петля, даже если
// инструмент отвечает успехом: результат не меняется, работа не идёт.
func TestLoopDetectorDetectsRepeatedSignature(t *testing.T) {
	d := newLoopDetector()
	c := okCall("Run", "npm test")

	if reason := d.observeRound([]loopRoundCall{c}); reason != "" {
		t.Fatalf("один успешный вызов не должен считаться петлёй, got %q", reason)
	}
	if reason := d.observeRound([]loopRoundCall{c}); reason != "" {
		t.Fatalf("второй вызов — ещё не петля, got %q", reason)
	}
	reason := d.observeRound([]loopRoundCall{c})
	if reason == "" {
		t.Fatal("третий одинаковый вызов должен подтвердить петлю")
	}
	if !strings.Contains(reason, "Run") || !strings.Contains(reason, "3") {
		t.Fatalf("причина должна называть инструмент и число повторов, got %q", reason)
	}
}

// Разные аргументы одного инструмента, которые всегда падают: сигнатуры
// уникальны (повтора нет), но раундов без единого успеха достаточно.
func TestLoopDetectorDetectsFailStreak(t *testing.T) {
	d := newLoopDetector()
	var reason string
	for i := 1; i <= loopFailStreakDefault; i++ {
		reason = d.observeRound([]loopRoundCall{failCall("BoardCreateEpic", string(rune('A'+i)))})
		if reason != "" && i < loopFailStreakDefault {
			t.Fatalf("петля подтверждена слишком рано: раунд %d, %q", i, reason)
		}
	}
	if reason == "" {
		t.Fatal("серия раундов без успеха должна подтвердить петлю")
	}
	if !strings.Contains(reason, "успешного") {
		t.Fatalf("причина должна говорить про отсутствие успеха, got %q", reason)
	}
}

// Только чтения при задаче, где надо писать: даже без повторов долгое чтение
// означает, что агент не начал работу.
func TestLoopDetectorDetectsReadOnlyRounds(t *testing.T) {
	d := newLoopDetector()
	var reason string
	for i := 0; i < loopNoWriteDefault; i++ {
		reason = d.observeRound([]loopRoundCall{okCall("ReadFiles", string(rune('a'+i%20)))})
	}
	if reason == "" || !strings.Contains(reason, "чтением") {
		t.Fatalf("ожидали вердикт про одни чтения, got %q", reason)
	}
}

// Мутирующий вызов обнуляет счётчик «только чтение»: нормальная работа
// разработчика (прочитал → записал) петлёй не считается.
func TestLoopDetectorMutationResetsReadOnly(t *testing.T) {
	d := newLoopDetector()
	for i := 0; i < loopNoWriteDefault-1; i++ {
		if reason := d.observeRound([]loopRoundCall{okCall("ReadFiles", string(rune('a'+i%20)))}); reason != "" {
			t.Fatalf("чтение с периодической записью не должно считаться петлёй (раунд %d): %q", i+1, reason)
		}
		d.observeRound([]loopRoundCall{okCall("WriteFiles", string(rune('a'+i%20)))})
	}
	if d.noWrite != 0 {
		t.Fatalf("после мутации счётчик чтений должен быть обнулён, got %d", d.noWrite)
	}
}

// Полная картина петли: повторяющийся вызов в дайджесте, запрет под запрет
// и перевзведённые счётчики (иначе первая же попытка повторить запрещённое
// тут же объявила бы новую петлю).
func TestLoopBreakLoopBansAndResetsCounters(t *testing.T) {
	d := newLoopDetector()
	c := okCall("Run", "npm test")
	for i := 0; i < loopSigRepeatsDefault; i++ {
		d.observeRound([]loopRoundCall{c})
	}
	msg, taken := d.breakLoop("проверка", "")
	if taken != 1 {
		t.Fatalf("под запрет должен попасть один вызов, got %d", taken)
	}
	if !strings.Contains(msg, "Run") || !strings.Contains(msg, "npm test") {
		t.Fatalf("дайджест должен перечислять запрещённый вызов, got:\n%s", msg)
	}
	if !strings.Contains(msg, "С ДРУГОЙ СТОРОНЫ") {
		t.Fatalf("дайджест должен требовать идти с другой стороны, got:\n%s", msg)
	}
	if !d.bannedSignature(c.sig, c.name) {
		t.Fatal("повторённый вызов должен быть запрещён")
	}
	if len(d.sigRound) != 0 || d.failStreak != 0 || d.noWrite != 0 {
		t.Fatalf("счётчики должны быть перевзведены, got %#v", d)
	}
	// Счётчик «за весь цикл» переживает разрыв: дайджест показывает модели
	// полную картину попыток.
	if d.sigTotal[c.sig] != loopSigRepeatsDefault {
		t.Fatalf("общий счётчик попыток должен сохраниться, got %d", d.sigTotal[c.sig])
	}
}

// Петля перебора аргументов: повторов по сигнатуре нет, поэтому под запрет
// попадает сам инструмент (иначе модель продолжит перебор).
func TestLoopBreakLoopBansToolWhenArgsVary(t *testing.T) {
	d := newLoopDetector()
	for i := 0; i < loopFailStreakDefault; i++ {
		d.observeRound([]loopRoundCall{failCall("BoardCreateEpic", string(rune('A'+i)))})
	}
	msg, taken := d.breakLoop("проверка", "")
	if taken != 1 {
		t.Fatalf("под запрет должен попасть один инструмент, got %d", taken)
	}
	if !d.bannedSignature("любая сигнатура", "BoardCreateEpic") {
		t.Fatal("инструмент, который ни разу не сработал, должен быть запрещён целиком")
	}
	if !strings.Contains(msg, "BoardCreateEpic") || !strings.Contains(msg, "уже существует") && !strings.Contains(msg, "boom") {
		t.Fatalf("дайджест должен называть инструмент и его ошибку, got:\n%s", msg)
	}
}

// Инструмент, который иногда срабатывает, под тотальный запрет не попадает:
// его успешный вызов — это прогресс.
func TestLoopBreakLoopKeepsPartiallyWorkingTool(t *testing.T) {
	d := newLoopDetector()
	d.observeRound([]loopRoundCall{okCall("WriteFiles", "a")})
	for i := 0; i < loopFailStreakDefault; i++ {
		d.observeRound([]loopRoundCall{failCall("WriteFiles", string(rune('b'+i)))})
	}
	if d.canBreak() != true {
		t.Fatal("разрыв ещё доступен")
	}
	d.breakLoop("проверка", "")
	if d.bannedSignature("любая сигнатура", "WriteFiles") {
		t.Fatal("инструмент с успешным вызовом нельзя запрещать целиком")
	}
}

// Дайджест напоминает о невыполненном обязательном действии: без этого разрыв
// ломает лида, который так и не опубликовал задачи.
func TestLoopBreakMessageKeepsRequiredAction(t *testing.T) {
	d := newLoopDetector()
	c := okCall("List", "{}")
	for i := 0; i < loopSigRepeatsDefault; i++ {
		d.observeRound([]loopRoundCall{c})
	}
	msg, _ := d.breakLoop("проверка", "BoardCreateTask")
	if !strings.Contains(msg, "BoardCreateTask") {
		t.Fatalf("дайджест должен напомнить обязательное действие, got:\n%s", msg)
	}
}

// Модель повторяет один и тот же вызов: вместо сжигания всех раундов раннер
// разрывает петлю (дайджест + запрет) и даёт шанс ответить с другой стороны.
func TestGenerateBreaksLoopAndModelChangesApproach(t *testing.T) {
	agent := &fakeAgent{}
	provider := &fakeChatProvider{
		replies: []*ModelReply{
			// раунды 1-3: один и тот же вызов — детектор подтверждает петлю
			{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: `{"files":[{"filename":"a.go"}]}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: `{"files":[{"filename":"a.go"}]}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: `{"files":[{"filename":"a.go"}]}`}}, FinishReason: "tool_calls"},
			// раунд 4: после разрыва модель пошла другой дорогой
			{ToolCalls: []tools.ToolCall{{Name: "ReadFiles", Arguments: `{"filenames":["a.go"]}`}}, FinishReason: "tool_calls"},
			// раунд 5: итог
			{Content: "сделал иначе", FinishReason: "stop"},
		},
	}

	resp := testGenerate(t, agent, provider)

	if resp.Looped {
		t.Fatalf("цикл не должен прерываться: модель сменила подход, %s", resp.LoopReason)
	}
	if resp.Content != "сделал иначе" {
		t.Fatalf("ожидали финальный ответ после разрыва петли, got %q", resp.Content)
	}
	if provider.calls != 5 {
		t.Fatalf("ожидали 5 запросов (3 повтора + новый подход + итог), got %d", provider.calls)
	}

	// В историю ушёл разрыв с запретом повторяемого вызова.
	digest := findDigest(resp.Messages)
	if digest == "" {
		t.Fatalf("в истории нет дайджеста разрыва: %#v", resp.Messages)
	}
	for _, want := range []string{"СТОП", "WriteFiles", "С ДРУГОЙ СТОРОНЫ"} {
		if !strings.Contains(digest, want) {
			t.Fatalf("дайджест должен содержать %q, got:\n%s", want, digest)
		}
	}
	// Зацикленные раунды схлопнуты: в истории не осталось повторов
	// (после системного/пользовательского сообщения идёт разрыв и новый подход).
	if repeats := strings.Count(digest, "WriteFiles"); repeats == 0 {
		t.Fatalf("дайджест должен называть повторявшийся инструмент:\n%s", digest)
	}
	last := resp.Messages[len(resp.Messages)-1]
	if last.Role != "assistant" || last.Content != "сделал иначе" {
		t.Fatalf("история должна заканчиваться финальным ответом, got %#v", last)
	}
}

// Из аргументов вызова собираются только пути: содержимое файла и команды
// («перечитать» их нельзя) в список для перечитывания не попадают.
func TestLoopCallPaths(t *testing.T) {
	cases := []struct {
		name string
		args any
		want []string
	}{
		{"ReadFiles", map[string]any{"paths": []any{"server/api.go", "tools/loop.go"}}, []string{"server/api.go", "tools/loop.go"}},
		{"WriteFiles", map[string]any{"files": []any{
			map[string]any{"filename": "server/api.go", "content": "package main"},
		}}, []string{"server/api.go"}},
		{"ReadMap", map[string]any{"path": "server", "maxlines": float64(200)}, []string{"server"}},
		{"Run", map[string]any{"command": "go build ./server/ && cat Makefile > notes.txt"}, nil},
	}
	for _, tc := range cases {
		got := loopCallPaths(tc.args)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: loopCallPaths = %v, ожидалось %v", tc.name, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: путь %d = %q, ожидалось %q", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

// Указание начать заново: постановка задачи перепечатывается последней, уже
// тронутые файлы перечисляются для перечитывания.
func TestLoopRestartMessage(t *testing.T) {
	msg := loopRestartMessage("Почини хендлер входа", []string{"server/api.go", "server/auth.go"}, "submit_architecture_backlog")

	for _, want := range []string{
		"НАЧНИ ЗАДАЧУ ЗАНОВО",
		"ПОСТАНОВКА ЗАДАЧИ",
		"Почини хендлер входа",
		"server/api.go",
		"server/auth.go",
		"ПЕРВЫЙ ДЕЙСТВИЕ РАУНДА",
		"submit_architecture_backlog",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("сообщение о перезапуске должно содержать %q, got:\n%s", want, msg)
		}
	}

	// Без списка файлов подсказка остаётся рабочей: перечитывать нечего.
	empty := loopRestartMessage("Задача", nil, "")
	if !strings.Contains(empty, "List/ReadMap") {
		t.Fatalf("без файлов нужно предложить сориентироваться в проекте, got:\n%s", empty)
	}
}

// После разрыва петли модель получает постановку задачи заново и список уже
// тронутых файлов — и действительно начинает с перечитывания, а не продолжает
// «по памяти».
func TestGenerateLoopRestartRepeatsTaskAndTouchedFiles(t *testing.T) {
	agent := &fakeAgent{} // постановка задачи в user-сообщении: «напиши код»
	provider := &fakeChatProvider{
		replies: []*ModelReply{
			// раунды 1-3: пишет один и тот же файл — детектор подтверждает петлю
			{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: `{"files":[{"filename":"server/api.go","content":"v1"}]}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: `{"files":[{"filename":"server/api.go","content":"v1"}]}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: `{"files":[{"filename":"server/api.go","content":"v1"}]}`}}, FinishReason: "tool_calls"},
			// раунд 4: модель перечитала файл
			{ToolCalls: []tools.ToolCall{{Name: "ReadFiles", Arguments: `{"paths":["server/api.go"]}`}}, FinishReason: "tool_calls"},
			{Content: "починил иначе", FinishReason: "stop"},
		},
	}

	resp := testGenerate(t, agent, provider)
	if resp.Looped {
		t.Fatalf("цикл не должен прерываться: %s", resp.LoopReason)
	}

	digest := findDigest(resp.Messages)
	if digest == "" {
		t.Fatalf("в истории нет разрыва петли: %#v", resp.Messages)
	}
	for _, want := range []string{"НАЧНИ ЗАДАЧУ ЗАНОВО", "напиши код", "server/api.go"} {
		if !strings.Contains(digest, want) {
			t.Fatalf("разрыв должен содержать %q, got:\n%s", want, digest)
		}
	}

	// Перечитывание пришло в том же цикле: после разрыва был вызов чтения.
	if !strings.Contains(digest, "ПЕРВЫЙ ДЕЙСТВИЕ РАУНДА") {
		t.Fatalf("разрыв должен требовать начать с перечитывания, got:\n%s", digest)
	}
	// Вызовы после разрыва: первые три — зацикленные правки, дальше перечитывание.
	var names []string
	for _, tc := range resp.ToolCalls {
		names = append(names, tc.Name)
	}
	if len(names) < 4 || names[3] != "ReadFiles" {
		t.Fatalf("после разрыва модель должна была перечитать файлы, вызовы: %v", names)
	}
}

// Модель не поняла предупреждение и повторяет запрещённый вызов: цикл
// прерывается приговором Looped, бюджет раундов не выжигается.
func TestGenerateReportsLoopedAfterBanIgnored(t *testing.T) {
	agent := &fakeAgent{}
	call := func() *ModelReply {
		return &ModelReply{
			FinishReason: "tool_calls",
			ToolCalls:    []tools.ToolCall{{Name: "WriteFiles", Arguments: `{"files":[{"filename":"a.go"}]}`}},
		}
	}
	provider := &fakeChatProvider{replies: []*ModelReply{call()}}

	resp := testGenerate(t, agent, provider)

	if !resp.Looped {
		t.Fatalf("цикл должен быть прерван как зацикливание, Truncated=%v", resp.Truncated)
	}
	if resp.LoopReason == "" {
		t.Fatal("в приговоре должна быть причина зацикливания")
	}
	if !strings.Contains(resp.LoopReason, "WriteFiles") {
		t.Fatalf("причина должна называть повторённый вызов, got %q", resp.LoopReason)
	}
	if resp.Truncated {
		t.Fatal("петля не должна выглядеть как исчерпание лимита раундов")
	}
	if provider.calls > maxRepeatedToolCalls*2 {
		t.Fatalf("петля сожгла слишком много раундов: %d", provider.calls)
	}
}

// Петля из одних ошибок (архитектор пересоздаёт существующие эпики) тоже
// приводит к разрыву: дайджест перечисляет инструмент и его ошибку.
func TestGenerateBreaksLoopOnRepeatedToolErrors(t *testing.T) {
	agent := &fakeAgent{
		requiredGroups: [][]string{{"submit_architecture_backlog"}},
		callResults: [][]byte{
			[]byte(`{"status":"error","message":"эпик ARCH-01 уже существует"}`),
			[]byte(`{"status":"error","message":"эпик ARCH-02 уже существует"}`),
			[]byte(`{"status":"error","message":"эпик ARCH-03 уже существует"}`),
			[]byte(`{"status":"error","message":"эпик ARCH-04 уже существует"}`),
			[]byte(`{"status":"error","message":"эпик ARCH-05 уже существует"}`),
		},
	}
	// Разные task_id: повторов по сигнатуре нет — запрещается инструмент.
	provider := &fakeChatProvider{
		replies: []*ModelReply{
			{ToolCalls: []tools.ToolCall{{Name: "BoardCreateEpic", Arguments: `{"task_id":"ARCH-01"}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "BoardCreateEpic", Arguments: `{"task_id":"ARCH-02"}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "BoardCreateEpic", Arguments: `{"task_id":"ARCH-03"}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "BoardCreateEpic", Arguments: `{"task_id":"ARCH-04"}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "BoardCreateEpic", Arguments: `{"task_id":"ARCH-05"}`}}, FinishReason: "tool_calls"},
			// после разрыва модель публикует бэклог
			{ToolCalls: []tools.ToolCall{{Name: "submit_architecture_backlog", Arguments: `{}`}}, FinishReason: "tool_calls"},
			{Content: "бэклог опубликован", FinishReason: "stop"},
		},
	}

	resp := testGenerate(t, agent, provider)

	if resp.Looped {
		t.Fatalf("цикл не должен прерываться: после разрыва модель выполнила обязательное действие (%s)", resp.LoopReason)
	}
	digest := findDigest(resp.Messages)
	if digest == "" {
		t.Fatalf("в истории нет дайджеста разрыва: %#v", resp.Messages)
	}
	if !strings.Contains(digest, "BoardCreateEpic") {
		t.Fatalf("дайджест должен называть инструмент в петле, got:\n%s", digest)
	}
	if !strings.Contains(digest, "submit_architecture_backlog") {
		t.Fatalf("дайджест должен напомнить обязательное действие, got:\n%s", digest)
	}
}

// Разрыв не теряет постановку задачи: без неё модель начнёт «с другой
// стороны» от неизвестно чего.
func TestGenerateBreakKeepsTaskStatement(t *testing.T) {
	agent := &fakeAgent{}
	call := &ModelReply{
		FinishReason: "tool_calls",
		ToolCalls:    []tools.ToolCall{{Name: "WriteFiles", Arguments: `{"files":[{"filename":"a.go"}]}`}},
	}
	provider := &fakeChatProvider{replies: []*ModelReply{call, call, call, call, call, call}}

	resp := testGenerate(t, agent, provider)

	if len(resp.Messages) < 2 {
		t.Fatalf("история слишком короткая: %#v", resp.Messages)
	}
	if resp.Messages[0].Role != "system" || resp.Messages[0].Content != "ты агент" {
		t.Fatalf("системный промпт должен сохраниться, got %#v", resp.Messages[0])
	}
	if resp.Messages[1].Role != "user" || resp.Messages[1].Content != "напиши код" {
		t.Fatalf("постановка задачи должна сохраниться, got %#v", resp.Messages[1])
	}
}

// Пороги детектора настраиваются окружением: жёсткие значения по умолчанию
// можно ослабить для длинных задач (агент-исследователь).
func TestLoopConfigFromEnv(t *testing.T) {
	cfg := loopConfigFromEnv()
	if cfg.sigRepeats != loopSigRepeatsDefault || cfg.failStreak != loopFailStreakDefault {
		t.Fatalf("пороги по умолчанию неверны: %#v", cfg)
	}
	t.Setenv("LOOP_SIG_REPEATS", "5")
	t.Setenv("LOOP_MAX_BREAKS", "1")
	t.Setenv("LOOP_FAIL_STREAK", "не число")
	cfg = loopConfigFromEnv()
	if cfg.sigRepeats != 5 || cfg.breaks != 1 {
		t.Fatalf("переопределение окружением не применилось: %#v", cfg)
	}
	if cfg.failStreak != loopFailStreakDefault {
		t.Fatalf("битое значение должно оставаться дефолтом: %#v", cfg)
	}
}

// Схлопывание истории: хвост после последней точки прогресса убирается,
// постановка задачи остаётся.
func TestLoopCollapsedMessages(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: "пробую"},
		{Role: "tool", Content: "ошибка"},
		{Role: "assistant", Content: "ещё раз"},
		{Role: "tool", Content: "ошибка"},
	}
	got := loopCollapsedMessages(msgs, 4, 2)
	if len(got) != 4 {
		t.Fatalf("ожидали 4 сообщения (схлопнуто 2), got %d: %#v", len(got), got)
	}
	if got[1].Content != "task" {
		t.Fatalf("постановка задачи потеряна: %#v", got)
	}
	// Точка прогресса правее хвоста — ничего не схлопывается.
	if got := loopCollapsedMessages(msgs, 6, 2); len(got) != len(msgs) {
		t.Fatalf("схлопывать нечего, got %d", len(got))
	}
	// Точка прогресса левее постановки — постановка не теряется.
	if got := loopCollapsedMessages(msgs, 0, 2); len(got) != 2 {
		t.Fatalf("ожидали 2 сообщения (постановка), got %d", len(got))
	}
}

// findDigest возвращает текст дайджеста разрыва петли из истории диалога.
func findDigest(messages []Message) string {
	for _, m := range messages {
		if m.Role == "user" && strings.Contains(m.Content, "СТОП: ты попал в повторяющийся цикл") {
			return m.Content
		}
	}
	return ""
}

// StopReason/LoopError — единая точка правды для вызывающего кода: зацикливание
// и лимит раундов различаются, но оба означают «задача не выполнена».
func TestAgentResponseStopReasonAndLoopError(t *testing.T) {
	if got := (*AgentResponse)(nil).StopReason(); got != "" {
		t.Fatalf("nil-ответ не остановлен, got %q", got)
	}
	if err := (*AgentResponse)(nil).LoopError("фаза"); err != nil {
		t.Fatalf("nil-ответ без петли не даёт ошибки, got %v", err)
	}

	looped := &AgentResponse{Looped: true, LoopReason: "повтор Run"}
	if got := looped.StopReason(); got != "зацикливание" {
		t.Fatalf("StopReason = %q", got)
	}
	err := looped.LoopError("фаза архитектора")
	if err == nil {
		t.Fatal("зацикливание должно давать ошибку вызывающему коду")
	}
	if !strings.Contains(err.Error(), "фаза архитектора") || !strings.Contains(err.Error(), "повтор Run") {
		t.Fatalf("ошибка должна нести контекст и причину, got %v", err)
	}

	trunc := &AgentResponse{Truncated: true}
	if got := trunc.StopReason(); got != "лимит раундов" {
		t.Fatalf("StopReason = %q", got)
	}
	if err := trunc.LoopError("фаза"); err != nil {
		t.Fatalf("лимит раундов — не петля, ошибки быть не должно: %v", err)
	}
	ok := &AgentResponse{Content: "готово"}
	if got := ok.StopReason(); got != "" {
		t.Fatalf("успешный ответ не остановлен, got %q", got)
	}
}

// WithoutResumeState: задача, взятая «с другой стороны», не продолжает
// застрявший диалог — цикл стартует заново.
func TestWithoutResumeState(t *testing.T) {
	ctx := WithResumeState(context.Background(), &ResumeState{Rounds: 42, Messages: []Message{{Role: "system", Content: "s"}}})
	if st := ResumeStateFromContext(ctx); st == nil || st.Rounds != 42 {
		t.Fatalf("resume-состояние должно читаться, got %#v", st)
	}
	if st := ResumeStateFromContext(WithoutResumeState(ctx)); st != nil {
		t.Fatalf("после сброса resume-состояния быть не должно, got %#v", st)
	}
	if st := ResumeStateFromContext(context.Background()); st != nil {
		t.Fatalf("без resume-состояния nil, got %#v", st)
	}
}
