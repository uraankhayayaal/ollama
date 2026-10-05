package server

// Ручной откат кода задачи (Ф-6, этап 3,
// PLAN-2026-10-05-todo-kanban-rollback.md): POST
// /api/projects/{id}/tasks/{tid}/rollback с телом {"to": "last_good"|"base"|
// "<sha>"}. Откатывает worktree задачи (вместе с сабмодулями) и возвращает
// задачу в очередь, чтобы агент продолжил с откатанного кода.
//
// Откат делает только человек и только по явному подтверждению в UI: он
// необратим в рабочем дереве (коммиты остаются в reflog) и отбрасывает всё
// незакоммиченное. Автоматических откатов в системе нет намеренно (Р-1):
// заменой автооткату служит watchdog этапа 4 — смена модели и возобновление.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"ai/board"
	"ai/chat"
	"ai/gitops"
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
	target, err := rollbackTarget(task, req.To)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	res, err := gitops.RollbackWorktree(r.Context(), s.gitExec, ref.Worktree, target)
	if err != nil {
		// Несуществующий коммит — вина запроса (опечатка в SHA, «последняя
		// рабочая версия» ещё не существует), а не сбой git: 400, а не 502.
		if errors.Is(err, gitops.ErrNoSuchCommit) {
			writeErr(w, http.StatusBadRequest, "откат не выполнен: "+err.Error())
			return
		}
		writeErr(w, http.StatusBadGateway, "откат не выполнен: "+err.Error())
		return
	}

	// Чекпойнт переезжает на откатанную точку: HEAD задачи теперь равен цели
	// отката, а отменённые раунды больше не являются ни «последним», ни
	// «последним рабочим». Ошибка проверки отменённого кода тоже снимается —
	// иначе UI продолжал бы показывать падение уже несуществующей версии.
	if err := store.PatchTask(r.Context(), taskID, func(t *board.Task) error {
		if t.Checkpoint == nil {
			t.Checkpoint = &board.TaskCheckpoint{}
		}
		t.Checkpoint.LastSHA = res.SHA
		t.Checkpoint.LastGoodSHA = res.SHA
		t.LastError = ""
		t.AgentState = ""
		return nil
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "обновление чекпойнта: "+err.Error())
		return
	}
	// Задача возвращается в очередь: агент следующего прогона продолжит с
	// откатанного кода. Отмена/пауза/готово остаются без изменений (иначе
	// откат тихо воскресил бы отменённую задачу).
	status := task.Status
	if status == board.StatusInProgress {
		if err := store.SetTaskStatus(r.Context(), taskID, board.StatusReady); err != nil {
			writeErr(w, http.StatusInternalServerError, "возврат в очередь: "+err.Error())
			return
		}
		status = board.StatusReady
	}

	short := res.SHA
	if len(short) > 7 {
		short = short[:7]
	}
	msg := fmt.Sprintf("Задача %s откатана к %s", taskID, short)
	if n := len(res.Submodules); n > 0 {
		msg += fmt.Sprintf(" (сабмодули: %s)", strings.Join(res.Submodules, ", "))
	}
	if status == board.StatusReady {
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
		SHA:        res.SHA,
		Submodules: res.Submodules,
		Status:     string(status),
	})
}

// rollbackTarget переводит `to` из тела запроса в SHA. Именованные точки —
// last_good (последний раунд, прошедший проверку) и base (состояние на
// момент старта задачи); всё остальное считается SHA: произвольный откат к
// нужному коммиту должен быть возможен без правок кода.
func rollbackTarget(task *board.Task, to string) (string, error) {
	to = strings.TrimSpace(to)
	if to == "" {
		return "", fmt.Errorf("не указана точка отката: %s или %s или SHA", "last_good", "base")
	}
	cp := task.Checkpoint
	switch to {
	case "last_good":
		if cp == nil || strings.TrimSpace(cp.LastGoodSHA) == "" {
			return "", fmt.Errorf("неизвестно последнее рабочее состояние: в задаче не было раунда, прошедшего проверку")
		}
		return cp.LastGoodSHA, nil
	case "base":
		if cp == nil || strings.TrimSpace(cp.BaseSHA) == "" {
			return "", fmt.Errorf("неизвестна база задачи: worktree не создавался в этой сессии")
		}
		return cp.BaseSHA, nil
	}
	if strings.ContainsAny(to, " \t") {
		return "", fmt.Errorf("некорректная цель отката %q", to)
	}
	return to, nil
}
