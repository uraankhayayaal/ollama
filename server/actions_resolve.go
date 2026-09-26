// Мосты резолва git-конфликтов для чат-ассистента (Ф-4b).
//
// Почему не tools.ResolveGitConflicts из общего реестра: он работает через
// FileOps.OutputDir — каталог проекта, тогда как резолв Ф-4 идёт в отдельном
// постоянном worktree (.conflict-<проект>-<эпик>) ВНЕ каталога проекта. Выдать
// модели файловые инструменты с OutputDir этого worktree нельзя: они действуют
// весь сеанс, и ассистент (который по замыслу не пишет файлы проекта) начал бы
// писать «не туда». Поэтому резолв доступен ассистенту через серверные мосты
// поверх того же ядра, что и REST (handleEpicRebase / handleEpicResolve) —
// git-логика не дублируется, расхождение REST и чата исключено.
//
// Модель получает три примитива: ResolveGitConflicts (status — что и где
// конфликтует, start — открыть/возобновить процесс резолва, apply — записать
// выбранное содержимое) и EpicResolve (обязательная приёмка, коммит резолва,
// влитие в main и push — деструктивно, по подтверждению из чата).
//
// Безопасность apply: править можно только файлы из списка конфликтов
// текущего процесса резолва, только внутри worktree (путь проверяется на
// относительность, выход за пределы каталога и не-обычные файлы отклоняются),
// а в результате не должно остаться маркеров конфликта.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ai/chat"
	"ai/gitops"
	"ai/tools"
)

// Имена мостов-инструментов резолва (вне общего реестра tools).
const (
	actionResolveConflicts = "ResolveGitConflicts"
	actionEpicResolve      = "EpicResolve"
)

// Ограничения выдачи конфликтов модели: слишком большие файлы отдаём зоной
// конфликта (файл помечается в truncated — править его нужно через edits).
const (
	maxConflictFileBytes = 48 << 10
	maxConflictFiles     = 20
	conflictRegionLines  = 80
)

// ConflictEdit — точечная замена фрагмента в конфликтном файле (для файлов,
// которые не влезли в ответ целиком). Old обязан встречаться в файле ровно
// один раз, иначе запись отклоняется целиком (никаких «угадываний» правок).
type ConflictEdit struct {
	File string `json:"file"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// ConflictRequest — параметр моста ConflictResolve.
type ConflictRequest struct {
	// EpicID — эпик с конфликтом main ↔ релизная ветка.
	EpicID string
	// Action — start | status | apply (пусто = start).
	Action string
	// Files — полное новое содержимое конфликтных файлов (путь → текст).
	Files map[string]string
	// Edits — точечные замены в конфликтных файлах (для больших файлов).
	Edits []ConflictEdit
}

// conflictView — что модели нужно знать о конфликте: файлы, где ещё есть
// маркеры, и их содержимое (для больших — только зона конфликта).
type conflictView struct {
	// Files — конфликтные файлы, которые ещё содержат маркеры.
	Files []string `json:"files"`
	// Content — содержимое каждого файла из Files.
	Content map[string]string `json:"content"`
	// Truncated — файлы, отданные фрагментом (править только через edits).
	Truncated []string `json:"truncated,omitempty"`
	// Branch — релизная ветка эпика, в которую идёт резолв.
	Branch string `json:"branch,omitempty"`
	// Hint — что делать модели с этими данными.
	Hint string `json:"hint,omitempty"`
}

// newConflictResolveTools конструирует мосты резолва (Ф-4b). ResolveGitConflicts
// безопасен (правит только файлы в отдельном конфликтном worktree, main и
// remote не трогает), EpicResolve — деструктивен: приёмка + коммит + merge
// main + push, поэтому идёт через единый confirm-гейт actionTool.
func newConflictResolveTools(b ActionsBackend) []tools.Tool {
	return []tools.Tool{
		&actionTool{
			name: actionResolveConflicts, b: b,
			description: "Резолв конфликта эпика (main ↔ релизная ветка) в постоянном конфликтном worktree. " +
				"Действия: status — какие файлы конфликтуют и их содержимое с маркерами; " +
				"start — открыть (или возобновить) процесс резолва, создаёт конфликтный worktree, тривиальные конфликты закрывает автоматически; " +
				"apply — записать выбранное тобой содержимое (files — полный текст файла, edits — точечная замена для больших файлов). " +
				"Финализацию (приёмка + влитие в main + push) делает отдельный инструмент EpicResolve. " +
				"Рабочие файлы проекта ассистент не правит — только эти конфликтные файлы, и только через этот инструмент.",
			args: map[string]any{
				"epic_id": map[string]any{"type": "string", "description": "ID эпика на доске (например CHAT-01, ARCH-01)"},
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"status", "start", "apply"},
					"description": "status — посмотреть конфликты; start — открыть/возобновить резолв; apply — записать свои правки",
				},
				"files": map[string]any{
					"type":                 "object",
					"description":          "action=apply: полное новое содержимое конфликтных файлов (ключ — путь из conflicts.files, значение — полный текст файла БЕЗ маркеров конфликта)",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"edits": map[string]any{
					"type":        "array",
					"description": "action=apply: точечные замены для больших файлов (file + old — уникальный фрагмент конфликта, new — замена)",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"file": map[string]any{"type": "string", "description": "путь конфликтного файла"},
							"old":  map[string]any{"type": "string", "description": "фрагмент, который встречается в файле ровно один раз"},
							"new":  map[string]any{"type": "string", "description": "замена (без маркеров конфликта)"},
						},
						"required":             []string{"file", "old", "new"},
						"additionalProperties": false,
					},
				},
			},
			required: []string{"epic_id"},
			run: func(ctx context.Context, args map[string]any) (map[string]any, error) {
				req := ConflictRequest{
					EpicID: actionArg(args, "epic_id"),
					Action: actionArg(args, "action"),
					Files:  actionArgFiles(args, "files"),
					Edits:  actionArgEdits(args, "edits"),
				}
				return b.ConflictResolve(ctx, req)
			},
		},
		&actionTool{
			name: actionEpicResolve, b: b, destructive: true,
			description: "Завершить резолв конфликта эпика: обязательная приёмка (сборка/тесты/линт) в конфликтном worktree, " +
				"коммит резолва, влитие релизной ветки в main и push на remote. Используй ПОСЛЕ того, как ResolveGitConflicts " +
				"с action=apply записал правки без маркеров конфликта. ДЕСТРУКТИВНО — требует подтверждения пользователем в чате.",
			args: map[string]any{
				"epic_id": map[string]any{"type": "string", "description": "ID эпика на доске (например CHAT-01, ARCH-01)"},
			},
			required: []string{"epic_id"},
			run: func(ctx context.Context, args map[string]any) (map[string]any, error) {
				return b.ConflictFinish(ctx, actionArg(args, "epic_id"))
			},
		},
	}
}

// --- реализация ActionsBackend на Session (Ф-4b) ---

// ConflictResolve — безопасная часть резолва: посмотреть конфликт, открыть
// процесс резолва, записать выбранное содержимое в конфликтный worktree.
func (sess *Session) ConflictResolve(ctx context.Context, req ConflictRequest) (map[string]any, error) {
	epicID := strings.TrimSpace(req.EpicID)
	if epicID == "" {
		return nil, errors.New("не указан epic_id")
	}
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "", "start":
		code, body := sess.callResolveCore(ctx, sess.srv.handleEpicRebase, epicID)
		out, err := conflictReply(code, body)
		if err != nil {
			return nil, err
		}
		if code == http.StatusOK {
			if st, _ := out["resolve_status"].(string); st == "resolving" {
				// Открытый процесс сразу отдаём модели с содержимым файлов: вручную
				// просить ещё один status не нужно.
				sess.decorateConflictView(out)
			} else if msg, _ := out["message"].(string); msg != "" {
				// Прогноз merge-tree чист: конфликтов нет, резолв не нужен.
				out["message"] = msg + " (резолв не нужен — релизуй эпик через " + actionEpicRelease + ")"
			}
		}
		return out, nil
	case "status":
		return sess.conflictStatus(epicID)
	case "apply":
		if len(req.Files) == 0 && len(req.Edits) == 0 {
			return nil, errors.New("apply без правок: передай files (полное содержимое конфликтного файла) или edits (точечные замены)")
		}
		return sess.conflictApply(epicID, req.Files, req.Edits)
	default:
		return nil, fmt.Errorf("неизвестное действие %q (доступно: status, start, apply)", req.Action)
	}
}

// ConflictFinish — финализация резолва: приёмка worktree, коммит, merge main
// ← релизной ветки, push. Деструктивно — подтверждение обеспечивает actionTool.
func (sess *Session) ConflictFinish(ctx context.Context, epicID string) (map[string]any, error) {
	epicID = strings.TrimSpace(epicID)
	if epicID == "" {
		return nil, errors.New("не указан epic_id")
	}
	code, body := sess.callResolveCore(ctx, sess.srv.handleEpicResolve, epicID)
	return conflictReply(code, body)
}

// conflictStatus — текущее состояние процесса резолва эпика.
func (sess *Session) conflictStatus(epicID string) (map[string]any, error) {
	rs, err := sess.srv.reg.EpicResolve(sess.project)
	if err != nil || rs.EpicID != epicID {
		return map[string]any{
			"resolve_status": "none",
			"message": fmt.Sprintf("активного процесса резолва для эпика %s нет — вызови %s с action=start (или сообщи пользователю, что конфликт нужно разбирать вручную)",
				epicID, actionResolveConflicts),
		}, nil
	}
	if _, err := os.Stat(rs.Worktree); err != nil {
		return nil, fmt.Errorf("конфликтный worktree %s не существует — начните заново (action=start)", rs.Worktree)
	}
	out := map[string]any{
		"resolve_status": "resolving",
		"epic_id":        rs.EpicID,
		"branch":         rs.Branch,
		"message": "прочитай conflicts.content, запиши корректное содержимое через action=apply, затем вызови " +
			actionEpicResolve + " (вливаение в main и push — спроси подтверждение)",
	}
	sess.decorateConflictView(out)
	return out, nil
}

// conflictApply — запись выбранного моделью содержимого в конфликтный worktree.
func (sess *Session) conflictApply(epicID string, files map[string]string, edits []ConflictEdit) (map[string]any, error) {
	rs, err := sess.srv.reg.EpicResolve(sess.project)
	if err != nil {
		return nil, fmt.Errorf("активного процесса резолва нет — сначала %s с action=start", actionResolveConflicts)
	}
	if rs.EpicID != epicID {
		return nil, fmt.Errorf("в резолве другой эпик (%s) — закончите его, потом этот", rs.EpicID)
	}
	if _, err := os.Stat(rs.Worktree); err != nil {
		return nil, fmt.Errorf("конфликтный worktree %s не существует — начните заново (action=start)", rs.Worktree)
	}
	pending, err := gitops.HasConflictMarkers(rs.Worktree, rs.Files)
	if err != nil {
		return nil, fmt.Errorf("проверка маркеров: %v", err)
	}
	if len(pending) == 0 {
		return nil, fmt.Errorf("в конфликтных файлах больше нет маркеров — осталось только %s", actionEpicResolve)
	}
	// Править разрешено только файлы, где маркеры ещё есть: иначе модель могла
	// бы «починить» любой файл worktree вслепую.
	allowed := make(map[string]bool, len(pending))
	for _, p := range pending {
		allowed[p] = true
	}
	touched, err := applyConflictFiles(rs.Worktree, allowed, pending, files, edits)
	if err != nil {
		return nil, err
	}
	sort.Strings(touched)
	// Финализация сама проверит маркеры, но дешевле поймать остаток здесь: так
	// модели не придётся гадать, что не так с её правкой.
	left, err := gitops.HasConflictMarkers(rs.Worktree, touched)
	if err != nil {
		return nil, fmt.Errorf("проверка маркеров после записи: %v", err)
	}
	if len(left) > 0 {
		return nil, fmt.Errorf("в файлах остались маркеры конфликта: %s — убери их и повтори apply", strings.Join(left, ", "))
	}
	sess.log.Infof("gitflow: ассистент записал резолв эпика %s: %s", epicID, strings.Join(touched, ", "))
	sess.append(chat.RoleStatus,
		fmt.Sprintf("Эпик %s: ассистент записал резолв конфликтов в %s — осталась приёмка и релиз в main", epicID, strings.Join(touched, ", ")),
		"", "", nil)
	out := map[string]any{
		"resolve_status": "applied",
		"epic_id":        epicID,
		"applied":        touched,
		"message": fmt.Sprintf("правки записаны в конфликтный worktree. Осталась приёмка, коммит и релиз в main: вызови %s (спроси у пользователя подтверждение)",
			actionEpicResolve),
	}
	sess.decorateConflictView(out)
	return out, nil
}

// decorateConflictView дописывает в результат инструмента содержимое конфликтных
// файлов (conflicts) — единственный способ ассистенту их «прочитать»: его
// ReadFiles смотрит в каталог проекта, а конфликтный worktree лежит вне его.
func (sess *Session) decorateConflictView(out map[string]any) {
	rs, err := sess.srv.reg.EpicResolve(sess.project)
	if err != nil {
		return
	}
	view := newConflictView(rs.Worktree, rs.Files)
	if len(view.Files) == 0 {
		// Всё закрыто: подсказываем финализацию, а не пустой список файлов.
		if msg, ok := out["message"].(string); ok && msg != "" {
			out["message"] = msg + " Все конфликтные файлы уже без маркеров — можно вызывать " + actionEpicResolve + "."
		}
		out["conflicts"] = view
		return
	}
	// Абсолютный путь worktree модели не нужен (его файловые инструменты туда
	// не умеют) и только провоцирует неудачные ReadFiles — убираем.
	delete(out, "worktree")
	out["conflicts"] = view
}

// newConflictView собирает список оставшихся конфликтных файлов и их
// содержимое (для файлов больше maxConflictFileBytes — только зона конфликта).
func newConflictView(wt string, files []string) *conflictView {
	v := &conflictView{Content: map[string]string{}}
	if len(files) > maxConflictFiles {
		files = files[:maxConflictFiles]
	}
	pending, err := gitops.HasConflictMarkers(wt, files)
	if err != nil {
		pending = files
	}
	v.Files = pending
	for _, p := range pending {
		data, err := os.ReadFile(filepath.Join(wt, filepath.FromSlash(p)))
		if err != nil {
			continue
		}
		if len(data) <= maxConflictFileBytes {
			v.Content[p] = string(data)
			continue
		}
		v.Truncated = append(v.Truncated, p)
		v.Content[p] = conflictRegion(string(data))
	}
	switch {
	case len(v.Files) == 0:
		v.Hint = "конфликтных файлов не осталось"
	case len(v.Truncated) > 0:
		v.Hint = "файлы из truncated показаны фрагментом — правь их через edits (file+old+new), остальные можно целиком через files"
	default:
		v.Hint = "выбери корректное содержимое и запиши его через action=apply (files: путь → полный текст без маркеров)"
	}
	return v
}

// conflictRegion вырезает строки рядом с маркерами конфликта — для файлов,
// которые целиком в ответ модели не влезают.
func conflictRegion(content string) string {
	lines := strings.Split(content, "\n")
	keep := make([]bool, len(lines))
	half := conflictRegionLines / 2
	for i, ln := range lines {
		if !isConflictMarkerLine(ln) {
			continue
		}
		lo, hi := i-half, i+half
		if lo < 0 {
			lo = 0
		}
		if hi >= len(lines) {
			hi = len(lines) - 1
		}
		for j := lo; j <= hi; j++ {
			keep[j] = true
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "… [файл показан фрагментом вокруг конфликтов, всего строк %d] …\n", len(lines))
	for i, ln := range lines {
		if !keep[i] {
			continue
		}
		fmt.Fprintf(&b, "%d\t%s\n", i+1, ln)
	}
	return b.String()
}

// isConflictMarkerLine — строка с маркером конфликта git (<<<<<<< / ======= /
// >>>>>>> возможно с префиксом-меткой).
func isConflictMarkerLine(ln string) bool {
	t := strings.TrimSpace(ln)
	switch {
	case strings.HasPrefix(t, "<<<<<<<"), strings.HasPrefix(t, "|||||||"),
		strings.HasPrefix(t, "======="), strings.HasPrefix(t, ">>>>>>>"):
		return true
	}
	return false
}

// applyConflictFiles — запись правок модели в конфликтный worktree: files
// (полное содержимое) затем edits (точечные замены, по порядку). Возвращает
// список изменённых файлов (rel-пути, через «/»).
func applyConflictFiles(wt string, allowed map[string]bool, pending []string, files map[string]string, edits []ConflictEdit) ([]string, error) {
	touched := map[string]bool{}
	names := sortedKeys(files)
	for _, p := range names {
		rel, err := checkConflictFile(wt, allowed, p, pending)
		if err != nil {
			return nil, err
		}
		full := filepath.Join(wt, filepath.FromSlash(rel))
		fi, err := os.Lstat(full)
		if err != nil {
			return nil, fmt.Errorf("файла %s нет в конфликтном worktree: %v", p, err)
		}
		if fi.Size() > maxConflictFileBytes {
			return nil, fmt.Errorf("%s слишком большой (%d байт) — передай правку через edits (file+old+new)", p, fi.Size())
		}
		if err := os.WriteFile(full, []byte(files[p]), fi.Mode().Perm()); err != nil {
			return nil, fmt.Errorf("запись %s: %v", p, err)
		}
		touched[rel] = true
	}
	for i, e := range edits {
		rel, err := checkConflictFile(wt, allowed, e.File, pending)
		if err != nil {
			return nil, err
		}
		full := filepath.Join(wt, filepath.FromSlash(rel))
		cur, err := os.ReadFile(full)
		if err != nil {
			return nil, fmt.Errorf("чтение %s: %v", e.File, err)
		}
		if e.Old == "" {
			return nil, fmt.Errorf("edits[%d] (%s): old пустой — укажи уникальный фрагмент конфликта", i, e.File)
		}
		if n := strings.Count(string(cur), e.Old); n != 1 {
			return nil, fmt.Errorf("edits[%d] (%s): фрагмент old встречается %d раз (нужен ровно 1) — пришли уникальный фрагмент конфликта", i, e.File, n)
		}
		out := strings.Replace(string(cur), e.Old, e.New, 1)
		fi, err := os.Lstat(full)
		if err != nil {
			return nil, fmt.Errorf("файла %s нет: %v", e.File, err)
		}
		if err := os.WriteFile(full, []byte(out), fi.Mode().Perm()); err != nil {
			return nil, fmt.Errorf("запись %s: %v", e.File, err)
		}
		touched[rel] = true
	}
	out := make([]string, 0, len(touched))
	for p := range touched {
		out = append(out, p)
	}
	return out, nil
}

// checkConflictFile проверяет, что путь из аргумента модели — конфликтный файл
// текущего резолва, лежащий внутри worktree (без выхода по «..» и без symlink).
func checkConflictFile(wt string, allowed map[string]bool, p string, pending []string) (string, error) {
	rel, err := conflictSafeRel(p)
	if err != nil {
		return "", err
	}
	if !allowed[rel] {
		return "", fmt.Errorf("файл %s не конфликтный — править можно: %s (остальные файлы ассистент не трогает)", p, strings.Join(pending, ", "))
	}
	full := filepath.Join(wt, filepath.FromSlash(rel))
	if !withinDir(wt, full) {
		return "", fmt.Errorf("путь %s выходит за пределы конфликтного worktree", p)
	}
	fi, err := os.Lstat(full)
	if err != nil {
		return "", fmt.Errorf("файла %s нет в конфликтном worktree: %v", p, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s — не обычный файл (%s), правка запрещена", p, fi.Mode())
	}
	return rel, nil
}

// conflictSafeRel — безопасный относительный путь внутри каталога (аналог
// gitops.safePathRel, который неэкспортируемый).
func conflictSafeRel(p string) (string, error) {
	q := filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))
	if q == "" || q == "." || q == ".." || filepath.IsAbs(filepath.FromSlash(p)) {
		return "", fmt.Errorf("недопустимый путь %q: нужен путь файла относительно корня проекта (как в conflicts.files)", p)
	}
	if strings.HasPrefix(q, "../") || strings.Contains(q, "/../") {
		return "", fmt.Errorf("недопустимый путь %q: выход за пределы каталога запрещён", p)
	}
	return q, nil
}

// withinDir — лежит ли p внутри root (после Clean).
func withinDir(root, p string) bool {
	r := filepath.Clean(root)
	c := filepath.Clean(p)
	return c == r || strings.HasPrefix(c, r+string(filepath.Separator))
}

// callResolveCore прогоняет REST-хендлер ядра резолва (единственный источник
// git-логики Ф-4) и возвращает код ответа и разобранный JSON. Мост переиспользует
// ядро, а не копирует его, поэтому REST и чат не могут разойтись.
func (sess *Session) callResolveCore(ctx context.Context, h http.HandlerFunc, epicID string) (int, map[string]any) {
	rec := newCaptureWriter()
	target := "/api/projects/" + url.PathEscape(sess.project) + "/epics/" + url.PathEscape(epicID) + "/resolve"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return http.StatusInternalServerError, map[string]any{"message": err.Error()}
	}
	req.SetPathValue("id", sess.project)
	req.SetPathValue("eid", epicID)
	h(rec, req)
	var body map[string]any
	if raw := rec.body.Bytes(); len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			body = map[string]any{"message": strings.TrimSpace(string(raw))}
		}
	}
	return rec.code, body
}

// conflictReply переводит ответ ядра в результат инструмента. 409 — не ошибка
// инструмента (конфликт ещё есть / процесс занят / приёмка не прошла): модель
// должна видеть детали и продолжать работу, поэтому отдаём payload с
// resolve_status. Остальные коды (400/404/502) — реальные ошибки вызова.
func conflictReply(code int, body map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(body)+1)
	for k, v := range body {
		out[k] = v
	}
	if st, _ := out["status"].(string); st != "" {
		out["resolve_status"] = st
		delete(out, "status")
	}
	msg, _ := out["message"].(string)
	switch {
	case code >= 200 && code < 300:
		if _, ok := out["resolve_status"]; !ok {
			out["resolve_status"] = "ok"
		}
		return out, nil
	case code == http.StatusConflict:
		if _, ok := out["resolve_status"]; !ok {
			out["resolve_status"] = "conflict"
		}
		if strings.TrimSpace(msg) == "" {
			out["message"] = "конфликт не разрешён — проверь files и доведи резолв до конца"
		}
		return out, nil
	default:
		if strings.TrimSpace(msg) == "" {
			msg = fmt.Sprintf("ядро резолва ответило HTTP %d", code)
		}
		return nil, errors.New(msg)
	}
}

// actionArgFiles — объект-аргумент «путь → текст». Аргументы инструментов
// приходят разобранными из JSON (map[string]any), но hermetic-провайдеры могут
// передать и готовый map[string]string — принимаем оба вида.
func actionArgFiles(args map[string]any, key string) map[string]string {
	switch m := args[key].(type) {
	case map[string]string:
		if len(m) == 0 {
			return nil
		}
		out := make(map[string]string, len(m))
		for k, v := range m {
			out[strings.TrimSpace(k)] = v
		}
		return out
	case map[string]any:
		if len(m) == 0 {
			return nil
		}
		out := make(map[string]string, len(m))
		for k, v := range m {
			s, _ := v.(string)
			out[strings.TrimSpace(k)] = s
		}
		return out
	}
	return nil
}

// actionArgEdits — массив-аргумент edits (file/old/new).
func actionArgEdits(args map[string]any, key string) []ConflictEdit {
	raw, ok := args[key].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]ConflictEdit, 0, len(raw))
	for _, it := range raw {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		file, _ := m["file"].(string)
		old, _ := m["old"].(string)
		nw, _ := m["new"].(string)
		out = append(out, ConflictEdit{File: strings.TrimSpace(file), Old: old, New: nw})
	}
	return out
}

// sortedKeys — ключи map в стабильном порядке (детерминированный apply).
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// captureWriter — минимальный http.ResponseWriter, записывающий ответ REST-ядра
// в память: мост возвращает его модели как JSON результата инструмента.
type captureWriter struct {
	hdr  http.Header
	code int
	body bytes.Buffer
}

func newCaptureWriter() *captureWriter { return &captureWriter{hdr: http.Header{}} }

func (c *captureWriter) Header() http.Header { return c.hdr }

func (c *captureWriter) Write(p []byte) (int, error) { return c.body.Write(p) }

func (c *captureWriter) WriteHeader(code int) { c.code = code }
