package sandbox

// proxy_test.go — тар-контекст образа прокси, имена контейнеров, разбор IP.

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Тар-контекст сборки: ровно Dockerfile и бинарь, права и содержимое верны.
func TestProxyImageTar(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "sandbox-proxy")
	if err := os.WriteFile(bin, []byte("#!/bin/true\nELF-fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := proxyImageTar(bin)
	if err != nil {
		t.Fatalf("proxyImageTar: %v", err)
	}

	tr := tar.NewReader(bytes.NewReader(data))
	got := map[string]struct {
		mode int64
		body string
	}{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("чтение тара: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("чтение %s: %v", hdr.Name, err)
		}
		got[hdr.Name] = struct {
			mode int64
			body string
		}{hdr.Mode, string(body)}
	}
	if len(got) != 2 {
		t.Fatalf("в таре %d файлов, ожидалось 2: %v", len(got), got)
	}
	if d, ok := got["Dockerfile"]; !ok || d.body != proxyDockerfile {
		t.Errorf("Dockerfile: %q", got["Dockerfile"].body)
	} else if d.mode != 0o644 {
		t.Errorf("Dockerfile mode = %o, ожидался 644", d.mode)
	}
	b, ok := got["sandbox-proxy"]
	if !ok {
		t.Fatal("в таре нет sandbox-proxy")
	}
	if b.body != "#!/bin/true\nELF-fake" {
		t.Errorf("бинарь повреждён: %q", b.body)
	}
	if b.mode != 0o755 {
		t.Errorf("бинарь mode = %o, ожидался 755", b.mode)
	}
}

// Dockerfile обязан класть бинарь с правами на исполнение (иначе контейнер
// не стартует) и запускать его от непривилегированного пользователя.
func TestProxyDockerfileContract(t *testing.T) {
	for _, want := range []string{"FROM scratch", "COPY sandbox-proxy /sandbox-proxy", "ENTRYPOINT"} {
		if !strings.Contains(proxyDockerfile, want) {
			t.Errorf("proxyDockerfile без %q: %s", want, proxyDockerfile)
		}
	}
	if !strings.Contains(proxyDockerfile, "USER") {
		t.Errorf("прокси должен работать не от root: %s", proxyDockerfile)
	}
}

// Имя прокси-контейнера: свой префикс, те же правила санитайза, что у сессии.
func TestProxyContainerName(t *testing.T) {
	cases := map[string]string{
		"my-trip":      "ai-sandbox-proxy-my-trip",
		"My.Project_1": "ai-sandbox-proxy-my.project_1",
		"Проект/имя":   "ai-sandbox-proxy-project",
		"":             "ai-sandbox-proxy-project",
		"live-smoke":   "ai-sandbox-proxy-live-smoke",
	}
	for in, want := range cases {
		if got := proxyContainerName(in); got != want {
			t.Errorf("proxyContainerName(%q) = %q, ожидалось %q", in, got, want)
		}
	}
	// Прокси и сессия — разные имена при одном проекте (два контейнера).
	if proxyContainerName("x") == containerName("x") {
		t.Error("имя прокси совпало с именем сессии")
	}
}

// IPv4 контейнера в сети — адрес из Inspect, разбор вложенной структуры.
func TestContainerIPv4(t *testing.T) {
	info := map[string]any{
		"NetworkSettings": map[string]any{
			"Networks": map[string]any{
				"ai-sandbox-egress": map[string]any{"IPAddress": "172.20.0.3"},
				"bridge":            map[string]any{"IPAddress": "172.17.0.4"},
			},
		},
	}
	ip, err := containerIPv4(info, "ai-sandbox-egress")
	if err != nil || ip != "172.20.0.3" {
		t.Errorf("containerIPv4 = %q, %v", ip, err)
	}
	if _, err := containerIPv4(info, "nope"); err == nil {
		t.Error("ожидалась ошибка для несуществующей сети")
	}
	if _, err := containerIPv4(map[string]any{}, "x"); err == nil {
		t.Error("ожидалась ошибка для пустого inspect")
	}
}

// Корень модуля находится от исходников (нужен для кросс-сборки прокси).
func TestModuleRoot(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatalf("moduleRoot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Errorf("в %s нет go.mod: %v", root, err)
	}
}
