package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSkeletonizeGo проверяет карту Go-файла: сигнатуры без тел функций,
// поля структур, методы интерфейсов и номера строк.
func TestSkeletonizeGo(t *testing.T) {
	src := `package user

import (
	"context"
	"time"
)

type User struct {
	ID        string
	Name      string
	LastLogin time.Time
	Tags      []string
}

type Repo interface {
	ByID(ctx context.Context, id string) (*User, error)
	Save(ctx context.Context, u *User) error
}

var defaultLimit int = 20

const maxName = 64

func (u *User) login(t time.Time) {
	u.LastLogin = t
}

func NewUser(name string) *User {
	return &User{Name: name}
}

func (r *repo) ByID(ctx context.Context, id string) (*User, error) {
	return &User{}, nil
}
`
	got := SkeletonizeFile("internal/user/user.go", []byte(src))

	for _, want := range []string{
		"package user",
		`type User struct { ID string; Name string; LastLogin time.Time; Tags []string }`,
		`type Repo interface { ByID func(ctx context.Context, id string) (*User, error); Save func(ctx context.Context, u *User) error }`,
		"func (*User) login(t time.Time)",
		"func NewUser(name string) *User",
		"func (*repo) ByID(ctx context.Context, id string) (*User, error)",
		"import (context; time)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("карта Go-файла должна содержать %q\ngot:\n%s", want, got)
		}
	}

	// Тела функций обязаны отсутствовать.
	for _, leaked := range []string{"u.LastLogin = t", "return &User{Name: name}", "return &User{}, nil"} {
		if strings.Contains(got, leaked) {
			t.Errorf("карта не должна содержать тело функции: %q\ngot:\n%s", leaked, got)
		}
	}

	// Номера строк деклараций должны присутствовать.
	if !strings.Contains(got, "// L") {
		t.Errorf("карта должна помечать декларации номерами строк, got:\n%s", got)
	}
}

// TestSkeletonizeTS проверяет карту TS/TSX-файла: interface/type целиком,
// функции/классы — сигнатурами с номерами строк.
func TestSkeletonizeTS(t *testing.T) {
	src := `import { Client } from './client'

export interface UserProfileProps {
	id: string
	onUpdate: () => void
}

type AccountID = string

export function useUserActions(): {
	deleteUser: (id: string) => Promise<void>
} {
	return { deleteUser: async (id) => {} }
}

export const UserProfile: React.FC<UserProfileProps> = ({ id, onUpdate }) => {
	return <div>{id}</div>
}

export class UserService {
	private base = '/api'

	async fetchUsers(id: string): Promise<User[]> {
		const r = await fetch(this.base)
		return r.json()
	}
}
`
	got := SkeletonizeFile("frontend/src/features/user.tsx", []byte(src))

	for _, want := range []string{
		"interface UserProfileProps {",
		"id: string",
		"onUpdate: () => void",
		"type AccountID = string",
		"function useUserActions",
		"const UserProfile",
		"class UserService {",
		"fetchUsers(id: string)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("карта TS-файла должна содержать %q\ngot:\n%s", want, got)
		}
	}

	// Тела не должны просачиваться в карту.
	for _, leaked := range []string{"await fetch(this.base)", "return r.json()", "return <div>"} {
		if strings.Contains(got, leaked) {
			t.Errorf("карта не должна содержать тело функции: %q\ngot:\n%s", leaked, got)
		}
	}
}

// TestSkeletonizeFallback проверяет fallback для незнакомых расширений.
func TestSkeletonizeFallback(t *testing.T) {
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = "line" // нумерация не важна, только объём
	}
	got := SkeletonizeFile("configs/server.yaml", []byte(strings.Join(lines, "\n")))
	if !strings.Contains(got, "1:") {
		t.Errorf("fallback должен нумеровать строки, got:\n%s", got)
	}
	if !strings.Contains(got, "ещё 20 строк файла") {
		t.Errorf("fallback должен показывать первые 40 строк и пометку об обрезании хвоста, got:\n%s", got)
	}
}

// TestParseRange проверяет разбор интервалов строк.
func TestParseRange(t *testing.T) {
	cases := []struct {
		spec       string
		start, end int
		ok         bool
	}{
		{"20-45", 20, 45, true},
		{"40", 40, 40, true},
		{"90-", 90, 0, true},
		{"20:45", 20, 45, true},
		{"0", 0, 0, false},
		{"20-10", 0, 0, false},
		{"abc", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, c := range cases {
		s, e, ok := parseRange(c.spec)
		if ok != c.ok || s != c.start || e != c.end {
			t.Errorf("parseRange(%q) = (%d,%d,%v), want (%d,%d,%v)", c.spec, s, e, ok, c.start, c.end, c.ok)
		}
	}
}

// TestSplitRangeTarget проверяет разделение цели дозаправки на путь и интервал.
func TestSplitRangeTarget(t *testing.T) {
	cases := []struct {
		target, path, rng string
	}{
		{"internal/user.go", "internal/user.go", ""},
		{"internal/user.go:20-45", "internal/user.go", "20-45"},
		{"internal/user.go:40", "internal/user.go", "40"},
		{"internal/user.go 20-45", "internal/user.go", "20-45"},
		{"frontend/src/a.tsx", "frontend/src/a.tsx", ""},
	}
	for _, c := range cases {
		p, r := splitRangeTarget(c.target)
		if p != c.path || r != c.rng {
			t.Errorf("splitRangeTarget(%q) = (%q,%q), want (%q,%q)", c.target, p, r, c.path, c.rng)
		}
	}
}

// TestReadMapTool проверяет инструмент ReadMap: возвращает карты файлов без
// тел и уважает scope.
func TestReadMapTool(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "service.go"), []byte("package svc\n\nfunc Do(a int) int {\n\treturn a + 1\n}\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "secret.go"), []byte("package sec\n"), 0644)

	ops := &FileOps{OutputDir: dir}
	ops.SetScope([]string{"service.go"})

	result, err := ops.ReadMap(map[string]any{
		"filenames": []any{"service.go", "secret.go"},
	})
	if err != nil {
		t.Fatalf("ReadMap: %v", err)
	}
	var items []map[string]string
	if err := json.Unmarshal(result, &items); err != nil {
		t.Fatalf("ReadMap JSON: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("ReadMap должен вернуть 2 записи, got %d", len(items))
	}
	// service.go — успешная карта без тела.
	if items[0]["status"] != "success" {
		t.Fatalf("service.go должен быть прочитан: %#v", items[0])
	}
	if strings.Contains(items[0]["map"], "return a + 1") {
		t.Errorf("карта не должна содержать тело, got: %s", items[0]["map"])
	}
	if !strings.Contains(items[0]["map"], "func Do(a int) int") {
		t.Errorf("карта должна содержать сигнатуру, got: %s", items[0]["map"])
	}
	// secret.go — вне scope.
	if items[1]["status"] != "error" {
		t.Fatalf("secret.go вне scope должен быть отклонён: %#v", items[1])
	}
}

// TestFetchContext проверяет дозаправку контекста: карта файла и диапазон строк.
func TestFetchContext(t *testing.T) {
	dir := t.TempDir()
	src := "a\nb\nc\nd\ne\nf\ng\nh\n"
	_ = os.WriteFile(filepath.Join(dir, "x.txt"), []byte(src), 0644)

	ops := &FileOps{OutputDir: dir}

	// Карта (fallback для .txt): первые строки с нумерацией.
	got, ok := ops.FetchContext("x.txt")
	if !ok {
		t.Fatal("FetchContext(x.txt) должен найти файл")
	}
	if !strings.Contains(got, "1: a") {
		t.Errorf("карта x.txt должна начинаться с 1: a, got:\n%s", got)
	}

	// Диапазон строк.
	got, ok = ops.FetchContext("x.txt:3-5")
	if !ok {
		t.Fatal("FetchContext(x.txt:3-5) должен найти файл")
	}
	if !strings.Contains(got, "3: c") || !strings.Contains(got, "5: e") {
		t.Errorf("диапазон должен вернуть строки 3..5, got:\n%s", got)
	}
	if strings.Contains(got, "2: b") {
		t.Errorf("диапазон не должен включать строку 2, got:\n%s", got)
	}

	// Несуществующий файл — false.
	if _, ok := ops.FetchContext("missing.go"); ok {
		t.Error("FetchContext(missing.go) должен вернуть false")
	}

	// Вне области — false.
	_ = os.WriteFile(filepath.Join(dir, "y.go"), []byte("package y\n"), 0644)
	scoped := &FileOps{OutputDir: dir}
	scoped.SetScope([]string{"x.txt"})
	if _, ok := scoped.FetchContext("y.go"); ok {
		t.Error("FetchContext(y.go) вне scope должен вернуть false")
	}
}

// TestReadFilesLines проверяет «хирургическое окно» в ReadFiles.
func TestReadFilesLines(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "f.txt"), []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"), 0644)

	ops := &FileOps{OutputDir: dir}
	result, err := ops.ReadFiles(map[string]any{
		"filenames": []any{"f.txt"},
		"lines":     "3-6",
	})
	if err != nil {
		t.Fatalf("ReadFiles: %v", err)
	}
	var items []map[string]string
	if err := json.Unmarshal(result, &items); err != nil {
		t.Fatalf("ReadFiles JSON: %v", err)
	}
	if len(items) != 1 || items[0]["status"] != "success" {
		t.Fatalf("ReadFiles должен вернуть success: %#v", items)
	}
	if !strings.Contains(items[0]["content"], "3") || strings.Contains(items[0]["content"], "8") {
		t.Fatalf("ReadFiles с lines должен вернуть только диапазон строк: %#v", items[0])
	}
}

// Кеш карт кода: повторное построение карты того же файла обходит разбор
// (второй вызов возвращает тот же результат без записи в кеш повторно),
// изменение файла (новый mtime/размер) корректно инвалидирует запись.
func TestSkeletonCacheInvalidatesOnChange(t *testing.T) {
	t.Setenv("CODEGEN_SKELETON_CACHE", "1")
	skeletonCacheMu.Lock()
	clear(skeletonCache)
	skeletonCacheMu.Unlock()

	dir := t.TempDir()
	path := filepath.Join(dir, "api.go")
	src := []byte("package api\nfunc Alpha() {}\n")
	if err := os.WriteFile(path, src, 0644); err != nil {
		t.Fatal(err)
	}

	a := skeletonizeCached(path, src)
	b := skeletonizeCached(path, src)
	if a != b {
		t.Fatal("два вызова одного файла должны дать одинаковую карту")
	}
	if !strings.Contains(a, "func Alpha()") {
		t.Fatalf("карта должна содержать сигнатуру Alpha: %q", a)
	}

	// Меняем содержимое и mtime: кеш должен отдать актуальную карту.
	src2 := []byte("package api\ntype Beta struct{}\nfunc Gamma() {}\n")
	if err := os.WriteFile(path, src2, 0644); err != nil {
		t.Fatal(err)
	}
	newTime := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	c := skeletonizeCached(path, src2)
	if c == a {
		t.Fatal("карта после изменения файла должна отличаться от старой")
	}
	if !strings.Contains(c, "type Beta struct") || !strings.Contains(c, "func Gamma()") {
		t.Fatalf("после изменения файла карта должна отражать новые декларации: %q", c)
	}
}

// Отключённый кеш (CODEGEN_SKELETON_CACHE=0) не пишет и не читает записи.
func TestSkeletonCacheDisabled(t *testing.T) {
	t.Setenv("CODEGEN_SKELETON_CACHE", "0")
	skeletonCacheMu.Lock()
	clear(skeletonCache)
	skeletonCacheMu.Unlock()

	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	src := []byte("package main\nfunc Main() {}\n")
	if err := os.WriteFile(path, src, 0644); err != nil {
		t.Fatal(err)
	}
	_ = skeletonizeCached(path, src)
	if len(skeletonCache) != 0 {
		t.Fatalf("при отключённом кеше записей быть не должно, got %d", len(skeletonCache))
	}
}