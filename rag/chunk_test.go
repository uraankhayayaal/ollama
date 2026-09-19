package rag

import (
	"strings"
	"testing"
)

// ChunkFile нарезает Go-файл по функциям/типам с doc-комментариями и
// правильными координатами строк.
func TestChunkGoFunctionsAndStruct(t *testing.T) {
	src := `// Пакет примера.
package example

// Sum складывает два числа.
func Sum(a, b int) int {
	return a + b
}

type User struct {
	Name string
}

// Greet печатает приветствие.
func Greet(u User) string {
	return "hi " + u.Name
}
`
	chunks := ChunkFile("example.go", src)
	if len(chunks) != 3 {
		t.Fatalf("чанков: got %d, want 3 (Sum, User, Greet)", len(chunks))
	}

	if !strings.Contains(chunks[0].Content, "// Sum складывает два числа.") {
		t.Fatalf("чанк 1 должен включать doc-комментарий Sum: %q", chunks[0].Content)
	}
	if chunks[0].StartLine != 4 || chunks[0].EndLine != 7 {
		t.Fatalf("чанк 1 координаты: got %d-%d, want 4-7", chunks[0].StartLine, chunks[0].EndLine)
	}

	if !strings.Contains(chunks[1].Content, "type User struct") {
		t.Fatalf("чанк 2 должен содержать struct: %q", chunks[1].Content)
	}
	if !strings.Contains(chunks[2].Content, "// Greet печатает приветствие.") {
		t.Fatalf("чанк 3 должен включать doc-комментарий Greet: %q", chunks[2].Content)
	}
	if chunks[2].StartLine != 13 {
		t.Fatalf("чанк 3 StartLine: got %d, want 13", chunks[2].StartLine)
	}
}

// Импорты не становятся чанками (шум без кода).
func TestChunkGoSkipsImports(t *testing.T) {
	src := `package main

import (
	"fmt"
	"os"
)

var version = "1.0"

func main() {
	fmt.Println(version)
}
`
	chunks := ChunkFile("main.go", src)
	if len(chunks) != 2 {
		t.Fatalf("чанков: got %d, want 2 (version, main)", len(chunks))
	}
	if strings.Contains(chunks[0].Content, "import") {
		t.Fatalf("чанк не должен содержать import: %q", chunks[0].Content)
	}
	if chunks[0].StartLine != 8 {
		t.Fatalf("чанк var StartLine: got %d, want 8", chunks[0].StartLine)
	}
}

// Некорректный Go не роняет нарезку — фолбэк на линейные чанки.
func TestChunkGoParseErrorFallback(t *testing.T) {
	src := `package main

func broken( {{
`
	chunks := ChunkFile("bad.go", src)
	if len(chunks) != 1 {
		t.Fatalf("чанков после падения парсера: got %d, want 1", len(chunks))
	}
	if chunks[0].StartLine != 1 || chunks[0].EndLine != 4 {
		t.Fatalf("координаты: got %d-%d, want 1-4", chunks[0].StartLine, chunks[0].EndLine)
	}
}

// Длинная функция режется на участки не длиннее maxChunkLines строк.
func TestChunkSplitLongDecl(t *testing.T) {
	var b strings.Builder
	b.WriteString("package example\n\n")
	b.WriteString("func Huge() {\n")
	for i := 0; i < maxChunkLines*3; i++ {
		b.WriteString("\tprintln(\"line\")\n")
	}
	b.WriteString("}\n")
	content := b.String()

	chunks := ChunkFile("huge.go", content)
	if len(chunks) < 3 {
		t.Fatalf("длинная функция должна дать >=3 чанков, got %d", len(chunks))
	}
	for _, c := range chunks {
		if got := strings.Count(c.Content, "\n") + 1; got > maxChunkLines+1 {
			t.Fatalf("чанк длиннее лимита: %d строк", got)
		}
		if c.EndLine < c.StartLine {
			t.Fatalf("инвертированные координаты: %d-%d", c.StartLine, c.EndLine)
		}
	}
	// Чанки идут подряд без разрывов и перекрытий.
	prev := 0
	for i, c := range chunks {
		_ = i
		if c.StartLine < prev+1 {
			t.Fatalf("чанк начинается внутри предыдущего: %d >= %d+1", c.StartLine, prev)
		}
		prev = c.EndLine
	}
}

// TS: функции, класс с методами и type-декларации разбиваются на чанки.
func TestChunkTSDefinitions(t *testing.T) {
	src := `import { x } from "./x";

export interface Token {
  value: string;
}

// Создаёт токен.
export function makeToken(v: string): Token {
  return { value: v };
}

class Store {
  private cache = new Map();

  public get(key: string): string | undefined {
    return this.cache.get(key);
  }

  public set(key: string, val: string): void {
    this.cache.set(key, val);
  }
}

const isEven = (n: number): boolean => n % 2 === 0;
`
	chunks := ChunkFile("store.ts", src)
	if len(chunks) != 5 {
		t.Fatalf("чанков: got %d, want 5 (шапка, interface, makeToken, Store, isEven)", len(chunks))
	}
	if !strings.Contains(chunks[0].Content, "import") {
		t.Fatalf("первый чанк должен содержать import-шапку: %q", chunks[0].Content)
	}
	if !strings.Contains(chunks[1].Content, "export interface Token") {
		t.Fatalf("чанк interface: %q", chunks[1].Content)
	}
	if !strings.Contains(chunks[2].Content, "// Создаёт токен.") {
		t.Fatalf("чанк makeToken должен включать комментарий: %q", chunks[2].Content)
	}
	if !strings.Contains(chunks[2].Content, "export function makeToken") {
		t.Fatalf("чанк makeToken: %q", chunks[2].Content)
	}
	if !strings.Contains(chunks[3].Content, "class Store") {
		t.Fatalf("чанк Store: %q", chunks[3].Content)
	}
	// Методы внутри класса не открыли отдельные чанки: оба в одном куске.
	if !strings.Contains(chunks[3].Content, "public get(key") || !strings.Contains(chunks[3].Content, "public set(key") {
		t.Fatalf("методы должны быть внутри чанка класса: %q", chunks[3].Content)
	}
}

// Скобки внутри строк и комментариев не влияют на границы чанков.
func TestChunkTSBraceInString(t *testing.T) {
	src := `const a = "{";
function b() {
  const t = "}";
  return t;
}
const c = () => { return "{ }"; };
`
	chunks := ChunkFile("str.ts", src)
	// a (не стрелка — без =>), b, c.
	if len(chunks) != 3 {
		t.Fatalf("чанков: got %d, want 3 (a, b, c)", len(chunks))
	}
	if !strings.Contains(chunks[0].Content, `const a = "{"`) {
		t.Fatalf("чанк a: %q", chunks[0].Content)
	}
	if !strings.Contains(chunks[1].Content, "function b()") {
		t.Fatalf("чанк b: %q", chunks[1].Content)
	}
	if !strings.Contains(chunks[2].Content, "const c = () =>") {
		t.Fatalf("чанк c: %q", chunks[2].Content)
	}
}

// Python: def и class с декораторами и комментариями.
func TestChunkPythonDefClass(t *testing.T) {
	src := `# Модуль.
import os

CONFIG = {"x": 1}


# Приветствие.
def greet(name: str) -> str:
    return "hi " + name


@app.route("/")
def index():
    return "ok"


class Service:
    def __init__(self, name: str):
        self.name = name

    def start(self) -> None:
        print(self.name)
`
	chunks := ChunkFile("svc.py", src)
	// Шапка, greet, index, класс целиком — как отдельные чанки и методы класса.
	if len(chunks) != 6 {
		t.Fatalf("чанков: got %d, want 6 (шапка, greet, index, class, __init__, start)", len(chunks))
	}
	if !strings.Contains(chunks[1].Content, "# Приветствие.") {
		t.Fatalf("чанк greet должен включать комментарий: %q", chunks[1].Content)
	}
	if !strings.Contains(chunks[2].Content, `@app.route("/")`) {
		t.Fatalf("чанк index должен включать декоратор: %q", chunks[2].Content)
	}
	if !strings.Contains(chunks[3].Content, "class Service:") {
		t.Fatalf("чанк Service: %q", chunks[3].Content)
	}
	if !strings.Contains(chunks[4].Content, "def __init__") || !strings.Contains(chunks[4].Content, "self.name = name") {
		t.Fatalf("чанк __init__: %q", chunks[4].Content)
	}
	if !strings.Contains(chunks[5].Content, "def start") || !strings.Contains(chunks[5].Content, "print(self.name)") {
		t.Fatalf("чанк start: %q", chunks[5].Content)
	}
}

// Линейная нарезка для файлов без структурных маркеров.
func TestChunkLinesPlain(t *testing.T) {
	lines := make([]string, 0, maxChunkLines*2+5)
	for i := 1; i <= maxChunkLines*2+5; i++ {
		lines = append(lines, "строка")
	}
	src := strings.Join(lines, "\n")
	chunks := ChunkFile("notes.txt", src)
	if len(chunks) != 3 {
		t.Fatalf("чанков: got %d, want 3", len(chunks))
	}
	if chunks[0].StartLine != 1 || chunks[0].EndLine != maxChunkLines {
		t.Fatalf("чанк 0 координаты: got %d-%d", chunks[0].StartLine, chunks[0].EndLine)
	}
	if chunks[1].StartLine != maxChunkLines+1 {
		t.Fatalf("чанк 1 StartLine: got %d, want %d", chunks[1].StartLine, maxChunkLines+1)
	}
	if chunks[2].EndLine != maxChunkLines*2+5 {
		t.Fatalf("чанк 2 EndLine: got %d, want %d", chunks[2].EndLine, maxChunkLines*2+5)
	}
}

// Пустой файл даёт ноль чанков.
func TestChunkEmptyFile(t *testing.T) {
	if got := ChunkFile("empty.go", ""); len(got) != 0 {
		t.Fatalf("пустой файл дал чанки: %v", got)
	}
}