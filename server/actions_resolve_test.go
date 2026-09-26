package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ai/board"
	"ai/chat"
	"ai/tools"
)

// --- hermetic: мосты, аргументы, границы безопасности (без git) ---

// TestConflictResolveToolsWiring — оба моста Ф-4b попадают в набор ассистента с
// ожидаемыми именами и схемой; ResolveGitConflicts безопасный (вызывается без
// подтверждения), EpicResolve деструктивный (confirm-гейт до обращения к
// backend); аргументы files/edits разбираются в ConflictRequest.
func TestConflictResolveToolsWiring(t *testing.T) {
	back := &fakeActions{conflictOut: map[string]any{"resolve_status": "resolving"}}
	ts := byName(t, newConflictResolveTools(back))
	res, ok := ts[actionResolveConflicts]
	if !ok {
		t.Fatalf("в наборе нет %s", actionResolveConflicts)
	}
	fin, ok := ts[actionEpicResolve]
	if !ok {
		t.Fatalf("в наборе нет %s", actionEpicResolve)
	}
	for _, tc := range []struct {
		tool  tools.Tool
		epic  string
		props []string
	}{
		{res, actionResolveConflicts, []string{"epic_id", "action", "files", "edits"}},
		{fin, actionEpicResolve, []string{"epic_id"}},
	} {
		def := tc.tool.Definition()
		if def.Name != tc.epic {
			t.Errorf("имя = %q, want %q", def.Name, tc.epic)
		}
		params, _ := def.Parameters["properties"].(map[string]any)
		for _, p := range tc.props {
			if _, ok := params[p]; !ok {
				t.Errorf("%s: нет свойства %q", def.Name, p)
			}
		}
		req, _ := def.Parameters["required"].([]string)
		if len(req) != 1 || req[0] != "epic_id" {
			t.Errorf("%s: required = %v, want [epic_id]", def.Name, req)
		}
	}

	// Безопасный мост: выполняется сразу, backend видит разобранные аргументы.
	out := decodeToolResult(t, mustExec(t, res, map[string]any{
		"epic_id": "epic-1",
		"action":  "apply",
		"files":   map[string]any{"main.go": "package main\n"},
		"edits": []any{map[string]any{
			"file": "big.go", "old": "<<<<<<< HEAD", "new": "// выбрано",
		}},
	}))
	if out["status"] != "success" {
		t.Fatalf("статус моста = %v (%v)", out["status"], out)
	}
	if back.conflictReq.EpicID != "epic-1" || back.conflictReq.Action != "apply" {
		t.Fatalf("ConflictRequest = %+v", back.conflictReq)
	}
	if back.conflictReq.Files["main.go"] != "package main\n" {
		t.Fatalf("files не разобраны: %+v", back.conflictReq.Files)
	}
	if len(back.conflictReq.Edits) != 1 || back.conflictReq.Edits[0].File != "big.go" ||
		back.conflictReq.Edits[0].Old != "<<<<<<< HEAD" || back.conflictReq.Edits[0].New != "// выбрано" {
		t.Fatalf("edits не разобраны: %+v", back.conflictReq.Edits)
	}

	// Деструктивный мост: без «да» — confirm, backend не тронут.
	got := decodeToolResult(t, mustExec(t, fin, map[string]any{"epic_id": "epic-1"}))
	if got["status"] != "confirm" {
		t.Fatalf("EpicResolve без подтверждения = %v", got)
	}
	if back.finishEpic != "" {
		t.Fatalf("backend вызван без подтверждения (epic=%q)", back.finishEpic)
	}
	back.confirmed = true
	got = decodeToolResult(t, mustExec(t, fin, map[string]any{"epic_id": "epic-1"}))
	if got["status"] != "success" || back.finishEpic != "epic-1" {
		t.Fatalf("EpicResolve после «да» = %v (epic=%q)", got, back.finishEpic)
	}
}

// TestConflictReplySemantics — ответ ядра резолва переводится в результат
// инструмента: 2xx — успех, 409 (ещё конфликт / приёмка не прошла) — НЕ ошибка
// инструмента, остальные коды — ошибка вызова.
func TestConflictReplySemantics(t *testing.T) {
	out, err := conflictReply(200, map[string]any{"status": "ok", "message": "ок"})
	if err != nil || out["resolve_status"] != "ok" {
		t.Fatalf("200 → %v, %v", out, err)
	}
	if _, ok := out["status"]; ok {
		t.Fatalf("ключ status должен быть заменён на resolve_status: %v", out)
	}
	out, err = conflictReply(409, map[string]any{
		"status": "still_conflicts", "files": []any{"main.go"},
		"message": "остались маркеры конфликта",
	})
	if err != nil {
		t.Fatalf("409 не должен быть ошибкой инструмента: %v", err)
	}
	if out["resolve_status"] != "still_conflicts" {
		t.Fatalf("resolve_status = %v", out["resolve_status"])
	}
	if _, err := conflictReply(400, map[string]any{"message": "эпик не done"}); err == nil ||
		!strings.Contains(err.Error(), "эпик не done") {
		t.Fatalf("400 → %v, want error", err)
	}
}

// TestConflictSafeRelAndWithinDir — путь из аргументов модели проверяется:
// относительность, запрет выхода по «..»/абсолютности, попадание внутрь
// worktree (symlink-подобный путь через «..» отклоняется).
func TestConflictSafeRelAndWithinDir(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "/etc/passwd", "../outside.go", "a/../../b.go"} {
		if _, err := conflictSafeRel(bad); err == nil {
			t.Errorf("conflictSafeRel(%q) — ожидалась ошибка", bad)
		}
	}
	got, err := conflictSafeRel("./src/../src/main.go")
	if err != nil || got != "src/main.go" {
		t.Fatalf("conflictSafeRel = %q, %v", got, err)
	}
	wt := filepath.Join(t.TempDir(), ".conflict-p-e")
	if !withinDir(wt, filepath.Join(wt, "main.go")) {
		t.Fatal("файл внутри worktree не распознан")
	}
	if withinDir(wt, wt+"-evil/main.go") {
		t.Fatal("путь-сосед распознан как внутри worktree")
	}
}

// TestConflictViewTruncatesBigFiles — большой конфликтный файл отдаётся
// фрагментом вокруг маркеров и помечается в truncated (править его можно
// только через edits), небольшой — целиком.
func TestConflictViewTruncatesBigFiles(t *testing.T) {
	dir := t.TempDir()
	small := "package main\n<<<<<<< HEAD\nv1\n=======\nv2\n>>>>>>> other\n"
	if err := os.WriteFile(filepath.Join(dir, "small.go"), []byte(small), 0o644); err != nil {
		t.Fatal(err)
	}
	big := "package main\n" + strings.Repeat("// filler\n", 8000) +
		"<<<<<<< HEAD\nv1\n=======\nv2\n>>>>>>> other\n"
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	v := newConflictView(dir, []string{"small.go", "big.go"})
	if len(v.Files) != 2 || len(v.Content) != 2 {
		t.Fatalf("view = %+v", v)
	}
	if v.Content["small.go"] != small {
		t.Fatalf("малый файл не отдан целиком:\n%s", v.Content["small.go"])
	}
	if len(v.Truncated) != 1 || v.Truncated[0] != "big.go" {
		t.Fatalf("truncated = %v", v.Truncated)
	}
	frag := v.Content["big.go"]
	if !strings.Contains(frag, "<<<<<<< HEAD") || !strings.Contains(frag, "фрагментом") {
		t.Fatalf("фрагмент без маркеров/пояснения:\n%s", frag[:min(400, len(frag))])
	}
	if len(frag) >= len(big) {
		t.Fatalf("фрагмент не короче файла (%d vs %d)", len(frag), len(big))
	}
	if !strings.Contains(v.Hint, "edits") {
		t.Fatalf("hint = %q", v.Hint)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func mustExec(t *testing.T, tool tools.Tool, args map[string]any) []byte {
	t.Helper()
	raw, err := tool.Execute(args)
	if err != nil {
		t.Fatalf("%s: %v", tool.Name(), err)
	}
	return raw
}

// --- реальный git: полный цикл резолва через мосты ассистента (Ф-4b) ---

// TestChatAssistantResolvesEpicConflictViaBridges — ассистент автономно
// проходит цикл: start (конфликтный worktree) → status (файлы + содержимое с
// маркерами) → apply (запись выбранного содержимого) → EpicResolve (приёмка +
// merge в main + push). До «да» в чате финализация не выполняется.
func TestChatAssistantResolvesEpicConflictViaBridges(t *testing.T) {
	srv, _, sess, dest, origin := setupRealGitEpicConflict(t)
	toolsByName := byName(t, sess.serverActionTools())
	resolve, ok := toolsByName[actionResolveConflicts]
	if !ok {
		t.Fatalf("у ассистента нет моста %s", actionResolveConflicts)
	}
	finish, ok := toolsByName[actionEpicResolve]
	if !ok {
		t.Fatalf("у ассистента нет моста %s", actionEpicResolve)
	}
	wtPath := filepath.Join(filepath.Dir(dest), ".conflict-myrepo-epic-1")

	// 1) start: открывается процесс резолва, тривиальные конфликты закрыты
	// автоматически, модели отдаётся содержимое сложного файла.
	out := decodeToolResult(t, mustExec(t, resolve, map[string]any{
		"epic_id": "epic-1", "action": "start",
	}))
	if out["status"] != "success" || out["resolve_status"] != "resolving" {
		t.Fatalf("start = %v", out)
	}
	files, _ := out["conflicts"].(map[string]any)
	fList, _ := files["files"].([]any)
	if len(fList) != 1 || fList[0] != "main.go" {
		t.Fatalf("conflicts.files = %v (%v)", fList, out)
	}
	content, _ := files["content"].(map[string]any)
	wtContent, _ := content["main.go"].(string)
	if !strings.Contains(wtContent, "<<<<<<< HEAD") || !strings.Contains(wtContent, "v1") {
		t.Fatalf("модели не отдан конфликт:\n%s", wtContent)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("конфликтный worktree не создан: %v", err)
	}
	// Абсолютный путь worktree модели не нужен — его файловые инструменты туда
	// не умеют и только спровоцируют неудачные ReadFiles.
	if _, has := out["worktree"]; has {
		t.Fatalf("в ответе моста не должно быть worktree: %v", out)
	}

	// 2) status: то же состояние, без новых побочных эффектов.
	out = decodeToolResult(t, mustExec(t, resolve, map[string]any{
		"epic_id": "epic-1", "action": "status",
	}))
	if out["resolve_status"] != "resolving" {
		t.Fatalf("status = %v", out)
	}

	// 3) apply: отказ на не-конфликтном файле, на выходе за worktree и на
	// содержимом с оставшимися маркерами — файловая система не меняется.
	if _, err := os.Stat(filepath.Join(wtPath, "go.mod")); err != nil {
		t.Fatalf("в worktree нет go.mod: %v", err)
	}
	// Снимок файлов вне конфликтного worktree: после неудачных apply они должны
	// остаться байт-в-байт прежними.
	outsideBefore := map[string]string{}
	for _, outside := range []string{filepath.Join(dest, "main.go"), filepath.Join(origin, "main.go")} {
		data, err := os.ReadFile(outside)
		if err != nil {
			t.Fatal(err)
		}
		outsideBefore[outside] = string(data)
	}
	bad := []struct {
		args map[string]any
		want string
	}{{
		map[string]any{"epic_id": "epic-1", "action": "apply",
			"files": map[string]any{"go.mod": "module x\n"}},
		"не конфликтный",
	}, {
		map[string]any{"epic_id": "epic-1", "action": "apply",
			"files": map[string]any{"../main.go": "pwned\n"}},
		"недопустимый путь",
	}, {
		map[string]any{"epic_id": "epic-1", "action": "apply",
			"files": map[string]any{"main.go": "package main\n<<<<<<< HEAD\nv1\n=======\nv2\n>>>>>>> other\n"}},
		"маркер",
	}}
	for i, tc := range bad {
		got := decodeToolResult(t, mustExec(t, resolve, tc.args))
		if got["status"] != "error" {
			t.Fatalf("apply[%d] ожидался отказ, args=%v → %v", i, tc.args, got)
		}
		if msg, _ := got["message"].(string); !strings.Contains(msg, tc.want) {
			t.Fatalf("apply[%d]: сообщение %q не содержит %q", i, msg, tc.want)
		}
	}
	// Ни рабочая копия клона, ни origin не тронуты: неудачные apply не выходят
	// за пределы конфликтного worktree.
	for outside, want := range outsideBefore {
		if got, _ := os.ReadFile(outside); string(got) != want {
			t.Fatalf("файл вне конфликтного worktree изменён (%s):\n%s", outside, got)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(wtPath, "go.mod")); !strings.Contains(string(got), "module myrepo") {
		t.Fatalf("apply тронул не-конфликтный файл:\n%s", got)
	}

	// 4) apply: корректное содержимое (версия эпика) — модель выбрала v1.
	resolved := "package main\n\nvar version = \"v1\"\n\nfunc main() {}\n"
	out = decodeToolResult(t, mustExec(t, resolve, map[string]any{
		"epic_id": "epic-1", "action": "apply",
		"files": map[string]string{"main.go": resolved},
	}))
	if out["resolve_status"] != "applied" {
		t.Fatalf("apply = %v", out)
	}
	applied, _ := out["applied"].([]any)
	if len(applied) != 1 || applied[0] != "main.go" {
		t.Fatalf("applied = %v", applied)
	}
	wtFile, _ := os.ReadFile(filepath.Join(wtPath, "main.go"))
	if string(wtFile) != resolved {
		t.Fatalf("worktree main.go = %q, want %q", wtFile, resolved)
	}

	// 5) apply повторно: конфликтов больше нет — осталась только финализация.
	var got map[string]any = decodeToolResult(t, mustExec(t, resolve, map[string]any{
		"epic_id": "epic-1", "action": "apply",
		"files": map[string]any{"main.go": resolved},
	}))
	if got["status"] != "error" {
		t.Fatalf("повторный apply = %v, want error", got)
	}
	if msg, _ := got["message"].(string); !strings.Contains(msg, actionEpicResolve) {
		t.Fatalf("повторный apply: ожидалась подсказка про %s, сообщение %q", actionEpicResolve, msg)
	}

	// 6) EpicResolve без подтверждения: main не двигается.
	t.Setenv("ACCEPT_INSTALL_DEPS", "0")
	t.Setenv("ACCEPT_LSP", "0")
	got = decodeToolResult(t, mustExec(t, finish, map[string]any{"epic_id": "epic-1"}))
	if got["status"] != "confirm" {
		t.Fatalf("EpicResolve без «да» = %v", got)
	}
	if _, err := srv.reg.EpicResolve("myrepo"); err != nil {
		t.Fatalf("процесс резолва закрыт до финализации: %v", err)
	}
	if log, _ := exec.Command("git", "-C", dest, "log", "main", "--format=%s").CombinedOutput(); strings.Contains(string(log), "резолв конфликтов") {
		t.Fatalf("main уже продвинута до подтверждения:\n%s", log)
	}

	// 7) После явного «да» — приёмка, merge в main, push, cleanup.
	sess.append(chat.RoleUser, "да, разрешай", "user", "", nil)
	got = decodeToolResult(t, mustExec(t, finish, map[string]any{"epic_id": "epic-1"}))
	if got["status"] != "success" || got["resolve_status"] != "ok" {
		t.Fatalf("EpicResolve после «да» = %v", got)
	}
	origMain, _ := exec.Command("git", "-C", origin, "rev-parse", "refs/heads/main").CombinedOutput()
	destMain, _ := exec.Command("git", "-C", dest, "rev-parse", "main").CombinedOutput()
	if strings.TrimSpace(string(origMain)) != strings.TrimSpace(string(destMain)) {
		t.Fatalf("main не запушена в origin: %s vs %s", origMain, destMain)
	}
	merged, _ := exec.Command("git", "-C", dest, "show", "main:main.go").CombinedOutput()
	if !strings.Contains(string(merged), `"v1"`) {
		t.Fatalf("main без резолвленной версии:\n%s", merged)
	}
	if _, err := os.Stat(wtPath); err == nil {
		t.Fatal("конфликтный worktree не снят после финализации")
	}
	if _, err := srv.reg.EpicResolve("myrepo"); err == nil {
		t.Fatal("процесс резолва не закрыт после финализации")
	}
	// Признак конфликта на доске снят.
	store, err := srv.boardStore(context.Background(), "myrepo")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	epic, err := store.GetEpic(context.Background(), "epic-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(epic.MergeConflictFiles) != 0 {
		t.Fatalf("эпик всё ещё помечен конфликтом: %v", epic.MergeConflictFiles)
	}
}

// TestConflictApplyEditsForBigFile — резолв большого конфликтного файла через
// точечные замены (edits): уникальный фрагмент меняется, apply отклоняет
// неуникальный old и запись полного содержимого большого файла.
func TestConflictApplyEditsForBigFile(t *testing.T) {
	_, _, sess, dest, _ := setupRealGitEpicConflict(t)
	wtPath := filepath.Join(filepath.Dir(dest), ".conflict-myrepo-epic-1")

	// Наполняем main.go так, чтобы конфликт оказался в большом файле.
	if _, err := sess.ConflictResolve(context.Background(), ConflictRequest{
		EpicID: "epic-1", Action: "start",
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	wtFile := filepath.Join(wtPath, "main.go")
	big := "package main\n" + strings.Repeat("// filler\n", 6000) +
		"<<<<<<< HEAD\nvar version = \"v1\"\n=======\nvar version = \"v2\"\n>>>>>>> other\n\nfunc main() {}\n"
	if err := os.WriteFile(wtFile, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	// Файл вырос — процесс резолва должен перечитать его при apply.
	out, err := sess.ConflictResolve(context.Background(), ConflictRequest{EpicID: "epic-1", Action: "status"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	view, _ := out["conflicts"].(*conflictView)
	if len(view.Truncated) != 1 || view.Truncated[0] != "main.go" {
		t.Fatalf("truncated = %v (out=%v)", view.Truncated, out)
	}

	// Неуникальный old — отказ, файл не тронут.
	if _, err := sess.ConflictResolve(context.Background(), ConflictRequest{
		EpicID: "epic-1", Action: "apply",
		Edits: []ConflictEdit{{File: "main.go", Old: "// filler\n", New: "x"}},
	}); err == nil || !strings.Contains(err.Error(), "встречается") {
		t.Fatalf("неуникальный old: err=%v", err)
	}
	// Полное содержимое большого файла — отказ с подсказкой про edits.
	if _, err := sess.ConflictResolve(context.Background(), ConflictRequest{
		EpicID: "epic-1", Action: "apply",
		Files: map[string]string{"main.go": strings.Replace(big, "<<<<<<< HEAD\n", "", 1)},
	}); err == nil || !strings.Contains(err.Error(), "edits") {
		t.Fatalf("files для большого файла: err=%v", err)
	}
	// Точечная замена блока конфликта — принимается.
	if _, err := sess.ConflictResolve(context.Background(), ConflictRequest{
		EpicID: "epic-1", Action: "apply",
		Edits: []ConflictEdit{{
			File: "main.go",
			Old:  "<<<<<<< HEAD\nvar version = \"v1\"\n=======\nvar version = \"v2\"\n>>>>>>> other",
			New:  "var version = \"v1\"",
		}},
	}); err != nil {
		t.Fatalf("edits: %v", err)
	}
	got, _ := os.ReadFile(wtFile)
	if strings.Contains(string(got), "<<<<<<<") || !strings.Contains(string(got), `"v1"`) {
		t.Fatalf("edits не закрыли конфликт:\n%s", got[:min(400, len(got))])
	}
	if !strings.Contains(string(got), "// filler") {
		t.Fatal("edits потеряли остальную часть файла")
	}
}

// TestConflictResolveNoProcess — без активного процесса резолва мост сообщает об
// этом (status → none, apply/finish — ошибка), а не создаёт worktree сам.
func TestConflictResolveNoProcess(t *testing.T) {
	_, _, sess, dest, _ := setupRealGitEpicConflict(t)
	out, err := sess.ConflictResolve(context.Background(), ConflictRequest{EpicID: "epic-1", Action: "status"})
	if err != nil {
		t.Fatalf("status без процесса: %v", err)
	}
	if out["resolve_status"] != "none" || !strings.Contains(out["message"].(string), actionResolveConflicts) {
		t.Fatalf("status без процесса = %v", out)
	}
	if _, err := sess.ConflictResolve(context.Background(), ConflictRequest{
		EpicID: "epic-1", Action: "apply", Files: map[string]string{"main.go": "x"},
	}); err == nil || !strings.Contains(err.Error(), "action=start") {
		t.Fatalf("apply без процесса: err=%v", err)
	}
	// finish без процесса — не ошибка инструмента, а понятный 409-статус.
	out, err = sess.ConflictFinish(context.Background(), "epic-1")
	if err != nil {
		t.Fatalf("finish без процесса: %v", err)
	}
	if out["resolve_status"] != "no_resolve" {
		t.Fatalf("finish без процесса = %v", out)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), ".conflict-myrepo-epic-1")); err == nil {
		t.Fatal("мост создал worktree вне резолва")
	}
	// Чужой эпик / неизвестное действие.
	if _, err := sess.ConflictResolve(context.Background(), ConflictRequest{EpicID: "epic-1"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := sess.ConflictResolve(context.Background(), ConflictRequest{
		EpicID: "epic-2", Action: "status",
	}); err != nil {
		t.Fatalf("status чужого эпика должен быть «none»: %v", err)
	}
	if _, err := sess.ConflictResolve(context.Background(), ConflictRequest{
		EpicID: "epic-1", Action: "commit",
	}); err == nil || !strings.Contains(err.Error(), "неизвестное действие") {
		t.Fatalf("неизвестное действие: err=%v", err)
	}
	if _, err := sess.ConflictResolve(context.Background(), ConflictRequest{Action: "status"}); err == nil {
		t.Fatal("без epic_id: ожидалась ошибка")
	}
	_ = board.StatusDone
}
