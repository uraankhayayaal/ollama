package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// logTestSession поднимает сервер с проектом и сессией на нём — ровно то, что
// видит ассистент в чате (через getOrCreate, чтобы сессия получила стор чата и
// была зарегистрирована в сервере).
func logTestSession(t *testing.T, projName string) (*Session, *Server) {
	t.Helper()
	srv, _, _ := newTestServer(t)
	registerTestDir(t, srv, projName)
	sess, _, err := srv.getOrCreate(projName)
	if err != nil {
		t.Fatal(err)
	}
	return sess, srv
}

func writeLogFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Мост обязателен: логи проекта лежат в logs/<проект>.log, то есть ВНЕ корня
// рабочей папки, а файловые инструменты ассистента за его пределы не пускают.
// Без моста ассистент физически не может докапывать находку.
func TestReadProjectLogsReadsGlobalProjectFileOutsideRoot(t *testing.T) {
	const proj = "logbridge"
	sess, _ := logTestSession(t, proj)
	global := t.TempDir()
	t.Setenv("LOG_DIR", global)
	writeLogFile(t, global, proj+".log", "2026-01-01 10:00:00.000 ERROR boom: cannot connect\n  at com.foo.Bar(Bar.java:42)\n")

	out, err := sess.readProjectLogs(logToolArgs{file: proj + ".log"})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := out["content"].([]logToolFile)
	if len(files) != 1 {
		t.Fatalf("файлов=%d, want 1: %+v", len(files), out["content"])
	}
	if !strings.Contains(files[0].Content, "cannot connect") {
		t.Fatalf("лог не прочитан: %+v", files[0])
	}
	if !strings.Contains(files[0].Content, "at com.foo.Bar") {
		t.Errorf("соседняя строка (кадр стека) потеряна: %+v", files[0])
	}
}

// Инструмент не должен протекать в чужие логи: в общем каталоге сервера
// лежат логи всех проектов и server.log.
func TestReadProjectLogsHidesForeignLogs(t *testing.T) {
	const proj = "logbridge-own"
	sess, _ := logTestSession(t, proj)
	global := t.TempDir()
	t.Setenv("LOG_DIR", global)
	writeLogFile(t, global, proj+".log", "свой лог\n")
	writeLogFile(t, global, "other-proj.log", "чужая тайна\n")
	writeLogFile(t, global, "server.log", "служебный лог процесса\n")

	out, err := sess.readProjectLogs(logToolArgs{file: "all"})
	if err != nil {
		t.Fatal(err)
	}
	body := dumpReadLogs(t, out)
	for _, forbidden := range []string{"чужая тайна", "служебный лог процесса", "other-proj.log", "server.log"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("ответ моста содержит чужой лог %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, "свой лог") {
		t.Errorf("ответ моста не содержит свой лог: %s", body)
	}
}

// grep — основной сценарий докапывания: найти все вхождения ошибки из находки.
func TestReadProjectLogsGrepFiltersLines(t *testing.T) {
	const proj = "logbridge-grep"
	sess, _ := logTestSession(t, proj)
	global := t.TempDir()
	t.Setenv("LOG_DIR", global)
	body := strings.Join([]string{
		"2026-01-01 10:00:00.000 INFO   раунд 1",
		"2026-01-01 10:00:01.000 ERROR cannot connect",
		"2026-01-01 10:00:02.000 INFO   раунд 2",
		"2026-01-01 10:00:03.000 ERROR cannot connect",
	}, "\n")
	writeLogFile(t, global, proj+".log", body)

	out, err := sess.readProjectLogs(logToolArgs{file: proj + ".log", grep: "cannot connect"})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := out["content"].([]logToolFile)
	if len(files) != 1 {
		t.Fatalf("файлов=%d, want 1", len(files))
	}
	f := files[0]
	if f.Matched != 2 || f.Lines != 2 {
		t.Errorf("matched=%d lines=%d, want 2/2", f.Matched, f.Lines)
	}
	if f.Total != 4 {
		t.Errorf("total=%d, want 4 (все строки файла)", f.Total)
	}
	if strings.Contains(f.Content, "раунд") {
		t.Errorf("grep не отфильтровал: %q", f.Content)
	}
	if f.Truncated {
		t.Errorf("обрезание не ожидалось: %+v", f)
	}
}

// grep регистронезависимый: модель ищет «error» по образцу из дайджеста,
// а в логе может быть «ERROR».
func TestReadProjectLogsGrepIsCaseInsensitive(t *testing.T) {
	const proj = "logbridge-case"
	sess, _ := logTestSession(t, proj)
	global := t.TempDir()
	t.Setenv("LOG_DIR", global)
	writeLogFile(t, global, proj+".log", "2026-01-01 ERROR Failed\n2026-01-01 INFO ok\n")

	out, _ := sess.readProjectLogs(logToolArgs{file: proj + ".log", grep: "failed"})
	files, _ := out["content"].([]logToolFile)
	if len(files) == 1 && files[0].Matched != 1 {
		t.Errorf("matched=%d, want 1", files[0].Matched)
	}
	if len(files) == 1 && !strings.Contains(files[0].Content, "Failed") {
		t.Errorf("совпадение не найдено: %q", files[0].Content)
	}
}

// Границы выдачи: limit и лимит по байтам. Без них модель одной пачкой
// вытащит весь лог и зальёт контекст.
func TestReadProjectLogsBoundsOutput(t *testing.T) {
	const proj = "logbridge-limit"
	sess, _ := logTestSession(t, proj)
	global := t.TempDir()
	t.Setenv("LOG_DIR", global)
	var b strings.Builder
	for i := 0; i < 5000; i++ {
		b.WriteString("2026-01-01 10:00:00.000 INFO   строка ")
		b.WriteString(strings.Repeat("x", 100))
		b.WriteString("\n")
	}
	writeLogFile(t, global, proj+".log", b.String())

	// limit меньше числа строк.
	out, _ := sess.readProjectLogs(logToolArgs{file: proj + ".log", lines: 10})
	files, _ := out["content"].([]logToolFile)
	if len(files) != 1 {
		t.Fatalf("файлов=%d, want 1", len(files))
	}
	f := files[0]
	if f.Lines != 10 {
		t.Errorf("lines=%d, want 10", f.Lines)
	}
	if f.Total != 5000 {
		t.Errorf("total=%d, want 5000", f.Total)
	}
	if !f.Truncated {
		t.Errorf("обрезание не отмечено: %+v", f)
	}
	// Без limit — потолок по умолчанию.
	out, _ = sess.readProjectLogs(logToolArgs{file: proj + ".log"})
	files, _ = out["content"].([]logToolFile)
	if files[0].Lines != logToolDefaultLines {
		t.Errorf("lines=%d, want %d", files[0].Lines, logToolDefaultLines)
	}
	// limit выше потолка не проходит.
	out, _ = sess.readProjectLogs(logToolArgs{file: proj + ".log", lines: 99999})
	files, _ = out["content"].([]logToolFile)
	if files[0].Lines != logToolMaxLines {
		t.Errorf("lines=%d, want %d", files[0].Lines, logToolMaxLines)
	}
	if len(files[0].Content) > logToolMaxBytes+4 {
		t.Errorf("содержимое %d байт, потолок %d", len(files[0].Content), logToolMaxBytes)
	}
}

// Модель не должна угадывать имена файлов вслепую: при промахе показываем
// список доступных (Logboard показывает ровно эти файлы).
func TestReadProjectLogsUnknownFileListsAvailable(t *testing.T) {
	const proj = "logbridge-miss"
	sess, _ := logTestSession(t, proj)
	global := t.TempDir()
	t.Setenv("LOG_DIR", global)
	writeLogFile(t, global, proj+".log", "свой лог\n")

	out, err := sess.readProjectLogs(logToolArgs{file: "calc.log"})
	if err != nil {
		t.Fatal(err)
	}
	list, _ := out["files"].([]map[string]any)
	if len(list) != 1 || list[0]["name"] != proj+".log" {
		t.Fatalf("список файлов = %+v", list)
	}
	if note, _ := out["note"].(string); !strings.Contains(note, "не найден") {
		t.Errorf("note = %q", note)
	}
	if _, ok := out["content"]; ok {
		t.Errorf("при промахе нечего читать: %+v", out["content"])
	}
}

// Пустой каталог логов — не ошибка, а норма: проект мог ещё не запускаться.
func TestReadProjectLogsNoFiles(t *testing.T) {
	const proj = "logbridge-empty"
	sess, _ := logTestSession(t, proj)
	t.Setenv("LOG_DIR", t.TempDir())

	out, err := sess.readProjectLogs(logToolArgs{})
	if err != nil {
		t.Fatalf("пустой каталог не должен быть ошибкой: %v", err)
	}
	if note, _ := out["note"].(string); note == "" {
		t.Errorf("нет пояснения: %+v", out)
	}
}

// Список файлов и содержимое — единый источник правды с Logboard: тот же
// сборщик collectProjectLogs, иначе панель показывала бы не то, что читает
// ассистент.
func TestReadProjectLogsSeesProjectLocalLogs(t *testing.T) {
	const proj = "logbridge-local"
	sess, srv := logTestSession(t, proj)
	global := t.TempDir()
	t.Setenv("LOG_DIR", global)
	inf, err := srv.reg.Get(proj)
	if err != nil {
		t.Fatal(err)
	}
	writeLogFile(t, filepath.Join(inf.Root, "logs"), "app.log", "лог приложения\n")
	writeLogFile(t, global, proj+".log", "лог агента\n")

	out, err := sess.readProjectLogs(logToolArgs{file: "all"})
	if err != nil {
		t.Fatal(err)
	}
	body := dumpReadLogs(t, out)
	for _, want := range []string{"app.log", "лог приложения", "лог агента"} {
		if !strings.Contains(body, want) {
			t.Errorf("нет %q в ответе: %s", want, body)
		}
	}
}

// Инструмент входит в набор ассистента — иначе мост есть, а модель его не
// видит.
func TestReadProjectLogsIsWiredIntoAssistantTools(t *testing.T) {
	const proj = "logbridge-wiring"
	sess, _ := logTestSession(t, proj)
	byName(t, sess.serverActionTools())[actionReadLogs].Definition()
}

func TestReadProjectLogsToolDefinition(t *testing.T) {
	sess, _ := logTestSession(t, "logbridge-def")
	def := newLogReadTool(sess).Definition()
	if def.Name != actionReadLogs {
		t.Errorf("имя = %q", def.Name)
	}
	params, _ := def.Parameters["properties"].(map[string]any)
	for _, p := range []string{"file", "grep", "limit"} {
		if _, ok := params[p]; !ok {
			t.Errorf("нет свойства %q", p)
		}
	}
	if !strings.Contains(def.Description, "лог") {
		t.Errorf("описание не объясняет назначение: %q", def.Description)
	}
}

// dumpReadLogs — весь ответ моста строкой: удобно проверять утечки.
func dumpReadLogs(t *testing.T, out map[string]any) string {
	t.Helper()
	var b strings.Builder
	for _, f := range out["files"].([]map[string]any) {
		b.WriteString(nameOf(f) + "\n")
	}
	if files, ok := out["content"].([]logToolFile); ok {
		for _, f := range files {
			b.WriteString(f.Name + ":\n" + f.Content + "\n" + f.Note + "\n")
		}
	}
	if note, ok := out["note"].(string); ok {
		b.WriteString(note)
	}
	return b.String()
}

func nameOf(m map[string]any) string {
	s, _ := m["name"].(string)
	return s
}
