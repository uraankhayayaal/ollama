package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"ai/infrastructure/board"
	"ai/infrastructure/chat"
	"ai/infrastructure/workspace"
	"ai/logging"
)

// handleGetBoard возвращает снимок доски (meta + epics + tasks + bugs).
// Ф-3: пагинация через ?limit=&offset= — ограничивает каждую секцию и
// добавляет полные счётчики в total.
func (s *Server) handleGetBoard(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	ctx := r.Context()

	limit, offset := 0, 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if intval, err := strconv.Atoi(l); err == nil && intval > 0 {
			limit = intval
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if intval, err := strconv.Atoi(o); err == nil && intval > 0 {
			offset = intval
		}
	}

	// Сначала пробуем сессию (board store уже открыт).
	if sess := s.session(project); sess != nil {
		snap, err := boardViewPage(ctx, sess.board, limit, offset)
		if err == nil {
			snap.Git = s.gitStatus(ctx, project, snap.Epics, snap.Tasks)
			s.reconcileMRsAsync(project)
			writeJSON(w, http.StatusOK, snap)
			return
		}
	}

	// Фолбек: открываем store на лету.
	store, err := board.NewStore(ctx, board.LoadConfig().StoreConfig(project))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "доска недоступна: "+err.Error())
		return
	}
	defer store.Close()
	snap, err := boardViewPage(ctx, store, limit, offset)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "доска недоступна: "+err.Error())
		return
	}
	snap.Git = s.gitStatus(ctx, project, snap.Epics, snap.Tasks)
	s.reconcileMRsAsync(project)
	writeJSON(w, http.StatusOK, snap)
}

// --- REST: чат ---

// handleUpdateTask обновляет поля задачи (PATCH через PUT).
func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")

	var patch map[string]any
	if err := decodeBody(r, &patch); err != nil {
		writeErr(w, http.StatusBadRequest, "невалидный JSON")
		return
	}

	store, err := board.NewStore(r.Context(), board.LoadConfig().StoreConfig(project))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer store.Close()
	// Ф-1..Ф-4: авто-действия git-workflow при изменениях доски (ветки при
	// создании, мёрдж done→релиз, worktree задачи, синхрон с main).
	s.attachGitHooks(project, store)

	t, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "задача не найдена")
		return
	}

	// Применяем патч.
	if v, ok := patch["status"].(string); ok {
		if err := store.SetTaskStatus(r.Context(), taskID, board.Status(v)); err != nil {
			var stErr *board.StatusError
			if errors.As(err, &stErr) {
				writeErr(w, http.StatusBadRequest, stErr.Error())
				return
			}
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Перечитываем.
		t, _ = store.GetTask(r.Context(), taskID)
	}
	if v, ok := patch["title"].(string); ok {
		t.Title = v
	}
	if v, ok := patch["description"].(string); ok {
		t.Description = v
	}
	if v, ok := patch["assignee"].(string); ok {
		t.Assignee = v
	}
	if v, ok := patch["epic_id"].(string); ok {
		if v != t.EpicID {
			if err := store.MoveTask(r.Context(), taskID, v); err != nil {
				writeErr(w, http.StatusBadRequest, "перенос в эпик: "+err.Error())
				return
			}
		}
	}

	// Промпт-инъекции задачи: список заменяется целиком, нормализуется
	// (выдаётся id) и валидируется. Раньше поле было только для чтения — задать
	// инъекции задачи было нечем, кроме прямой правки Redis.
	injectionsChanged := false
	if raw, ok := patch["injections"]; ok {
		list, err := decodeInjections(raw)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "injections: "+err.Error())
			return
		}
		if err := board.ValidateInjections(list); err != nil {
			writeErr(w, http.StatusBadRequest, "injections: "+err.Error())
			return
		}
		t.Injections = list
		injectionsChanged = true
	}

	// Комментарии задачи: поле-список из UI (PUT /tasks/{id} c полем
	// comments) раньше молча терялось — обработчик его не разбирал, и
	// добавленный в модалке задачи комментарий не сохранялся. Список
	// заменяется целиком и валидируется, как в инструменте BoardUpdateTask.
	commentsChanged := false
	if raw, ok := patch["comments"]; ok {
		list, err := decodeTaskComments(raw, taskID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "comments: "+err.Error())
			return
		}
		t.Comments = list
		commentsChanged = true
	}

	if err := store.SaveTask(r.Context(), t); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if injectionsChanged {
		// Инъекции видны модели со следующего запроса: агентский цикл читает
		// их из записи задачи перед каждым обращением (board.InjectionSource).
		logging.For(project).Infof("[задача %s] промпт-инъекции обновлены: %d", taskID, len(t.Injections))
	}
	if commentsChanged {
		logging.For(project).Infof("[задача %s] комментарии обновлены: %d", taskID, len(t.Comments))
	}

	// Публикуем обновённую доску.
	s.srvEmitBoard(project, "REST: задача обновлена")
	writeJSON(w, http.StatusOK, t)
}

// handleDeleteTask удаляет задачу с доски (Ф-3). Задачи в работе/done/cancelled
// удалять нельзя — Store.DeleteTask вернёт ошибку. Для git-проектов снимается
// и запись ветки задачи из side-реестра.
func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")

	store, err := board.NewStore(r.Context(), board.LoadConfig().StoreConfig(project))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer store.Close()

	if err := store.DeleteTask(r.Context(), taskID); err != nil {
		if errors.Is(err, board.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "задача не найдена")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Git-workflow (Ф-1): снимаем из side-реестра ветку задачи.
	s.removeTaskBranch(project, taskID)

	// Публикуем обновлённую доску.
	s.srvEmitBoard(project, "REST: задача удалена")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// removeTaskBranch снимает из side-реестра ветку задачи git-проекта (Ф-3).
// No-op для не-git проектов и незаведённых веток — несуществующую ветку
// реестр и так не возвращает.
func (s *Server) removeTaskBranch(project, taskID string) {
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return
	}
	if err := s.reg.DeleteTaskBranch(project, taskID); err != nil {
		logging.For(project).Warnf("gitflow: снятие ветки задачи %s: %v", taskID, err)
	}
	if err := s.reg.DeleteTaskMR(project, taskID); err != nil {
		logging.For(project).Warnf("gitflow: снятие MR задачи %s: %v", taskID, err)
	}
	s.invalidateDiffs(project)
}

// handleSetEpicStatus — REST-перевод эпика в новый статус (кнопки «Помощь
// человека»/«Продолжить»/«Отменить», Ф-6). Эпики изолированы в своих git-ветках,
// поэтому остановка/отмена не откатывает код: работа просто приостанавливается
// (статус human_help, задачи каскадом останавливаются) либо запись помечается
// отменённой, а ветка остаётся как артефакт. Работает через Store.SetEpicStatus
// (FSM + каскад) с подключёнными git-хуками (done эпика по-прежнему синхронит
// релизную ветку с main).
func (s *Server) handleSetEpicStatus(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	epicID := r.PathValue("eid")

	var body struct {
		Status string `json:"status"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "невалидный JSON")
		return
	}
	st := board.Status(strings.TrimSpace(body.Status))
	if !st.Valid() {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("неизвестный статус %q", string(body.Status)))
		return
	}

	store, err := s.boardStore(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "доска недоступна: "+err.Error())
		return
	}
	defer store.Close()

	if err := store.SetEpicStatus(r.Context(), epicID, st); err != nil {
		var stErr *board.StatusError
		if errors.As(err, &stErr) {
			writeErr(w, http.StatusBadRequest, stErr.Error())
			return
		}
		if errors.Is(err, board.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "эпик не найден")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	epic, _ := store.GetEpic(r.Context(), epicID)
	logging.For(project).Infof("доска: эпик %s → %s (через REST)", epicID, st)
	s.srvEmitBoard(project, "REST: эпик сменил статус")
	writeJSON(w, http.StatusOK, epic)
}

// handleReorderEpics — ручная пересортировка эпиков на доске (Web UI,
// перетаскивание строк или стрелки). Тело: { "order": ["EPIC-1", "EPIC-2", …] } —
// первый ID получает наибольший приоритет (см. Store.ReorderEpics). Порядок
// действует внутри фазы эпика: фаза остаётся первичным ключом очереди лидов.
// Если оркестрация сейчас идёт — она ЖЁСТКО рестартуется в фоне, чтобы новая
// очередь применялась немедленно (стоп → старт). Ответ возвращаем сразу,
// статус рестарта виден по WS-событиям status (stopped → running).
func (s *Server) handleReorderEpics(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	var body struct {
		Order []string `json:"order"`
	}
	if err := decodeBody(r, &body); err != nil || len(body.Order) == 0 {
		writeErr(w, http.StatusBadRequest, `невалидный JSON: ожидается {"order": ["EPIC-1", "EPIC-2", ...]}`)
		return
	}

	store, err := s.boardStore(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "доска недоступна: "+err.Error())
		return
	}
	defer store.Close()

	updated, err := store.ReorderEpics(r.Context(), body.Order)
	if err != nil {
		if errors.Is(err, board.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "пересортировка: "+err.Error())
			return
		}
		writeErr(w, http.StatusBadRequest, "пересортировка: "+err.Error())
		return
	}

	restarting := false
	if sess := s.session(project); sess != nil {
		sess.emitBoard("эпики пересортированы")
		if sess.isRunning() {
			restarting = true
			prov, perr := s.provider()
			if perr != nil {
				sess.log.Warnf("пересортировка: LLM-провайдер недоступен: %v", perr)
				sess.append(chat.RoleStatus,
					"Пересортировка эпиков сохранена, но перезапуск оркестрации отложен: LLM-провайдер недоступен.",
					"", "", nil)
			} else {
				go func() {
					if rerr := sess.Restart(context.Background(), prov); rerr != nil {
						sess.log.Warnf("пересортировка: перезапуск оркестрации: %v", rerr)
						sess.append(chat.RoleStatus,
							"Пересортировка эпиков сохранена, но не удалось перезапустить оркестрацию: "+rerr.Error(),
							"", "", nil)
					}
				}()
			}
		}
	}
	logging.For(project).Infof("доска: пересортировка %d эпиков (%s), порядок: %s, перезапуск: %v",
		updated, project, strings.Join(body.Order, ", "), restarting)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": updated, "restarting": restarting})
}

// handleDeleteEpic удаляет эпик вместе с его задачами. Допустимо только для
// эпиков, задачи которых ещё не взяты в работу специалистами (статусы
// new/analysis/ready); иначе Store.DeleteEpic вернёт ошибку.
func (s *Server) handleDeleteEpic(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	epicID := r.PathValue("eid")

	store, err := board.NewStore(r.Context(), board.LoadConfig().StoreConfig(project))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer store.Close()

	// Список задач эпика нужен до удаления — после DeleteEpic их уже нет.
	var epicTasks []string
	if e, gerr := store.GetEpic(r.Context(), epicID); gerr == nil {
		epicTasks = e.Tasks
	}

	if err := store.DeleteEpic(r.Context(), epicID); err != nil {
		if errors.Is(err, board.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "эпик не найден")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Git-workflow (Ф-1): снимаем из side-реестра ветки эпика и его задач.
	s.deleteEpicBranches(project, epicID, epicTasks)

	// Публикуем обновлённую доску.
	s.srvEmitBoard(project, "REST: эпик удалён")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleListBugs возвращает список багрепортов проекта.
func (s *Server) handleListBugs(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	store, err := board.NewStore(r.Context(), board.LoadConfig().StoreConfig(project))
	if err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	defer store.Close()
	bugs, err := store.ListBugReports(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, bugs)
}

// --- WebSocket ---

// decodeInjections разбирает поле injections из JSON-патча: массив объектов
// или одиночный объект (удобно для точечных правок из UI). Общая реализация
// живёт в board, чтобы ею пользовался и инструмент доски.
func decodeInjections(raw any) ([]board.Injection, error) {
	return board.DecodeInjections(raw)
}

// decodeTaskComments разбирает поле comments из JSON-патча (список заменяется
// целиком). Оптимистичные id вида «tmp-…», которые фронтенд ставит до ответа
// сервера, отбрасываются — хранилище получает серверный id. Логика повторяет
// инструмент BoardUpdateTask (tools/board_tools.go).
func decodeTaskComments(raw any, taskID string) (board.Comments, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	list, err := board.DecodeComments(b)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if strings.HasPrefix(list[i].ID, "tmp-") {
			list[i].ID = ""
		}
		list[i].TaskID = taskID
		list[i].Normalize()
		if err := list[i].Validate(); err != nil {
			return nil, err
		}
	}
	return list, nil
}
