// Авто-резолв конфликтов «main ↔ релизная ветка эпика» (Ф-4).
//
// Когда POST .../epics/:eid/release выявляет конфликты main ↔ релизной ветки
// (merge-tree), конфликтный worktree больше не снимается: сервер разворачивает
// настоящий merge в постоянном worktree (.conflict-<проект>-<эпик>) и передаёт
// «сложные» файлы модели через инструмент ResolveGitConflicts. POST .../resolve
// доводит процесс до конца: обязательная приёмка (acceptor), коммит резолва,
// финальный merge main ← релизной ветки и push на remote.
//
// Логика вынесена в application/gitflow (срез F3j); здесь — HTTP-обвязка и
// адаптер conflictHost.
package server

import (
	"ai/acceptor"
	"ai/application/gitflow"
	"ai/chat"
	"ai/domain"
	"ai/infrastructure/git"
	"ai/workspace"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// conflictResolver — use-case конфликтного резолва поверх адаптера.
func (s *Server) conflictResolver() gitflow.ConflictResolver {
	return gitflow.ConflictResolver{Host: conflictHost{mergeTaskHost: mergeTaskHost{s: s}}}
}

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

	out, err := s.conflictResolver().StartRebase(r.Context(), project, epicID)
	if err != nil {
		writeConflictErr(w, err)
		return
	}
	switch out.Status {
	case "busy":
		writeJSON(w, http.StatusConflict, map[string]any{
			"status":  "busy",
			"epic_id": out.EpicID,
			"message": fmt.Sprintf("сначала завершите резолв конфликтов эпика %s (POST .../resolve)", out.EpicID),
		})
		return
	case "ok":
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"message": out.Message,
			"files":   []string{},
		})
		return
	}
	// Идемпотентный повтор: состояние уже открытого процесса, без побочных
	// эффектов в чате/доске.
	if out.Repeat {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "resolving",
			"files":    out.Files,
			"branch":   out.Branch,
			"worktree": out.Worktree,
		})
		return
	}
	if sess := s.session(project); sess != nil {
		sess.append(chat.RoleStatus,
			fmt.Sprintf("Эпик %s: конфликты main ↔ %s в файлах [%s]. Авто-резолвено %d, осталось у модели %d. Правьте файлы инструментом ResolveGitConflicts и завершите POST .../resolve.",
				epicID, out.Branch, strings.Join(out.All, ", "), len(out.Resolved), len(out.Files)),
			"", "", nil)
	}
	s.srvEmitBoard(project, "gitflow: rebase эпика, конфликты для резолва")
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "resolving",
		"files":    out.Files,
		"resolved": out.Resolved,
		"branch":   out.Branch,
		"worktree": out.Worktree,
		"message":  out.Message,
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

	out, err := s.conflictResolver().FinishResolve(r.Context(), project, epicID)
	if err != nil {
		writeConflictErr(w, err)
		return
	}
	switch out.Status {
	case "no_resolve":
		msg := out.Message
		if msg == "" {
			msg = "активного процесса резолва нет — начните с POST .../epics/" + epicID + "/rebase"
		}
		writeJSON(w, http.StatusConflict, map[string]any{"status": "no_resolve", "message": msg})
		return
	case "busy":
		writeJSON(w, http.StatusConflict, map[string]any{
			"status":  "busy",
			"epic_id": out.EpicID,
			"message": fmt.Sprintf("в резолве другой эпик (%s)", out.EpicID),
		})
		return
	case "still_conflicts":
		writeJSON(w, http.StatusConflict, map[string]any{
			"status":  "still_conflicts",
			"files":   out.Files,
			"message": "в файлах остались маркеры конфликта или незакрытые записи индекса — продолжите работу инструментом ResolveGitConflicts",
		})
		return
	case "acceptance_failed":
		writeJSON(w, http.StatusConflict, map[string]any{
			"status":  "acceptance_failed",
			"verdict": out.Verdict,
			"summary": out.Summary,
			"issues":  out.Issues,
			"message": "проект после резолва не проходит обязательную приёмку; правки сохранены в worktree",
		})
		return
	case "conflicts":
		writeJSON(w, http.StatusConflict, map[string]any{
			"status":  "conflicts",
			"files":   out.Files,
			"message": out.Message,
		})
		return
	}
	// ok: релиз выполнен.
	s.clearEpicMergeConflict(r.Context(), project, epicID)
	s.setEpicMergedIntoMain(r.Context(), project, epicID, true)
	if sess := s.session(project); sess != nil {
		sess.append(chat.RoleStatus,
			fmt.Sprintf("Эпик %s: конфликты разрешены, релизная ветка %s влита в main и запушена. Приёмка: %s",
				epicID, out.Source, out.Summary), "", "", nil)
	}
	s.srvEmitBoard(project, "gitflow: резолв конфликтов, релиз в main")
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"branch":         out.Branch,
		"source":         out.Source,
		"message":        out.Message,
		"already_merged": out.AlreadyMerged,
		"acceptance": map[string]any{
			"verdict": out.Verdict,
			"summary": out.Summary,
		},
	})
}

// writeConflictErr маппит доменные ошибки резолва в HTTP-ответы.
func writeConflictErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gitflow.ErrProjectNotFound), errors.Is(err, gitflow.ErrEpicNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, gitflow.ErrBoardUnavailable):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, gitflow.ErrResolveStateSave):
		writeErr(w, http.StatusInternalServerError, err.Error())
	case errors.Is(err, gitflow.ErrMergeFailed):
		writeErr(w, http.StatusBadGateway, err.Error())
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
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

// conflictHost — адаптер gitflow.ConflictHost поверх Server (встраивает
// mergeTaskHost для Project/EpicStatus/EpicBranch/Locked/DropEpicWorktreeLocked).
type conflictHost struct {
	mergeTaskHost
}

var _ gitflow.ConflictHost = conflictHost{}

// Repo отдаёт git-репозиторий проекта как ConflictRepo.
func (h conflictHost) Repo(ctx context.Context, project string) (gitflow.ConflictRepo, error) {
	repo, err := h.s.repoOf(ctx, project)
	if err != nil {
		return nil, err
	}
	return serverConflictRepo{repo: repo}, nil
}

// ResolveState читает активный процесс резолва из side-реестра.
func (h conflictHost) ResolveState(project string) (*gitflow.ResolveState, bool) {
	rs, err := h.s.reg.EpicResolve(project)
	if err != nil || rs == nil {
		return nil, false
	}
	return &gitflow.ResolveState{
		EpicID:    rs.EpicID,
		Branch:    rs.Branch,
		Worktree:  rs.Worktree,
		Files:     rs.Files,
		StartedAt: rs.StartedAt,
	}, true
}

// SetResolveState сохраняет активный процесс резолва.
func (h conflictHost) SetResolveState(project string, st *gitflow.ResolveState) error {
	return h.s.reg.SetEpicResolve(project, &workspace.EpicResolve{
		EpicID:    st.EpicID,
		Branch:    st.Branch,
		Worktree:  st.Worktree,
		Files:     st.Files,
		StartedAt: st.StartedAt,
	})
}

// ClearResolveState снимает процесс резолва.
func (h conflictHost) ClearResolveState(project string) error {
	return h.s.reg.ClearEpicResolve(project)
}

// Worktree открывает конфликтный worktree как git-рабочую копию.
func (h conflictHost) Worktree(project, worktree, branch string) (gitflow.ConflictWorktree, error) {
	inf, err := h.s.reg.Get(project)
	if err != nil {
		return nil, err
	}
	return git.RepoFromState(h.s.gitExec, worktree, inf.GitRemote, branch, strings.TrimSpace(inf.GitBase)), nil
}

// TrivialResolve — детерминированный авто-резолв тривиальных блоков.
func (h conflictHost) TrivialResolve(dir string, files []string) (resolved, hard []string, err error) {
	return git.TrivialResolve(dir, files)
}

// Markers — поиск остаточных маркеров конфликта.
func (h conflictHost) Markers(dir string, files []string) ([]string, error) {
	return git.HasConflictMarkers(dir, files)
}

// Accept — обязательная приёмка собранного worktree.
func (h conflictHost) Accept(dir string) *domain.Report {
	return acceptor.Accept(dir, acceptor.LoadConfig())
}

// SetEpicMergeConflict помечает эпик конфликтом на доске.
func (h conflictHost) SetEpicMergeConflict(ctx context.Context, project, epicID string, files []string) {
	h.s.setEpicMergeConflict(ctx, project, epicID, files)
}

// serverConflictRepo — адаптер gitflow.ConflictRepo поверх *git.Repo.
type serverConflictRepo struct {
	repo *git.Repo
}

func (r serverConflictRepo) MergeFeature(ctx context.Context, base, source string, opts git.MergeFeatureOptions) (*git.MergeResult, error) {
	return r.repo.MergeFeature(ctx, base, source, opts)
}

func (r serverConflictRepo) MergeBase(ctx context.Context, a, b string) (string, error) {
	return r.repo.MergeBase(ctx, a, b)
}

func (r serverConflictRepo) MergeTree(ctx context.Context, base, ours, theirs string) ([]string, error) {
	return r.repo.MergeTree(ctx, base, ours, theirs)
}

func (r serverConflictRepo) Root() string { return r.repo.Root }

func (r serverConflictRepo) AddWorktree(ctx context.Context, worktreePath, branch string) (gitflow.ConflictWorktree, error) {
	return r.repo.AddWorktree(ctx, worktreePath, branch)
}

func (r serverConflictRepo) RemoveWorktree(ctx context.Context, worktreePath string) error {
	return r.repo.RemoveWorktree(ctx, worktreePath)
}
