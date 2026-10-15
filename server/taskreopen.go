package server

// Переоткрытие выполненной задачи на доработку: POST
// /api/projects/{id}/tasks/{tid}/reopen. Задача возвращается из done в ready
// прежнему исполнителю, оркестратор забирает её в порядке очереди. Обоснование
// обязательно — комментарий (уже добавленный к задаче или переданный телом
// запроса); использованные комментарии помечаются учтёнными, чтобы по одному и
// тому же поводу нельзя было переоткрывать задачу повторно (board.ReopenTask).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"ai/infrastructure/board"
	"ai/infrastructure/chat"
)

// reopenRequest — тело POST .../reopen. Все поля необязательны: если body
// пуст, обоснованием служат уже добавленные к задаче необработанные
// комментарии.
type reopenRequest struct {
	Body   string `json:"body"`
	Author string `json:"author"`
	Type   string `json:"type"`
}

func (s *Server) handleReopenTask(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")

	var req reopenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "тело запроса: "+err.Error())
		return
	}

	store, err := s.boardStore(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "доска недоступна: "+err.Error())
		return
	}
	defer store.Close()

	reason := board.Comment{Author: "user", Type: board.CommentTypeUser, Body: req.Body}
	if a := strings.TrimSpace(req.Author); a != "" {
		reason.Author = a
	}
	if t := strings.TrimSpace(req.Type); t != "" {
		reason.Type = t
	}

	task, accounted, err := store.ReopenTask(r.Context(), taskID, reason)
	if err != nil {
		if errors.Is(err, board.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "задача не найдена")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	msg := fmt.Sprintf("Задача %s переоткрыта на доработку (%s, учтено комментариев: %d)",
		taskID, task.Assignee, accounted)
	if sess := s.session(project); sess != nil {
		sess.append(chat.RoleStatus, msg, "", "", nil)
	}
	s.srvEmitBoard(project, "REST: задача переоткрыта: "+taskID)
	writeJSON(w, http.StatusOK, task)
}
