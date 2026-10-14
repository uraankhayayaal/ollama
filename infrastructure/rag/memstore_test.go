package rag

// memStore — hermetic-реализация QdrantStore с семантикой фильтров Qdrant
// (для версионированного индекса по веткам, см.
// PLAN-2026-09-27-done-branch-aware-rag.md): Upsert/Delete/Scroll/SetPayload/Query/
// Count работают по общему набору точек в памяти. Нужна, чтобы проверять
// РЕАЛЬНОЕ поведение версионирования и ветко-зависимого поиска (какая точка
// активна, какая помечена устаревшей, что попадёт в выдачу) без сети и без
// Qdrant. Поддерживаются условия, которые строит rag: match keyword/keywords,
// is_null, is_empty, has_id и вложенные filter (см. memMatchCond).

import (
	"context"
	"hash/fnv"
	"math"
	"sort"

	"github.com/qdrant/go-client/qdrant"
)

// memPoint — точка коллекции в памяти.
type memPoint struct {
	id      uint64
	payload map[string]*qdrant.Value
	vector  []float32
}

// memStore — QdrantStore в памяти.
type memStore struct {
	points  map[uint64]memPoint
	exists  bool
	upserts int
	deletes int
	scrolls int
	// deleted — зафиксированные вызовы Delete.
	deleted []*qdrant.DeletePoints
	// setPayload — payload-ы, проставленные SetPayload (в порядке вызовов).
	setPayload []map[string]*qdrant.Value
	queryReqs  []*qdrant.QueryPoints
}

func newMemStore() *memStore {
	return &memStore{points: map[uint64]memPoint{}, exists: true}
}

func (m *memStore) CollectionExists(ctx context.Context, collectionName string) (bool, error) {
	return m.exists, nil
}

func (m *memStore) CreateCollection(ctx context.Context, request *qdrant.CreateCollection) error {
	m.exists = true
	return nil
}

func (m *memStore) Upsert(ctx context.Context, request *qdrant.UpsertPoints) (*qdrant.UpdateResult, error) {
	m.upserts++
	for _, p := range request.GetPoints() {
		m.points[p.GetId().GetNum()] = memPoint{
			id:      p.GetId().GetNum(),
			payload: p.GetPayload(),
			vector:  p.GetVectors().GetVector().GetDense().GetData(),
		}
	}
	return &qdrant.UpdateResult{}, nil
}

func (m *memStore) Delete(ctx context.Context, request *qdrant.DeletePoints) (*qdrant.UpdateResult, error) {
	m.deletes++
	m.deleted = append(m.deleted, request)
	sel := request.GetPoints()
	switch {
	case sel.GetFilter() != nil:
		for id, p := range m.points {
			if memMatchFilter(sel.GetFilter(), id, p.payload) {
				delete(m.points, id)
			}
		}
	case sel.GetPoints() != nil:
		for _, id := range sel.GetPoints().GetIds() {
			delete(m.points, id.GetNum())
		}
	}
	return &qdrant.UpdateResult{}, nil
}

func (m *memStore) Scroll(ctx context.Context, request *qdrant.ScrollPoints) ([]*qdrant.RetrievedPoint, error) {
	m.scrolls++
	var ids []uint64
	for id, p := range m.points {
		if !memMatchFilter(request.GetFilter(), id, p.payload) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	// Offset = «начать с этого ID» (как в Qdrant).
	if off := request.GetOffset(); off != nil {
		start := 0
		for i, id := range ids {
			if id >= off.GetNum() {
				start = i
				break
			}
			start = i + 1
		}
		ids = ids[start:]
	}
	if l := int(request.GetLimit()); l > 0 && len(ids) > l {
		ids = ids[:l]
	}
	out := make([]*qdrant.RetrievedPoint, 0, len(ids))
	for _, id := range ids {
		out = append(out, &qdrant.RetrievedPoint{
			Id:      qdrant.NewIDNum(id),
			Payload: m.points[id].payload,
		})
	}
	return out, nil
}

func (m *memStore) SetPayload(ctx context.Context, request *qdrant.SetPayloadPoints) (*qdrant.UpdateResult, error) {
	m.setPayload = append(m.setPayload, request.GetPayload())
	sel := request.GetPointsSelector()
	for id, p := range m.points {
		if sel.GetFilter() != nil && !memMatchFilter(sel.GetFilter(), id, p.payload) {
			continue
		}
		merged := map[string]*qdrant.Value{}
		for k, v := range p.payload {
			merged[k] = v
		}
		for k, v := range request.GetPayload() {
			merged[k] = v
		}
		p.payload = merged
		m.points[id] = p
	}
	return &qdrant.UpdateResult{}, nil
}

func (m *memStore) Query(ctx context.Context, request *qdrant.QueryPoints) ([]*qdrant.ScoredPoint, error) {
	m.queryReqs = append(m.queryReqs, request)
	qvec := request.GetQuery().GetNearest().GetDense().GetData()
	type scored struct {
		id    uint64
		score float32
	}
	var hits []scored
	for id, p := range m.points {
		if !memMatchFilter(request.GetFilter(), id, p.payload) {
			continue
		}
		hits = append(hits, scored{id: id, score: memCosine(qvec, p.vector)})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].id < hits[j].id
	})
	if l := int(request.GetLimit()); l > 0 && len(hits) > l {
		hits = hits[:l]
	}
	out := make([]*qdrant.ScoredPoint, 0, len(hits))
	for _, h := range hits {
		out = append(out, &qdrant.ScoredPoint{
			Id:      qdrant.NewIDNum(h.id),
			Score:   h.score,
			Payload: m.points[h.id].payload,
		})
	}
	return out, nil
}

func (m *memStore) Count(ctx context.Context, request *qdrant.CountPoints) (uint64, error) {
	var n uint64
	for id, p := range m.points {
		if memMatchFilter(request.GetFilter(), id, p.payload) {
			n++
		}
	}
	return n, nil
}

func (m *memStore) Close() error { return nil }

// activeChunkIDs — chunk_id всех активных точек (replaced_by пуст).
func (m *memStore) activeChunkIDs() []string {
	var out []string
	for _, p := range m.points {
		if p.payload[PayloadReplacedBy].GetStringValue() == "" {
			if cid := p.payload[PayloadChunkID].GetStringValue(); cid != "" {
				out = append(out, cid)
			}
		}
	}
	sort.Strings(out)
	return out
}

// codeOf — содержимое кода АКТИВНОЙ точки ветки branch по chunk_id (пусто —
// нет точки). Ветка обязательна: chunk_id делится версиями одной функции в
// разных ветках, активной в каждой может быть своя (это и есть изоляция).
func (m *memStore) codeOf(branch, chunkID string) string {
	for _, p := range m.points {
		if p.payload[PayloadChunkID].GetStringValue() == chunkID &&
			p.payload[PayloadReplacedBy].GetStringValue() == "" &&
			p.payload[PayloadBranch].GetStringValue() == branch {
			return p.payload[PayloadCode].GetStringValue()
		}
	}
	return ""
}

// memMatchFilter — семантика Filter Qdrant: все must, ни одного must_not и
// (если заданы should) хотя бы один should.
func memMatchFilter(f *qdrant.Filter, id uint64, payload map[string]*qdrant.Value) bool {
	if f == nil {
		return true
	}
	for _, c := range f.GetMust() {
		if !memMatchCond(c, id, payload) {
			return false
		}
	}
	for _, c := range f.GetMustNot() {
		if memMatchCond(c, id, payload) {
			return false
		}
	}
	if len(f.GetShould()) > 0 {
		ok := false
		for _, c := range f.GetShould() {
			if memMatchCond(c, id, payload) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// memMatchCond — оценка одного условия Qdrant (подмножество, достаточное для
// фильтров rag: match keyword/keywords, is_null, is_empty, has_id, filter).
func memMatchCond(c *qdrant.Condition, id uint64, payload map[string]*qdrant.Value) bool {
	switch {
	case c.GetField() != nil:
		fc := c.GetField()
		v := payload[fc.GetKey()]
		if m := fc.GetMatch(); m != nil {
			switch {
			case m.GetKeyword() != "":
				return v != nil && v.GetStringValue() == m.GetKeyword()
			case m.GetKeywords() != nil:
				for _, kw := range m.GetKeywords().GetStrings() {
					if v != nil && v.GetStringValue() == kw {
						return true
					}
				}
				return false
			}
		}
		return false
	case c.GetFilter() != nil:
		return memMatchFilter(c.GetFilter(), id, payload)
	case c.GetIsEmpty() != nil:
		v := payload[c.GetIsEmpty().GetKey()]
		return v == nil || v.GetStringValue() == "" && v.GetIntegerValue() == 0
	case c.GetIsNull() != nil:
		return payload[c.GetIsNull().GetKey()] == nil
	case c.GetHasId() != nil:
		for _, want := range c.GetHasId().GetHasId() {
			if want.GetNum() == id {
				return true
			}
		}
		return false
	}
	return false
}

// memCosine — косинусная мера векторов (score в memStore.Query).
func memCosine(a, b []float32) float32 {
	var dot, na, nb float64
	for i := range a {
		na += float64(a[i]) * float64(a[i])
		if i < len(b) {
			dot += float64(a[i]) * float64(b[i])
			nb += float64(b[i]) * float64(b[i])
		}
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

// textEmbedder — hermetic-эмбеддер: вектор детерминирован по тексту
// (разные куски кода получают разные векторы, поэтому в memStore работает
// ранжирование по релевантности, а fakeEmbedder с одним вектором на всё —
// нет).
type textEmbedder struct{ dim int }

func (e *textEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vec := make([]float32, e.dim)
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	sum := h.Sum64()
	for i := range vec {
		sum = sum*6364136223846793005 + 1442695040888963407
		vec[i] = float32(int64(sum>>32)%1000) / 1000
	}
	// Первые байты кодируют длину текста: похожие по объёму чанки ближе.
	vec[0] = float32(len(text)) / 100
	return vec, nil
}
