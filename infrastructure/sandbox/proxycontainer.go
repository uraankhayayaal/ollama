package sandbox

// proxycontainer.go — gateway-контейнер egress-прокси для Docker Desktop.
//
// На нативном Linux прокси живёт на шлюзе внутренней сети (шлюз и есть хост).
// На Docker Desktop шлюз принадлежит гостевой VM, хост на нём слушать не
// может, а маршрута из internal-сети до хоста не существует вовсе, поэтому
// прокси поднимается КОНТЕЙНЕРОМ с двумя endpoint'ами:
//
//	internal ai-sandbox-egress  ← сессионные контейнеры (on-link, без выхода)
//	bridge (обычная сеть)       ← выход в интернет через штатный NAT демона
//
// Сессия получает HTTP(S)_PROXY=http://<ip-прокси>:3128 — адрес on-link во
// внутренней сети, достижимый без default route. Allowlist исполняет наш
// статик-бинарь cmd/sandbox-proxy внутри контейнера (см. proxyimage.go).

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"ai/logging"
)

// proxyListen — фиксированный порт прокси внутри контейнера: IP контейнера
// уникален во внутренней сети, поэтому порт может быть любым стабильным.
const proxyListen = "3128"

// proxyMemory — лимит памяти прокси-контейнера (небольшой: он только
// форвардит байты, буферов на запрос не аллоцирует под завязку).
const proxyMemory = "64m"

// startProxyContainer — старт (или замена остатков) прокси-контейнера
// проекта. Возвращает его IPv4 во внутренней сети — адрес для env сессии.
func startProxyContainer(ctx context.Context, eng *Engine, project string, allow []string) (string, error) {
	image, err := ensureProxyImage(ctx, eng)
	if err != nil {
		return "", err
	}
	name := proxyContainerName(project)
	// Остатки прошлого прокси (падение процесса) — силой, как у сессии:
	// иначе повторный старт упирается в конфликт имени.
	if err := eng.RemoveContainer(ctx, name, true); err != nil {
		return "", err
	}
	id, err := eng.CreateContainer(ctx, name, containerReq{
		Image: image,
		Cmd: []string{
			"--listen", ":" + proxyListen,
			"--allow", strings.Join(allow, ","),
		},
		Labels: map[string]string{
			"ai.sandbox": "egress-proxy",
			"ai.project": project,
		},
		HostConfig: containerHost{
			// Primary — обычная сеть: штатный NAT демона даёт выход в
			// интернет. Вторая сеть (internal) подключается отдельным
			// NetworkConnect — create при заданном NetworkMode additional
			// endpoints не берёт (см. ConnectNetwork).
			NetworkMode: "bridge",
			ReadOnly:    true,
			Memory:      parseBytesLimit(proxyMemory),
			CapDrop:     []string{"ALL"},
			SecurityOpt: []string{"no-new-privileges"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("sandbox: создание egress-прокси: %w", err)
	}
	// Ошибка после создания обязана убрать контейнер — иначе в демоне
	// остаётся брошенный прокси (п. 1.7).
	fail := func(cause error) (string, error) {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		_ = eng.RemoveContainer(cctx, name, true)
		return "", cause
	}
	// Второй endpoint — во внутреннюю сеть сессий (on-link адрес для env).
	if err := eng.ConnectNetwork(ctx, egressNetworkName, id); err != nil {
		return fail(fmt.Errorf("sandbox: подключение прокси к внутренней сети: %w", err))
	}
	if err := eng.StartContainer(ctx, id); err != nil {
		return fail(fmt.Errorf("sandbox: запуск egress-прокси: %w", err))
	}
	info, err := eng.InspectContainer(ctx, name)
	if err != nil {
		return fail(err)
	}
	ip, err := containerIPv4(info, egressNetworkName)
	if err != nil {
		return fail(err)
	}
	logging.For(project).Infof("[sandbox] egress-прокси-контейнер %s запущен (%s:%s, allow: %s)",
		name, ip, proxyListen, strings.Join(allow, ", "))
	return ip, nil
}

// stopProxyContainer — остановка и удаление прокси-контейнера проекта.
// Идемпотентно: отсутствующий контейнер (404) — не ошибка.
func stopProxyContainer(ctx context.Context, eng *Engine, project string) error {
	name := proxyContainerName(project)
	if err := eng.StopContainer(ctx, name, 5); err != nil {
		return err
	}
	return eng.RemoveContainer(ctx, name, true)
}

// proxyAddr — адрес прокси для env сессии: IP контейнера + фиксированный порт.
func proxyAddr(ip string) string {
	return net.JoinHostPort(ip, proxyListen)
}
