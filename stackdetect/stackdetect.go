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
	KindPhp     Kind = "php"
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
// Приоритет: go.mod → composer.json → package.json → требования питона.
func DetectKind(dir string) Kind {
	switch {
	case HasFile(dir, "go.mod"):
		return KindGo
	// composer.json раньше package.json: Laravel/пакетные PHP-проекты несут оба
	// маркера, и приёмка по стеку PHP (composer + php -l + artisan) корректнее,
	// чем трактовка такого корня как Node-проекта.
	case HasFile(dir, "composer.json"):
		return KindPhp
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
