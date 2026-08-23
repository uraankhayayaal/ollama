package filepath

import (
	"fmt"
	"os"
	"path/filepath"
)

func WriteFile(filename string, content string) error {
	// 1. Указываем путь к папке и имя файла
	dir := "temp"

	if filename == "" || content == "" {
		return fmt.Errorf("Ошибка: имя файла или контент пусты")
	}

	// 2. Безопасно объединяем путь для любой операционной системы (Windows/Linux)
	filePath := filepath.Join(dir, filename)

	// 3. Создаем папки, если их еще нет (0755 — стандартные права для папок)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("Ошибка создания папки: %v", err)
	}

	// 4. Записываем данные в файл
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("Не удалось записать файл: %v", err)
	}

	return nil
}
