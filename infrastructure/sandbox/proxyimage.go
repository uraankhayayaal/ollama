package sandbox

// proxyimage.go — доставка egress-прокси в gateway-контейнер (Docker Desktop).
//
// Прокси исполняет наш же код (cmd/sandbox-proxy), но внутри контейнера
// нужен linux-бинарь. Он собирается кросс-компиляцией (CGO_ENABLED=0,
// GOOS=linux, GOARCH хоста) из исходников репозитория — один раз за жизнь
// процесса — и запекается в scratch-образ через Engine API: без docker CLI
// и без pull (FROM scratch встроен в демон).

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"ai/logging"
)

// proxyImageName — тег образа egress-прокси.
const proxyImageName = "ai-sandbox-proxy:latest"

// proxyDockerfile — минимальный образ: один статик-бинарь, без шелла и
// пакетного менеджера (прокси ничего не исполняет и никуда не подключается,
// кроме upstream по белому списку). USER — numeric: в scratch нет passwd.
const proxyDockerfile = "FROM scratch\n" +
	"COPY sandbox-proxy /sandbox-proxy\n" +
	"USER 65534:65534\n" +
	"ENTRYPOINT [\"/sandbox-proxy\"]\n"

var (
	proxyBuildMu   sync.Mutex
	proxyBuildPath string // linux-бинарь, собранный за жизнь процесса
)

// proxyBinary — путь к linux-бинарю прокси; собирается один раз за жизнь
// процесса. Исходники ищутся вверх от этого файла до go.mod — если исходников
// нет (deployed-бинарь без репозитория) или go toolchain недоступен,
// whitelist на Desktop падает с понятной ошибкой, а не тихо уходит в хост.
func proxyBinary(ctx context.Context) (string, error) {
	proxyBuildMu.Lock()
	defer proxyBuildMu.Unlock()
	if proxyBuildPath != "" {
		return proxyBuildPath, nil
	}
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "ai-sandbox-proxy-*")
	if err != nil {
		return "", fmt.Errorf("sandbox: каталог сборки прокси: %w", err)
	}
	out := filepath.Join(dir, "sandbox-proxy")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, "./cmd/sandbox-proxy")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH="+runtime.GOARCH,
	)
	// Прогресс/ошибки go — в лог сервера, не в stdout пользователя.
	var logBuf bytes.Buffer
	cmd.Stdout = &logBuf
	cmd.Stderr = &logBuf
	if err := cmd.Run(); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("sandbox: сборка sandbox-proxy (%s): %w: %s",
			filepath.Base(root), err, truncateRunes(logBuf.String(), 600))
	}
	proxyBuildPath = out
	logging.For("").Infof("[sandbox] egress-прокси собран: %s", out)
	return out, nil
}

// moduleRoot — каталог репозитория (ближайший с go.mod к этому файлу).
func moduleRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("sandbox: не удалось определить исходный каталог прокси")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("sandbox: go.mod не найден рядом с исходниками (%s): "+
		"для whitelist на Docker Desktop нужны исходники репозитория и go toolchain", file)
}

// ensureProxyImage — образ egress-прокси в кэше демона: быстрый путь —
// просто проверка наличия, иначе сборка тар-контекста и docker build.
func ensureProxyImage(ctx context.Context, eng *Engine) (string, error) {
	if ok, err := eng.HasImage(ctx, proxyImageName); err == nil && ok {
		return proxyImageName, nil
	}
	bin, err := proxyBinary(ctx)
	if err != nil {
		return "", err
	}
	tarData, err := proxyImageTar(bin)
	if err != nil {
		return "", err
	}
	logging.For("").Infof("[sandbox] собираю образ egress-прокси %s", proxyImageName)
	if err := eng.BuildImage(ctx, proxyImageName, tarData, nil); err != nil {
		return "", err
	}
	return proxyImageName, nil
}

// proxyImageTar — тар-контекст сборки: Dockerfile + бинарь прокси.
func proxyImageTar(bin string) ([]byte, error) {
	raw, err := os.ReadFile(bin)
	if err != nil {
		return nil, fmt.Errorf("sandbox: чтение бинаря прокси: %w", err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeEntry := func(name string, mode int64, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     mode,
			Size:     int64(len(data)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := writeEntry("Dockerfile", 0o644, []byte(proxyDockerfile)); err != nil {
		return nil, fmt.Errorf("sandbox: тар-контекст: %w", err)
	}
	if err := writeEntry("sandbox-proxy", 0o755, raw); err != nil {
		return nil, fmt.Errorf("sandbox: тар-контекст: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("sandbox: тар-контекст: %w", err)
	}
	return buf.Bytes(), nil
}

// truncateRunes — обрезка вывода до limit рун (для сообщений об ошибках).
func truncateRunes(s string, limit int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= limit {
		return string(r)
	}
	return string(r[:limit]) + "…"
}
