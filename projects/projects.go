// Package projects — общие для всех агентов-разработчиков утилиты: путь к
// выходной директории проекта (temp/<проект> в корне модуля) и конфиг
// разработчика (лимиты записи, имя модуля, бюджет self-repair). Ранее жили в
// пакете codegenerator; выделены сюда, чтобы backend/frontend-разработчики,
// планировщик и CLI не зависели от конкретного агента.
package projects

import (
	"os"
	"path/filepath"
)

// ProjectDir возвращает путь к выходной директории проекта
// temp/<projectName> в корне модуля. Единый источник пути для всех агентов:
// разработчики, планировщик, приёмка и CLI гарантированно работают с одной
// директорией независимо от рабочей директории запуска.
func ProjectDir(projectName string) string {
	return filepath.Join(moduleRoot(), "temp", projectName)
}

// moduleRoot находит корень модуля — директорию с go.mod, поднимаясь вверх от
// рабочей директории. Если go.mod не найден, возвращает рабочую директорию
// (запуск вне модуля): temp/ тогда окажется рядом с CWD и для всех агентов.
func moduleRoot() string {
	wd, _ := os.Getwd()
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return wd
		}
		dir = parent
	}
}
