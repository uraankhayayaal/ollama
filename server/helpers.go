package server

import (
	"encoding/json"
	"net/http"

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

// computeStatus собирает статус проекта из состояния сессии.
func computeStatus(running bool) string {
	if running {
		return "running"
	}
	return "idle"
}

const standbyReason = "нет работы — жду задачу"

func statusDetail(running bool, gating bool, standby bool) string {
	if standby && running && !gating {
		return standbyReason
	}
	return ""
}

// projectMeta собирает ProjectMeta для REST API.
func projectMeta(inf workspace.Info, running bool) map[string]any {
	status := computeStatus(running)
	out := map[string]any{
		"project_name": inf.Name,
		"kind":         inf.Kind,
		"task":         "",
		"status":       status,
		"created_at":   "",
		"updated_at":   "",
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
// возвращает 400, а не «успех, параметр молча потерян».
func decodeBodyStrict(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// --- REST: проекты ---
