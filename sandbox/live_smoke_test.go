package sandbox

// live_smoke_test.go — живая проверка сессионной песочницы на настоящем
// Docker-демоне. Вне рабочего набора: SANDBOX_LIVE=1 go test ./sandbox/ -run Live.
//
// Образ по умолчанию здесь — redis:7-alpine (58 МБ, есть в локальном
// кэше): busybox sh/timeout/wget покрывают все проверяемые сценарии без
// пулла гигабайтных тулчейн-образов. Поведение образа от этого не зависит —
// используются только POSIX-примитивы.

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

func liveSessionConfig(t *testing.T) Config {
	t.Helper()
	// Режим и сеть — как в проде при CODEGEN_SANDBOX=session (по умолчанию
	// изоляция сети); образ фиксируется, чтобы не тянуть тулчейн-гигабайты.
	t.Setenv("CODEGEN_SANDBOX", "session")
	t.Setenv("CODEGEN_SANDBOX_NETWORK", "")
	t.Setenv("CODEGEN_SANDBOX_ALLOW_DOMAINS", "")
	cfg := LoadConfig()
	cfg.Image = "redis:7-alpine"
	return cfg
}

func requireLive(t *testing.T) {
	t.Helper()
	if os.Getenv("SANDBOX_LIVE") != "1" {
		t.Skip("живой тест песочницы: SANDBOX_LIVE=1")
	}
}

// Полный цикл: старт → команды → сеть none → таймаут → уничтожение.
func TestLiveSessionSmoke(t *testing.T) {
	requireLive(t)
	cfg := liveSessionConfig(t)
	dir := t.TempDir()
	ctx := context.Background()

	ws, err := StartSession(ctx, SessionOptions{
		Project: "live-smoke",
		Dir:     dir,
		Mounts:  []string{dir},
		Config:  cfg,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	// Повторный старт при живой сессии — не конфликт имён, а замена (п. 1.7).
	ws2, err := StartSession(ctx, SessionOptions{
		Project: "live-smoke",
		Dir:     dir,
		Mounts:  []string{dir},
		Config:  cfg,
	})
	if err != nil {
		t.Fatalf("повторный StartSession: %v", err)
	}
	_ = ws
	ws = ws2
	t.Cleanup(func() {
		if err := StopSession(context.Background(), "live-smoke"); err != nil {
			t.Errorf("StopSession: %v", err)
		}
	})
	// Реестр обязан отдавать активную сессию по имени и по пути.
	if Lookup("live-smoke", dir) != Workspace(ws) {
		t.Error("Lookup не нашёл только что запущенную сессию")
	}

	// Путь внутри контейнера совпадает с хостовым (bind-mount по тем же
	// путям): скрипты и агенты не замечают подмены.
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := ws.Run(runCtx, dir, "pwd && echo from-container")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("команда упала: %+v", res)
	}
	if strings.TrimSpace(res.Stdout) != dir+"\nfrom-container" {
		t.Errorf("ожидался хостовый pwd и маркер, получено %q", res.Stdout)
	}

	// Файл, созданный в контейнере, виден на хосте (одни и те же пути).
	if _, err := ws.Run(runCtx, dir, "touch made-in-container.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir + "/made-in-container.txt"); err != nil {
		t.Errorf("файл из контейнера не виден на хосте: %v", err)
	}

	// Отбраковка разрушительных команд — до движка.
	if _, err := ws.Run(runCtx, dir, "rm -rf /"); err == nil ||
		!strings.Contains(err.Error(), "политик") {
		t.Errorf("ожидался отказ политики, получено %v", err)
	}
	// Каталог вне монтирования — отказ до движка.
	if _, err := ws.Run(runCtx, "/etc", "id"); err == nil ||
		!strings.Contains(err.Error(), "вне смонтированных") {
		t.Errorf("ожидался отказ по покрытию, получено %v", err)
	}

	// Сеть по умолчанию для сессии — none (п. 1.10): доступа в интернет нет.
	if n := ws.SandboxNetwork(); n != "none" {
		t.Errorf("сеть сессии по умолчанию должна быть none, получено %q", n)
	}
	netCtx, netCancel := context.WithTimeout(ctx, 20*time.Second)
	defer netCancel()
	netRes, err := ws.Run(netCtx, dir, "wget -T 5 -O- http://example.com/ >/dev/null 2>&1")
	if err != nil {
		t.Fatalf("Run (сеть): %v", err)
	}
	if netRes.ExitCode == 0 {
		t.Error("при сети none загрузка из интернета должна быть невозможна")
	}

	// Таймаут: контейнерный timeout убивает команду раньше, чем ждали бы мы.
	start := time.Now()
	toCtx, toCancel := context.WithTimeout(ctx, 3*time.Second)
	defer toCancel()
	toRes, err := ws.Run(toCtx, dir, "sleep 30")
	if err != nil {
		t.Fatalf("Run (таймаут): %v", err)
	}
	if !toRes.TimedOut {
		t.Errorf("ожидался TimedOut, получено %+v", toRes)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("таймаут сработал за %s — контейнерный timeout не применился", elapsed)
	}

	// П. 1.7: после остановки контейнера демон не должен его хранить.
	name := ws.containerName
	if err := StopSession(context.Background(), "live-smoke"); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if Lookup("live-smoke", dir) != nil {
		t.Error("после StopSession запись должна уйти из реестра")
	}
	if _, err := ws.engine.InspectContainer(context.Background(), name); err == nil {
		t.Error("контейнер пережил StopSession")
	}
}

// Whitelist (п. 1.11): изолированная сеть + прокси. На Docker Desktop прокси
// — gateway-контейнер с двумя сетями (хост не может слушать шлюз VM),
// на Linux — слушатель на шлюзе хоста. Проверяется обе стороны списка:
// разрешённый домен реально доходит до интернета через прокси, чужой — 403.
func TestLiveSessionWhitelistDeniesForeignDomain(t *testing.T) {
	requireLive(t)
	cfg := liveSessionConfig(t)
	eng, err := NewEngineFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := eng.EnsureInternalNetwork(ctx, egressNetworkName); err != nil {
		t.Fatalf("EnsureInternalNetwork: %v", err)
	}
	cfg.Network = "whitelist"
	cfg.AllowDomains = []string{"example.org"}

	dir := t.TempDir()
	ws, err := StartSession(ctx, SessionOptions{
		Project: "live-white",
		Dir:     dir,
		Mounts:  []string{dir},
		Config:  cfg,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	t.Cleanup(func() { _ = StopSession(context.Background(), "live-white") })

	if nws := ws.SandboxNetwork(); nws != "whitelist" {
		t.Errorf("сеть = %q, ожидался whitelist", nws)
	}
	// Прокси-контейнер (Docker Desktop): жив, в ОБЕИХ сетях — internal для
	// сессий и bridge для выхода в интернет.
	proxyInfo, perr := eng.InspectContainer(ctx, proxyContainerName("live-white"))
	if perr == nil {
		ns, _ := proxyInfo["NetworkSettings"].(map[string]any)
		nets, _ := ns["Networks"].(map[string]any)
		for _, netName := range []string{egressNetworkName, "bridge"} {
			if _, ok := nets[netName]; !ok {
				t.Errorf("прокси-контейнер не подключён к сети %s (сети: %v)", netName, mapKeys(nets))
			}
		}
	} else if !eng.IsDesktop(ctx) {
		// На нативном Linux прокси-контейнера нет — хостовый на шлюзе.
		t.Logf("хостовый прокси (не Desktop): %v", perr)
	} else {
		t.Errorf("прокси-контейнер недоступен: %v", perr)
	}
	// Прокси-переменные дошли в контейнера: адрес on-link (IP внутренней
	// сети + фиксированный порт прокси).
	envRes, err := ws.Run(ctx, dir, "echo $HTTPS_PROXY")
	if err != nil {
		t.Fatal(err)
	}
	proxyEnvVal := strings.TrimSpace(envRes.Stdout)
	if !strings.HasPrefix(proxyEnvVal, "http://") || !strings.HasSuffix(proxyEnvVal, ":"+proxyListen) {
		t.Errorf("HTTPS_PROXY не указывает на прокси (порт %s): %q", proxyListen, envRes.Stdout)
	}
	// Чужой домен: отказ приходит от ПРОКСИ (403), а не по несуществующему
	// маршруту внутренней сети — wget завершается быстро и с 403.
	denyStart := time.Now()
	denyRes, err := ws.Run(ctx, dir, "wget -T 5 -O- http://foreign-domain.test/ 2>&1 || true")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(denyRes.Stdout, "403") {
		t.Errorf("ожидался 403 от прокси, вывод: %q", denyRes.Stdout)
	}
	if elapsed := time.Since(denyStart); elapsed > 15*time.Second {
		t.Errorf("отказ занял %s — похоже, запрос пошёл мимо прокси", elapsed)
	}
	// Разрешённый домен реально доходит до интернета через прокси:
	// internal-сеть без default route иначе его не видит вовсе.
	allowRes, err := ws.Run(ctx, dir, "wget -T 15 -O- http://example.org/ 2>&1")
	if err != nil {
		t.Fatal(err)
	}
	if allowRes.ExitCode != 0 || !strings.Contains(allowRes.Stdout, "Example Domain") {
		t.Errorf("разрешённый example.org не прошёл через прокси (exit=%d): %q",
			allowRes.ExitCode, allowRes.Stdout)
	}
	// После остановки сессии прокси-контейнер уходит вместе с ней.
	if err := StopSession(context.Background(), "live-white"); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if _, err := eng.InspectContainer(ctx, proxyContainerName("live-white")); err == nil {
		t.Error("прокси-контейнер пережил StopSession")
	}
}

// mapKeys — отсортированные ключи map для сообщений об ошибках.
func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
