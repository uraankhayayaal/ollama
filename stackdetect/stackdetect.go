// Package stackdetect — определение типа проекта по маркерам в корне
// директории. Нейтральный пакет: единый источник для агента-приёмщика
// (agents/acceptor) и инструментов генератора (tools) — без цикла импортов.
package stackdetect

import (
	"os"
	"path/filepath"
)

// Kind — тип проекта, определяемый по маркерам в корне директории.
type Kind string

const (
	KindGo      Kind = "go"
	KindNode    Kind = "node"
	KindPython  Kind = "python"
	KindUnknown Kind = "unknown"
)

// HasFile проверяет наличие обычного файла (не директории) в директории.
func HasFile(dir, name string) bool {
	info, err := os.Stat(filepath.Join(dir, name))
	return err == nil && !info.IsDir()
}

// DetectKind определяет тип проекта по маркерам в корне директории.
// Приоритет: go.mod → package.json → требовательные питон-маркеры.
func DetectKind(dir string) Kind {
	switch {
	case HasFile(dir, "go.mod"):
		return KindGo
	case HasFile(dir, "package.json"):
		return KindNode
	case HasFile(dir, "requirements.txt"),
		HasFile(dir, "pyproject.toml"),
		HasFile(dir, "setup.py"),
		HasFile(dir, "main.py"),
		HasFile(dir, "app.py"):
		return KindPython
	default:
		return KindUnknown
	}
}
