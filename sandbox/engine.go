package sandbox

// engine.go — минимальный клиент Docker Engine API (п. 1.3: запуск через
// Docker API, а не через CLI-обёртку).
//
// Почему свой, а не docker/docker SDK: нужен узкий набор операций (ping,
// сеть, контейнер, exec), а зависимость в ~200 транзитивных пакетов для
// этого невыгодна. Протокол стабилен и прост: JSON поверх HTTP, exec-attach —
// хижиная связка с 8-байтовыми фреймами stdout/stderr.
//
// Адрес демона: CODEGEN_DOCKER_HOST → DOCKER_HOST → unix:///var/run/docker.sock.
// TLS (DOCKER_TLS_VERIFY) пока не поддерживается — терминация mTLS входит в
// Этап 5 (сетевой контур).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai/logging"
)

// defaultDockerSocket — путь к сокету демона по умолчанию.
const defaultDockerSocket = "/var/run/docker.sock"

// Engine — клиент Engine API.
type Engine struct {
	// base — базовый URL запросов (для unix-сокета — фиктивный http://docker,
	// соединение всё равно идёт через dial).
	base string
	// dial — способ добраться до демона; nil — обычный TCP.
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// scheme — http/https (только http см. шапку файла).
	scheme string
	client *http.Client
}

// NewEngineFromEnv собирает клиент по переменным окружения.
func NewEngineFromEnv() (*Engine, error) {
	host := FirstEnv("CODEGEN_DOCKER_HOST", "DOCKER_HOST")
	switch {
	case host == "":
		return newUnixEngine(defaultDockerSocket), nil
	case strings.HasPrefix(host, "unix://"):
		return newUnixEngine(strings.TrimPrefix(host, "unix://")), nil
	case strings.HasPrefix(host, "tcp://"):
		addr := strings.TrimPrefix(host, "tcp://")
		return newTCPEngine("http://" + addr), nil
	case strings.HasPrefix(host, "http://"), strings.HasPrefix(host, "https://"):
		u, err := url.Parse(host)
		if err != nil {
			return nil, fmt.Errorf("sandbox: некорректный DOCKER_HOST %q: %w", host, err)
		}
		if u.Scheme == "https" {
			return nil, fmt.Errorf("sandbox: https:// DOCKER_HOST не поддерживается (TLS — Этап 5)")
		}
		return newTCPEngine(host), nil
	default:
		return nil, fmt.Errorf("sandbox: неподдерживаемая схема DOCKER_HOST %q (ожидается unix:// или tcp://)", host)
	}
}

func newUnixEngine(socket string) *Engine {
	e := &Engine{
		base:   "http://docker",
		scheme: "http",
		dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}
	e.client = &http.Client{Transport: &http.Transport{DialContext: e.dial, DisableCompression: true}}
	return e
}

func newTCPEngine(base string) *Engine {
	e := &Engine{base: strings.TrimRight(base, "/"), scheme: "http"}
	e.client = &http.Client{Transport: &http.Transport{DisableCompression: true}}
	return e
}

// EngineAvailable — доступен ли демон (для выбора auto-режима в LoadConfig).
func EngineAvailable() bool {
	eng, err := NewEngineFromEnv()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return eng.Ping(ctx) == nil
}

// dockerDaemonAvailable — см. EngineAvailable (имя для LoadConfig).
func dockerDaemonAvailable() bool { return EngineAvailable() }

// Ping — проверка демона (GET /_ping).
func (e *Engine) Ping(ctx context.Context) error {
	status, body, err := e.do(ctx, http.MethodGet, "/_ping", nil)
	if err != nil {
		return fmt.Errorf("sandbox: docker-демон недоступен: %w", err)
	}
	if status != http.StatusOK {
		return engineStatusError("ping", status, body)
	}
	return nil
}

// IsDesktop — работает ли демон в виртуальной машине Docker Desktop
// (macOS/Windows). У Desktop адреса bridge-сетей принадлежат гостевой VM:
// процесс на хосте НЕ может слушать на IP шлюза контейнера — egress-прокси
// в этом случае поднимается на самом хосте, а контейнер ходит в него через
// host.docker.internal. Проверка — GET /version: имя лежит в
// Platform.Name («Docker Desktop 4.x»), у нативного демона там «Engine».
func (e *Engine) IsDesktop(ctx context.Context) bool {
	status, body, err := e.do(ctx, http.MethodGet, "/version", nil)
	if err != nil || status != http.StatusOK {
		return false
	}
	var v struct {
		Name     string `json:"Name"`
		Platform struct {
			Name string `json:"Name"`
		} `json:"Platform"`
	}
	if json.Unmarshal(body, &v) != nil {
		return false
	}
	combined := strings.ToLower(v.Name + " " + v.Platform.Name)
	return strings.Contains(combined, "docker desktop")
}

// do — JSON-запрос к API. Возвращает статус и тело (для не-2xx — для ошибок).
func (e *Engine) do(ctx context.Context, method, path string, payload any) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.base+path, body)
	if err != nil {
		return 0, nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

// engineStatusError — ошибка API с текстом демона {"message": "..."}.
func engineStatusError(op string, status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	var j struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &j) == nil && j.Message != "" {
		msg = j.Message
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return fmt.Errorf("sandbox: docker %s: HTTP %d: %s", op, status, msg)
}

// --- Сеть -------------------------------------------------------------

// networkInfo — ответ GET /networks/{id}.
type networkInfo struct {
	ID   string `json:"Id"`
	IPAM struct {
		Config []struct {
			Gateway string `json:"Gateway"`
		} `json:"Config"`
	} `json:"IPAM"`
}

// EnsureInternalNetwork создаёт (если нет) внутреннюю сеть без маршрута на
// ружу и возвращает её шлюз — адрес, на котором контейнеры достучатся до
// egress-прокси на хосте. Хост доступен из контейнера внутренней сети по
// шлюзу: это локальная доставка через bridge-интерфейс, а не форвардинг.
func (e *Engine) EnsureInternalNetwork(ctx context.Context, name string) (string, error) {
	status, body, err := e.do(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil)
	if err != nil {
		return "", err
	}
	if status == http.StatusOK {
		return gatewayOf(body, name)
	}
	if status != http.StatusNotFound {
		return "", engineStatusError("inspect network", status, body)
	}
	create := map[string]any{
		"Name":           name,
		"CheckDuplicate": true,
		"Driver":         "bridge",
		"Internal":       true,
		"Labels":         map[string]string{"ai.sandbox": "egress"},
	}
	status, body, err = e.do(ctx, http.MethodPost, "/networks/create", create)
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated && status != http.StatusConflict {
		return "", engineStatusError("create network", status, body)
	}
	// Inspect после создания: gateway демон назначает на связывании.
	status, body, err = e.do(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", engineStatusError("inspect network", status, body)
	}
	return gatewayOf(body, name)
}

func gatewayOf(body []byte, name string) (string, error) {
	var ni networkInfo
	if err := json.Unmarshal(body, &ni); err != nil {
		return "", fmt.Errorf("sandbox: разбор сети %s: %w", name, err)
	}
	for _, c := range ni.IPAM.Config {
		if c.Gateway != "" {
			return c.Gateway, nil
		}
	}
	return "", fmt.Errorf("sandbox: у сети %s нет шлюза — egress-прокси невозможен", name)
}

// --- Контейнер --------------------------------------------------------

// containerReq — тело POST /containers/create.
type containerReq struct {
	Image      string            `json:"Image"`
	Cmd        []string          `json:"Cmd,omitempty"`
	Env        []string          `json:"Env,omitempty"`
	User       string            `json:"User,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
	WorkingDir string            `json:"WorkingDir,omitempty"`
	HostConfig containerHost     `json:"HostConfig"`
}

// ConnectNetwork — подключить контейнер к сети (второй endpoint). Отдельный
// вызов, а не NetworkingConfig при create: демон при заданном
// HostConfig.NetworkMode additional endpoints в теле create игнорирует —
// проверено на Docker 29 (подключалась только primary-сеть).
func (e *Engine) ConnectNetwork(ctx context.Context, network, containerID string) error {
	status, body, err := e.do(ctx, http.MethodPost,
		"/networks/"+url.PathEscape(network)+"/connect",
		map[string]any{"Container": containerID})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return engineStatusError("connect network", status, body)
	}
	return nil
}

type containerHost struct {
	Binds       []string          `json:"Binds,omitempty"`
	NetworkMode string            `json:"NetworkMode,omitempty"`
	Tmpfs       map[string]string `json:"Tmpfs,omitempty"`
	ReadOnly    bool              `json:"ReadOnly,omitempty"`
	Memory      int64             `json:"Memory,omitempty"`
	NanoCPUs    int64             `json:"NanoCPUs,omitempty"`
	CapDrop     []string          `json:"CapDrop,omitempty"`
	SecurityOpt []string          `json:"SecurityOpt,omitempty"`
}

// CreateContainer создаёт контейнер и возвращает его Id.
func (e *Engine) CreateContainer(ctx context.Context, name string, req containerReq) (string, error) {
	path := "/containers/create"
	if name != "" {
		path += "?name=" + url.QueryEscape(name)
	}
	status, body, err := e.do(ctx, http.MethodPost, path, req)
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated {
		return "", engineStatusError("create container", status, body)
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.ID == "" {
		return "", fmt.Errorf("sandbox: docker не вернул Id контейнера: %s", strings.TrimSpace(string(body)))
	}
	return out.ID, nil
}

// StartContainer запускает контейнер.
func (e *Engine) StartContainer(ctx context.Context, id string) error {
	status, body, err := e.do(ctx, http.MethodPost, "/containers/"+id+"/start", nil)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusOK {
		return engineStatusError("start container", status, body)
	}
	return nil
}

// StopContainer останавливает контейнер (404/304 — не ошибка: уже стоит /
// уже удалён — состояние идемпотентно, очистка (п. 1.7) не должна падать).
func (e *Engine) StopContainer(ctx context.Context, id string, seconds int) error {
	status, body, err := e.do(ctx, http.MethodPost, "/containers/"+id+"/stop?t="+strconv.Itoa(seconds), nil)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusNoContent, http.StatusNotFound, http.StatusNotModified:
		return nil
	default:
		return engineStatusError("stop container", status, body)
	}
}

// RemoveContainer удаляет контейнер (force — вместе с бегущими процессами).
// 404 — уже удалён, не ошибка.
func (e *Engine) RemoveContainer(ctx context.Context, id string, force bool) error {
	path := "/containers/" + url.PathEscape(id) + "?v=true"
	if force {
		path += "&force=true"
	}
	status, body, err := e.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusNoContent, http.StatusNotFound:
		return nil
	default:
		return engineStatusError("remove container", status, body)
	}
}

// InspectContainer — состояние контейнера (диагностика и проверки «контейнер
// удалён»). 404 — отдельная ошибка: вызывающий отличает «нет контейнера» от
// сбоя демона.
func (e *Engine) InspectContainer(ctx context.Context, id string) (map[string]any, error) {
	status, body, err := e.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, fmt.Errorf("sandbox: контейнер %s не найден", id)
	}
	if status != http.StatusOK {
		return nil, engineStatusError("inspect container", status, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("sandbox: разбор inspect: %w", err)
	}
	return out, nil
}

// HasImage — есть ли образ в локальном кэше демона (не пуллим зря).
func (e *Engine) HasImage(ctx context.Context, ref string) (bool, error) {
	status, body, err := e.do(ctx, http.MethodGet, "/images/"+url.PathEscape(ref)+"/json", nil)
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, engineStatusError("inspect image", status, body)
	}
}

// BuildImage собирает образ из тар-контекста (Dockerfile + файлы) и
// помечает тегом. Ответ демона — NDJSON-поток событий; ошибка сборки может
// прийти и при HTTP 200 (в поле error/errorDetail), поэтому парсится
// каждая строка, а не только статус.
//
// buildArgs — значения ARG сборки (видны в Dockerfile), передаются демону
// как buildargs; пустой map аргументы не добавляет.
func (e *Engine) BuildImage(ctx context.Context, tag string, tarData []byte, buildArgs map[string]string) error {
	q := "t=" + url.QueryEscape(tag)
	if len(buildArgs) > 0 {
		raw, err := json.Marshal(buildArgs)
		if err != nil {
			return fmt.Errorf("sandbox: аргументы сборки: %w", err)
		}
		q += "&buildargs=" + url.QueryEscape(string(raw))
	}
	u := e.base + "/build?" + q
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(tarData))
	if err != nil {
		return fmt.Errorf("sandbox: запрос сборки образа: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("sandbox: сборка образа: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return engineStatusError("build image", resp.StatusCode, body)
	}
	var buildErrs []string
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		var msg struct {
			Stream      string `json:"stream"`
			Error       string `json:"error"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		switch {
		case msg.Error != "":
			buildErrs = append(buildErrs, msg.Error)
		case msg.ErrorDetail != nil && msg.ErrorDetail.Message != "":
			buildErrs = append(buildErrs, msg.ErrorDetail.Message)
		case msg.Stream != "":
			// Прогресс демона — в stderr сервера, не в вывод теста.
			logging.Warnf("docker build: %s", strings.TrimRight(msg.Stream, "\n"))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("sandbox: чтение потока сборки: %w", err)
	}
	if len(buildErrs) > 0 {
		return fmt.Errorf("docker build %s: %s", tag, strings.Join(buildErrs, "; "))
	}
	return nil
}

// containerIPv4 — IPv4 контейнера в указанной docker-сети (адрес прокси
// контейнера для env сессии). info — JSON InspectContainer.
func containerIPv4(info map[string]any, network string) (string, error) {
	ns, _ := info["NetworkSettings"].(map[string]any)
	nets, _ := ns["Networks"].(map[string]any)
	n, _ := nets[network].(map[string]any)
	ip, _ := n["IPAddress"].(string)
	if ip == "" {
		return "", fmt.Errorf("sandbox: у контейнера нет IPv4 в сети %s", network)
	}
	return ip, nil
}

// --- Exec --------------------------------------------------------------

// ExecOpts — параметры запуска команды внутри контейнера.
type ExecOpts struct {
	Workdir string
	Env     []string // "K=V", порядок не важен (демон нормализует)
	User    string
	Cmd     []string
}

// ExecCreate создаёт exec-сессию внутри контейнера.
func (e *Engine) execCreate(ctx context.Context, containerID string, o ExecOpts) (string, error) {
	payload := map[string]any{
		"AttachStdout": true,
		"AttachStderr": true,
		"Cmd":          o.Cmd,
		"WorkingDir":   o.Workdir,
		"User":         o.User,
		"Env":          o.Env,
		"Tty":          false,
	}
	status, body, err := e.do(ctx, http.MethodPost, "/containers/"+containerID+"/exec", payload)
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated {
		return "", engineStatusError("create exec", status, body)
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.ID == "" {
		return "", fmt.Errorf("sandbox: docker не вернул Id exec: %s", strings.TrimSpace(string(body)))
	}
	return out.ID, nil
}

// execInspect — статус exec-сессии (ExitCode после завершения).
type execInspect struct {
	ExitCode int  `json:"ExitCode"`
	Running  bool `json:"Running"`
}

func (e *Engine) execInspect(ctx context.Context, execID string) (execInspect, error) {
	status, body, err := e.do(ctx, http.MethodGet, "/exec/"+execID+"/json", nil)
	var out execInspect
	if err != nil {
		return out, err
	}
	if status != http.StatusOK {
		return out, engineStatusError("inspect exec", status, body)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("sandbox: разбор exec: %w", err)
	}
	return out, nil
}

// Exec выполняет команду в контейнере и собирает stdout/stderr.
//
// Attach идёт хижащей связкой: демон отвечает 101/200 и потокует stdout
// (тип 1) и stderr (тип 2) 8-байтовыми фреймами [type,0,0,0,len32 BE].
// По дедлайну ctx связка закрывается (grace — чтобы внутренний timeout
// контейнера успел убить команду и закрыть поток сам), exec снимается
// отдельным Inspect.
func (e *Engine) Exec(ctx context.Context, containerID string, o ExecOpts) (stdout, stderr string, exitCode int, err error) {
	execID, err := e.execCreate(ctx, containerID, o)
	if err != nil {
		return "", "", -1, err
	}
	var outBuf, errBuf bytes.Buffer
	streamErr := e.execAttach(ctx, execID, &outBuf, &errBuf)
	// Дедлайн проверяется СРАЗУ после закрытия потока, до осмотра: команда,
	// которая не завершилась к дедлайну, обязана попасть в TimedOut, даже если
	// контейнерный timeout успел убить её и отдать код 124/137 (иначе модель
	// видит «упала с 137» вместо «остановлена по таймауту»).
	deadlineHit := ctx.Err() != nil

	// Код выхода: после штатного завершения поток закрыт демоном и exec уже
	// финализирован; после обрыва по ctx — best-effort осмотр на свежем
	// контексте (exec может ещё бежать, тогда код неизвестен).
	ictx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	insp, inspErr := e.execInspect(ictx, execID)
	switch {
	case deadlineHit:
		// Таймаут: код не важен — вызывающий помечает TimedOut.
		return outBuf.String(), errBuf.String(), -1, ctx.Err()
	case streamErr == nil && inspErr == nil && !insp.Running:
		exitCode = insp.ExitCode
	default:
		if inspErr != nil {
			return outBuf.String(), errBuf.String(), -1, fmt.Errorf("sandbox: осмотр exec: %w", inspErr)
		}
		if insp.Running {
			return outBuf.String(), errBuf.String(), -1, ctx.Err()
		}
		exitCode = insp.ExitCode
	}
	return outBuf.String(), errBuf.String(), exitCode, streamErr
}

// execAttach — хижина POST /exec/{id}/start и чтение фреймов потока.
func (e *Engine) execAttach(ctx context.Context, execID string, stdout, stderr *bytes.Buffer) error {
	conn, err := e.dialRaw(ctx)
	if err != nil {
		return fmt.Errorf("sandbox: соединение с docker: %w", err)
	}
	defer conn.Close()

	body := `{"Detach":false,"Tty":false}`
	req := "POST /exec/" + execID + "/start HTTP/1.1\r\n" +
		"Host: docker\r\n" +
		"Content-Type: application/json\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: tcp\r\n" +
		"User-Agent: ai-sandbox\r\n" +
		"Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
	if _, err := io.WriteString(conn, req); err != nil {
		return fmt.Errorf("sandbox: exec start: %w", err)
	}

	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		return fmt.Errorf("sandbox: exec start: нет ответа демона: %w", err)
	}
	var proto string
	var code int
	if _, err := fmt.Sscanf(statusLine, "%s %d", &proto, &code); err != nil {
		return fmt.Errorf("sandbox: exec start: некорректный ответ %q", strings.TrimSpace(statusLine))
	}
	// Заголовки до пустой строки (включая возможные 100-continue и
	// хедеры апгрейда) — читаются, но игнорируются: после них — поток фреймов.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return fmt.Errorf("sandbox: exec start: обрыв заголовков: %w", err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	if code != http.StatusSwitchingProtocols && code != http.StatusOK {
		rest, _ := io.ReadAll(br)
		return engineStatusError("exec start", code, rest)
	}

	// Чтение фреймов в горутине: по дедлайну ctx закрываем сокет после grace
	// (внутренний timeout контейнера успевает убить команду и закрыть поток).
	type readResult struct{ err error }
	done := make(chan readResult, 1)
	go func() {
		done <- readResult{err: readFrames(br, stdout, stderr)}
	}()

	var grace time.Duration = 5 * time.Second
	select {
	case res := <-done:
		return res.err
	case <-ctx.Done():
		select {
		case <-done:
			// Поток закрылся сам (timeout контейнера успел) — штатно.
			return nil
		case <-time.After(grace):
			_ = conn.Close()
			<-done
			return ctx.Err()
		}
	}
}

// readFrames демультиплексирует поток attach: фрейм = 8 байт заголовка
// [type,0,0,0,len BE] + полезная нагрузка. type: 0 stdin (не бывает),
// 1 stdout, 2 stderr; прочие типы (например, exit-код в mux-режиме) —
// игнорируются.
func readFrames(br *bufio.Reader, stdout, stderr *bytes.Buffer) error {
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			// Закрытие соединения после обрыва — не ошибка чтения.
			if strings.Contains(err.Error(), "use of closed") {
				return nil
			}
			return fmt.Errorf("sandbox: чтение потока exec: %w", err)
		}
		n := binary.BigEndian.Uint32(hdr[4:8])
		if n == 0 {
			continue
		}
		if n > 64<<20 {
			return fmt.Errorf("sandbox: фрейм exec %d байт больше лимита 64МБ", n)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(br, buf); err != nil {
			return fmt.Errorf("sandbox: чтение фрейма exec: %w", err)
		}
		switch hdr[0] {
		case 1:
			stdout.Write(buf)
		case 2:
			stderr.Write(buf)
		}
	}
}

// dialRaw — сырое соединение для хижиных запросов (тот же диалер, что у
// http-клиента).
func (e *Engine) dialRaw(ctx context.Context) (net.Conn, error) {
	if e.dial != nil {
		return e.dial(ctx, "tcp", "")
	}
	var d net.Dialer
	u, _ := url.Parse(e.base)
	return d.DialContext(ctx, "tcp", u.Host)
}
