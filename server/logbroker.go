// LogBroker — «перематывает» лог-файлы проектов и транслирует новые строки
// в WebSocket-хаб (pub/sub per project, type="log").
//
// Модель данных: на проект — набор отслеживаемых файлов (ключ dir::name).
// Каждый файл читается инкрементально с сохранением позиции (pos) и «хвоста»
// неполной строки (remainder). Подписка идемпотентна и добирает новые файлы:
// лог может появиться позже первого запроса REST-снапшота.

package server

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// logMessage — полезная нагрузка события "log" в WebSocket-хабе.
type logMessage struct {
	Project string `json:"project"`
	File    string `json:"file"`
	Line    string `json:"line"`
}

// logSubscriber — подписка проекта.
type logSubscriber struct {
	project string
	dirs    []string            // каталоги для рескана (лог может появиться позже)
	files   map[string]*subFile // ключ: dir + "::" + name
}

// logBroker — per-project log watcher.
type logBroker struct {
	mu          sync.Mutex
	subscribers map[string]*logSubscriber
	hub         *Hub
}

// scanEvery — каждые N тактов пересканируем каталоги: лог-файл может быть
// создан позже подписки (проект только стартовал, логов ещё нет).
const scanEvery = 10

// headLen — длина отпечатка начала файла для детектирования ротации.
// Одно сравнение размеров не спасает: после copytruncate новый файл может
// сразу оказаться больше старой позиции, и тогда хвост читается с середины.
const headLen = 64

// subFile — состояние отслеживания одного лог-файла.
type subFile struct {
	name string // имя файла без каталога — уходит в logMessage.File
	path string // полный путь

	file      *os.File
	pos       int64
	remainder string
	head      string // отпечаток первых байт (см. headLen)
}

func newLogBroker(h *Hub) *logBroker {
	return &logBroker{
		subscribers: make(map[string]*logSubscriber),
		hub:         h,
	}
}

// Run запускает фоновое сканирование. Выполняется до ctx.Done.
// Вызывать в отдельной горутине: цикл блокирующий.
func (b *logBroker) Run(ctx context.Context) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	tick := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick++
			if tick%scanEvery == 0 {
				b.rescan()
			}
			b.broadcast()
		}
	}
}

// listLogFiles возвращает имена *.log файлов в перечисленных каталогах.
func listLogFiles(dirs []string) []string {
	var names []string
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		infos, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, de := range infos {
			if !de.IsDir() && strings.HasSuffix(strings.ToLower(de.Name()), ".log") {
				names = append(names, de.Name())
			}
		}
	}
	return names
}

// Subscribe регистрирует проект и добирает отслеживаемые файлы. Повторный
// вызов идемпотентен: уже открытые файлы не пересоздаются (позиция чтения
// сохраняется), отсутствующие — добавляются.
//
// files — общий список имён на все dirs (вызывающий не знает, в каком каталоге
// какой файл), поэтому несуществующие комбинации отсекаются по Stat.
func (b *logBroker) Subscribe(project string, dirs []string, files []string) {
	if project == "" || len(dirs) == 0 {
		return
	}
	b.addFiles(project, dirs, files)
}

// addFiles заводит подписку проекта (если её нет) и открывает новые файлы.
func (b *logBroker) addFiles(project string, dirs []string, files []string) {
	b.mu.Lock()
	sub := b.subscribers[project]
	if sub == nil {
		sub = &logSubscriber{project: project, files: make(map[string]*subFile)}
		b.subscribers[project] = sub
	}
	sub.dirs = dirs
	added := 0
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		for _, name := range files {
			if name == "" {
				continue
			}
			key := dir + "::" + name
			if sub.files[key] != nil {
				continue
			}
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err != nil {
				continue
			}
			sf := &subFile{name: name, path: path}
			if !sf.openAtEnd() {
				continue
			}
			sub.files[key] = sf
			added++
		}
	}
	total := len(sub.files)
	b.mu.Unlock()

	if added > 0 {
		log.Printf("logbroker: project=%s +%d файл(ов), отслеживается %d", project, added, total)
	}
}

// rescan добирает лог-файлы, появившиеся после подписки.
func (b *logBroker) rescan() {
	b.mu.Lock()
	subs := make([]*logSubscriber, 0, len(b.subscribers))
	for _, s := range b.subscribers {
		subs = append(subs, s)
	}
	b.mu.Unlock()

	for _, sub := range subs {
		b.addFiles(sub.project, sub.dirs, listLogFiles(sub.dirs))
	}
}

// broadcast опрашивает подписки и публикует появившиеся строки.
func (b *logBroker) broadcast() {
	// Снапшот под мьютексом: Subscribe добавляет файлы конкурентно, а
	// итерация по живой карте одновременно с записью — фатальная ошибка Go.
	type target struct {
		project string
		sf      *subFile
	}
	b.mu.Lock()
	targets := make([]target, 0, 32)
	for _, sub := range b.subscribers {
		for _, sf := range sub.files {
			targets = append(targets, target{project: sub.project, sf: sf})
		}
	}
	b.mu.Unlock()

	for _, t := range targets {
		for _, ln := range t.sf.readNew() {
			b.hub.publish(t.project, "log", logMessage{
				Project: t.project,
				File:    t.sf.name,
				Line:    ln,
			})
		}
	}
}

// readHead возвращает отпечаток начала файла: первые min(headLen, size) байт.
// Позицию чтения восстанавливает — иначе следующий ReadFull прочитал бы
// пустоту (курсор остался бы на границе отпечатка).
func readHead(f *os.File, size int64) string {
	n := int64(headLen)
	if size < n {
		n = size
	}
	if n <= 0 {
		return ""
	}
	at, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return ""
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	buf := make([]byte, n)
	got, _ := io.ReadFull(f, buf)
	if _, err := f.Seek(at, io.SeekStart); err != nil {
		return ""
	}
	return string(buf[:got])
}

// openAtEnd открывает файл и ставит курсор в конец: текущее содержимое уже
// отдано REST-снапшотом, в стрим должны попадать только новые строки.
func (sf *subFile) openAtEnd() bool {
	f, err := os.Open(sf.path)
	if err != nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return false
	}
	head := readHead(f, info.Size())
	if _, err := f.Seek(info.Size(), io.SeekStart); err != nil {
		_ = f.Close()
		return false
	}
	sf.file = f
	sf.pos = info.Size()
	sf.remainder = ""
	sf.head = head
	return true
}

// openFromStart открывает файл с начала: после ротации/пересоздания старое
// содержимое потеряно, новое надо забрать целиком.
func (sf *subFile) openFromStart() bool {
	f, err := os.Open(sf.path)
	if err != nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return false
	}
	sf.file = f
	sf.pos = 0
	sf.remainder = ""
	sf.head = readHead(f, info.Size())
	return true
}

// rotated сообщает, подменили ли содержимое файла с момента открытия.
// Сравниваем только сохранённый префикс: файл мог вырасти, но начало у него
// прежнее — это обычная допись, а не ротация.
func (sf *subFile) rotated() bool {
	if sf.head == "" {
		return false
	}
	f, err := os.Open(sf.path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	cur := readHead(f, info.Size())
	if len(cur) > len(sf.head) {
		cur = cur[:len(sf.head)]
	}
	return cur != sf.head
}

// close закрывает handle и сбрасывает позицию чтения.
func (sf *subFile) close() {
	if sf.file != nil {
		_ = sf.file.Close()
		sf.file = nil
	}
	sf.pos = 0
	sf.remainder = ""
	sf.head = ""
}

// readNew дочитывает файл от сохранённой позиции и возвращает полные строки.
// Неполная строка запоминается в remainder до следующего такта. Обрабатывает
// удаление файла и ротацию (усечение либо подмену содержимого) без паники и
// без потери handle.
func (sf *subFile) readNew() []string {
	info, err := os.Stat(sf.path)
	if err != nil {
		// Файл удалён/недоступен: закрываем handle, при появлении прочитаем
		// заново с начала.
		sf.close()
		return nil
	}

	if sf.file == nil {
		// Файл (пере)создан после удаления — читаем с начала.
		if !sf.openFromStart() {
			return nil
		}
		if info, err = os.Stat(sf.path); err != nil {
			return nil
		}
	}

	// Ротация: файл усечён (размер меньше позиции) либо его начало не
	// совпадает с отпечатком. Отпечаток сверяем только когда есть новые байты,
	// чтобы не открывать файл попусту на каждом такте.
	stale := info.Size() < sf.pos
	if !stale && info.Size() > sf.pos {
		stale = sf.rotated()
	}
	if stale {
		sf.close()
		if !sf.openFromStart() {
			return nil
		}
		if info, err = os.Stat(sf.path); err != nil {
			return nil
		}
	}

	n := info.Size() - sf.pos
	if n <= 0 {
		return nil
	}
	buf := make([]byte, n)
	nn, err := io.ReadFull(sf.file, buf)
	if nn > 0 {
		// Файл могли не дописать — фиксируем реально прочитанное, остаток
		// заберём на следующем такте.
		sf.pos += int64(nn)
	}
	if nn == 0 {
		return nil
	}
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil
	}

	data := buf[:nn]
	if sf.remainder != "" {
		data = append([]byte(sf.remainder), data...)
		sf.remainder = ""
	}

	var lines []string
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			lines = append(lines, strings.TrimSuffix(string(data[start:i]), "\r"))
			start = i + 1
		}
	}
	if start < len(data) {
		sf.remainder = string(data[start:])
	}
	return lines
}
