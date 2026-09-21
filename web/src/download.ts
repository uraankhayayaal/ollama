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

// Имя файла экспорта с локальным timestamp (дата+время, ведущие нули) в начале:
// `YYYY-MM-DD_HH-mm-ss_<prefix><name>`. Timestamp в начале гарантирует, что
// повторные экспорты не перезаписывают друг друга и порядок по имени совпадает
// с хронологией.
export function stampedName(prefix: string, name: string): string {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, "0");
  const ts = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}_${p(d.getHours())}-${p(d.getMinutes())}-${p(d.getSeconds())}`;
  return `${ts}_${prefix}${name}`;
}