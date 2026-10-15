package server

import (
	"encoding/json"
	"net/http"

	"ai/infrastructure/board"
	"ai/shared/logging"
)

// handleAddInjection добавляет сессионную инъекцию. Идентификатор выдаётся
// ДО сохранения: раньше он присваивался после AddSessionInjection, поэтому
// клиент получал id, которого в сессии не было, а DELETE по нему молча
// ничего не удалял.
func (s *Server) handleAddInjection(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	var body board.Injection
	if err := decodeBodyStrict(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "некорректный JSON: "+err.Error())
		return
	}
	// Инъекция сессии по умолчанию принадлежит scope session.
	if body.Scope == "" {
		body.Scope = board.InjectionScopeSession
	}
	body.Normalize()
	if err := body.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.ID == "" {
		body.ID = randomID()
	}
	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка: "+err.Error())
		return
	}
	if err := sess.UpsertSessionInjection(body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Ответ отдаёт id той записи, которая реально лежит в сессии.
	writeJSON(w, http.StatusCreated, map[string]string{
		"id":  body.ID,
		"msg": "инъекция добавлена; применяется со следующего запроса к модели",
	})
}

// handleRemoveInjection удаляет сессионную инъекцию по ID. Отсутствующий id —
// 404, а не 200: иначе клиент считает удаление успешным, а инъекция продолжает
// применяться.
func (s *Server) handleRemoveInjection(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	injID := r.PathValue("injID")
	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка: "+err.Error())
		return
	}
	if !sess.RemoveSessionInjection(injID) {
		writeErr(w, http.StatusNotFound, "инъекция не найдена: "+injID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"msg": "инъекция удалена"})
}

// handleListInjections возвращает список сессионных инъекций.
func (s *Server) handleListInjections(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка: "+err.Error())
		return
	}
	injs := sess.SessionInjections()
	if injs == nil {
		injs = []board.Injection{}
	}
	json.NewEncoder(w).Encode(injs)
}

// handleAddTaskInjection добавляет промпт-инъекцию к конкретной задаче доски
// (или перезаписывает запись с тем же id). Инъекция применяется к исполнителю
// этой задачи со СЛЕДУЮЩЕГО обращения к модели — в том числе если её цикл уже
// идёт: агентский цикл перечитывает задачу перед каждым запросом.
func (s *Server) handleAddTaskInjection(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")

	var inj board.Injection
	if err := decodeBodyStrict(r, &inj); err != nil {
		writeErr(w, http.StatusBadRequest, "некорректный JSON: "+err.Error())
		return
	}
	inj.Normalize()
	if err := inj.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if inj.ID == "" {
		inj.ID = randomID()
	}

	store, err := board.NewStore(r.Context(), board.LoadConfig().StoreConfig(project))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer store.Close()

	list, err := store.AddTaskInjection(r.Context(), taskID, inj)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "задача "+taskID+": "+err.Error())
		return
	}
	logging.For(project).Infof("[задача %s] добавлена промпт-инъекция %s (%d всего); применится со следующего запроса к модели",
		taskID, inj.Name, len(list))
	s.srvEmitBoard(project, "REST: инъекция задачи добавлена")
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         inj.ID,
		"injections": list,
	})
}

// handleRemoveTaskInjection удаляет инъекцию задачи по id; отсутствующий id —
// 404 (иначе клиент считал бы удаление успешным, а текст продолжал бы уходить
// в модель).
func (s *Server) handleRemoveTaskInjection(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")
	injID := r.PathValue("injID")

	store, err := board.NewStore(r.Context(), board.LoadConfig().StoreConfig(project))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer store.Close()

	list, found, err := store.RemoveTaskInjection(r.Context(), taskID, injID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "задача "+taskID+": "+err.Error())
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "инъекция не найдена: "+injID)
		return
	}
	logging.For(project).Infof("[задача %s] удалена промпт-инъекция %s (осталось %d)", taskID, injID, len(list))
	s.srvEmitBoard(project, "REST: инъекция задачи удалена")
	writeJSON(w, http.StatusOK, map[string]any{"injections": list})
}

// handleListTaskComments возвращает комментарии задачи.
func (s *Server) handleListTaskComments(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")

	store, err := board.NewStore(r.Context(), board.LoadConfig().StoreConfig(project))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer store.Close()

	t, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "задача не найдена: "+taskID)
		return
	}
	if t.Comments == nil {
		t.Comments = board.Comments{}
	}
	writeJSON(w, http.StatusOK, t.Comments)
}

// handleAddTaskComment добавляет комментарий к задаче.
func (s *Server) handleAddTaskComment(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")

	var cm board.Comment
	if err := decodeBodyStrict(r, &cm); err != nil {
		writeErr(w, http.StatusBadRequest, "некорректный JSON: "+err.Error())
		return
	}
	store, err := board.NewStore(r.Context(), board.LoadConfig().StoreConfig(project))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer store.Close()

	list, err := store.AddTaskComment(r.Context(), taskID, cm)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "задача "+taskID+": "+err.Error())
		return
	}
	s.srvEmitBoard(project, "REST: комментарий задачи добавлен")
	writeJSON(w, http.StatusCreated, map[string]any{"comments": list})
}

// handleUpdateTaskComment обновляет комментарий по ID (полная замена содержимого).
func (s *Server) handleUpdateTaskComment(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")
	cmID := r.PathValue("cmID")

	var cm board.Comment
	if err := decodeBodyStrict(r, &cm); err != nil {
		writeErr(w, http.StatusBadRequest, "некорректный JSON: "+err.Error())
		return
	}
	cm.ID = cmID
	cm.TaskID = taskID
	cm.Normalize()

	store, err := board.NewStore(r.Context(), board.LoadConfig().StoreConfig(project))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer store.Close()

	list, err := store.AddTaskComment(r.Context(), taskID, cm)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "задача "+taskID+": "+err.Error())
		return
	}
	s.srvEmitBoard(project, "REST: комментарий задачи обновлён")
	writeJSON(w, http.StatusOK, map[string]any{"comments": list})
}

// handleRemoveTaskComment удаляет комментарий задачи по id.
func (s *Server) handleRemoveTaskComment(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")
	cmID := r.PathValue("cmID")

	store, err := board.NewStore(r.Context(), board.LoadConfig().StoreConfig(project))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer store.Close()

	list, found, err := store.RemoveTaskComment(r.Context(), taskID, cmID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "задача "+taskID+": "+err.Error())
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "комментарий не найден: "+cmID)
		return
	}
	s.srvEmitBoard(project, "REST: комментарий задачи удалён")
	writeJSON(w, http.StatusOK, map[string]any{"comments": list})
}
