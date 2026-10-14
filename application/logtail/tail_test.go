package logtail

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// appendToFile дописывает строки в лог-файл.
func appendToFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertLines(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("строки = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("строки[%d] = %q, want %q (все: %v)", i, got[i], want[i], got)
		}
	}
}

// recordingBroker собирает опубликованные строки (под b.mu).
func recordingBroker() (*Broker, *[]Line) {
	var b *Broker
	var got []Line
	b = NewBroker(func(project string, l Line) {
		b.mu.Lock()
		got = append(got, l)
		b.mu.Unlock()
	})
	return b, &got
}

// Содержимое до подписки не дублируется в стрим: его уже отдал REST-снапшот.
func TestBrokerSkipsPreexistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proj.log")
	if err := os.WriteFile(path, []byte("старая\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	b, _ := recordingBroker()
	b.Subscribe("proj", []string{dir}, ListLogFiles([]string{dir}))
	b.Broadcast() // до новых записей — тишина

	appendToFile(t, path, "свежая\n")
	b.Broadcast()

	b.mu.Lock()
	sf := b.subscribers["proj"].files[dir+"::proj.log"]
	b.mu.Unlock()
	if sf == nil {
		t.Fatal("файл не отслеживается")
	}
	assertLines(t, sf.ReadNew(), nil) // всё уже разобрано Broadcast
}

// Незавершённая строка копится в remainder и склеивается на следующем такте.
func TestSubFileReadNewHandlesPartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proj.log")
	if err := os.WriteFile(path, []byte("нулевая\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sf := &SubFile{Name: "proj.log", Path: path}
	if !sf.OpenAtEnd() {
		t.Fatal("OpenAtEnd = false")
	}
	if got := sf.ReadNew(); len(got) != 0 {
		t.Fatalf("до записи строк быть не должно: %v", got)
	}

	appendToFile(t, path, "первая\nвторая\nнепол")
	assertLines(t, sf.ReadNew(), []string{"первая", "вторая"})
	if sf.remainder != "непол" {
		t.Fatalf("remainder = %q, want «непол»", sf.remainder)
	}

	appendToFile(t, path, "ная\nтретья\n")
	assertLines(t, sf.ReadNew(), []string{"неполная", "третья"})
	if sf.remainder != "" {
		t.Fatalf("remainder = %q, want пусто", sf.remainder)
	}
}

// Удаление файла и ротация не должны ронять ReadNew (раньше sf.file = nil
// давал панику на следующем такте) и должны перечитывать файл с начала.
func TestSubFileReadNewSurvivesDeleteAndRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proj.log")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sf := &SubFile{Name: "proj.log", Path: path}
	if !sf.OpenAtEnd() {
		t.Fatal("OpenAtEnd = false")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := sf.ReadNew(); got != nil {
		t.Fatalf("после удаления строк быть не должно: %v", got)
	}
	if sf.file != nil {
		t.Fatal("handle не закрыт после удаления файла")
	}
	// Повторный такт на отсутствующем файле — без паники.
	if got := sf.ReadNew(); got != nil {
		t.Fatalf("после удаления (2-й такт): %v", got)
	}

	if err := os.WriteFile(path, []byte("новая1\nновая2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertLines(t, sf.ReadNew(), []string{"новая1", "новая2"})

	// Ротация, когда новый файл БОЛЬШЕ старой позиции: одно сравнение размеров
	// её не ловит, хвост читался бы с середины чужого содержимого.
	if err := os.WriteFile(path, []byte("после ротации\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertLines(t, sf.ReadNew(), []string{"после ротации"})

	// Обычная допись ротацией не считается — иначе строки дублировались бы.
	appendToFile(t, path, "продолжение\n")
	assertLines(t, sf.ReadNew(), []string{"продолжение"})

	// Усечение (copytruncate): файл стал короче позиции.
	if err := os.WriteFile(path, []byte("короче\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertLines(t, sf.ReadNew(), []string{"короче"})
}

// Подписка идемпотентна и добирает файлы, созданные позже (Rescan): проект
// может стартовать вообще без логов.
func TestBrokerSubscribeIdempotentAndRescans(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "proj.log"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	b, _ := recordingBroker()
	b.Subscribe("p", []string{dir}, ListLogFiles([]string{dir}))
	b.Subscribe("p", []string{dir}, ListLogFiles([]string{dir}))

	b.mu.Lock()
	n := len(b.subscribers["p"].files)
	b.mu.Unlock()
	if n != 1 {
		t.Fatalf("отслеживается файлов = %d, want 1 (дублей быть не должно)", n)
	}

	// Подписка при отсутствии логов — каталоги запоминаются.
	b.Subscribe("fresh", []string{dir}, ListLogFiles([]string{dir}))
	if err := os.Remove(filepath.Join(dir, "proj.log")); err != nil {
		t.Fatal(err)
	}
	late := filepath.Join(dir, "late.log")
	if err := os.WriteFile(late, []byte("поздняя\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.Rescan()

	b.mu.Lock()
	_, ok := b.subscribers["fresh"].files[dir+"::late.log"]
	b.mu.Unlock()
	if !ok {
		t.Fatal("Rescan не подобрал лог-файл, созданный после подписки")
	}
}

// Конкурентные Subscribe (HTTP-хендлеры) и Broadcast/Rescan (тикер) не должны
// давать гонку на карте подписчиков. Запускать с -race.
func TestBrokerConcurrentSubscribeAndBroadcast(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proj.log")
	if err := os.WriteFile(path, []byte("start\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	b, _ := recordingBroker()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				b.Rescan()
				b.Broadcast()
			}
		}
	}()

	for i := 0; i < 20; i++ {
		b.Subscribe(fmt.Sprintf("p%d", i), []string{dir}, ListLogFiles([]string{dir}))
		appendToFile(t, path, fmt.Sprintf("line %d\n", i))
	}
	cancel()
	<-done

	b.mu.Lock()
	subs := len(b.subscribers)
	b.mu.Unlock()
	if subs != 20 {
		t.Fatalf("подписок = %d, want 20", subs)
	}
}

// ListLogFiles отбирает только *.log и не падает на отсутствующем каталоге.
func TestListLogFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.log", "b.LOG", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.log"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, n := range ListLogFiles([]string{dir, filepath.Join(dir, "нет-такого")}) {
		got[n] = true
	}
	if !got["a.log"] || !got["b.LOG"] {
		t.Fatalf("не собраны *.log: %v", got)
	}
	if got["c.txt"] || got["sub.log"] {
		t.Fatalf("лишнее в списке: %v", got)
	}
}

// Очистка логов обрезает файл и тут же пишет заголовок, а агент может
// дописать строки ДО следующего такта брокера. Если обрезка не была поймана
// по размеру (новая запись успела перекрыть старую позицию), стрим обязан
// взять файл с начала по отпечатку — иначе хвост читался бы с середины
// чужого содержимого и в панель попадали бы обрывки.
func TestSubFileReadNewAfterTruncateWithFastRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proj.log")
	if err := os.WriteFile(path, []byte("старое содержимое\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sf := &SubFile{Name: "proj.log", Path: path}
	if !sf.OpenAtEnd() {
		t.Fatal("OpenAtEnd = false")
	}
	pos := sf.pos

	// Новый файл ЛОНЖЕ прежней позиции: сравнение размеров ротацию не видит.
	fresh := "=== Логи очищены ===\n"
	for len(fresh) <= int(pos) {
		fresh += "новая строка после очистки\n"
	}
	if err := os.WriteFile(path, []byte(fresh), 0o644); err != nil {
		t.Fatal(err)
	}
	want := strings.Split(strings.TrimSuffix(fresh, "\n"), "\n")
	assertLines(t, sf.ReadNew(), want)
}

// Run публикует новые строки через Publish и завершается по ctx.
func TestBrokerRunPublishesAndStops(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proj.log")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	b, got := recordingBroker()
	b.Subscribe("p", []string{dir}, ListLogFiles([]string{dir}))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); b.Run(ctx) }()

	appendToFile(t, path, "первая\nвторая\n")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		n := len(*got)
		b.mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	b.mu.Lock()
	defer b.mu.Unlock()
	if len(*got) != 2 {
		t.Fatalf("опубликовано строк = %d, want 2: %+v", len(*got), *got)
	}
	if (*got)[0].Line != "первая" || (*got)[1].Line != "вторая" {
		t.Fatalf("строки = %+v", *got)
	}
	if (*got)[0].Project != "p" || (*got)[0].File != "proj.log" {
		t.Fatalf("метаданные строки = %+v", (*got)[0])
	}
}
