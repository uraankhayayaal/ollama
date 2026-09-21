// Скачивание текста файлом (используется экспортами чата и логов).
export function downloadText(filename: string, text: string): void {
  const blob = new Blob([text], { type: "text/plain;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  // Даём браузеру запустить загрузку до отзыва блоба.
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}

// Безопасное имя файла из имени проекта (слеши/пробелы → «_»).
export function safeName(name: string): string {
  return name.replace(/[^\w.\-]+/g, "_");
}