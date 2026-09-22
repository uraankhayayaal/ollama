package runner

// Сжатие истории диалога (Скорость 3 → Ф-6..Ф-11).
//
// Длинный контекст агентского цикла (nudge-подсказки, результаты инструментов)
// раздувает запрос до десятков тысяч токенов. Перед каждым запросом история
// сжимается под символьный бюджет. Базовый режим (как раньше): сохраняются
// системные сообщения, первая user-постановка задачи (head) и «хвост» последних
// сообщений; старейшие полные юниты из середины выбрасываются детерминированно.
// Юнит — это assistant с tool_calls вместе со своими tool-результатами: вызов
// инструмента никогда не остаётся без результата.
//
// Расширения (опциональны, отключены по умолчанию):
//   - Ф-6  CODEGEN_HISTORY_NOTICE  — «памятка» в системе: модель узнаёт, что
//     часть истории убрана, и может точечно дочитать факты через
//     CodeSearch/ReadFiles, а не галлюцинировать.
//   - Ф-7  CODEGEN_HISTORY_EVICT   — выброшенные юниты не уничтожаются, а
//     индексируются в векторную память (sink) как «эпизоды»; позже их находит
//     CodeSearch (вытеснение вместо потери).
//   - Ф-8  CODEGEN_HISTORY_RANK    — выбор кандидатов на выброс семантический
//     (близость к задаче), а не только позиционный.
//   - Ф-9  CODEGEN_HISTORY_OUTLINE — для убранного кода в памятку кладутся
//     LSP-оглавления файлов (documentSymbol: имя + диапазон строк), чтобы
//     модель могла точечно дочитать нужный диапазон.
//   - Ф-10 CODEGEN_HISTORY_COMPACT — абстрактивная компакция убранной середины
//     в «протокол» (решения/файлы/незакрытое) одним вызовом провайдера.
//   - Ф-11 CODEGEN_HISTORY_TOKENS  — бюджет в токенах по фактическому usage
//     провайдера (управляется в цикле runner.go).
//
// Resume остаётся безопасным: базовое сжатие детерминировано, побочные шаги
// (RAG/LSP/компакция) не меняют чередования ролей сообщений.

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// historyBudget возвращает символьный бюджет истории (CODEGEN_HISTORY_BUDGET,
// 0 = сжатие выключено). Приблизительно: 1 токен ≈ 4 символа.
func historyBudget() int {
	if v := os.Getenv("CODEGEN_HISTORY_BUDGET"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// historyTokens возвращает токен-лимит истории по фактическому usage
// провайдера (CODEGEN_HISTORY_TOKENS, 0 = выключено, Ф-11).
func historyTokens() int {
	if v := os.Getenv("CODEGEN_HISTORY_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// envFlag — включает флаг по 1/true/yes/on (для CODEGEN_HISTORY_*).
func envFlag(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// estimateLen оценивает «вес» истории в символах (содержимое + аргументы
// вызовов инструментов).
func estimateLen(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
		for _, tc := range m.ToolCalls {
			n += len(tc.Name) + len(tc.Arguments)
		}
	}
	return n
}

// EvictionItem — единица вытесняемого контента (Ф-7): один выброшенный
// кусок истории (assistant-текст или tool-результат).
type EvictionItem struct {
	Tool string `json:"tool"` // имя инструмента; пусто — assistant-сообщение
	Text string `json:"text"`
}

// CompressionReport — что произошло при сжатии истории (для логов, sink'а и
// памятки). Ошибки опциональных побочных шагов не фатальны — переходят в Errors.
type CompressionReport struct {
	Dropped       int            // сколько сообщений выброшено
	Evicted       int            // сколько юнитов вытеснено в векторную память (Ф-7)
	EvictionItems []EvictionItem // выброшенный контент (для sink и LSP-оглавлений)
	Summary       string         // компакция убранной середины (Ф-10)
	Outlines      string         // LSP-оглавления убранных файлов (Ф-9)
	Notice        string         // текст памятки (Ф-6), если была добавлена
	Errors        []string       // warn-ошибки опциональных шагов (не падают)
}

// CompressOptions — опции расширенного сжатия. Все побочные шаги опциональны:
// nil-функции и выключенные флаги оставляют сжатие чисто позиционным.
// Функции (а не интерфейсы) позволяют адаптерам жить в пакетах rag/lsp без
// импорта runner (нет циклов) и делают тесты hermetic.
type CompressOptions struct {
	Budget  int
	Project string

	Notice bool // Ф-6: памятка в систему о сжатии

	Evict   bool // Ф-7: вытеснять выброшенное в векторную память
	EvictFn func(ctx context.Context, project string, items []EvictionItem) error

	Rank      bool // Ф-8: семантический выбор кандидатов
	RankFn    func(ctx context.Context, query string, texts []string) ([]float32, error)
	RankLimit int // максимум юнитов, сохраняемых из середины (0 — без лимита)

	Outline   bool // Ф-9: LSP-оглавления убранных файлов
	OutlineFn func(ctx context.Context, project string, rels []string) (string, error)

	Compact   bool // Ф-10: абстрактивная компакция середины
	CompactFn func(ctx context.Context, text string) (string, error)
}

// compressionUnit — неделимая единица выброса: начальная граница юнита и его
// вес. Юнит либо одиночное сообщение, либо assistant(tool_calls) вместе со
// следующими за ним tool-сообщениями — вызов не остаётся без своих результатов.
type compressionUnit struct {
	start, end int
	weight     int
	text       string
}

func makeUnit(msgs []Message, start, end int) compressionUnit {
	w := 0
	var b strings.Builder
	for i := start; i < end; i++ {
		w += estimateLen(msgs[i : i+1])
		if msgs[i].Role == "tool" {
			b.WriteString("tool ")
			b.WriteString(msgs[i].ToolName)
			b.WriteString(": ")
		} else {
			b.WriteString(msgs[i].Role)
			b.WriteString(": ")
		}
		b.WriteString(msgs[i].Content)
		b.WriteByte('\n')
	}
	return compressionUnit{start: start, end: end, weight: w, text: b.String()}
}

// splitUnits разбивает историю на неделимые юниты (assistant+его tool-результаты
// не разрываются). Случайные tool-сообщения без своего assistant приклеиваются
// к предыдущему юниту, чтобы сжатие не оставляло «висящих» результатов.
func splitUnits(msgs []Message) []compressionUnit {
	var units []compressionUnit
	i := 0
	for i < len(msgs) {
		start := i
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 {
			i++
			for i < len(msgs) && msgs[i].Role == "tool" {
				i++
			}
		} else {
			i++
		}
		units = append(units, makeUnit(msgs, start, i))
	}

	out := make([]compressionUnit, 0, len(units))
	for _, u := range units {
		if len(out) > 0 && msgs[u.start].Role == "tool" && out[len(out)-1].end == u.start {
			prev := &out[len(out)-1]
			prev.end = u.end
			prev.weight += u.weight
			prev.text += u.text
			continue
		}
		out = append(out, u)
	}
	return out
}

// headIndex — граница «головы»: всё до первой user-постановки включительно.
// Голова всегда сохраняется (система + задача).
func headIndex(msgs []Message) int {
	for i, m := range msgs {
		if m.Role == "user" {
			return i + 1
		}
	}
	return len(msgs)
}

// maxEvictionItemChars — предел текста одной единицы вытеснения (защита памяти
// при гигантских tool-результатах).
const maxEvictionItemChars = 32 * 1024

func clipEvictText(s string) string {
	if len(s) <= maxEvictionItemChars {
		return s
	}
	return s[:maxEvictionItemChars] + "\n[...урезано...]"
}

// truncateLines оставляет первые и последние строки long текста с эллипсисом
// (фолбэк компакции и обрезка памятки).
func truncateLines(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n[...обрезано компакцией...]"
}

// fileMarkerRe — маркер вида "=== path ===" (gitresolve и др.).
var fileMarkerRe = regexp.MustCompile(`(?m)^===\s+([^\n=]+?)\s+===\s*$`)

// namedFieldRe — поля "filename" и "file" в JSON-результатах ReadFiles/CodeSearch.
var namedFieldRe = regexp.MustCompile(`"(?:filename|file)"\s*:\s*"([^"]+)"`)

// extractFiles вытаскивает относительные пути файлов из выброшенного контента
// (для LSP-оглавлений, Ф-9): маркеры "=== path ===" и JSON-поля filename/file.
// Список детерминирован: первое вхождение, отсортированные, без служебного мусора.
func extractFiles(texts []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(strings.Trim(p, "\"'"))
		if p == "" || p == "." || strings.HasPrefix(p, "@episode") {
			return
		}
		if strings.HasPrefix(p, ".git/") || strings.HasPrefix(p, "node_modules/") || strings.Contains(p, "/node_modules/") {
			return
		}
		if !strings.Contains(p, "/") && !strings.Contains(p, ".") {
			return
		}
		if seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, t := range texts {
		for _, m := range fileMarkerRe.FindAllStringSubmatch(t, -1) {
			add(m[1])
		}
		for _, m := range namedFieldRe.FindAllStringSubmatch(t, -1) {
			add(m[1])
		}
	}
	sort.Strings(out)
	return out
}

// maxOutlineFiles — максимум LSP-оглавлений в памятке (Ф-9).
const maxOutlineFiles = 4

// CompressHistory сжимает историю под символьный бюджет (базовый позиционный
// режим, без памятки и побочных шагов). Сохранена для совместимости и является
// частным случаем CompressContext.
func CompressHistory(msgs []Message, budget int) []Message {
	out, _ := CompressContext(context.Background(), msgs, CompressOptions{Budget: budget})
	return out
}

// CompressContext сжимает историю через пайплайн Ф-6..Ф-10: позиционное
// ядро плюс опциональные вытеснение/ранжирование/оглавления/компакцию/памятку.
// Возвращает новую историю и отчёт о сжатии. Опциональные шаги не роняют
// сжатие: их ошибки уходят в report.Errors (degrade, как RAG/LSP инструменты).
func CompressContext(ctx context.Context, msgs []Message, opts CompressOptions) ([]Message, *CompressionReport) {
	rep := &CompressionReport{}
	if len(msgs) <= 2 || opts.Budget <= 0 || estimateLen(msgs) <= opts.Budget {
		return msgs, rep
	}

	head := headIndex(msgs)
	headW := estimateLen(msgs[:head])
	units := splitUnits(msgs)

	// index юнита с началом head (юниты непрерывны; head лежит на границе юнита,
	// т.к. первое user-сообщение всегда одиночный юнит без tool_calls).
	headUnit := 0
	for headUnit < len(units) && units[headUnit].start < head {
		headUnit++
	}

	// Позиционный хвост: жадно добавляем юниты с конца, пока вес не превышает
	// бюджет (остаток после головы). Хвост никогда не открывается tool-юнитом —
	// юниты неделимы.
	tailStart := len(units)
	tailW := 0
	for i := len(units) - 1; i >= headUnit; i-- {
		if headW+tailW+units[i].weight > opts.Budget {
			break
		}
		tailStart = i
		tailW += units[i].weight
	}

	// Юниты середины [headUnit, tailStart) — кандидаты на выброс.
	middle := units[headUnit:tailStart]

	// keep — индексы юнитов (абсолютные), которые НЕ выбрасываются.
	keep := map[int]bool{}

	// Ф-8: retrieval-guided выбор. Ранжируем юниты середины по близости к
	// задаче (голова) и оставляем наиболее релевантные, пока влезают в остаток
	// бюджета. Ошибка ранжировщика — фолбэк на позиционный выброс (все середины).
	if opts.Rank && opts.RankFn != nil && len(middle) > 0 {
		remaining := opts.Budget - (headW + tailW)
		if remaining > 0 {
			query := msgs[:head][0].Content
			for _, m := range msgs[:head] {
				if len(m.Content) > len(query) {
					query = m.Content
				}
			}
			texts := make([]string, len(middle))
			for i, u := range middle {
				texts[i] = u.text
			}
			scores, err := opts.RankFn(ctx, query, texts)
			if err == nil && len(scores) == len(middle) {
				order := make([]int, len(middle))
				for i := range order {
					order[i] = i
				}
				sort.SliceStable(order, func(a, b int) bool { return scores[order[a]] > scores[order[b]] })
				keptW := 0
				for _, oi := range order {
					u := middle[oi]
					if keptW+u.weight > remaining {
						continue
					}
					keep[headUnit+oi] = true
					keptW += u.weight
					if opts.RankLimit > 0 && len(keep) >= opts.RankLimit {
						break
					}
				}
				rep.Evicted = len(middle) - len(keep)
			} else {
				if err != nil {
					rep.Errors = append(rep.Errors, "ранжирование кандидатов недоступно ("+err.Error()+"), позиционный выброс")
				}
				rep.Evicted = len(middle)
			}
		} else {
			rep.Evicted = len(middle)
		}
	} else {
		rep.Evicted = len(middle)
	}

	// Собственно выброс: голова всегда сохраняется; середина — только keep;
	// хвост (если есть) — целиком.
	out := make([]Message, 0, head+(len(msgs)-head))
	out = append(out, msgs[:head]...)
	for i := headUnit; i < tailStart; i++ {
		if keep[i] {
			out = append(out, msgs[units[i].start:units[i].end]...)
			continue
		}
		rep.Dropped += units[i].end - units[i].start
		rep.EvictionItems = append(rep.EvictionItems, unitItems(units[i], msgs)...)
	}
	if tailStart < len(units) {
		out = append(out, msgs[units[tailStart].start:]...)
	} else if headUnit < len(units) {
		// Хвост не влез нисколько: максимальная компакция до головы. Хвостовые
		// юниты тоже уходят в вытеснение (не теряются), а не выбрасываются.
		for i := headUnit; i < len(units); i++ {
			rep.Dropped += units[i].end - units[i].start
			rep.EvictionItems = append(rep.EvictionItems, unitItems(units[i], msgs)...)
		}
	}

	if len(rep.EvictionItems) == 0 {
		return out, rep
	}

	// Ф-7: вытеснение выброшенного в векторную память (RAG). Тихо (degrade):
	// недоступный Qdrant логируется и не роняет генерацию.
	if opts.Evict && opts.EvictFn != nil {
		if err := opts.EvictFn(ctx, opts.Project, rep.EvictionItems); err != nil {
			rep.Errors = append(rep.Errors, "вытеснение в векторную память: "+err.Error())
			Debugf("COMPRESS: вытеснение в RAG не удалось: %v", err)
		}
	}

	// Ф-10: абстрактивная компакция середины — «протокол» убранного.
	if opts.Compact && opts.CompactFn != nil {
		if s, err := opts.CompactFn(ctx, unitItemsText(rep.EvictionItems)); err == nil && strings.TrimSpace(s) != "" {
			rep.Summary = s
		} else if err != nil && err.Error() != context.Canceled.Error() {
			rep.Errors = append(rep.Errors, "компакция убранного: "+err.Error())
		}
	}

	// Ф-9: LSP-оглавления убранных файлов — якоря для точечного дочитывания.
	if opts.Outline && opts.OutlineFn != nil {
		files := extractFiles(evictTexts(rep.EvictionItems))
		if len(files) > maxOutlineFiles {
			files = files[:maxOutlineFiles]
		}
		if len(files) > 0 {
			if o, err := opts.OutlineFn(ctx, opts.Project, files); err == nil && strings.TrimSpace(o) != "" {
				rep.Outlines = o
			} else if err != nil {
				rep.Errors = append(rep.Errors, "LSP-оглавления: "+err.Error())
			}
		}
	}

	// Ф-6: памятка в систему — модель знает, что история сжата, и может
	// дочитать факты инструментами вместо галлюцинаций.
	if opts.Notice {
		rep.Notice = compressionNoticeMessage(opts.Project, rep.Dropped, rep.Summary, rep.Outlines)
		out = attachNotice(out, head, rep.Notice)
	}

	return out, rep
}

// unitItems превращает юнит в список вытесняемых элементов (по одному на
// сообщение; assistant с tool_calls складывает и свои аргументы, и текст).
func unitItems(u compressionUnit, msgs []Message) []EvictionItem {
	items := make([]EvictionItem, 0, u.end-u.start)
	for i := u.start; i < u.end; i++ {
		txt := msgs[i].Content
		if msgs[i].Role == "assistant" {
			for _, tc := range msgs[i].ToolCalls {
				if tc.Arguments != "" {
					txt += "\ntool_calls " + tc.Name + ": " + tc.Arguments
				}
			}
		}
		items = append(items, EvictionItem{Tool: toolNameOf(msgs[i]), Text: clipEvictText(txt)})
	}
	return items
}

func toolNameOf(m Message) string {
	if m.Role == "tool" {
		return m.ToolName
	}
	return ""
}

func unitItemsText(items []EvictionItem) string {
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteByte('\n')
		}
		if it.Tool != "" {
			b.WriteString("tool ")
			b.WriteString(it.Tool)
			b.WriteString(": ")
		}
		b.WriteString(it.Text)
	}
	return b.String()
}

// EvictionItemsText склеивает вытесненные юниты в единый текст «памятки»
// (пометки инструментов сохранены) — используется адаптерами вытеснения в
// векторную память (Ф-7) вне пакета runner.
func EvictionItemsText(items []EvictionItem) string {
	return unitItemsText(items)
}

func evictTexts(items []EvictionItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Text
	}
	return out
}

// maxNoticeChars — лимит объёма памятки (Ф-6) в одном system-сообщении.
const maxNoticeChars = 6 * 1024

// compressionNoticeMessage составляет текст памятки о сжатии.
func compressionNoticeMessage(project string, dropped int, summary, outlines string) string {
	var b strings.Builder
	b.WriteString("История твоего диалога была сжата для экономии токенов: из середины убрано ")
	b.WriteString(strconv.Itoa(dropped))
	b.WriteString(" сообщений (старейшие шаги и результаты инструментов). Система, постановка задачи и актуальный хвост сохранены.\n")
	b.WriteString("Если для продолжения нужны детали убранных шагов — найди их инструментами (CodeSearch/ReadFiles/LspDefinition/LspHover), НЕ изобретай их по памяти и НЕ повторяй маркеры NEED_*.\n")
	if strings.TrimSpace(summary) != "" {
		b.WriteString("\nРезюме убранной части:\n")
		b.WriteString(summary)
		b.WriteByte('\n')
	}
	if strings.TrimSpace(outlines) != "" {
		b.WriteString("\nГде лежат убранные файлы (оглавления):\n")
		b.WriteString(outlines)
		b.WriteByte('\n')
	}
	if project != "" {
		b.WriteString("\nПроект: ")
		b.WriteString(project)
		b.WriteByte('\n')
	}
	s := b.String()
	if len(s) > maxNoticeChars {
		s = s[:maxNoticeChars] + "\n[памятка урезана]"
	}
	return s
}

// attachNotice дописывает памятку в последнее system-сообщение «головы»
// (после неё идут только user/tool-раунды — чередование ролей не ломается).
// Если system-сообщений нет — памятка становится первым сообщением.
func attachNotice(msgs []Message, head int, notice string) []Message {
	notice = "\n\n" + notice
	for i := head - 1; i >= 0; i-- {
		if msgs[i].Role == "system" {
			msgs[i].Content += notice
			return msgs
		}
	}
	return append([]Message{{Role: "system", Content: strings.TrimPrefix(notice, "\n\n")}}, msgs...)
}