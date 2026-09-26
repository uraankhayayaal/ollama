// Авто-резолв конфликтов «main ↔ релизная ветка эпика» (Ф-4).
//
// Когда POST .../epics/:eid/release выявляет конфликты main ↔ релизной ветки
// (merge-tree), конфликтный worktree больше не снимается: сервер разворачивает
// настоящий merge в постоянном worktree (.conflict-<проект>-<эпик>) и передаёт
// «сложные» файлы модели через инструмент ResolveGitConflicts. POST .../resolve
// доводит процесс до конца: обязательная приёмка (acceptor), коммит резолва,
// финальный merge main ← релизной ветки и push на remote.
package server

import (
	"ai/agents/acceptor"
	"ai/board"
	"ai/chat"
	"ai/gitops"
	"ai/logging"
	"ai/workspace"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// handleEpicRebase — вход Ф-4: вливает main в релизную ветку эпика в
// постоянном конфликтном worktree для авто/модельного резолва конфликтов.
// Идемпотентно: повторный вызов при активном процессе резолва возвращает его
// состояние (файлы, ждущие правок), не трогая уже сделанные правки.
//
// Ответы:
//
//   - 200 {status:"resolving", files, resolved, branch} — конфликты есть,
//     процесс открыт (worktree создан, тривиальные блоки авто-разрешены);
//   - 200 {status:"ok", message} — прогноз merge-tree чистый: конфликтов нет,
//     нужно POST .../release (не rebase);
//   - 400 — эпик не done / ветки нет / проект не git и т.п.;
//   - 409 {status:"busy", ...} — другой эпик проекта уже в процессе резолва;
//   - 502 — техническая ошибка git.
func (s *Server) handleEpicRebase(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	epicID := r.PathValue("eid")

	inf, err := s.reg.Get(project)
	if err != nil {
		writeErr(w, http.StatusNotFound, "проект не найден")
		return
	}
	if inf.Kind != workspace.KindGit {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("резолв конфликтов доступен только git-проектам (kind=%s)", inf.Kind))
		return
	}
	main := strings.TrimSpace(inf.GitBase)
	if main == "" {
		writeErr(w, http.StatusBadRequest, "у проекта не задана базовая ветка (git_base)")
		return
	}

	repo, err := s.repoOf(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	store, err := s.boardStore(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "доска недоступна: "+err.Error())
		return
	}
	defer store.Close()
	epic, err := store.GetEpic(r.Context(), epicID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "эпик не найден")
		return
	}
	if epic.Status != board.StatusDone {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("резолв доступен только эпику со статусом done (сейчас %s)", epic.Status))
		return
	}
	epicRef, err := s.reg.EpicBranch(project, epicID)
	if err != nil {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("сначала создайте ветку эпика %s: %v", epicID, err))
		return
	}

	// Резолв процесса — идемпотентное продолжение работы над готовым состоянием.
	if rs, rerr := s.reg.EpicResolve(project); rerr == nil {
		if rs.EpicID != epicID {
			writeJSON(w, http.StatusConflict, map[string]any{
				"status":  "busy",
				"epic_id": rs.EpicID,
				"message": fmt.Sprintf("сначала завершите резолв конфликтов эпика %s (POST .../resolve)", rs.EpicID),
			})
			return
		}
		logging.For(project).Infof("gitflow: rebase %s повторный — продолжаем резолв (%d файлов)", epicID, len(rs.Files))
		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "resolving",
			"files":    rs.Files,
			"branch":   rs.Branch,
			"worktree": rs.Worktree,
		})
		return
	}

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	// Прогноз конфликтов merge-tree БЕЗ изменения рабочей копии: если чисто —
	// резолв не нужен (идите POST .../release). Стрим дополнительно убирает
	// «уже влито» (MergedInto) — release реализует этот короткий путь сам.
	base, err := repo.MergeBase(r.Context(), main, epicRef.Branch)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "merge-base: "+err.Error())
		return
	}
	forecast, err := repo.MergeTree(r.Context(), base, main, epicRef.Branch)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "merge-tree: "+err.Error())
		return
	}
	if len(forecast) == 0 {
		logging.For(project).Infof("gitflow: rebase %s: конфликтов нет, идите POST /release", epicID)
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"message": "конфликтов не найдено — используйте POST /api/projects/" + project + "/epics/" + epicID + "/release",
			"files":   []string{},
		})
		return
	}

	wtPath := filepath.Join(filepath.Dir(repo.Root),
		".conflict-"+project+"-"+gitops.SanitizeBranchName(epicID))

	// Чистим остатки прерванного процесса (каталог существует, но реестр пуст).
	if _, err := os.Stat(wtPath); err == nil {
		logging.For(project).Warnf("gitflow: удаляю осиротевший конфликтный worktree %s", wtPath)
		_ = repo.RemoveWorktree(r.Context(), wtPath)
	}

	wt, err := repo.AddWorktree(r.Context(), wtPath, epicRef.Branch)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "создание конфликтного worktree: "+err.Error())
		return
	}
	cleanup := func() {
		_ = repo.RemoveWorktree(r.Context(), wtPath)
		_ = s.reg.ClearEpicResolve(project)
	}

	// Настоящий merge main → релизная ветка прямо в worktree. Ошибка НЕ фатальна:
	// конфликты оставляют маркеры в файлах и stage-записи в индексе worktree —
	// это и есть рабочая поверхность резолва. Других причин падения тут нет
	// (worktree свежий).
	_, _ = wt.MergeBranch(r.Context(), main,
		fmt.Sprintf("эпик %s: влитие main в релизную ветку (резолв конфликтов)", epicID))

	unmerged, err := wt.UnmergedFiles(r.Context())
	if err != nil {
		cleanup()
		writeErr(w, http.StatusBadGateway, "git ls-files -u: "+err.Error())
		return
	}
	resolved, hard, err := gitops.TrivialResolve(wtPath, unmerged)
	if err != nil {
		cleanup()
		writeErr(w, http.StatusBadGateway, "авто-резолв тривиальных конфликтов: "+err.Error())
		return
	}
	if err := wt.Stage(r.Context(), resolved); err != nil {
		cleanup()
		writeErr(w, http.StatusBadGateway, "git add резолвов: "+err.Error())
		return
	}

	rs := &workspace.EpicResolve{
		EpicID:    epicID,
		Branch:    epicRef.Branch,
		Worktree:  wtPath,
		Files:     hard,
		StartedAt: time.Now().UTC(),
	}
	if err := s.reg.SetEpicResolve(project, rs); err != nil {
		cleanup()
		writeErr(w, http.StatusInternalServerError, "запись процесса резолва: "+err.Error())
		return
	}

	logging.For(project).Infof("gitflow: rebase %s: конфликты в %s, авто-резолв %d, на модель %d (%s)",
		epicID, strings.Join(forecast, ", "), len(resolved), len(hard), wtPath)
	// Конфликт main ↔ релизная ветка виден на доске: помечаем эпик списком
	// конфликтующих файлов (снимется успешным resolve/релизом).
	epic.MergeConflictFiles = forecast
	if serr := store.SaveEpic(r.Context(), epic); serr != nil {
		logging.For(project).Warnf("gitflow: rebase %s: запись конфликта на доске: %v", epicID, serr)
	}
	if sess := s.session(project); sess != nil {
		sess.append(chat.RoleStatus,
			fmt.Sprintf("Эпик %s: конфликты main ↔ %s в файлах [%s]. Авто-резолвено %d, осталось у модели %d. Правьте файлы инструментом ResolveGitConflicts и завершите POST .../resolve.",
				epicID, epicRef.Branch, strings.Join(forecast, ", "), len(resolved), len(hard)),
			"", "", nil)
	}
	s.srvEmitBoard(project, "gitflow: rebase эпика, конфликты для резолва")
	if hard == nil {
		hard = []string{}
	}
	if resolved == nil {
		resolved = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "resolving",
		"files":    hard,
		"resolved": resolved,
		"branch":   epicRef.Branch,
		"worktree": wtPath,
		"message":  "правьте сложные файлы инструментом ResolveGitConflicts и завершите POST /api/projects/" + project + "/epics/" + epicID + "/resolve",
	})
}

// handleEpicResolve — финализация резолва Ф-4: обязательная приёмка
// (acceptor) в конфликтном worktree, коммит резолва, финальный merge main ←
// релизной ветки и push. Значимо атомарно через пер-проектный лок: реестр
// очищается только после успешного merge.
//
// Ответы:
//
//   - 200 {status:"ok", branch:main, source, already_merged, message} — релиз
//     выполнен (main продвинут и запушен), worktree снят, процесс закрыт;
//   - 409 {status:"still_conflicts", files} — маркеры/незакрытые записи есть;
//   - 409 {status:"acceptance_failed", verdict, summary, issues} — проекты не
//     собрался/запуск не удался; правки остаются, worktree на месте;
//   - 409 {status:"no_resolve"} — активного процесса резолва нет;
//   - 502 — техническая ошибка git.
func (s *Server) handleEpicResolve(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	epicID := r.PathValue("eid")

	inf, err := s.reg.Get(project)
	if err != nil {
		writeErr(w, http.StatusNotFound, "проект не найден")
		return
	}
	if inf.Kind != workspace.KindGit {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("резолв доступен только git-проектам (kind=%s)", inf.Kind))
		return
	}
	repo, err := s.repoOf(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	main := strings.TrimSpace(inf.GitBase)
	if main == "" {
		writeErr(w, http.StatusBadRequest, "у проекта не задана базовая ветка (git_base)")
		return
	}

	rs, err := s.reg.EpicResolve(project)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"status":  "no_resolve",
			"message": "активного процесса резолва нет — начните с POST .../epics/" + epicID + "/rebase",
		})
		return
	}
	if rs.EpicID != epicID {
		writeJSON(w, http.StatusConflict, map[string]any{
			"status":  "busy",
			"epic_id": rs.EpicID,
			"message": fmt.Sprintf("в резолве другой эпик (%s)", rs.EpicID),
		})
		return
	}
	if _, err := os.Stat(rs.Worktree); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"status":  "no_resolve",
			"message": "конфликтный worktree " + rs.Worktree + " не существует — начните заново: POST .../rebase",
		})
		return
	}

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	wt := gitops.RepoFromState(s.gitExec, rs.Worktree, inf.GitRemote, rs.Branch, strings.TrimSpace(inf.GitBase))

	// 1) Закрываем всё, что модель успела почистить/не трогала: повторный
	// авто-резолв + stage всех разрешённых + проверка «остались маркеры».
	resolved, _, err := gitops.TrivialResolve(rs.Worktree, rs.Files)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "авто-резолв: "+err.Error())
		return
	}
	if err := wt.Stage(r.Context(), resolved); err != nil {
		writeErr(w, http.StatusBadGateway, "git add резолвов: "+err.Error())
		return
	}
	unmerged, err := wt.UnmergedFiles(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "git ls-files -u: "+err.Error())
		return
	}
	markers, err := gitops.HasConflictMarkers(rs.Worktree, rs.Files)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "проверка маркеров: "+err.Error())
		return
	}
	if remain := unionPaths(unmerged, markers); len(remain) > 0 {
		logging.For(project).Infof("gitflow: resolve %s: ещё конфликты в %s — резолв не завершён",
			epicID, strings.Join(remain, ", "))
		writeJSON(w, http.StatusConflict, map[string]any{
			"status":  "still_conflicts",
			"files":   remain,
			"message": "в файлах остались маркеры конфликта или незакрытые записи индекса — продолжите работу инструментом ResolveGitConflicts",
		})
		return
	}

	// 2) Обязательная приёмка собранного worktree (безопасность релиза).
	cfgreport := acceptor.Accept(rs.Worktree, acceptor.LoadConfig())
	if cfgreport.Verdict != acceptor.VerdictApprove {
		logging.For(project).Warnf("gitflow: resolve %s: приёмка не прошла — %s", epicID, cfgreport.Summary)
		writeJSON(w, http.StatusConflict, map[string]any{
			"status":  "acceptance_failed",
			"verdict": cfgreport.Verdict,
			"summary": cfgreport.Summary,
			"issues":  cfgreport.Issues,
			"message": "проект после резолва не проходит обязательную приёмку; правки сохранены в worktree",
		})
		return
	}

	// 3) Коммит резолва в worktree (завершает merge main → релизная ветка).
	// Когда резолв возвращает дерево на HEAD-содержимое (например, модель
	// выбрала нашу версию файла), рабочая копия чиста — но merge всё равно
	// нужно завершить merge-коммитом. CommitAllowEmpty создаёт его и в этом
	// случае, иначе релизная ветка не получит main предком и финальный
	// MergeFeature снова натолкнётся на конфликт.
	dirty, err := wt.Dirty(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "git status: "+err.Error())
		return
	}
	msg := fmt.Sprintf("эпик %s: резолв конфликтов main ↔ %s", epicID, rs.Branch)
	if dirty {
		if err := wt.Commit(r.Context(), msg); err != nil {
			writeErr(w, http.StatusBadGateway, "git commit резолва: "+err.Error())
			return
		}
	} else if err := wt.CommitAllowEmpty(r.Context(), msg); err != nil {
		writeErr(w, http.StatusBadGateway, "git commit --allow-empty резолва: "+err.Error())
		return
	}

	// 4) Финальный merge main ← релизной ветки (после резолва merge-tree чист,
	// main уже среди предков релизной ветки — merge-коммит без конфликтов).
	res, err := repo.MergeFeature(r.Context(), main, rs.Branch, gitops.MergeFeatureOptions{
		Message: fmt.Sprintf("эпик %s: релиз в main после резолва конфликтов", epicID),
		PushURL: remotePushURL(inf),
	})
	if err != nil {
		var ce *gitops.MergeConflictError
		if errors.As(err, &ce) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"status":  "conflicts",
				"files":   ce.Files,
				"message": "неожиданные конфликты при финальном merge: " + ce.Error(),
			})
			return
		}
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	// 5) Cleanup: снять worktree, закрыть процесс, уведомить.
	if err := repo.RemoveWorktree(r.Context(), rs.Worktree); err != nil {
		logging.For(project).Warnf("gitflow: снятие конфликтного worktree %s: %v", rs.Worktree, err)
	}
	if err := s.reg.ClearEpicResolve(project); err != nil {
		logging.For(project).Warnf("gitflow: снятие процесса резолва %s: %v", epicID, err)
	}

	logging.For(project).Infof("gitflow: resolve %s: релиз в main выполнен (%s, already=%v)",
		epicID, res.Message, res.AlreadyMerged)
	// Резолв завершён — снимаем признак конфликта с эпика на доске (если был).
	s.clearEpicMergeConflict(r.Context(), project, epicID)
	s.setEpicMergedIntoMain(r.Context(), project, epicID, true)
	if sess := s.session(project); sess != nil {
		sess.append(chat.RoleStatus,
			fmt.Sprintf("Эпик %s: конфликты разрешены, релизная ветка %s влита в main и запушена. Приёмка: %s",
				epicID, rs.Branch, cfgreport.Summary), "", "", nil)
	}
	s.srvEmitBoard(project, "gitflow: резолв конфликтов, релиз в main")
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"branch":         main,
		"source":         rs.Branch,
		"message":        res.Message,
		"already_merged": res.AlreadyMerged,
		"acceptance": map[string]any{
			"verdict": cfgreport.Verdict,
			"summary": cfgreport.Summary,
		},
	})
}

// handleResolveStatus — статус активного процесса резолва (GET): для UI и
// программной проверки «есть что дожимать».
func (s *Server) handleResolveStatus(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	epicID := r.PathValue("eid")

	rs, err := s.reg.EpicResolve(project)
	if err != nil || rs.EpicID != epicID {
		writeJSON(w, http.StatusOK, map[string]any{"status": "none"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "resolving",
		"epic_id":    rs.EpicID,
		"branch":     rs.Branch,
		"worktree":   rs.Worktree,
		"files":      rs.Files,
		"started_at": rs.StartedAt,
	})
}

// unionPaths склеивает пути без дубликатов (локальная копия: избегаем
// циклического импорта tools).
func unionPaths(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var out []string
	for _, p := range append(a, b...) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
