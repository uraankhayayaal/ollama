package sandbox

// ensureimage.go — автосборка локального dev-образа песочницы, если его нет
// в кэше демона.
//
// ai-sandbox:latest собирается из sandbox/Dockerfile и в реестре НЕ суще-
// ствует: штатный неявный pull при docker run/create даёт «pull access denied»,
// и первая же команда агента в монорепо (или без распознанного стека) падала
// без понятного диагноза (живой случай: mytrip на хосте без собранного образа —
// агент гонял одну и ту же пробу несколько раундов, прежде чем оркестрация
// остановилась по таймауту модели). Здесь образ собирается сам: тем же
// Dockerfile, что и ручной `docker compose -f sandbox/compose.yaml build`,
// и через тот же Engine API (без docker CLI). Реестровые образы (golang:*,
// node:*, python:*, php:*) docker доставляет неявным pull — их не касаемся.

import (
	"archive/tar"
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"strings"
	"sync"

	"ai/logging"
)

//go:embed Dockerfile
var devDockerfile string

// EnsureImage гарантирует, что выбранный образ песочницы есть в кэше демона.
// Для локального dev-образа (DefaultImage), которого в реестре нет, при
// отсутствии — автосборка; для реестровых образов — no-op (их docker тянет
// неявно при run/create).
func EnsureImage(ctx context.Context, image string) error {
	if image != DefaultImage {
		return nil
	}
	eng, err := NewEngineFromEnv()
	if err != nil {
		return err
	}
	return ensureDevImage(ctx, eng, image)
}

// ensureDevImage — та же гарантия на уже открытом движке (сессионный путь
// StartSession держит eng). Сборка обходится мьютексом: параллельные старты
// команд/сессий не должны гонять один и тот же docker build.
func ensureDevImage(ctx context.Context, eng *Engine, image string) error {
	if image != DefaultImage {
		return nil
	}
	devImageMu.Lock()
	defer devImageMu.Unlock()
	if ok, err := eng.HasImage(ctx, image); err == nil && ok {
		return nil
	}
	return buildDevImage(ctx, eng)
}

var devImageMu sync.Mutex

func buildDevImage(ctx context.Context, eng *Engine) error {
	tarData, err := devImageTar()
	if err != nil {
		return err
	}
	logging.For("").Infof("[sandbox] образа %s нет в кэше — автосборка из sandbox/Dockerfile", DefaultImage)
	if err := eng.BuildImage(ctx, DefaultImage, tarData, devBuildArgs()); err != nil {
		return fmt.Errorf("sandbox: автосборка %s: %w", DefaultImage, err)
	}
	return nil
}

// devBuildArgs — значения ARG для сборки dev-образа: те же переменные
// окружения, что подставляет compose ({SANDBOX_UID,SANDBOX_GID,
// CODEGEN_SANDBOX_PLAYWRIGHT}:дефолты). Пустой env не передаётся — в
// Dockerfile стоят дефолты.
func devBuildArgs() map[string]string {
	args := map[string]string{}
	for _, pair := range [][2]string{
		{"SANDBOX_UID", "TARGET_UID"},
		{"SANDBOX_GID", "TARGET_GID"},
		{"CODEGEN_SANDBOX_PLAYWRIGHT", "CODEGEN_SANDBOX_PLAYWRIGHT"},
	} {
		if v := strings.TrimSpace(os.Getenv(pair[0])); v != "" {
			args[pair[1]] = v
		}
	}
	return args
}

// devImageTar — тар-контекст сборки: Dockerfile. Исходник запекается в бинарь
// (go:embed): деплой без репозитория не теряет контекст автосборки.
func devImageTar() ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "Dockerfile",
		Mode:     0o644,
		Size:     int64(len(devDockerfile)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		return nil, fmt.Errorf("sandbox: тар-контекст dev-образа: %w", err)
	}
	if _, err := tw.Write([]byte(devDockerfile)); err != nil {
		return nil, fmt.Errorf("sandbox: тар-контекст dev-образа: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("sandbox: тар-контекст dev-образа: %w", err)
	}
	return buf.Bytes(), nil
}
