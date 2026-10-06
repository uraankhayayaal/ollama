package sandbox

// stack.go — определение стека проекта и образа песочницы.
//
// Перенесено из tools/sandbox.go без изменений логики: выбор образа нужен и
// DockerWorkspace (контейнер сессии), а tools зависит от sandbox, но не
// наоборот. В tools остались одноимённые обёртки — на них завязаны тесты
// (sandbox_test.go, sandbox_stack_test.go) и ephemeral-путь docker run.
//
// Порядок выбора образа: явно заданный (CODEGEN_SANDBOX_IMAGE) → по стеку
// проекта → dev-образ песочницы (DefaultImage). Последний шаг обязателен:
// у ЛСП-чекера манифеста проекта нет, и без него контейнерный режим был бы
// недостижим вовсе.

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultImage — dev-образ песочницы по умолчанию. Собирается из
// sandbox/Dockerfile (docker compose -f sandbox/compose.yaml build) и содержит
// тулчейн для монорепо.
const DefaultImage = "ai-sandbox:latest"

// stackManifests — манифест → стек. Порядок фиксирован: обход map в Go
// недетерминирован, и при двух манифестах в корне (go.mod + package.json)
// проект получал то golang, то node — в зависимости от запуска.
var stackManifests = []struct{ name, stack string }{
	{"go.mod", "go"},
	{"package.json", "node"},
	{"requirements.txt", "python"},
	{"pyproject.toml", "python"},
	{"composer.json", "php"},
}

// ImageFor — образ по стеку проекта: тот же список, что у acceptor.detectKind
// и ReadAppLogs, иначе песочница и остальной конвейер будут считать один и
// тот же проект разными.
//
// Учитывает и CODEGEN_SANDBOX_IMAGE, и его алиас CODEGEN_IMAGE. Возвращает
// "" для нераспознанного стека — вызывающий решает, брать ли DefaultImage.
func ImageFor(dir string) string {
	if v := FirstEnv("CODEGEN_SANDBOX_IMAGE", "CODEGEN_IMAGE"); v != "" {
		return v
	}
	switch DetectStack(dir) {
	case "go":
		return "golang:1.24"
	case "node":
		return "node:22"
	case "python":
		return "python:3.12"
	case "php":
		return "php:8.3-cli"
	}
	return ""
}

// DetectStack — стек проекта по манифестам. Отдельная копия
// acceptor.detectKind, а не импорт: tools не должен зависеть от агентов
// (acceptor сам импортирует tools), иначе получается цикл.
//
// Смотрим корень И первый уровень вглубь: монорепозиторий (Go-бэкенд +
// Node-фронтенд в frontend/) держит манифесты в подкаталогах, и раньше
// подбирался образ по корневому go.mod — golang без npm. Агент закономерно
// скачивал Node в /workspace (живой случай: mytrip, FEL-05 — 46 МБ
// node.tar.gz и 4287 файлов тулчейна в коммите задачи). Несколько стеков →
// "" → ai-sandbox:latest (dev-образ с go+node+python), который для монорепо и
// предназначен. Пустой стек по-прежнему даёт dev-образ.
func DetectStack(dir string) string {
	stacks := DetectStacks(dir, 1)
	if len(stacks) == 1 {
		for s := range stacks {
			return s
		}
	}
	return ""
}

// DetectStacks — множество стеков на глубине не глубже depth (0 = только корень).
func DetectStacks(dir string, depth int) map[string]bool {
	stacks := map[string]bool{}
	for _, m := range stackManifests {
		if _, err := os.Stat(filepath.Join(dir, m.name)); err == nil {
			stacks[m.stack] = true
		}
	}
	if depth <= 0 {
		return stacks
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return stacks
	}
	for _, e := range entries {
		if !e.IsDir() || skipStackDir(e.Name()) {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		for _, m := range stackManifests {
			if _, err := os.Stat(filepath.Join(sub, m.name)); err == nil {
				stacks[m.stack] = true
			}
		}
	}
	return stacks
}

// skipStackDir — каталоги, которые не имеет смысла обходить: служебные и уже
// установленные зависимости (там манифесты лежат всегда и ничего не говорят о
// стеке проекта).
func skipStackDir(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "dist", "build", "target", "tmp", "temp":
		return true
	}
	return false
}
