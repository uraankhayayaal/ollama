package server

// Ручной откат кода задачи (Ф-6, этап 3,
// PLAN-2026-10-05-todo-kanban-rollback.md): POST
// /api/projects/{id}/tasks/{tid}/rollback с телом {"to": "last_good"|"base"|
// "<sha>"}. Откатывает worktree задачи (вместе с сабмодулями) и возвращает
// задачу в очередь, чтобы агент продолжил с откатанного кода.
//
// Бизнес-логика отката живёт в application/usecases (Этап 6); здесь —
// HTTP-адаптер: разбор запроса, разрешение worktree, вызов use-case, маппинг
// доменных ошибок в коды и публикация события.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"ai/application/usecases"
	"ai/domain"
	"ai/infrastructure/board"
	"ai/infrastructure/chat"
	"ai/infrastructure/git"
)

// rollbackRequest — тело POST .../rollback. `to` — либо именованная точка
// отката, либо SHA коммита.
type rollbackRequest struct {
	To string `json:"to"`
}

type rollbackResponse struct {
	OK         bool     `json:"ok"`
	TaskID     string   `json:"task_id"`
	Project    string   `json:"project"`
	SHA        string   `json:"sha"`
	Submodules []string `json:"submodules,omitempty"`
	Status     string   `json:"status"`
}

func (s *Server) handleRollbackTask(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")

	var req rollbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "тело запроса: "+err.Error())
		return
	}

	store, err := s.boardStore(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "доска недоступна: "+err.Error())
		return
	}
	defer store.Close()
	task, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "задача не найдена")
		return
	}

	// Сначала worktree: откатывать нечего, если кода задачи не существует
	// (ветка не создавалась или её уже сняли). Отказ по нему фундаментальнее,
	// чем разбор цели, поэтому проверяем его до чтения чекпойнта.
	ref, err := s.reg.TaskBranch(project, taskID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "у задачи нет worktree (ветка не создавалась)")
		return
	}
	if strings.TrimSpace(ref.Worktree) == "" {
		writeErr(w, http.StatusBadRequest, "у задачи нет worktree: откатывать нечего")
		return
	}

	out, err := usecases.RollbackTask(r.Context(), board.NewDomainStore(store), task, ref.Worktree, req.To, gitRollbacker{ex: s.gitExec})
	if err != nil {
		// Несуществующий коммит — вина запроса (опечатка в SHA, «последняя
		// рабочая версия» ещё не существует), а не сбой git: 400, а не 502.
		if errors.Is(err, usecases.ErrBadTarget) || errors.Is(err, usecases.ErrNoSuchCommit) {
			writeErr(w, http.StatusBadRequest, "откат не выполнен: "+err.Error())
			return
		}
		writeErr(w, http.StatusBadGateway, "откат не выполнен: "+err.Error())
		return
	}

	short := out.SHA
	if len(short) > 7 {
		short = short[:7]
	}
	msg := fmt.Sprintf("Задача %s откатана к %s", taskID, short)
	if n := len(out.Submodules); n > 0 {
		msg += fmt.Sprintf(" (сабмодули: %s)", strings.Join(out.Submodules, ", "))
	}
	if out.Status == domain.StatusReady {
		msg += "; задача вернулась в очередь"
	}
	if sess := s.session(project); sess != nil {
		sess.append(chat.RoleStatus, msg, "", "", nil)
	}
	s.srvEmitBoard(project, "task: откат "+taskID)
	s.invalidateDiffs(project)
	writeJSON(w, http.StatusOK, rollbackResponse{
		OK:         true,
		TaskID:     taskID,
		Project:    project,
		SHA:        out.SHA,
		Submodules: out.Submodules,
		Status:     string(out.Status),
	})
}

// gitRollbacker — адаптер git-отката (infrastructure/git) к порту
// usecases.WorktreeRollbacker: переводит «нет такого коммита» в доменную ошибку
// usecases.ErrNoSuchCommit, остальные сбои git отдаёт как есть (502).
type gitRollbacker struct{ ex git.Executor }

func (g gitRollbacker) RollbackWorktree(ctx context.Context, worktree, target string) (string, []string, error) {
	res, err := git.RollbackWorktree(ctx, g.ex, worktree, target)
	if err != nil {
		if errors.Is(err, git.ErrNoSuchCommit) {
			return "", nil, usecases.ErrNoSuchCommit
		}
		return "", nil, err
	}
	return res.SHA, res.Submodules, nil
}
