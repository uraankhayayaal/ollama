package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Реестровые образы docker тянет неявным pull при run/create: EnsureImage для
// них должен быть no-op и не трогать демон вообще (иначе проверка образа
// падала бы до выполнения команды, хотя docker.c родит сам).
func TestEnsureImageSkipsRegistryImages(t *testing.T) {
	t.Setenv("CODEGEN_DOCKER_HOST", "tcp://127.0.0.1:1") // недоступный демон
	for _, image := range []string{"golang:1.24", "node:22", "python:3.12", "custom:tag"} {
		if err := EnsureImage(context.Background(), image); err != nil {
			t.Errorf("EnsureImage(%q): реестровый образ не должен лезть в демон, ошибка: %v", image, err)
		}
	}
}

// Автосборка — только для локального dev-образа; недоступный демон обязан
// вернуть ошибку, а не «успех» (молчаливый хост здесь врал бы про изоляцию).
func TestEnsureImageFailsWithoutDaemon(t *testing.T) {
	t.Setenv("CODEGEN_DOCKER_HOST", "tcp://127.0.0.1:1")
	if err := EnsureImage(context.Background(), DefaultImage); err == nil {
		t.Fatal("без демона автосборка dev-образа должна падать, а не молчать")
	}
}

// Тар-контекст автосборки: один файл Dockerfile, содержимое равно исходнику
// на диске (go:embed не должен разойтись с тем, что собирает compose).
func TestDevImageTarContainsDockerfile(t *testing.T) {
	tarData, err := devImageTar()
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(tarData))
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("первый заголовок: %v", err)
	}
	if hdr.Name != "Dockerfile" {
		t.Errorf("ожидался Dockerfile, получен %q", hdr.Name)
	}
	got, err := io.ReadAll(tr)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("запечённый Dockerfile расходится с исходником")
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Errorf("в контексте должен быть только Dockerfile, найден ещё файл: %v", err)
	}
}

// devBuildArgs повторяет дефолты compose: пустой env не передаёт ничего,
// заданные переменные отражаются одноимёнными ARG.
func TestDevBuildArgsReflectEnv(t *testing.T) {
	t.Setenv("SANDBOX_UID", "")
	t.Setenv("SANDBOX_GID", "")
	t.Setenv("CODEGEN_SANDBOX_PLAYWRIGHT", "")
	if got := devBuildArgs(); len(got) != 0 {
		t.Errorf("пустой env обязан дать пустые buildargs, получено %v", got)
	}
	t.Setenv("SANDBOX_UID", "1001")
	t.Setenv("SANDBOX_GID", "1002")
	t.Setenv("CODEGEN_SANDBOX_PLAYWRIGHT", "1")
	got := devBuildArgs()
	want := map[string]string{
		"TARGET_UID":                 "1001",
		"TARGET_GID":                 "1002",
		"CODEGEN_SANDBOX_PLAYWRIGHT": "1",
	}
	if len(got) != len(want) {
		t.Fatalf("devBuildArgs = %v, ожидалось %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("buildarg %s = %q, ожидалось %q", k, got[k], v)
		}
	}
}

// Образ уже в кэше → ensureDevImage не собирает ничего (быстрый путь, без
// POST /build). Проверяем на подменённом движке через httptest.
func TestEnsureDevImageSkipsBuildWhenPresent(t *testing.T) {
	var buildCalls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/build" {
			buildCalls++
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	if err := ensureDevImage(context.Background(), newTCPEngine(ts.URL), DefaultImage); err != nil {
		t.Fatal(err)
	}
	if buildCalls != 0 {
		t.Errorf("образ присутствует: POST /build не должен вызываться, вызван %d раз", buildCalls)
	}
}

// Образ отсутствует → автосборка: GET /images возвращает 404, POST /build
// обязан получить тар-контекст и buildargs. Сборка считается успешной по
// NDJSON-потоку демона.
func TestEnsureDevImageBuildsWhenMissing(t *testing.T) {
	t.Setenv("SANDBOX_UID", "1000")
	t.Setenv("SANDBOX_GID", "1000")
	t.Setenv("CODEGEN_SANDBOX_PLAYWRIGHT", "")
	var gotBuild bool
	var gotBuildArgs, gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/images/ai-sandbox%3Alatest/json":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/build":
			gotBuild = true
			gotBuildArgs = r.URL.Query().Get("buildargs")
			body, _ := io.ReadAll(r.Body)
			gotBody = string(body)
			io.WriteString(w, `{"stream":"Step 1/1: FROM golang:1.24-bookworm"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	if err := ensureDevImage(context.Background(), newTCPEngine(ts.URL), DefaultImage); err != nil {
		t.Fatal(err)
	}
	if !gotBuild {
		t.Fatal("POST /build не вызывался: отсутствующий образ обязан собираться")
	}
	if gotBuildArgs == "" {
		t.Fatal("buildargs не переданы демону")
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(gotBuildArgs), &parsed); err != nil {
		t.Fatalf("buildargs не JSON: %q: %v", gotBuildArgs, err)
	}
	for k, v := range devBuildArgs() {
		if parsed[k] != v {
			t.Errorf("buildarg %s = %q, ожидалось %q (kontext: %v)", k, parsed[k], v, parsed)
		}
	}
	if !strings.Contains(gotBody, "Dockerfile") {
		t.Errorf("тар-контекст не содержит Dockerfile")
	}
}

// Ошибка демона в потоке сборки (HTTP 200, поле error) обязана всплыть, а не
// превратиться в «успешную» сборку.
func TestBuildImageSurfacesStreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"errorDetail":{"message":"dockerfile parse error"}}`)
	}))
	defer ts.Close()

	err := newTCPEngine(ts.URL).BuildImage(context.Background(), DefaultImage, []byte("tar"), nil)
	if err == nil || !strings.Contains(err.Error(), "dockerfile parse error") {
		t.Errorf("ошибка потока сборки не всплыла: %v", err)
	}
}
