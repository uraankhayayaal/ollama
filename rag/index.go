// Индексация чанков в Qdrant (см. PLAN-2026-09-19-done-qdrant.md, Ф-2;
// версионирование по веткам — PLAN-2026-09-27-done-branch-aware-rag.md, Р-1/Р-3/Р-8).
//
// Чанк становится точкой коллекции с payload (project_name/file_path/
// start_line/end_line/code_content/scope) и версионными полями (branch/
// commit_sha/chunk_id/replaced_by). Каждая точка — версия чанка В ВЕТКЕ: перед
// загрузкой новой версии прежние активные точки этой ветки получают
// replaced_by = <commit>, а новая точка грузится с commit_sha = <commit> и
// replaced_by = null («актуальна»). Поиск по ветке (Р-4) берёт только активные
// точки ветки, поэтому мутация в соседнем эпике не видна агенту.
//
// Точки одной версии различаются по ID (проект+файл+ветка+коммит+начало
// чанка), поэтому повторный прогон с тем же коммитом не плодит дубликаты, а
// замена версии сохраняет предыдущую (см. pruneSuperseded).

package rag

import (
	"context"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"strconv"
	"strings"

	"ai/logging"

	"github.com/qdrant/go-client/qdrant"
)

// Ключи payload чанка в Qdrant.
const (
	PayloadProject   = "project_name"
	PayloadFile      = "file_path"
	PayloadStartLine = "start_line"
	PayloadEndLine   = "end_line"
	PayloadCode      = "code_content"
	PayloadScope     = "scope"
	// PayloadBranch — ветка, в чьей версии снят чанк (см. branch.go).
	PayloadBranch = "branch"
	// PayloadCommit — коммит, на котором проиндексирован чанк.
	PayloadCommit = "commit_sha"
	// PayloadChunkID — стабильный идентификатор чанка между коммитами
	// (см. ChunkID): одна и та же функция в разных коммитах делит chunk_id.
	PayloadChunkID = "chunk_id"
	// PayloadContentHash — отпечаток содержимого чанка. Нужен, чтобы
	// переиндексация НЕЗАКОММИЧЕННЫХ правок агента (тот же HEAD, другое
	// содержимое) не считалась «уже проиндексированной» и не оставляла в
	// индексе устаревший код (Ф-5 + Р-3).
	PayloadContentHash = "content_hash"
	// PayloadReplacedBy — коммит, чьей версией чанк заменён; пусто/null —
	// чанк актуален в ветке (условие поиска).
	PayloadReplacedBy = "replaced_by"
)

// Лимиты версионированной индексации.
const (
	// indexBatchSize — максимум точек в одном Upsert-вызове.
	indexBatchSize = 64
	// payloadBatchSize — максимум точек в одном SetPayload/Delete по списку ID.
	payloadBatchSize = 256
	// scrollPageSize — страница Scroll при обходе активных точек ветки.
	scrollPageSize = 512
	// scrollMaxPoints — предел обхода активных точек ветки (защита от
	// бесконечного цикла на очень больших индексах; при достижении предела
	// лишние точки остаются активными и не будут помечены устаревшими).
	scrollMaxPoints = 50_000
)

// IndexOptions — ветка и версия индексации (PLAN-2026-09-27-done-branch-aware-rag.md,
// Р-3). Пустые поля трактуются как «ветка по умолчанию без коммита»:
// branch = MainBranch, commit пуст — точки просто заменяют друг друга
// (обратная совместимость для проектов без Git, решение №7).
type IndexOptions struct {
	// Branch — ветка, чьи точки обновляются (main, ai/epic/ARCH-01, ...).
	Branch string
	// CommitSHA — коммит, на котором сняты чанки. Одинаковый коммит делает
	// повторную индексацию идемпотентной (точки не пересоздаются), а
	// изменение коммита помечает прежние точки как устаревшие.
	CommitSHA string
}

// branchOrMain — ветка из опций; пусто — MainBranch (проекты без Git).
func (o IndexOptions) branchOrMain() string {
	if b := strings.TrimSpace(o.Branch); b != "" {
		return b
	}
	return MainBranch
}

// commit — обрезанный SHA коммита (пусто — версия не задана).
func (o IndexOptions) commit() string { return strings.TrimSpace(o.CommitSHA) }

// IndexItem — один файл для полной индексации проекта.
type IndexItem struct {
	Path    string // относительный slash-путь от корня проекта
	Scope   string // область проекта (каталог первого уровня, "root" для корня)
	Content string
}

// IndexResult — сводка индексации проекта.
type IndexResult struct {
	Files  int      // сколько файлов исходно взято в обработку
	Chunks int      // сколько чанков успешно загружено
	Errors []string // источники, не загруженные по ошибке
}

// activePoint — сведения об активной (не заменённой) точке чанка в ветке.
type activePoint struct {
	pointID     uint64
	chunkID     string
	commit      string
	contentHash string
	file        string
}

// IndexProject полностью переиндексирует список файлов проекта В ВЕТКЕ opts:
// активные точки ветки помечаются устаревшими (replaced_by = коммит), новые
// версии чанков грузятся с chunk_id/commit_sha. Файлы, исчезнувшие из прогона,
// также помечаются устаревшими — «удаление файла» (Р-8). Точки других веток и
// проектов не трогаются. Повторный прогон с тем же коммитом идемпотентен.
func (c *Client) IndexProject(ctx context.Context, projectName string, files []IndexItem, opts IndexOptions) (*IndexResult, error) {
	branch := opts.branchOrMain()
	commit := opts.commit()

	// Индекс, собранный до версионирования, не имеет поля branch: для ветки
	// main такие точки считаются её собственными и чистятся на полном прогоне
	// (иначе они дублировали бы новые версии тех же чанков в выдаче).
	if branch == MainBranch {
		if err := c.deleteByFilter(ctx, &qdrant.Filter{Must: []*qdrant.Condition{
			qdrant.NewMatchKeyword(PayloadProject, projectName),
			qdrant.NewIsNull(PayloadBranch),
		}}); err != nil {
			return nil, err
		}
	}

	// Активные точки ветки до прогона: нужны, чтобы пометить устаревшими
	// версии чанков, которых в новом срезе уже нет (удалённые функции/файлы).
	prev, err := c.activePoints(ctx, projectName, branch, "")
	if err != nil {
		return nil, err
	}

	res := &IndexResult{Files: len(files)}
	indexed := make(map[string]bool, len(files))
	for _, f := range files {
		indexed[f.Path] = true
		chunks := ChunkFile(f.Path, f.Content)
		if len(chunks) == 0 {
			// Файл без чанков (пустой/неиндексируемый) — его прежние версии
			// в ветке становятся устаревшими.
			if err := c.supersedeFile(ctx, projectName, f.Path, branch, commit); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", f.Path, err))
			}
			continue
		}
		uploaded, err := c.upsertChunks(ctx, projectName, f.Path, f.Scope, opts, chunks)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", f.Path, err))
			continue
		}
		res.Chunks += uploaded
	}

	// Файлы, которые были в индексе ветки, но не попали в прогон (удалены
	// мутацией или переименованы), — их версии тоже устаревают.
	vanished := make([]string, 0, 4)
	seenFile := map[string]bool{}
	for _, p := range prev {
		if p.file == "" || indexed[p.file] || seenFile[p.file] {
			continue
		}
		seenFile[p.file] = true
		vanished = append(vanished, p.file)
	}
	for _, rel := range vanished {
		if err := c.supersedeFile(ctx, projectName, rel, branch, commit); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", rel, err))
		}
	}
	return res, nil
}

// IndexFile переиндексирует один файл в ветке (частичная переиндексация после
// мутации, Ф-5): прежние версии чанков файла в ветке помечаются устаревшими,
// новые грузятся с chunk_id/commit_sha (Р-3). Файл без чанков только
// помечает прежние версии устаревшими (файл очищен). Возвращает число чанков
// файла (загруженных и уже бывших актуальными).
func (c *Client) IndexFile(ctx context.Context, projectName, relPath, scope, content string, opts IndexOptions) (int, error) {
	chunks := ChunkFile(relPath, content)
	if len(chunks) == 0 {
		if err := c.supersedeFile(ctx, projectName, relPath, opts.branchOrMain(), opts.commit()); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if _, err := c.upsertChunks(ctx, projectName, relPath, scope, opts, chunks); err != nil {
		return 0, err
	}
	return len(chunks), nil
}

// DeleteFile удаляет все чанки файла в ветке: точки помечаются устаревшими}
// (replaced_by = коммит), поэтому поиск их больше не возвращает, а история
// версий сохраняется (Р-8). Для полного снятия индексов (IndexProject по main
// на другом коммите) сверх одной версии используется pruneSuperseded.
func (c *Client) DeleteFile(ctx context.Context, projectName, relPath string, opts IndexOptions) error {
	return c.supersedeFile(ctx, projectName, relPath, opts.branchOrMain(), opts.commit())
}

// upsertChunks эмбеддит чанки и грузит их пакетами (indexBatchSize) с
// версионным payload (branch/commit_sha/chunk_id/content_hash/replaced_by).
// Перед загрузкой прежние активные версии тех же chunk_id в ветке помечаются
// устаревшими. Чанк, уже проиндексированный с тем же коммитом И тем же
// содержимым, не пересоздаётся (идемпотентность); правка рабочего дерева без
// коммита меняет content_hash, поэтому попадает в индекс как новая версия.
// Возвращает число реально загруженных чанков. Текст на эмбеддинг
// предваряется служебной шапкой — путь и координаты файла.
func (c *Client) upsertChunks(ctx context.Context, projectName, relPath, scope string, opts IndexOptions, chunks []Chunk) (int, error) {
	branch := opts.branchOrMain()
	commit := opts.commit()

	prev, err := c.activePoints(ctx, projectName, branch, relPath)
	if err != nil {
		return 0, err
	}

	// Готовим точки к загрузке, попутно решая, какие прежние версии устареют.
	points := make([]*qdrant.PointStruct, 0, len(chunks))
	keep := make(map[string]bool, len(chunks))
	for _, ch := range chunks {
		cid := ChunkID(projectName, relPath, chunkKey(ch))
		hash := contentHash(ch.Content)
		if old, existed := prev[cid]; existed && commit != "" &&
			old.commit == commit && old.contentHash == hash {
			// Та же версия с того же коммита и то же содержимое: точка уже
			// актуальна, повторная загрузка лишь повторила бы эмбеддинг.
			keep[cid] = true
			continue
		}
		vec, eerr := c.embed.Embed(ctx, chunkEmbedText(relPath, ch))
		if eerr != nil {
			return 0, fmt.Errorf("rag: эмбеддинг %s:%d: %w", relPath, ch.StartLine, eerr)
		}
		points = append(points, &qdrant.PointStruct{
			Id: qdrant.NewIDNum(pointID(projectName, relPath, branch, commit, ch.StartLine, hash)),
			Payload: qdrant.NewValueMap(map[string]any{
				PayloadProject:     projectName,
				PayloadFile:        relPath,
				PayloadStartLine:   int64(ch.StartLine),
				PayloadEndLine:     int64(ch.EndLine),
				PayloadCode:        ch.Content,
				PayloadScope:       scope,
				PayloadBranch:      branch,
				PayloadCommit:      commit,
				PayloadChunkID:     cid,
				PayloadContentHash: hash,
			}),
			Vectors: qdrant.NewVectorsDense(vec),
		})
	}

	// Прежние версии устаревают: и те chunk_id, что перезаписаны сейчас, и те,
	// что исчезли из файла (функция удалена). Точки той же версии (keep)
	// остаются активными.
	var superseded []*qdrant.PointId
	for cid, p := range prev {
		if !keep[cid] {
			superseded = append(superseded, qdrant.NewIDNum(p.pointID))
		}
	}

	if err := c.supersede(ctx, projectName, relPath, branch, commit, superseded); err != nil {
		return 0, err
	}

	for start := 0; start < len(points); start += indexBatchSize {
		end := start + indexBatchSize
		if end > len(points) {
			end = len(points)
		}
		if _, err := c.store.Upsert(ctx, &qdrant.UpsertPoints{
			CollectionName: c.collection,
			Points:         points[start:end],
		}); err != nil {
			return 0, &UnavailableError{Err: err}
		}
	}
	return len(points), nil
}

// supersede помечает точки файла устаревшими в ветке. Пустой commit означает
// версию без Git: прежние точки удаляются целиком (обратная совместимость с
// прежним поведением IndexFile/DeleteFile). С коммитом — проставляется
// replaced_by, затем подчищаются версии старше предыдущей (одна история на
// файл), чтобы индекс не рос бесконечно.
func (c *Client) supersede(ctx context.Context, projectName, relPath, branch, commit string, points []*qdrant.PointId) error {
	if len(points) == 0 {
		return nil
	}
	if commit == "" {
		return c.deleteIDs(ctx, points)
	}
	if _, err := c.store.SetPayload(ctx, &qdrant.SetPayloadPoints{
		CollectionName: c.collection,
		Payload:        qdrant.NewValueMap(map[string]any{PayloadReplacedBy: commit}),
		// Wait: точка должна уйти из выдачи ДО следующего prune/поиска —
		// иначе между обновлением payload и подчисткой осталось бы окно, в
		// котором старая и новая версии чанка видны одновременно.
		Wait: qdrant.PtrOf(true),
		PointsSelector: qdrant.NewPointsSelectorFilter(&qdrant.Filter{
			Must: []*qdrant.Condition{qdrant.NewHasID(points...)},
		}),
	}); err != nil {
		return &UnavailableError{Err: err}
	}
	return c.pruneSuperseded(ctx, projectName, relPath, branch, commit)
}

// supersedeFile помечает устаревшими все активные точки файла в ветке (файл
// удалён, очищен или исчез из полного прогона индексации).
func (c *Client) supersedeFile(ctx context.Context, projectName, relPath, branch, commit string) error {
	prev, err := c.activePoints(ctx, projectName, branch, relPath)
	if err != nil {
		return err
	}
	points := make([]*qdrant.PointId, 0, len(prev))
	for _, p := range prev {
		points = append(points, qdrant.NewIDNum(p.pointID))
	}
	if err := c.supersede(ctx, projectName, relPath, branch, commit, points); err != nil {
		return err
	}
	// Версии без поля branch (индекс, собранный до версионирования) тоже
	// считаются версиями main-файла — иначе они остались бы в выдаче.
	if branch == MainBranch {
		return c.deleteByFilter(ctx, &qdrant.Filter{Must: []*qdrant.Condition{
			qdrant.NewMatchKeyword(PayloadProject, projectName),
			qdrant.NewMatchKeyword(PayloadFile, relPath),
			qdrant.NewIsNull(PayloadBranch),
		}})
	}
	return nil
}

// pruneSuperseded удаляет версии файла в ветке, устаревшие НЕ этим коммитом:
// остаётся ровно одна предыдущая версия (след чанка до последнего изменения).
// Без этого каждая переиндексация добавляла бы поколение точек навсегда.
// Правки рабочего дерева без коммита помечаются replaced_by тем же коммитом —
// они остаются как след предыдущей ревизии и подчищаются следующим коммитом.
func (c *Client) pruneSuperseded(ctx context.Context, projectName, relPath, branch, commit string) error {
	return c.deleteByFilter(ctx, &qdrant.Filter{
		Must: []*qdrant.Condition{
			qdrant.NewMatchKeyword(PayloadProject, projectName),
			qdrant.NewMatchKeyword(PayloadFile, relPath),
			qdrant.NewMatchKeyword(PayloadBranch, branch),
		},
		MustNot: []*qdrant.Condition{
			// replaced_by задан (точка устаревшая) и это НЕ текущий коммит —
			// то есть версия старше предыдущей, её история уже не нужна.
			qdrant.NewIsEmpty(PayloadReplacedBy),
			qdrant.NewMatchKeyword(PayloadReplacedBy, commit),
		},
	})
}

// activePoints собирает активные (replaced_by пуст) точки ветки: карта
// chunk_id → версия, по ключу файла (пусто — все файлы ветки). Нужна для
// версионирования: пометить прежние версии устаревшими и не трогать точки,
// уже актуальные на том же коммите.
func (c *Client) activePoints(ctx context.Context, projectName, branch, relPath string) (map[string]activePoint, error) {
	filter := &qdrant.Filter{Must: []*qdrant.Condition{
		qdrant.NewMatchKeyword(PayloadProject, projectName),
		qdrant.NewMatchKeyword(PayloadBranch, branch),
		qdrant.NewIsEmpty(PayloadReplacedBy),
	}}
	if relPath != "" {
		filter.Must = append(filter.Must, qdrant.NewMatchKeyword(PayloadFile, relPath))
	}
	return c.chunkPoints(ctx, projectName, filter)
}

// chunkPoints читает точки по фильтру (без векторов, постранично) и сводит их
// в карту chunk_id → версия. Точки без chunk_id (старый индекс) получают
// синтетический ключ по файлу и началу строки, чтобы версионирование и
// исключение перекрытых версий их тоже затрагивали.
func (c *Client) chunkPoints(ctx context.Context, projectName string, filter *qdrant.Filter) (map[string]activePoint, error) {
	out := map[string]activePoint{}
	var offset *qdrant.PointId
	for total := 0; total < scrollMaxPoints; {
		page, err := c.store.Scroll(ctx, &qdrant.ScrollPoints{
			CollectionName: c.collection,
			Filter:         filter,
			Limit:          qdrant.PtrOf(uint32(scrollPageSize)),
			WithPayload:    qdrant.NewWithPayload(true),
			WithVectors:    qdrant.NewWithVectors(false),
			Offset:         offset,
		})
		if err != nil {
			return nil, &UnavailableError{Err: err}
		}
		if len(page) == 0 {
			break
		}
		for _, pt := range page {
			payload := pt.GetPayload()
			p := activePoint{
				pointID:     pt.GetId().GetNum(),
				chunkID:     payload[PayloadChunkID].GetStringValue(),
				commit:      payload[PayloadCommit].GetStringValue(),
				contentHash: payload[PayloadContentHash].GetStringValue(),
				file:        payload[PayloadFile].GetStringValue(),
			}
			if p.chunkID == "" {
				p.chunkID = ChunkID(projectName, p.file,
					"L"+strconv.FormatInt(payload[PayloadStartLine].GetIntegerValue(), 10))
			}
			out[p.chunkID] = p
		}
		total += len(page)
		offset = page[len(page)-1].GetId()
		if len(page) < scrollPageSize {
			break
		}
		if total >= scrollMaxPoints {
			logging.Warnf("rag: обход точек прерван на пределе %d (часть версий не будет обработана)", scrollMaxPoints)
		}
	}
	return out, nil
}

// deleteIDs удаляет точки по списку ID пакетами (payloadBatchSize).
func (c *Client) deleteIDs(ctx context.Context, points []*qdrant.PointId) error {
	for start := 0; start < len(points); start += payloadBatchSize {
		end := start + payloadBatchSize
		if end > len(points) {
			end = len(points)
		}
		selector := qdrant.NewPointsSelectorIDs(points[start:end])
		if _, err := c.store.Delete(ctx, &qdrant.DeletePoints{
			CollectionName: c.collection,
			Points:         selector,
		}); err != nil {
			return &UnavailableError{Err: err}
		}
	}
	return nil
}

// deleteByFilter удаляет точки по фильтру (MUST + MUST_NOT).
func (c *Client) deleteByFilter(ctx context.Context, filter *qdrant.Filter) error {
	if filter == nil || (len(filter.Must) == 0 && len(filter.MustNot) == 0 && len(filter.Should) == 0) {
		return fmt.Errorf("rag: фильтр удаления пуст")
	}
	points := qdrant.NewPointsSelectorFilter(filter)
	if _, err := c.store.Delete(ctx, &qdrant.DeletePoints{
		CollectionName: c.collection,
		Points:         points,
	}); err != nil {
		return &UnavailableError{Err: err}
	}
	return nil
}

// chunkEmbedText — текст чанка для эмбеддинга: путь и координаты добавляются
// заголовком, чтобы модель кодировала и контекст (откуда кусок), и сам код.
func chunkEmbedText(relPath string, ch Chunk) string {
	return fmt.Sprintf("файл: %s\nстроки: %d-%d\n%s", relPath, ch.StartLine, ch.EndLine, ch.Content)
}

// pointID — детерминированный ID точки чанка: FNV-1a 64 по конкатенации
// проекта, файла, ветки, коммита, начальной строки и отпечатка содержимого.
// Ветка, коммит и содержимое входят в ID намеренно: разные версии чанка —
// разные точки (иначе upsert затирал бы историю), а повторный прогон с тем же
// коммитом и тем же содержимым перезаписывает свою точку.
func pointID(projectName, relPath, branch, commit string, startLine int, hash string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(projectName))
	h.Write([]byte{0})
	h.Write([]byte(relPath))
	h.Write([]byte{0})
	h.Write([]byte(branch))
	h.Write([]byte{0})
	h.Write([]byte(commit))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(startLine)))
	h.Write([]byte{0})
	h.Write([]byte(hash))
	return h.Sum64()
}

// contentHash — короткий отпечаток содержимого чанка (FNV-1a 64 в hex).
func contentHash(content string) string {
	h := fnv.New64a()
	h.Write([]byte(content))
	return strconv.FormatUint(h.Sum64(), 16)
}

// ScopeForPath определяет область файла по первому сегменту пути:
// "server/internal/auth/token.go" → "server"; файлы в корне → "root".
// Область используется фильтром поиска по scope шага плана/роли агента.
func ScopeForPath(relPath string) string {
	relPath = filepath.Clean(filepath.FromSlash(relPath))
	relPath = filepath.ToSlash(relPath)
	if relPath == "." || relPath == "" {
		return "root"
	}
	if i := strings.IndexByte(relPath, '/'); i >= 0 {
		return relPath[:i]
	}
	return "root"
}
