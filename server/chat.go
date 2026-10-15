package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"ai/chat"
)

// handlePostChat принимает сообщение. Чат работает ПАРАЛЛЕЛЬНО доске: сообщение
// идёт единому ассистенту (Ф-1), который сам по смыслу решает — создать
// эпик/задачу/баг инструментами Board* или ответить текстом. Бинарный сплит
// по ключевым словам убран: оркестрацию ассистент не запускает (берёт кнопка
// «Продолжить»).
// handlePostChat принимает сообщение пользователя (message) ИЛИ запрос разбора
// логов (log_analysis — кнопка «Разобрать» в панели логов). Второй вариант не
// пишет в историю выдуманную реплику пользователя: он добавляет служебную
// строку статуса, а разбор идёт отдельным ходом ассистента с дайджестом в
// промпте (см. runChatLogAnalysis).
func (s *Server) handlePostChat(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	var body struct {
		Message     string          `json:"message"`
		LogAnalysis *logAnalysisReq `json:"log_analysis"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "поле message обязательно")
		return
	}
	body.Message = strings.TrimSpace(body.Message)
	if body.Message == "" && body.LogAnalysis == nil {
		writeErr(w, http.StatusBadRequest, "поле message обязательно")
		return
	}

	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка создания сессии: "+err.Error())
		return
	}

	// Ветка разбора логов. Провайдера она берёт сама и только когда до него
	// дойдёт дело: отказ (нет файла), пустой разбор и cooldown не должны
	// требовать настроенной модели.
	if body.LogAnalysis != nil {
		s.handleChatLogAnalysis(w, sess, *body.LogAnalysis)
		return
	}

	// Единый ассистент: решает по смыслу (создание эпика/задачи/бага — через
	// его board-инструменты, а не по регэкспепу).
	prov, err := s.provider()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "LLM-провайдер не настроен: "+err.Error())
		return
	}
	log.Printf("[llm] модель: %s", s.prov.describe())

	// Пользовательское сообщение → чат (для истории) и в лог проекта.
	sess.log.Infof("[чат] пользователь: %s", truncateText(body.Message, 300))
	if _, err := sess.chat.Append(context.Background(), chat.Message{
		Role:    chat.RoleUser,
		Content: body.Message,
		Agent:   "user",
	}); err != nil {
		sess.log.Warnf("server: append user msg %s: %v", project, err)
	}

	// Публикуем в шину для live-таймлайна.
	sess.srv.hub.Publish(project, "chat", chat.Message{
		Role:    chat.RoleUser,
		Content: body.Message,
		Agent:   "user",
		Time:    time.Now().UTC(),
	})

	// Единый ассистент: решает по смыслу (создание эпика/задачи/бага — через
	// его board-инструменты, а не по регэкспепу).
	sess.log.Infof("[чат] маршрут: единый ассистент (действия по смыслу, без сплита)")
	sess.runChatAssistant(context.Background(), body.Message, prov)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleChatLogAnalysis обслуживает кнопку «Разобрать» в панели логов. Ответ
// уходит в чат (и в шину), в REST — короткий статус.
func (s *Server) handleChatLogAnalysis(w http.ResponseWriter, sess *Session, req logAnalysisReq) {
	ctx := context.Background()
	a, err := sess.buildLogAnalysis(req)
	if err != nil {
		sess.append(chat.RoleStatus, "Разбор логов не удался: "+err.Error(), "", "", nil)
		writeErr(w, http.StatusBadRequest, "разбор логов: "+err.Error())
		return
	}

	// Служебная строка в чате: пользователь нажал кнопку, а не что-то написал.
	status := "Разбор логов: " + a.FilesLabel()
	if a.Note != "" {
		status += " (" + a.Note + ")"
	}
	sess.log.Infof("[чат] %s", status)
	// Именно append, а не sess.chat.Append + отдельный hub.Publish: appendMsg
	// сам публикует в шину (type=chat), а вторая публикация отправила бы ту же
	// строку в UI дважды — фронт добавляет каждое событие чата в состояние
	// без дедупликации, и в панели появлялись два «Разбор логов: …».
	sess.append(chat.RoleStatus, status, "", "", nil)

	// Пустой разбор: критичных проблем нет. Отвечаем детерминированно, без
	// вызова модели — тратить токены на «всё хорошо» незачем, а модель на
	// пустом дайджесте склонна выдумать проблему.
	if a.Digest.Empty() {
		msg := fmt.Sprintf("В логе %s критичных проблем не найдено: ни одной записи уровня WARN/ERROR/FATAL%s.",
			a.FilesLabel(), logAnalysisWhere(a.Note))
		sess.append(chat.RoleAssistant, msg, "assistant", "", nil)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "empty": true})
		return
	}

	sess.log.Infof("[чат] маршрут: разбор логов (%d строк, %d находок)",
		a.Digest.Lines, len(a.Digest.Findings))
	if sess.markAnalyzed(a.Key()) {
		// Тот же лог только что разобран: модель второй раз не зовём, ответ
		// уже выше в чате.
		sess.append(chat.RoleStatus,
			fmt.Sprintf("Лог %s не изменился с прошлого разбора — повторно не запускаю, ответ выше в чате.", a.FilesLabel()),
			"", "", nil)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cached": true})
		return
	}

	prov, err := s.provider()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "LLM-провайдер не настроен: "+err.Error())
		return
	}
	log.Printf("[llm] модель: %s", s.prov.describe())
	sess.runChatLogAnalysis(ctx, a, prov)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "findings": len(a.Digest.Findings)})
}

// logAnalysisWhere — хвост фразы про объём выборки для ответа «проблем нет».
func logAnalysisWhere(note string) string {
	if note == "" {
		return ""
	}
	return " в выборке (" + note + ")"
}

// handleContinue запускает/возобновляет оркестрацию на текущей доске (кнопка
// «Продолжить»). Раннер работает в board-only режиме: в чат ничего не
// отправляется, новые эпики не создаются — берутся в работу записи, которые уже
// лежат на доске (эпики без задач декомпозируются лидами). Если брать в работу
// нечего, оркестрация переходит в режим ожидания и ждёт появления работы (см.
// Session.standby).
func (s *Server) handleContinue(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")

	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка создания сессии: "+err.Error())
		return
	}

	ctx := context.Background()
	// Текст задачи для раннера: приоритет meta-задачи проекта; если её нет
	// (задачи добавлены чатом/вручную до первого запуска) — берём обобщённое
	// описание доски. Пустая доска не ошибка: запуск уходит в режим ожидания.
	taskText, err := sess.continueTaskText(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "чтение доски: "+err.Error())
		return
	}

	prov, err := s.provider()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "LLM-провайдер не настроен: "+err.Error())
		return
	}

	// Кнопка «Продолжить» — работа только с доской: новые эпики не создаются,
	// текст задачи лишь описывает контекст для раннера.
	if err := sess.start(ctx, taskText, prov, true); err != nil {
		sess.log.Warnf("[continue] запуск оркестрации отклонён: %v", err)
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleProjectIndex — фоновая индексация RAG-памяти проекта по кнопке в Web
// UI (опциональный нюанс Ф-5, PLAN-2026-09-24-done-architect-intelligence.md,
// Р-2). Вызов не блокирует цикл: Session.IndexBackground запускает горутину;
// результат (файлы/чанки) уходит в лог и chat.RoleStatus. Повтор при уже
// идущей индексации — 409; недоступный клиент RAG (нет Qdrant/модели) — 503.
// Необязательный query-параметр branch задаёт ветку индексации (пусто — текущая
// ветка каталога проекта), см. PLAN-2026-09-27-done-branch-aware-rag.md.
func (s *Server) handleProjectIndex(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка создания сессии: "+err.Error())
		return
	}
	branch := strings.TrimSpace(r.URL.Query().Get("branch"))
	if err := sess.IndexBackground(r.Context(), branch); err != nil {
		sess.log.Warnf("[index] фоновая индексация %s: %v", project, err)
		msg := err.Error()
		if strings.Contains(msg, "уже запущена") {
			writeErr(w, http.StatusConflict, msg)
			return
		}
		writeErr(w, http.StatusServiceUnavailable, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "Индексация RAG-индекса запущена в фоне" + branchSuffix(branch),
	})
}

// handleChatHistory возвращает историю диалога.
func (s *Server) handleChatHistory(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	limit := int64(200)
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	msgs, err := sess.chat.History(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

// handleClearChat («кофе-брейк») стирает историю диалога, чтобы UI и модель
// начали общение с чистого листа. Стрим удаляется целиком; клиентам шлётся
// событие chat_clear (они очищают локальный массив сообщений), а в свежий
// стрим пишется системная пометка о начале нового диалога. Она не попадает
// в контекст модели (chatDialogueHistory игнорирует роль system).
func (s *Server) handleClearChat(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := sess.chat.Clear(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, "очистка чата: "+err.Error())
		return
	}
	// Знак для остальных клиентов: стрим пуст, стирайте локальный массив.
	sess.srv.hub.Publish(project, "chat_clear", map[string]bool{"ok": true})
	sess.append(chat.RoleSystem, "Кофе-брейк: диалог очищен, начинаем общение с чистого листа.", "system", "", nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleGetTokens возвращает накопленные токены проекта (вход/выход) и
// последнюю скорость генерации (вых. ток/с).
func (s *Server) handleGetTokens(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка создания сессии: "+err.Error())
		return
	}
	in, out, err := sess.tok.Get(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sess.mu.Lock()
	rate := sess.rate
	sess.mu.Unlock()
	writeJSON(w, http.StatusOK, tokenEvent{Input: in, Output: out, TPS: rate})
}

// --- REST: HITL ---

// handleGateDecide принимает решение человека по затвору (epics/tasks).
func (s *Server) handleGateDecide(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	gateName := r.PathValue("gate")

	if gateName != "epics" && gateName != "tasks" {
		writeErr(w, http.StatusBadRequest, "gate: epics или tasks")
		return
	}

	var body struct {
		Approved bool   `json:"approved"`
		Reason   string `json:"reason"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "невалидный JSON")
		return
	}

	sess := s.session(project)
	if sess == nil {
		writeErr(w, http.StatusNotFound, "сессия не найдена")
		return
	}
	if err := sess.approve(gateName, body.Approved, body.Reason); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleStop останавливает текущую оркестрацию.
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	sess := s.session(project)
	if sess == nil {
		writeErr(w, http.StatusNotFound, "сессия не найдена")
		return
	}
	sess.stop()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAskAnswer принимает ответ пользователя на один шаг пачки вопросов
// ассистента (AskUser): {question_id, selected, custom} → {ok, answered, total}.
// Когда отвечены все вопросы, AnswerAsk разблокирует агентский цикл.
func (s *Server) handleAskAnswer(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	askID := r.PathValue("askID")

	var body struct {
		QuestionID string   `json:"question_id"`
		Selected   []string `json:"selected"`
		Custom     string   `json:"custom"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "невалидный JSON")
		return
	}
	if body.QuestionID == "" {
		writeErr(w, http.StatusBadRequest, "question_id обязателен")
		return
	}

	sess := s.session(project)
	if sess == nil {
		writeErr(w, http.StatusNotFound, "сессия не найдена")
		return
	}
	answered, total, err := sess.AnswerAsk(askID, body.QuestionID, body.Selected, body.Custom)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "answered": answered, "total": total})
}

// --- REST: задачи (Ф-2, минимум для фронта) ---

// handleWS выполняет WS-upgrade и подписывает клиента на проект.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	conn, err := WsUpgrade(w, r)
	if err != nil {
		return // ошибка уже отправлена клиенту
	}
	s.hub.Subscribe(project, conn)
	// Снапшот текущего состояния: клиент сразу видит актуальные status/board/
	// tokens (а не устаревший idle), даже если оркестрация уже идёт.
	// getOrCreate: сессия обязана существовать, пока открыт дашборд — иначе
	// srvEmitBoard (кнопки «ветка/MR/релиз» в UI) некому публиковать после
	// перезапуска сервера, и доска в браузере останется устаревшей (Ф-5).
	if sess, _, err := s.getOrCreate(project); err == nil {
		sess.broadcastSnapshot()
	}
	// Ф-5: при открытии дашборда асинхронно сверяем MR с форджем (ветки,
	// созданные вне UI, и статусы merged/closed у отслеживаемых).
	s.reconcileMRsAsync(project)
}
