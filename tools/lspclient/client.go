package lspclient

// Нативный LSP-клиент: запуск языкового сервера по stdio, рукопожатие
// initialize/initialized, открытие документов и навигационные запросы
// (definition/references/hover). Реализация опирается на go.lsp.dev/jsonrpc2
// (фрейминг Content-Length) и go.lsp.dev/protocol (типизированные методы).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai/stackdetect"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// defaultTimeout — таймаут на один LSP-запрос (initialize/навигация/shutdown).
const defaultTimeout = 15 * time.Second

// timeout читает LSP_TIMEOUT (duration вроде "20s" или число секунд).
func timeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("LSP_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultTimeout
}

// RequestTimeout экспортирует таймаут одного LSP-запроса (LSP_TIMEOUT), чтобы
// вызывающая сторона могла задать внешний дедлайн с запасом на запуск сервера.
func RequestTimeout() time.Duration { return timeout() }

// Location — найденная позиция (путь относительно проекта, строки/колонки
// 1-based) в терминах инструментов агента.
type Location struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	EndLine int    `json:"end_line,omitempty"`
	EndCol  int    `json:"end_col,omitempty"`
}

// Hover — информация о символе под курсором.
type Hover struct {
	Contents string `json:"contents"`
	Line     int    `json:"line,omitempty"`
	Col      int    `json:"col,omitempty"`
	EndLine  int    `json:"end_line,omitempty"`
	EndCol   int    `json:"end_col,omitempty"`
}

// Navigator — минимальный контракт навигации, который потребляют инструменты.
// Позволяет подменять клиент в тестах без запуска процесса.
type Navigator interface {
	Definition(ctx context.Context, file string, line, col int) ([]Location, error)
	References(ctx context.Context, file string, line, col int, includeDeclaration bool) ([]Location, error)
	Hover(ctx context.Context, file string, line, col int) (*Hover, error)
}

// Diagnostic — одна нативная диагностика (publishDiagnostics).
type Diagnostic struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Severity string `json:"severity"` // error | warning
	Message  string `json:"message"`
}

// DiagnosticsProvider — источник нативных диагностик по файлам (Ф-4).
type DiagnosticsProvider interface {
	Diagnostics(ctx context.Context, files []string) ([]Diagnostic, error)
}

var (
	_ Navigator           = (*Client)(nil)
	_ DiagnosticsProvider = (*Client)(nil)
)

// diagStore хранит последние publishDiagnostics по URI и будит ожидающих.
type diagStore struct {
	mu      sync.Mutex
	diags   map[uri.URI][]protocol.Diagnostic
	waiters map[uri.URI][]chan struct{}
}

func newDiagStore() *diagStore {
	return &diagStore{diags: make(map[uri.URI][]protocol.Diagnostic), waiters: make(map[uri.URI][]chan struct{})}
}

func (s *diagStore) set(u uri.URI, ds []protocol.Diagnostic) {
	s.mu.Lock()
	s.diags[u] = ds
	ws := s.waiters[u]
	delete(s.waiters, u)
	s.mu.Unlock()
	for _, w := range ws {
		close(w)
	}
}

// wait регистрирует ожидание следующей публикации для URI.
func (s *diagStore) wait(u uri.URI) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan struct{})
	s.waiters[u] = append(s.waiters[u], ch)
	return ch
}

func (s *diagStore) get(u uri.URI) []protocol.Diagnostic {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.diags[u]
}

// clientHandler — обработчик встречных запросов сервера. Встраивает
// UnimplementedClient: все уведомления игнорируются (не рвут соединение), а
// запросы конфигурации получают пустой ответ, чтобы gopls/tsserver не ждали.
// publishDiagnostics складываются в diagStore.
type clientHandler struct {
	protocol.UnimplementedClient
	store *diagStore
}

func (h clientHandler) Configuration(context.Context, *protocol.ConfigurationParams) ([]protocol.LSPAny, error) {
	return []protocol.LSPAny{}, nil
}

func (h clientHandler) PublishDiagnostics(_ context.Context, p *protocol.PublishDiagnosticsParams) error {
	if h.store != nil {
		h.store.set(p.URI, p.Diagnostics)
	}
	return nil
}

// Config — параметры запуска клиента.
type Config struct {
	// Kind — стек проекта (выбор команды сервера).
	Kind stackdetect.Kind
	// Dir — корень проекта: CWD процесса и rootUri/workspaceFolder.
	Dir string
	// Command — явная команда сервера; пусто — ServerCommand(Kind, Dir).
	Command []string
	// Env — дополнительные переменные окружения процесса.
	Env []string
}

// openDoc — последнее состояние документа, отправленное серверу (didOpen/
// didChange), плюс сигнатура файла на диске (stat) для быстрого определения
// «файл не менялся» без чтения содержимого при каждом обращении.
type openDoc struct {
	content string
	size    int64
	mod     time.Time
}

// Client — соединение с одним языковым сервером.
type Client struct {
	dir       string
	kind      stackdetect.Kind
	conn      jsonrpc2.Conn
	server    protocol.Server
	cancel    context.CancelFunc
	transport io.Closer
	diags     *diagStore

	mu      sync.Mutex
	open    map[string]openDoc // абсолютный путь -> последнее отправленное состояние
	version int32

	// pubAvg — экспоненциальное среднее времени ожидания публикаций после
	// синхронизации: адаптивный бюджет для Diagnostics (не ждать LSP_DIAG_WAIT
	// целиком, если сервер успевает быстрее).
	pubAvg time.Duration

	closeOnce sync.Once
}

// Start запускает языковой сервер и выполняет рукопожатие initialize/
// initialized. ctx ограничивает время рукопожатия; время жизни соединения от
// него не зависит (используется отдельный контекст, отменяемый в Close).
func Start(ctx context.Context, cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Dir) == "" {
		return nil, fmt.Errorf("не задана директория проекта")
	}
	dir, err := filepath.Abs(cfg.Dir)
	if err != nil {
		return nil, err
	}

	command := cfg.Command
	if len(command) == 0 {
		command, err = ServerCommand(cfg.Kind, dir)
		if err != nil {
			return nil, err
		}
	}

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), cfg.Env...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin сервера: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout сервера: %w", err)
	}
	// stderr дренируем в буфер для диагностики ошибок запуска; syncBuffer
	// защищает от гонки с горутиной exec.
	var stderr syncBuffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("не удалось запустить %s: %w", strings.Join(command, " "), err)
	}

	transport := &stdioConn{cmd: cmd, stdin: stdin, stdout: stdout}
	stream := jsonrpc2.NewStream(transport)

	c, err := newClient(ctx, stream, dir, cfg.Kind)
	if err != nil {
		_ = transport.Close()
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			msg = ": " + truncate(msg, 300)
		}
		return nil, fmt.Errorf("рукопожатие LSP не удалось (%s)%s", err, msg)
	}
	c.transport = transport
	return c, nil
}

// newClient поднимает соединение поверх готового Stream и выполняет
// рукопожатие. Используется Start (stdio) и тестами (in-memory pair).
func newClient(ctx context.Context, stream jsonrpc2.Stream, dir string, kind stackdetect.Kind) (*Client, error) {
	dir = canonical(dir)
	store := newDiagStore()
	connCtx, cancel := context.WithCancel(context.Background())
	_, conn, server := protocol.NewClient(connCtx, clientHandler{store: store}, stream)

	c := &Client{
		dir:    dir,
		kind:   kind,
		conn:   conn,
		server: server,
		cancel: cancel,
		diags:  store,
		open:   make(map[string]openDoc),
	}
	if err := c.initialize(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// initialize выполняет initialize + initialized.
func (c *Client) initialize(ctx context.Context) error {
	root := uri.File(c.dir)
	cctx, cancel := context.WithTimeout(ctx, timeout())
	defer cancel()

	params := &protocol.InitializeParams{
		ProcessID: int32Ptr(int32(os.Getpid())),
		RootURI:   &root,
		ClientInfo: protocol.ClientInfo{
			Name: "ai-agent",
		},
		Capabilities: protocol.ClientCapabilities{
			TextDocument: &protocol.TextDocumentClientCapabilities{
				Synchronization: &protocol.TextDocumentSyncClientCapabilities{},
				Definition:      &protocol.DefinitionClientCapabilities{},
				References:      &protocol.ReferenceClientCapabilities{},
				Hover: &protocol.HoverClientCapabilities{
					ContentFormat: []protocol.MarkupKind{protocol.MarkupKindMarkdown, protocol.MarkupKindPlainText},
				},
			},
			Workspace: &protocol.WorkspaceClientCapabilities{
				WorkspaceFolders: boolPtr(true),
				Configuration:    boolPtr(true),
			},
		},
		WorkspaceFoldersInitializeParams: protocol.WorkspaceFoldersInitializeParams{
			WorkspaceFolders: protocol.NewNullable([]protocol.WorkspaceFolder{{URI: root, Name: filepath.Base(c.dir)}}),
		},
	}
	if _, err := c.server.Initialize(cctx, params); err != nil {
		return err
	}
	return c.server.Initialized(cctx, &protocol.InitializedParams{})
}

// Alive сообщает, живо ли соединение.
func (c *Client) Alive() bool {
	select {
	case <-c.conn.Done():
		return false
	default:
		return true
	}
}

// Definition возвращает определения символа в позиции (file относительно
// проекта, line/col 1-based).
func (c *Client) Definition(ctx context.Context, file string, line, col int) ([]Location, error) {
	abs, _, err := c.ensureOpen(ctx, file)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout())
	defer cancel()
	res, err := c.server.Definition(cctx, &protocol.DefinitionParams{
		TextDocumentPositionParams: position(abs, line, col),
	})
	if err != nil {
		return nil, err
	}
	return c.convert(definitionLocations(res)), nil
}

// References возвращает ссылки на символ в позиции.
func (c *Client) References(ctx context.Context, file string, line, col int, includeDeclaration bool) ([]Location, error) {
	abs, _, err := c.ensureOpen(ctx, file)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout())
	defer cancel()
	locs, err := c.server.References(cctx, &protocol.ReferenceParams{
		TextDocumentPositionParams: position(abs, line, col),
		Context:                    protocol.ReferenceContext{IncludeDeclaration: includeDeclaration},
	})
	if err != nil {
		return nil, err
	}
	return c.convert(locs), nil
}

// Hover возвращает информацию о символе в позиции.
func (c *Client) Hover(ctx context.Context, file string, line, col int) (*Hover, error) {
	abs, _, err := c.ensureOpen(ctx, file)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout())
	defer cancel()
	h, err := c.server.Hover(cctx, &protocol.HoverParams{
		TextDocumentPositionParams: position(abs, line, col),
	})
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, nil
	}
	out := &Hover{Contents: hoverText(h.Contents)}
	if h.Range != nil {
		out.Line = int(h.Range.Start.Line) + 1
		out.Col = int(h.Range.Start.Character) + 1
		out.EndLine = int(h.Range.End.Line) + 1
		out.EndCol = int(h.Range.End.Character) + 1
	}
	return out, nil
}

// diagItem — файл, ожидающий публикацию диагностик.
type diagItem struct {
	file string
	u    uri.URI
	ch   chan struct{}
}

// Diagnostics возвращает нативные диагностики по перечисленным файлам
// (publishDiagnostics). Изменённые файлы открываются/синхронизируются, после
// чего клиент ждёт публикации; неизменённые (сигнатура файла на диске та же) —
// отдают кэш diagStore сразу, не дожидаясь сервера. Бюджет ожидания адаптивный
// (см. diagBudget): измеренная на проекте латентность публикаций без лишнего
// ожидания при быстрых серверах. Пути к файлам — относительно проекта;
// в Diagnostic.File — тоже.
func (c *Client) Diagnostics(ctx context.Context, files []string) ([]Diagnostic, error) {
	items := make([]diagItem, 0, len(files))
	for _, f := range files {
		abs, err := c.resolve(f)
		if err != nil {
			return nil, err
		}
		u := uri.File(abs)
		items = append(items, diagItem{file: f, u: u, ch: c.diags.wait(u)})
	}
	// Открываем/синхронизируем файлы: это инициирует публикацию. Ожидание нужно
	// только там, где реально ушли didOpen/didChange; у неизменённых файлов
	// waiter не используется и отдаётся кэш.
	pending := make([]diagItem, 0, len(items))
	for _, it := range items {
		if _, changed, err := c.ensureOpen(ctx, it.file); err != nil {
			return nil, err
		} else if changed {
			pending = append(pending, it)
		}
	}

	uris := make([]uri.URI, 0, len(items))
	for _, it := range items {
		uris = append(uris, it.u)
	}

	// Пустых публикаций не ждём вообще. Публикации ждём с общим бюджетом
	// (адаптивным), так что суммарное ожидание не превышает diagBudget.
	if len(pending) > 0 {
		budget := time.After(c.diagBudget())
		start := time.Now()
		for _, it := range pending {
			select {
			case <-it.ch:
			case <-ctx.Done():
				return c.collectDiagnostics(uris), ctx.Err()
			case <-budget:
				return c.collectDiagnostics(uris), nil
			}
		}
		c.learnLatency(time.Since(start))
	}
	return c.collectDiagnostics(uris), nil
}

func (c *Client) collectDiagnostics(uris []uri.URI) []Diagnostic {
	out := make([]Diagnostic, 0)
	for _, u := range uris {
		rel, ok := c.relFile(u)
		if !ok {
			continue
		}
		for _, d := range c.diags.get(u) {
			sev := diagSeverity(d.Severity)
			if sev == "" {
				continue // information/hint отбрасываем, как CLI-парсеры
			}
			out = append(out, Diagnostic{
				File:     rel,
				Line:     int(d.Range.Start.Line) + 1,
				Col:      int(d.Range.Start.Character) + 1,
				Severity: sev,
				Message:  diagMessageText(d.Message),
			})
		}
	}
	return out
}

// diagMessageText извлекает текст сообщения диагностики (string | *MarkupContent).
func diagMessageText(m protocol.InlayHintTooltip) string {
	switch v := m.(type) {
	case protocol.String:
		return strings.TrimSpace(string(v))
	case *protocol.MarkupContent:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(v.Value)
	default:
		return ""
	}
}

// diagSeverity переводит LSP severity в строку; info/hint — пусто.
func diagSeverity(s protocol.DiagnosticSeverity) string {
	switch s {
	case protocol.DiagnosticSeverityError, 0:
		// 0 (поле опущено) по спецификации трактуется как error.
		return "error"
	case protocol.DiagnosticSeverityWarning:
		return "warning"
	default:
		return ""
	}
}

// diagWait — пауза ожидания publishDiagnostics (LSP_DIAG_WAIT, по умолчанию 3s).
func diagWait() time.Duration {
	if v := strings.TrimSpace(os.Getenv("LSP_DIAG_WAIT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 3 * time.Second
}

// minDiagBudget — нижняя граница адаптивного бюджета ожидания публикаций.
const minDiagBudget = 250 * time.Millisecond

// diagBudget возвращает бюджет ожидания publishDiagnostics для Diagnostics.
// Адаптивный: если латентность публикаций на проекте уже измерена, бюджет —
// 4x от неё (но не меньше minDiagBudget и не больше LSP_DIAG_WAIT); иначе —
// LSP_DIAG_WAIT целиком.
func (c *Client) diagBudget() time.Duration {
	max := diagWait()
	c.mu.Lock()
	avg := c.pubAvg
	c.mu.Unlock()
	if avg <= 0 {
		return max
	}
	b := avg * 4
	if b < minDiagBudget {
		b = minDiagBudget
	}
	if b > max {
		b = max
	}
	return b
}

// learnLatency обновляет экспоненциальное среднее латентности публикаций.
// Вызывается только когда все ожидаемые публикации реально пришли (без срабатывания
// бюджета/контекста), чтобы срез не засорялся таймаутами.
func (c *Client) learnLatency(d time.Duration) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pubAvg <= 0 {
		c.pubAvg = d
	} else {
		c.pubAvg = (c.pubAvg*3 + d) / 4
	}
}

// Close отправляет shutdown/exit и гарантированно завершает процесс.
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = c.server.Shutdown(cctx)
		_ = c.server.Exit(cctx)
		cancel()
		c.cancel()
		err = c.conn.Close()
		if c.transport != nil {
			_ = c.transport.Close()
		}
	})
	return err
}

// ensureOpen открывает документ при первом обращении (didOpen) и синхронизирует
// содержимое (didChange) при последующих изменениях. Возвращает абсолютный путь
// и changed=true, если серверу реально ушёл didOpen/didChange (значит, стоит
// ждать publishDiagnostics). Если сигнатура файла на диске (stat: размер+mtime)
// не изменилась — содержимое не перечитывается и сервер не трогается
// (changed=false); это основной путь для повторных вызовов Diagnostics по
// штатным файлам.
func (c *Client) ensureOpen(ctx context.Context, file string) (string, bool, error) {
	abs, err := c.resolve(file)
	if err != nil {
		return "", false, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", false, fmt.Errorf("файл не доступен: %w", err)
	}

	c.mu.Lock()
	prev, seen := c.open[abs]
	if seen && prev.size == info.Size() && prev.mod.Equal(info.ModTime()) {
		// Стат не менялся — содержимое то же (или файл перезаписан так, что
		// размер и время не изменились; редкий край читать нет смысла).
		c.mu.Unlock()
		return abs, false, nil
	}
	c.mu.Unlock()

	data, err := os.ReadFile(abs)
	if err != nil {
		return "", false, fmt.Errorf("файл не читается: %w", err)
	}
	content := string(data)
	u := uri.File(abs)

	c.mu.Lock()
	prev, seen = c.open[abs]
	c.open[abs] = openDoc{content: content, size: info.Size(), mod: info.ModTime()}
	c.version++
	version := c.version
	c.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, timeout())
	defer cancel()

	if !seen {
		// Первый didOpen для файла — содержимое открыто свежим чтением.
		return abs, true, c.server.DidOpen(cctx, &protocol.DidOpenTextDocumentParams{
			TextDocument: protocol.TextDocumentItem{
				URI:        u,
				LanguageID: languageFor(file, c.kind),
				Version:    version,
				Text:       content,
			},
		})
	}
	if prev.content == content {
		// Стат изменился, но содержимое то же (например touch) — синхронизация
		// не нужна, публикация не придёт.
		return abs, false, nil
	}
	return abs, true, c.server.DidChange(cctx, &protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: u},
			Version:                version,
		},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{
			&protocol.TextDocumentContentChangeWholeDocument{Text: content},
		},
	})
}

// resolve приводит относительный путь файла к абсолютному в пределах проекта.
func (c *Client) resolve(file string) (string, error) {
	name := strings.TrimSpace(filepath.ToSlash(file))
	if name == "" {
		return "", fmt.Errorf("не задан file")
	}
	if filepath.IsAbs(filepath.FromSlash(name)) {
		return "", fmt.Errorf("абсолютный путь запрещён: %s", file)
	}
	full := filepath.Clean(filepath.Join(c.dir, filepath.FromSlash(name)))
	root := filepath.Clean(c.dir)
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", fmt.Errorf("путь выходит за пределы проекта: %s", file)
	}
	return full, nil
}

// convert переводит позиции сервера в Location с путями относительно проекта.
func (c *Client) convert(locs []protocol.Location) []Location {
	out := make([]Location, 0, len(locs))
	for _, l := range locs {
		rel, ok := c.relFile(l.URI)
		if !ok {
			continue
		}
		out = append(out, Location{
			File:    rel,
			Line:    int(l.Range.Start.Line) + 1,
			Col:     int(l.Range.Start.Character) + 1,
			EndLine: int(l.Range.End.Line) + 1,
			EndCol:  int(l.Range.End.Character) + 1,
		})
	}
	return out
}

// relFile переводит file://-URI в путь относительно проекта. Пути вне проекта
// (зависимости, stdlib) отбрасываются.
func (c *Client) relFile(u uri.URI) (string, bool) {
	if !u.IsFile() {
		return "", false
	}
	rel, err := filepath.Rel(c.dir, canonical(u.FsPath()))
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// definitionLocations приводит union DefinitionResult к срезу Location.
func definitionLocations(res protocol.DefinitionResult) []protocol.Location {
	switch r := res.(type) {
	case *protocol.Location:
		if r == nil {
			return nil
		}
		return []protocol.Location{*r}
	case protocol.LocationSlice:
		return r
	case protocol.DefinitionLinkSlice:
		out := make([]protocol.Location, 0, len(r))
		for _, l := range r {
			out = append(out, protocol.Location{URI: l.TargetURI, Range: l.TargetSelectionRange})
		}
		return out
	default:
		return nil
	}
}

// hoverText извлекает текст из union HoverContents.
func hoverText(h protocol.HoverContents) string {
	switch v := h.(type) {
	case protocol.String:
		return strings.TrimSpace(string(v))
	case *protocol.MarkupContent:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(v.Value)
	case *protocol.MarkedStringWithLanguage:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(v.Value)
	case protocol.MarkedStringSlice:
		parts := make([]string, 0, len(v))
		for _, m := range v {
			switch mv := m.(type) {
			case protocol.String:
				parts = append(parts, string(mv))
			case *protocol.MarkedStringWithLanguage:
				if mv != nil {
					parts = append(parts, mv.Value)
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return ""
	}
}

// position собирает TextDocumentPositionParams (line/col 1-based → 0-based).
func position(abs string, line, col int) protocol.TextDocumentPositionParams {
	return protocol.TextDocumentPositionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(abs)},
		Position: protocol.Position{
			Line:      uint32(max(0, line-1)),
			Character: uint32(max(0, col-1)),
		},
	}
}

// stdioConn — io.ReadWriteCloser поверх stdio дочернего процесса; Close
// закрывает пайпы и убивает процесс (идемпотентно).
type stdioConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	once   sync.Once
}

func (c *stdioConn) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *stdioConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

func (c *stdioConn) Close() error {
	c.once.Do(func() {
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		_ = c.cmd.Wait()
	})
	return nil
}

// syncBuffer — потокобезопасный буфер для stderr процесса.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func int32Ptr(v int32) *int32 { return &v }
func boolPtr(v bool) *bool    { return &v }

// canonical приводит путь к абсолютному с раскрытием симлинков (на macOS
// /var → /private/var), чтобы пути сервера и клиента сравнивались корректно.
func canonical(path string) string {
	if strings.TrimSpace(path) == "" {
		return path
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		return resolved
	}
	return abs
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
