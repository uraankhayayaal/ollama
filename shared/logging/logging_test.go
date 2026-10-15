package logging

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// resetLogging закрывает потоки и возвращает пакет в «не настроенное»
// состояние: глобальные переменные не должны протекать между тестами.
func resetLogging(t *testing.T) {
	t.Helper()
	reset := func() {
		Close()
		mu.Lock()
		defName = ""
		mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// До Setup файлы не создаются: тесты и утилиты работают в консольный режим и
// не засоряют logs/.
func TestNoFilesBeforeSetup(t *testing.T) {
	resetLogging(t)
	dir := t.TempDir()
	t.Setenv("LOG_DIR", dir)

	Infof("сообщение в никуда")
	For("proj").Infof("сообщение проекта")
	Attach("proj")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("до Setup файлы создаваться не должны: %v", entries)
	}
}

// Setup направляет сообщения по умолчанию в logs/<имя>.log.
func TestSetupWritesDefaultFile(t *testing.T) {
	resetLogging(t)
	dir := t.TempDir()
	t.Setenv("LOG_DIR", dir)

	Setup("server")
	Infof("процесс запущен")

	got := readFile(t, filepath.Join(dir, "server.log"))
	if !strings.Contains(got, "процесс запущен") {
		t.Fatalf("в server.log нет сообщения: %q", got)
	}
}

// Главное требование: у каждого проекта ОТДЕЛЬНЫЙ файл logs/<проект>.log,
// строки не смешиваются ни между проектами, ни с логом процесса.
func TestForWritesProjectFile(t *testing.T) {
	resetLogging(t)
	dir := t.TempDir()
	t.Setenv("LOG_DIR", dir)

	Setup("server")
	Infof("служебное")
	For("alpha").Infof("строка альфы")
	For("beta").Warnf("строка беты")

	alpha := readFile(t, filepath.Join(dir, "alpha.log"))
	beta := readFile(t, filepath.Join(dir, "beta.log"))
	srv := readFile(t, filepath.Join(dir, "server.log"))

	if !strings.Contains(alpha, "строка альфы") {
		t.Fatalf("alpha.log без своего сообщения: %q", alpha)
	}
	if strings.Contains(alpha, "служебное") || strings.Contains(alpha, "строка беты") {
		t.Fatalf("alpha.log содержит чужие строки: %q", alpha)
	}
	if !strings.Contains(beta, "строка беты") || !strings.Contains(beta, "WARN") {
		t.Fatalf("beta.log: %q", beta)
	}
	if !strings.Contains(srv, "служебное") {
		t.Fatalf("server.log: %q", srv)
	}
	if strings.Contains(srv, "строка альфы") || strings.Contains(srv, "строка беты") {
		t.Fatalf("server.log содержит строки проектов: %q", srv)
	}
}

// Параллельные проекты (режим serve: несколько сессий одновременно) не
// пересекаются — иначе строки одной сессии попали бы в лог другой.
func TestConcurrentProjectsAreIsolated(t *testing.T) {
	resetLogging(t)
	dir := t.TempDir()
	t.Setenv("LOG_DIR", dir)
	Setup("server")

	names := []string{"proja", "projb", "projc", "projd"}
	const per = 50

	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			log := For(name)
			for i := 0; i < per; i++ {
				log.Infof("%s-%d", name, i)
			}
		}(name)
	}
	wg.Wait()

	for _, name := range names {
		body := readFile(t, filepath.Join(dir, name+".log"))
		if got := strings.Count(body, name+"-"); got != per {
			t.Fatalf("%s.log: своих строк %d, want %d", name, got, per)
		}
		for _, other := range names {
			if other == name {
				continue
			}
			if strings.Contains(body, other+"-") {
				t.Fatalf("%s.log содержит строки %s", name, other)
			}
		}
	}
}

// Нулевой получатель допустим (например, логгер ранера ещё не установлен):
// запись идёт в файл по умолчанию, без паники и без unnamed.log.
func TestNilLoggerFallsBackToDefault(t *testing.T) {
	resetLogging(t)
	dir := t.TempDir()
	t.Setenv("LOG_DIR", dir)
	Setup("server")

	var log *Logger
	log.Infof("из nil-логгера")
	log.Detailf("подробность из nil-логгера")

	body := readFile(t, filepath.Join(dir, "server.log"))
	if !strings.Contains(body, "из nil-логгера") {
		t.Fatalf("nil-логгер не записал в файл по умолчанию: %q", body)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("nil-логгер создал лишний файл: %v", entries)
	}
}

// Attach создаёт файл сразу, Detach закрывает его, а следующая запись
// открывает заново — строки не теряются.
func TestAttachDetachKeepsWriting(t *testing.T) {
	resetLogging(t)
	dir := t.TempDir()
	t.Setenv("LOG_DIR", dir)
	Setup("server")

	Attach("alpha")
	path := filepath.Join(dir, "alpha.log")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Attach не создал файл: %v", err)
	}

	For("alpha").Infof("после attach")
	Detach("alpha")
	For("alpha").Infof("после detach")

	body := readFile(t, path)
	if !strings.Contains(body, "после attach") || !strings.Contains(body, "после detach") {
		t.Fatalf("alpha.log: %q", body)
	}

	// Close закрывает всё; запись после него не падает и не создаёт файлов.
	Close()
	For("alpha").Infof("после close")
}

// Detach не должен закрывать файл по умолчанию, даже если имя совпадает.
func TestDetachDoesNotCloseDefault(t *testing.T) {
	resetLogging(t)
	dir := t.TempDir()
	t.Setenv("LOG_DIR", dir)
	Setup("alpha")

	Detach("alpha")
	Infof("всё ещё пишем")

	if got := readFile(t, filepath.Join(dir, "alpha.log")); !strings.Contains(got, "всё ещё пишем") {
		t.Fatalf("файл по умолчанию закрылся: %q", got)
	}
}

// Setup переиспользует уже открытый проектный поток: два независимых fd в один
// файл теряли бы строки друг друга.
func TestSetupReusesProjectSink(t *testing.T) {
	resetLogging(t)
	dir := t.TempDir()
	t.Setenv("LOG_DIR", dir)

	Setup("server")
	For("alpha").Infof("до setup")
	Setup("alpha")
	Infof("после setup")

	body := readFile(t, filepath.Join(dir, "alpha.log"))
	if !strings.Contains(body, "до setup") || !strings.Contains(body, "после setup") {
		t.Fatalf("alpha.log: %q", body)
	}
	mu.Lock()
	leftover := len(sinks)
	mu.Unlock()
	if leftover != 0 {
		t.Fatalf("в реестре осталось %d потоков, want 0", leftover)
	}
}

// Приведение имён: ProjectLogName — единая точка, по которой файл создают и
// по которой его ищет панель логов.
func TestProjectLogName(t *testing.T) {
	cases := map[string]string{
		"my-project":     "my-project",
		"My_Project.1":   "My_Project.1",
		"my project":     "my_project",
		"../../etc/pass": "etc_pass",
		"проект":         "unnamed",
		"":               "unnamed",
		"***":            "unnamed",
	}
	for in, want := range cases {
		if got := ProjectLogName(in); got != want {
			t.Errorf("ProjectLogName(%q) = %q, want %q", in, got, want)
		}
	}
}

// ProjectFile возвращает путь в каталоге LOG_DIR.
func TestProjectFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOG_DIR", dir)
	if got, want := ProjectFile("alpha"), filepath.Join(dir, "alpha.log"); got != want {
		t.Fatalf("ProjectFile = %q, want %q", got, want)
	}
}
