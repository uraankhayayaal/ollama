package server

import (
	"encoding/json"
	"net/http"

	"ai/infrastructure/board"
	"ai/infrastructure/workspace"
)

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// --- утилиты ---

// computeStatus собирает статус проекта из состояния оркестрации и доски.
// Единая точка правды: используется и в projectMeta (REST), и в снапшоте
// сессии при WS-подключении (server/session.go), чтобы клиент всегда видел
// одно и то же состояние.
// standby — режим ожидания работы (запуск по кнопке, на доске ничего нет):
// сессия жива, но ничего не исполняет, пока не появится работа. Не перекрывает
// gating (HITL-затвор важнее ожидания работы).
func computeStatus(running, gating, standby bool, meta *board.Meta) string {
	status := "idle"
	if running {
		status = "running"
	}
	if gating {
		status = "waiting"
	}
	if meta != nil {
		switch meta.Status {
		case board.StatusDone:
			status = "done"
		case board.StatusInProgress:
			if !running {
				status = "in_progress"
			}
		}
	}
	if standby && running && !gating {
		status = "standby"
	}
	return status
}

// standbyReason — причина режима ожидания оркестрации. Единая строка для
// broadcastStatus, снапшота при WS-подключении и REST-меты: фронт должен видеть
// одно объяснение, откуда бы статус ни пришёл (Ф-3, PLAN-done-dashboard-events).
const standbyReason = "нет работы на доске — жду эпики и задачи"

// statusDetail — человекочитаемая причина текущего состояния (поле detail
// рядом со status). Сейчас единственный случай, где компоненты-потребители
// (кнопка Стоп/Продолжить, список проектов) нуждаются в причине — standby:
// сессия активна, но не исполняет работу, и UI объясняет это пользователю.
func statusDetail(running, gating, standby bool) string {
	if standby && running && !gating {
		return standbyReason
	}
	return ""
}

// projectMeta собирает ProjectMeta для REST API.
func projectMeta(inf workspace.Info, meta *board.Meta, running, gating, standby bool) map[string]any {
	status := computeStatus(running, gating, standby, meta)
	out := map[string]any{
		"project_name": inf.Name,
		"kind":         inf.Kind,
		"task":         "",
		"status":       status,
		"created_at":   "",
		"updated_at":   "",
	}
	if detail := statusDetail(running, gating, standby); detail != "" {
		out["status_detail"] = detail
	}
	if inf.GitRemote != "" {
		out["git_remote"] = inf.GitRemote
	}
	if inf.GitBranch != "" {
		out["git_branch"] = inf.GitBranch
	}
	if inf.GitBase != "" {
		out["git_base"] = inf.GitBase
	}
	if meta != nil {
		out["task"] = meta.Task
		out["created_at"] = meta.CreatedAt
		out["updated_at"] = meta.UpdatedAt
	}
	return out
}

// writeJSON записывает JSON-ответ.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr записывает ошибку в формате {message}.
func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"message": msg})
}

// decodeBody декодирует JSON из тела запроса.
func decodeBody(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// decodeBodyStrict — декодер с DisallowUnknownFields: опечатка в имени поля
// возвращает 400, а не «успех, параметр молча потерян». Применяется там, где
// тело задаёт поведение (инъекции).
func decodeBodyStrict(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// --- REST: проекты ---
