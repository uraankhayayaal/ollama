package sandbox

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDomainAllowed(t *testing.T) {
	cases := []struct {
		host    string
		pattern []string
		want    bool
	}{
		{"proxy.golang.org", []string{"proxy.golang.org"}, true},
		{"Proxy.Golang.Org.", []string{"proxy.golang.org"}, true}, // регистр и «точка в конце»
		{"proxy.golang.org:443", []string{"proxy.golang.org"}, true},
		{"registry.npmjs.org", []string{"proxy.golang.org"}, false},
		{"cdn.example.com", []string{"*.example.com"}, true},
		{"example.com", []string{"*.example.com"}, true},               // apex входит в wildcard
		{"sub.evil.com", []string{"*.evil.com"}, true},                 // поддомен своего wildcard
		{"sub.evil.com", []string{"*.example.com", "evil.com"}, false}, // чужой поддомен
		{"notexample.com", []string{"*.example.com"}, false},
		{"", []string{"*.example.com"}, false},
		{"x.ru", nil, false},
	}
	for _, c := range cases {
		if got := DomainAllowed(c.host, c.pattern); got != c.want {
			t.Errorf("DomainAllowed(%q, %v) = %v, ожидалось %v", c.host, c.pattern, got, c.want)
		}
	}
}

// Прокси отдаёт 403 на домен вне белого списка ДО попытки соединения с ним:
// отказ — на уровне заголовков, а не после dial.
func TestEgressProxyDeniesBeforeDial(t *testing.T) {
	h := NewEgressHandler([]string{"allowed.test"})

	// Обычный HTTP-запрос на чужой домен.
	req := httptest.NewRequest(http.MethodGet, "http://evil.test/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("GET чужого домена: код %d, ожидался 403", rec.Code)
	}

	// CONNECT (HTTPS-туннель) на чужой домен — тоже отказ.
	creq := httptest.NewRequest(http.MethodConnect, "https://evil.test:443", nil)
	creq.Host = "evil.test:443"
	crec := httptest.NewRecorder()
	h.ServeHTTP(crec, creq)
	if crec.Code != http.StatusForbidden {
		t.Errorf("CONNECT чужого домена: код %d, ожидался 403", crec.Code)
	}
}

// Разрешённый домен реально проходит через прокси до локального сервера
// (прокси живёт на шлюзовой адресе — здесь 127.0.0.1 как заглушка шлюза).
func TestEgressProxyForwardsAllowedHost(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer backend.Close()
	backendHost := strings.TrimPrefix(backend.URL, "http://")

	p, err := startEgressProxy("127.0.0.1", "127.0.0.1", []string{backendHost})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	proxyURL, err := url.Parse("http://" + p.addr)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	resp, err := client.Get(backend.URL + "/hello")
	if err != nil {
		t.Fatalf("запрос через разрешённый прокси: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Errorf("ожидался 200/ok, получено %d/%q", resp.StatusCode, body)
	}
}

// Прокси-переменные контейнера указывают на адрес слушателя.
func TestEgressProxyEnv(t *testing.T) {
	p := &egressProxy{addr: "172.18.0.1:3128"}
	env := p.env()
	if env["HTTP_PROXY"] != "http://172.18.0.1:3128" || env["HTTPS_PROXY"] != "http://172.18.0.1:3128" {
		t.Errorf("прокси-переменные не указывают на шлюз: %v", env)
	}
	if !strings.Contains(env["NO_PROXY"], "localhost") {
		t.Errorf("localhost должен идти мимо прокси: %q", env["NO_PROXY"])
	}
}
