// Logtail — инкрементальное чтение лог-файлов проектов с сохранением
// позиции, склейкой неполных строк и детекцией ротации (усечение/copytruncate
// или подмена содержимого с совпадением размера).
//
// Модель данных: на проект — набор отслеживаемых файлов (ключ dir::name).
// Каждый файл читается инкрементально: позиция (pos), «хвост» неполной строки
// (remainder) и отпечаток первых байт (head). Подписка идемпотентна и добирает
// новые файлы: лог может появиться позже первого запроса REST-снапша.
package logtail

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

// ScanEvery — каждые N тактов пересканируем каталоги: лог-файл может быть
// создан позже подписки (проект только стартовал, логов ещё нет).
const ScanEvery = 10

// HeadLen — длина отпечатка начала файла для детектирования ротации.
// Одно сравнение размеров не спасает: после copytruncate новый файл может
// сразу оказаться больше старой позиции, и тогда хвост читается с середины.
const HeadLen = 64

// TickInterval — период фонового опроса файлов в Run.
const TickInterval = 200 * time.Millisecond

// Line — полезная нагрузка одной строки лога.
type Line struct {
	Project string `json:"project"`
	File    string `json:"file"`
	Line    string `json:"line"`
}

// Broker — per-project log watcher. Публикует новые строки через Publish.
type Broker struct {
	mu          sync.Mutex
	subscribers map[string]*Subscriber
	publish     func(project string, line Line)
}

// NewBroker создаёт брокер. Publish вызывается на каждую новую строку.
func NewBroker(publish func(project string, line Line)) *Broker {
	return &Broker{
		subscribers: make(map[string]*Subscriber),
		publish:     publish,
	}
}

// Run запускает фоновое сканирование. Выполняется до ctx.Done.
// Вызывать в отдельной горутине: цикл блокирующий.
func (b *Broker) Run(ctx context.Context) {
	ticker := time.NewTicker(TickInterval)
	defer ticker.Stop()
	tick := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick++
			if tick%ScanEvery == 0 {
				b.Rescan()
			}
			b.Broadcast()
		}
	}
}

// Subscribe регистрирует проект и добирает отслеживаемые файлы. Повторный
// вызов идемпотентен: уже открытые файлы не пересоздаются (позиция чтения
// сохраняется), отсутствующие — добавляются.
//
// files — общий список имён на все dirs (вызывающий не знает, в каком каталоге
// какой файл), поэтому несуществующие комбинации отсекаются по Stat.
func (b *Broker) Subscribe(project string, dirs []string, files []string) {
	if project == "" || len(dirs) == 0 {
		return
	}
	b.AddFiles(project, dirs, files)
}

// AddFiles заводит подписку проекта (если её нет) и открывает новые файлы.
func (b *Broker) AddFiles(project string, dirs []string, files []string) {
	b.mu.Lock()
	sub := b.subscribers[project]
	if sub == nil {
		sub = &Subscriber{project: project, files: make(map[string]*SubFile)}
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
			sf := &SubFile{Name: name, Path: path}
			if !sf.OpenAtEnd() {
				continue
			}
			sub.files[key] = sf
			added++
		}
	}
	total := len(sub.files)
	b.mu.Unlock()

	if added > 0 {
		log.Printf("logtail: project=%s +%d файл(ов), отслеживается %d", project, added, total)
	}
}

// Rescan добирает лог-файлы, появившиеся после подписки.
func (b *Broker) Rescan() {
	b.mu.Lock()
	subs := make([]*Subscriber, 0, len(b.subscribers))
	for _, s := range b.subscribers {
		subs = append(subs, s)
	}
	b.mu.Unlock()

	for _, sub := range subs {
		b.AddFiles(sub.project, sub.dirs, ListLogFiles(sub.dirs))
	}
}

// Broadcast опрашивает подписки и публикует появившиеся строки.
func (b *Broker) Broadcast() {
	// Снапшот под мьютексом: Subscribe добавляет файлы конкурентно, а
	// итерация по живой карте одновременно с записью — фатальная ошибка Go.
	type target struct {
		project string
		sf      *SubFile
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
		for _, ln := range t.sf.ReadNew() {
			b.publish(t.project, Line{
				Project: t.project,
				File:    t.sf.Name,
				Line:    ln,
			})
		}
	}
}

// ListLogFiles возвращает имена *.log файлов в перечисленных каталогах.
func ListLogFiles(dirs []string) []string {
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

// Subscriber — подписка проекта.
type Subscriber struct {
	project string
	dirs    []string            // каталоги для рескана (лог может появиться позже)
	files   map[string]*SubFile // ключ: dir + "::" + name
}

// SubFile — состояние отслеживания одного лог-файла.
type SubFile struct {
	Name string // имя файла без каталога — уходит в Line.File
	Path string // полный путь

	file      *os.File
	pos       int64
	remainder string
	head      string // отпечаток первых байт (см. HeadLen)
}

// readHead возвращает отпечаток начала файла: первые min(HeadLen, size) байт.
// Позицию чтения восстанавливает — иначе следующий ReadFull прочитал бы
// пустоту (курсор остался бы на границе отпечатка).
func readHead(f *os.File, size int64) string {
	n := int64(HeadLen)
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

// OpenAtEnd открывает файл и ставит курсор в конец: текущее содержимое уже
// отдано REST-снапшотом, в стрим должны попадать только новые строки.
func (sf *SubFile) OpenAtEnd() bool {
	f, err := os.Open(sf.Path)
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

// OpenFromStart открывает файл с начала: после ротации/пересоздания старое
// содержимое потеряно, новое надо забрать целиком.
func (sf *SubFile) OpenFromStart() bool {
	f, err := os.Open(sf.Path)
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

// Rotated сообщает, подменили ли содержимое файла с момента открытия.
// Сравниваем только сохранённый префикс: файл мог вырасти, но начало у него
// прежнее — это обычная допись, а не ротация.
func (sf *SubFile) Rotated() bool {
	if sf.head == "" {
		return false
	}
	f, err := os.Open(sf.Path)
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

// Close закрывает handle и сбрасывает позицию чтения.
func (sf *SubFile) Close() {
	if sf.file != nil {
		_ = sf.file.Close()
		sf.file = nil
	}
	sf.pos = 0
	sf.remainder = ""
	sf.head = ""
}

// ReadNew дочитывает файл от сохранённой позиции и возвращает полные строки.
// Неполная строка запоминается в remainder до следующего такта. Обрабатывает
// удаление файла и ротацию (усечение либо подмену содержимого) без паники и
// без потери handle.
func (sf *SubFile) ReadNew() []string {
	info, err := os.Stat(sf.Path)
	if err != nil {
		// Файл удалён/недоступен: закрываем handle, при появлении прочитаем
		// заново с начала.
		sf.Close()
		return nil
	}

	if sf.file == nil {
		// Файл (пере)создан после удаления — читаем с начала.
		if !sf.OpenFromStart() {
			return nil
		}
		if info, err = os.Stat(sf.Path); err != nil {
			return nil
		}
	}

	// Ротация: файл усечён (размер меньше позиции) либо его начало не
	// совпадает с отпечатком. Отпечаток сверяем только когда есть новые байты,
	// чтобы не открывать файл попусту на каждом такте.
	stale := info.Size() < sf.pos
	if !stale && info.Size() > sf.pos {
		stale = sf.Rotated()
	}
	if stale {
		sf.Close()
		if !sf.OpenFromStart() {
			return nil
		}
		if info, err = os.Stat(sf.Path); err != nil {
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
