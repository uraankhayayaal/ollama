package sandbox

// egress.go — сетевая изоляция сессии и белый список доменов (п. 1.10, 1.11).
//
// Схема. Сессионный контейнер сидит в ВНУТРЕННЕЙ docker-сети (bridge без
// маршрута наружу): наружу пакеты просто не идут, поэтому «обход прокси»
// сырым сокетом невозможен. Единственный выход из сети — шлюз, где слушает
// этот HTTP(S)-прокси. Прокси пускает только домены из
// CODEGEN_SANDBOX_ALLOW_DOMAINS и отвечает 403 на всё остальное. Прокси
// живёт в процессе сервера.
//
// Где слушает прокси: нативный Linux — на IP шлюза внутренней сети (шлюз и
// есть хост, LAN до него не достучится). Docker Desktop (macOS/Windows) —
// шлюз принадлежит гостевой VM, хост на нём слушать не может и маршрут до
// хоста из internal-сети не существует: поднимается gateway-КОНТЕЙНЕР
// (proxycontainer.go) — он одновременно сидит в internal-сети сессии и в
// обычной сети с выходом в интернет, а прокси внутри него исполняет наш
// статик-бинарь cmd/sandbox-proxy (ServeEgressProxy).
//
// Что это даёт и чего не даёт:
//   - даёт: нет прямого интернета (1.10), загрузка только с разрешённых
//     доменов — goproxy, реестры пакетов (1.11);
//   - не даёт: изоляции от хоста — контейнер видит служебные порты хоста по
//     адресу шлюза (как и любой контейнер bridge-сети). Это прежний уровень
//     ephemeral-песочницы; закрытие — сетевые политики Этапа 5.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ai/logging"
)

// egressNetworkName — внутренняя сеть сессий. Одна на процесс: сеть
// переиспользуется сессиями и остаётся после их конца (внутренняя сеть без
// контейнеров ничего не стоит); удаление создало бы гонку между
// закрывающейся и стартующей сессиями.
const egressNetworkName = "ai-sandbox-egress"

// DomainAllowed — подходит ли домен под белый список.
// Правила: "example.com" — точное совпадение; "*.example.com" — поддомены и
// сам домен. Сравнение без учёта регистра, точка в конце игнорируется.
func DomainAllowed(host string, patterns []string) bool {
	host = normalizeHost(host)
	if host == "" {
		return false
	}
	for _, p := range patterns {
		p = normalizeHost(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "*.") {
			suffix := p[1:] // ".example.com"
			if host == p[2:] || strings.HasSuffix(host, suffix) {
				return true
			}
			continue
		}
		if host == p {
			return true
		}
	}
	return false
}

func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimSuffix(h, ".")
	// Имя, а не host:port: порт для whitelist-правил нерелевантен.
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		if _, err := fmt.Sscanf(h[i+1:], "%d", new(int)); err == nil {
			h = h[:i]
		}
	}
	return h
}

// egressProxy — HTTP(S)-прокси с белым списком для сессионных контейнеров
// на шлюзе хоста (нативный Linux).
type egressProxy struct {
	ln   net.Listener
	srv  *http.Server
	addr string // host:port прокси С ТОЧКИ ЗРЕНИЯ КОНТЕЙНЕРА (env-переменные)
}

// startEgressProxy поднимает прокси.
//
//	bind — адрес listen на хосте: gateway внутренней сети (нативный Linux,
//		шлюз принадлежит хосту) или 127.0.0.1 (Docker Desktop — шлюз в
//		гостевой VM, хост на нём слушать не может);
//	dial — адрес, по которому контейнер достучится до прокси: тот же gateway
//		на Linux, host.docker.internal на Docker Desktop.
func startEgressProxy(bind, dial string, allow []string) (*egressProxy, error) {
	if bind == "" {
		return nil, fmt.Errorf("sandbox: нет адреса для egress-прокси")
	}
	if dial == "" {
		dial = bind
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, "0"))
	if err != nil {
		return nil, fmt.Errorf("sandbox: egress-прокси на %s: %w", bind, err)
	}
	// Порт берём у слушателя (ноль в bind), адрес прокси — dial-хост:
	// на Desktop слушаем 127.0.0.1, а контейнеру отдаём host.docker.internal.
	_, port, splitErr := net.SplitHostPort(ln.Addr().String())
	if splitErr != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("sandbox: адрес egress-прокси %q: %w", ln.Addr(), splitErr)
	}
	p := &egressProxy{
		ln:   ln,
		addr: net.JoinHostPort(dial, port),
	}
	p.srv = &http.Server{Handler: NewEgressHandler(allow), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = p.srv.Serve(ln) }()
	logging.Warnf("sandbox: egress-прокси поднят на %s (адрес контейнера %s), белый список: %s",
		ln.Addr(), p.addr, strings.Join(allow, ", "))
	return p, nil
}

// env — прокси-переменные для контейнера с адресом прокси p.addr.
func (p *egressProxy) env() map[string]string { return proxyEnv(p.addr) }

// proxyEnv — прокси-переменные контейнера (HTTP и HTTPS-трафик идут через
// прокси; localhost — мимо, он и без того локален). addr — адрес прокси с
// точки зрения контейнера: шлюз хоста (Linux) или IP gateway-контейнера
// (Docker Desktop).
func proxyEnv(addr string) map[string]string {
	proxyURL := "http://" + addr
	return map[string]string{
		"HTTP_PROXY":  proxyURL,
		"HTTPS_PROXY": proxyURL,
		"http_proxy":  proxyURL,
		"https_proxy": proxyURL,
		"NO_PROXY":    "localhost,127.0.0.1",
		"no_proxy":    "localhost,127.0.0.1",
	}
}

// Close останавливает прокси.
func (p *egressProxy) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return p.srv.Shutdown(ctx)
}

// egressFilter — обработчик прокси с белым списком. Живёт и на шлюзе хоста
// (Linux), и внутри gateway-контейнера (Docker Desktop) — там, где слушает
// ServeEgressProxy.
type egressFilter struct {
	allow []string
}

// NewEgressHandler — HTTP-обработчик прокси с заданным белым списком.
func NewEgressHandler(allow []string) http.Handler {
	return &egressFilter{allow: allow}
}

// ServeHTTP — обработчик прокси: CONNECT для HTTPS, абсолютный URL для HTTP.
func (f *egressFilter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		f.handleConnect(w, r)
		return
	}
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	if !DomainAllowed(host, f.allow) {
		f.deny(w, host)
		return
	}
	// Обычный HTTP: forward через штатный транспорт (URI должен быть
	// абсолютным — прокси-форма запроса это уже гарантирует).
	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	resp, err := http.DefaultTransport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, "упроксированный запрос не прошёл: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// handleConnect — туннель для HTTPS: разрешён только белый список доменов.
func (f *egressFilter) handleConnect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if !DomainAllowed(target, f.allow) {
		f.deny(w, target)
		return
	}
	addr := target
	if !strings.Contains(addr, ":") {
		addr += ":443"
	}
	dialCtx, dialCancel := context.WithTimeout(r.Context(), 10*time.Second)
	var d net.Dialer
	upstream, err := d.DialContext(dialCtx, "tcp", addr)
	dialCancel()
	if err != nil {
		http.Error(w, "upstream недоступен: "+err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "хижация невозможна", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	_, _ = io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n")
	go func() {
		_, _ = io.Copy(upstream, clientConn)
		_ = upstream.Close()
	}()
	go func() {
		_, _ = io.Copy(clientConn, upstream)
		_ = clientConn.Close()
	}()
}

// deny — отказ по белому списку: 403 и запись в лог (какой домен пытался
// пройти — нужно для настройки списка, а не для молчаливого «не работает»).
func (f *egressFilter) deny(w http.ResponseWriter, host string) {
	logging.Warnf("sandbox: egress заблокирован: %s (белый список: %s)",
		normalizeHost(host), strings.Join(f.allow, ", "))
	http.Error(w, "домен не входит в белый список песочницы", http.StatusForbidden)
}

// ServeEgressProxy — точка входа прокси внутри gateway-контейнера
// (cmd/sandbox-proxy): слушает addr («:3128») и гасится по SIGINT/SIGTERM —
// Docker Desktop гасит контейнер, и прокси завершается штатно.
func ServeEgressProxy(addr string, allow []string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("sandbox: egress-прокси на %s: %w", addr, err)
	}
	srv := &http.Server{Handler: NewEgressHandler(allow), ReadHeaderTimeout: 10 * time.Second}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("sandbox: egress-прокси: %w", err)
	}
	return nil
}
