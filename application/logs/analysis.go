// Разбор логов по запросу панели (кнопка «Разобрать»): сбор файлов проекта,
// фильтры поиска/уровня, детерминированный дайджест и текст промпта. Сырой лог
// целиком в промпт не идёт.
//
// Разбор строк, фильтры по уровню и сам дайджест — в domain/log; здесь —
// политика выборки файлов, cooldown повторного разбора и контракт ответа модели.
package logs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	logpkg "ai/domain/log"
)

// Req — запрос разбора: все поля опциональны, пустой запрос разбирает самые
// свежие логи проекта.
type Req struct {
	// File — имя файла лога (например calc.log). Пусто — самые свежие.
	File string `json:"file"`
	// Level — минимальная тяжесть: WARN или ERROR. Пусто — любые находки.
	Level string `json:"level"`
	// Query — подстрока-фильтр строк лога (из поиска панели).
	Query string `json:"query"`
}

// Analysis — подготовленный разбор для промпта.
type Analysis struct {
	Files  []string // какие файлы разобраны (для заголовка)
	Digest logpkg.LogDigest
	// Note — человекочитаемые условия выборки (поиск/уровень) для заголовка в
	// промпте. Модель должна знать, что она видит подмножество, а не весь лог.
	Note string
}

// FilesLabel — заголовок разбора: один файл или перечень.
func (a Analysis) FilesLabel() string { return FilesLabel(a.Files) }

// Key — ключ разбора для cooldown: файлы, условия выборки и первый/последний
// таймстамп находок. Меняется при любом изменении лога, поэтому «тот же лог»
// действительно тот же.
func (a Analysis) Key() string {
	fs := ""
	if len(a.Digest.Findings) > 0 {
		fs = fmt.Sprintf("%s|%d|%s|%s", a.Digest.File, a.Digest.Lines, a.Digest.Findings[0].FirstTS, a.Digest.Findings[0].LastTS)
	}
	return strings.Join(a.Files, ",") + "|" + a.Note + "|" + fs
}

// BuildAnalysis собирает логи проекта ровно теми же правилами, что и Logboard,
// фильтрует строки запросом и уровнем, после чего строит дайджест. root/global —
// рабочий каталог проекта и общий каталог логов; own — имя «своего» лога;
// maxFiles — предел числа разбираемых файлов при пустом File.
//
// Фильтр уровня — «не легче, чем запрошено»: при level=ERROR в разбор не
// попадают WARN, иначе кнопка «покажи ошибки» приносила бы тонну
// предупреждений. Фильтр применяется к сырым строкам ДО группировки, чтобы
// счётчики в находках отражали именно то, что запросили.
func BuildAnalysis(root, globalDir, own string, req Req, maxFiles int) (Analysis, error) {
	files, _ := CollectProject(Dirs(root, globalDir), globalDir, own)

	sel := PickFiles(files, strings.TrimSpace(req.File), own, maxFiles)
	if len(sel) == 0 {
		return Analysis{}, fmt.Errorf("лог %q не найден среди файлов проекта", req.File)
	}

	minLevel := ""
	switch strings.ToUpper(strings.TrimSpace(req.Level)) {
	case "ERROR", "ERR":
		minLevel = "ERROR"
	case "WARN", "WARNING":
		minLevel = "WARN"
	case "FATAL", "CRITICAL":
		minLevel = "FATAL"
	}
	query := strings.ToLower(strings.TrimSpace(req.Query))

	merged := logpkg.LogDigest{Findings: []logpkg.LogFinding{}}
	for _, f := range sel {
		body, err := os.ReadFile(f.Path)
		if err != nil {
			return Analysis{}, fmt.Errorf("чтение лога %s: %w", f.Name, err)
		}
		content := string(body)
		if query != "" {
			content = logpkg.FilterLogLines(content, query)
		}
		if minLevel != "" {
			content = logpkg.FilterLogByLevel(content, minLevel)
		}
		d := logpkg.BuildLogDigest(f.Name, content)
		merged.Lines += d.Lines
		merged.ProblemLines += d.ProblemLines
		merged.Dropped += d.Dropped
		// Шаблоны склеиваем по файлу: одинаковая ошибка в двух логах — одна
		// находка с суммой частот, а не две.
		merged.Findings = append(merged.Findings, d.Findings...)
	}
	merged.Findings = logpkg.MergeLogFindings(merged.Findings, logpkg.LogDigestMax)

	names := make([]string, 0, len(sel))
	for _, f := range sel {
		names = append(names, f.Name)
	}
	merged.File = FilesLabel(names)

	note := ""
	if query != "" {
		note = "только строки, содержащие «" + strings.TrimSpace(req.Query) + "»"
	}
	if minLevel != "" {
		if note != "" {
			note += "; "
		}
		note += "только уровни не легче " + minLevel
	}

	return Analysis{Files: names, Digest: merged, Note: note}, nil
}

// PickFiles выбирает файлы для чтения: по имени, либо (пусто/all) самые свежие
// (не больше max). Сначала — файл проекта (own): он почти всегда и есть нужный.
func PickFiles(in []Entry, want, own string, max int) []Entry {
	if want != "" && want != "all" {
		for _, f := range in {
			if f.Name == want || filepath.Base(f.Name) == want {
				return []Entry{f}
			}
		}
		return nil
	}
	sorted := append([]Entry(nil), in...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Modified > sorted[j].Modified })
	out := make([]Entry, 0, max)
	for _, f := range sorted {
		if len(out) == max {
			break
		}
		// Файл проекта при пустом запросе идёт первым и всегда: остальные
		// логи в общем каталоге принадлежат чужим прогонам.
		if f.Name == own {
			out = append([]Entry{f}, out...)
			continue
		}
		out = append(out, f)
	}
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// FilesLabel — заголовок перечня файлов для промпта и логов.
func FilesLabel(files []string) string {
	switch len(files) {
	case 0:
		return "(файлы не выбраны)"
	case 1:
		return files[0]
	default:
		return strings.Join(files, ", ")
	}
}

// DigestPrompt — блок промпта с дайджестом и контрактом ответа.
//
// Контракт задан явно, потому что без него модель по сводке находок пишет
// пересказ лога вместо разбора: без «цитаты-доказательства» и «гипотезы
// причины» она ограничивается «в логах есть ошибки».
func DigestPrompt(a Analysis) string {
	var b strings.Builder
	b.WriteString("\n\n=== РАЗБОР ЛОГА ПРОЕКТА ===\n")
	fmt.Fprintf(&b, "Пользователь нажал «Разобрать» в панели логов. Файл(ы): %s.\n", a.FilesLabel())
	fmt.Fprintf(&b, "Сводка подготовлена сервером детерминированно: разобрано строк %d, из них с признаком проблемы %d, сгруппировано в находки %d (частота — сколько раз встречался один и тот же шаблон; <path>/<file>/<n>/<uuid>/<str> — абстракции, конкретные значения есть в примере).\n",
		a.Digest.Lines, a.Digest.ProblemLines, len(a.Digest.Findings))
	if a.Note != "" {
		fmt.Fprintf(&b, "Условия выборки: %s.\n", a.Note)
	}
	if len(a.Digest.Findings) == 0 {
		b.WriteString("Находок нет: в выборке нет ни одной строки уровня WARN/ERROR/FATAL.\n")
		b.WriteString("Ответь коротко: критичных проблем в этом логе не найдено, объясни, что смотрел, и предложи, что проверить дальше.\n")
		return b.String()
	}
	b.WriteString("\nНаходки (по убыванию тяжести и частоты):\n")
	b.WriteString(a.Digest.Text())
	b.WriteString(`
Твой ответ (в чат, по-русски):
1. Возьми 5–7 самых значимых находок. Для каждой: что случилось, частота и тяжесть, ЦИТАТА-доказательство из примера, гипотеза причины (явно назови её гипотезой, если не уверен), является ли это шумом.
2. Отдельно скажи, что выглядит как первопричина остального.
3. Дай конкретные следующие шаги (что проверить, поправить, где смотреть).
4. Если по находкам ничего критичного нет — прямо скажи это, не выдумывай проблему.
Полный лог не читай целиком: он большой. Точечно докапывай находки инструментом ReadProjectLogs (grep по подстроке) — и только то, что реально нужно для вывода.
Отвечай сразу по этому разбору, не переспрашивай, что именно разобрать.
`)
	return b.String()
}

// Cooldown — окно, в котором повторный разбор того же лога не запускает
// модель заново. Кнопку легко кликнуть дважды, а каждый разбор — полный ход
// ассистента с промптом и инструментами.
type Cooldown struct {
	mu      sync.Mutex
	window  time.Duration
	lastKey string
	lastAt  time.Time
}

// NewCooldown создаёт окно подавления повторов длительностью window.
func NewCooldown(window time.Duration) *Cooldown {
	return &Cooldown{window: window}
}

// Mark запоминает разбор. Возвращает true, если такой же ключ разбирали
// недавно (в пределах окна).
func (c *Cooldown) Mark(key string) (recent bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.lastKey == key && now.Sub(c.lastAt) < c.window {
		return true
	}
	c.lastKey, c.lastAt = key, now
	return false
}

// Dirs — каталоги, где могут лежать лог-файлы проекта: общий каталог логов
// сервера + подкаталог logs внутри рабочей папки проекта (если есть).
func Dirs(root, globalDir string) []string {
	dirs := []string{globalDir}
	if root != "" {
		if local := filepath.Join(root, "logs"); FileExists(local) {
			dirs = append(dirs, local)
		}
	}
	return dirs
}
