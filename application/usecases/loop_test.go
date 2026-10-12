package usecases

import (
	"ai/domain"
	"strings"
	"testing"
)

// Вызов инструмента для детектора: успешный по умолчанию.
func okCall(name, args string) LoopRoundCall {
	return LoopRoundCall{Name: name, Sig: CallSignature(name, map[string]any{"k": args}), Result: "ok"}
}

func failCall(name, args string) LoopRoundCall {
	return LoopRoundCall{Name: name, Sig: CallSignature(name, map[string]any{"k": args}), Failed: true, Result: `{"status":"error","message":"boom"}`}
}

// Один и тот же вызов (имя+аргументы) три раза подряд — петля, даже если
// инструмент отвечает успехом: результат не меняется, работа не идёт.
func TestLoopDetectorDetectsRepeatedSignature(t *testing.T) {
	d := NewLoopDetector(nil)
	c := okCall("Run", "npm test")

	if reason := d.ObserveRound([]LoopRoundCall{c}); reason != "" {
		t.Fatalf("один успешный вызов не должен считаться петлёй, got %q", reason)
	}
	if reason := d.ObserveRound([]LoopRoundCall{c}); reason != "" {
		t.Fatalf("второй вызов — ещё не петля, got %q", reason)
	}
	reason := d.ObserveRound([]LoopRoundCall{c})
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
	d := NewLoopDetector(nil)
	var reason string
	for i := 1; i <= loopFailStreakDefault; i++ {
		reason = d.ObserveRound([]LoopRoundCall{failCall("BoardCreateEpic", string(rune('A'+i)))})
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
	d := NewLoopDetector(func(name string) bool { return name == "ReadFiles" })
	var reason string
	for i := 0; i < loopNoWriteDefault; i++ {
		reason = d.ObserveRound([]LoopRoundCall{okCall("ReadFiles", string(rune('a'+i%20)))})
	}
	if reason == "" || !strings.Contains(reason, "чтением") {
		t.Fatalf("ожидали вердикт про одни чтения, got %q", reason)
	}
}

// Мутирующий вызов обнуляет счётчик «только чтение»: нормальная работа
// разработчика (прочитал → записал) петлёй не считается.
func TestLoopDetectorMutationResetsReadOnly(t *testing.T) {
	d := NewLoopDetector(func(name string) bool { return name == "ReadFiles" })
	for i := 0; i < loopNoWriteDefault-1; i++ {
		if reason := d.ObserveRound([]LoopRoundCall{okCall("ReadFiles", string(rune('a'+i%20)))}); reason != "" {
			t.Fatalf("чтение с периодической записью не должно считаться петлёй (раунд %d): %q", i+1, reason)
		}
		d.ObserveRound([]LoopRoundCall{okCall("WriteFiles", string(rune('a'+i%20)))})
	}
	if d.noWrite != 0 {
		t.Fatalf("после мутации счётчик чтений должен быть обнулён, got %d", d.noWrite)
	}
}

// Полная картина петли: повторяющийся вызов в дайджесте, запрет под запрет
// и перевзведённые счётчики (иначе первая же попытка повторить запрещённое
// тут же объявила бы новую петлю).
func TestLoopBreakLoopBansAndResetsCounters(t *testing.T) {
	d := NewLoopDetector(nil)
	c := okCall("Run", "npm test")
	for i := 0; i < loopSigRepeatsDefault; i++ {
		d.ObserveRound([]LoopRoundCall{c})
	}
	msg, taken := d.BreakLoop("проверка", "")
	if taken != 1 {
		t.Fatalf("под запрет должен попасть один вызов, got %d", taken)
	}
	if !strings.Contains(msg, "Run") || !strings.Contains(msg, "npm test") {
		t.Fatalf("дайджест должен перечислять запрещённый вызов, got:\n%s", msg)
	}
	if !strings.Contains(msg, "С ДРУГОЙ СТОРОНЫ") {
		t.Fatalf("дайджест должен требовать идти с другой стороны, got:\n%s", msg)
	}
	if !d.BannedSignature(c.Sig, c.Name) {
		t.Fatal("повторённый вызов должен быть запрещён")
	}
	if len(d.sigRound) != 0 || d.failStreak != 0 || d.noWrite != 0 {
		t.Fatalf("счётчики должны быть перевзведены, got %#v", d)
	}
	// Счётчик «за весь цикл» переживает разрыв: дайджест показывает модели
	// полную картину попыток.
	if d.sigTotal[c.Sig] != loopSigRepeatsDefault {
		t.Fatalf("общий счётчик попыток должен сохраниться, got %d", d.sigTotal[c.Sig])
	}
}

// Петля перебора аргументов: повторов по сигнатуре нет, поэтому под запрет
// попадает сам инструмент (иначе модель продолжит перебор).
func TestLoopBreakLoopBansToolWhenArgsVary(t *testing.T) {
	d := NewLoopDetector(nil)
	for i := 0; i < loopFailStreakDefault; i++ {
		d.ObserveRound([]LoopRoundCall{failCall("BoardCreateEpic", string(rune('A'+i)))})
	}
	msg, taken := d.BreakLoop("проверка", "")
	if taken != 1 {
		t.Fatalf("под запрет должен попасть один инструмент, got %d", taken)
	}
	if !d.BannedSignature("любая сигнатура", "BoardCreateEpic") {
		t.Fatal("инструмент, который ни разу не сработал, должен быть запрещён целиком")
	}
	if !strings.Contains(msg, "BoardCreateEpic") || !strings.Contains(msg, "уже существует") && !strings.Contains(msg, "boom") {
		t.Fatalf("дайджест должен называть инструмент и его ошибку, got:\n%s", msg)
	}
}

// Инструмент, который иногда срабатывает, под тотальный запрет не попадает:
// его успешный вызов — это прогресс.
func TestLoopBreakLoopKeepsPartiallyWorkingTool(t *testing.T) {
	d := NewLoopDetector(nil)
	d.ObserveRound([]LoopRoundCall{okCall("WriteFiles", "a")})
	for i := 0; i < loopFailStreakDefault; i++ {
		d.ObserveRound([]LoopRoundCall{failCall("WriteFiles", string(rune('b'+i)))})
	}
	if d.CanBreak() != true {
		t.Fatal("разрыв ещё доступен")
	}
	d.BreakLoop("проверка", "")
	if d.BannedSignature("любая сигнатура", "WriteFiles") {
		t.Fatal("инструмент с успешным вызовом нельзя запрещать целиком")
	}
}

// Дайджест напоминает о невыполненном обязательном действии: без этого разрыв
// ломает лида, который так и не опубликовал задачи.
func TestLoopBreakMessageKeepsRequiredAction(t *testing.T) {
	d := NewLoopDetector(nil)
	c := okCall("List", "{}")
	for i := 0; i < loopSigRepeatsDefault; i++ {
		d.ObserveRound([]LoopRoundCall{c})
	}
	msg, _ := d.BreakLoop("проверка", "BoardCreateTask")
	if !strings.Contains(msg, "BoardCreateTask") {
		t.Fatalf("дайджест должен напомнить обязательное действие, got:\n%s", msg)
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
		got := LoopCallPaths(tc.args)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: LoopCallPaths = %v, ожидалось %v", tc.name, got, tc.want)
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
	msg := LoopRestartMessage("Почини хендлер входа", []string{"server/api.go", "server/auth.go"}, "submit_architecture_backlog")

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
	empty := LoopRestartMessage("Задача", nil, "")
	if !strings.Contains(empty, "List/ReadMap") {
		t.Fatalf("без файлов нужно предложить сориентироваться в проекте, got:\n%s", empty)
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
	msgs := []domain.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: "пробую"},
		{Role: "tool", Content: "ошибка"},
		{Role: "assistant", Content: "ещё раз"},
		{Role: "tool", Content: "ошибка"},
	}
	got := LoopCollapsedMessages(msgs, 4, 2)
	if len(got) != 4 {
		t.Fatalf("ожидали 4 сообщения (схлопнуто 2), got %d: %#v", len(got), got)
	}
	if got[1].Content != "task" {
		t.Fatalf("постановка задачи потеряна: %#v", got)
	}
	// Точка прогресса правее хвоста — ничего не схлопывается.
	if got := LoopCollapsedMessages(msgs, 6, 2); len(got) != len(msgs) {
		t.Fatalf("схлопывать нечего, got %d", len(got))
	}
	// Точка прогресса левее постановки — постановка не теряется.
	if got := LoopCollapsedMessages(msgs, 0, 2); len(got) != 2 {
		t.Fatalf("ожидали 2 сообщения (постановка), got %d", len(got))
	}
}
