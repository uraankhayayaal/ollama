package filepath

import (
	"fmt"
	"os"
	"path/filepath"
)

// FileData описывает структуру одного файла для записи
type FileData struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

// BulkWriteResult возвращает статус записи по каждому файлу для ИИ-агента
type BulkWriteResult struct {
	Success []string          `json:"success"` // Список успешно записанных файлов
	Errors  map[string]string `json:"errors"`  // Имя файла -> Текст ошибки
}

// WriteMultipleFiles записывает несколько файлов в указанную директорию.
// Этот инструмент идеален для ИИ-агентов, так как он не падает при ошибке в одном файле,
// а продолжает работу и возвращает подробный отчет.
func WriteMultipleFiles(files []FileData) (BulkWriteResult, error) {
	dir := "temp"
	result := BulkWriteResult{
		Success: make([]string, 0),
		Errors:  make(map[string]string),
	}

	if len(files) == 0 {
		return result, fmt.Errorf("ошибка: список файлов пуст")
	}

	// Создаем папку один раз перед циклом
	if err := os.MkdirAll(dir, 0755); err != nil {
		return result, fmt.Errorf("ошибка создания папки %s: %v", dir, err)
	}

	// Итерируемся по всем файлам
	for _, file := range files {
		if file.Filename == "" || file.Content == "" {
			result.Errors[file.Filename] = "имя файла или контент пусты"
			continue
		}

		// Безопасный путь для любой ОС
		filePath := filepath.Join(dir, file.Filename)

		// Записываем текущий файл
		if err := os.WriteFile(filePath, []byte(file.Content), 0644); err != nil {
			result.Errors[file.Filename] = fmt.Sprintf("не удалось записать: %v", err)
		} else {
			result.Success = append(result.Success, file.Filename)
		}
	}

	return result, nil
}
