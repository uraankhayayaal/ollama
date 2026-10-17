package kafkarelay

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

// runNamespace — фиксированный namespace детерминированных run_id: один и тот
// же эпик/ассистент должен давать один и тот же UUID в любом процессе и после
// любого рестарта (иначе шаги одного рана разъедутся по партициям).
var runNamespace = uuid.MustParse("f877313e-95ab-56cd-b630-6c5b030a66ff")

// TaskEpicFunc возвращает EpicID владельца задачи ("" — у задачи нет эпика).
// Ошибка или пустой ответ — запасной вариант: ран строится по самой задаче.
type TaskEpicFunc func(ctx context.Context, taskID string) (epicID string, err error)

// Mode определяет семантику run_id сессионного sink'а.
type Mode int

const (
	// ModeOrchestration — шаги оркестрации; run_id привязывается к эпику
	// целиком (кросс-сессионный анализ продукта без рваных запусков).
	ModeOrchestration Mode = iota
	// ModeAssistant — шаги чат-ассистента; свой параллельный run_id,
	// стабильный от сессии к сессии.
	ModeAssistant
)

// v5 — детерминированный UUIDv5 от набора частей (разделитель \x00, чтобы
// ("ab","c") и ("a","bc") не смешивались).
func v5(parts ...string) string {
	return uuid.NewSHA1(runNamespace, []byte(strings.Join(parts, "\x00"))).String()
}

// assistantRunID — стабильный run_id чат-ассистента проекта.
func assistantRunID(project string) string {
	return v5("assistant", project)
}

// scopeRunID — стабильный run_id для scope'а события оркестрации:
//   - epic:<id>  → ран самого эпика;
//   - task:<id>  → ран эпика-владельца задачи (эпик целиком, включая вклад
//     всех его задач); без известного эпика — ран самой задачи;
//   - architecture/bugs/пустой scope → стабильный вспомогательный ран проекта.
//
// epicOf может быть nil (без резолва задач).
func scopeRunID(ctx context.Context, project, scope string, epicOf TaskEpicFunc) string {
	switch {
	case strings.HasPrefix(scope, "epic:"):
		return v5("epic", strings.TrimPrefix(scope, "epic:"))
	case strings.HasPrefix(scope, "task:"):
		taskID := strings.TrimPrefix(scope, "task:")
		if epicOf != nil {
			cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			epicID, err := epicOf(cctx, taskID)
			cancel()
			if err == nil && epicID != "" {
				return v5("epic", epicID)
			}
		}
		return v5("task", taskID)
	case scope == "":
		return v5("project", project, "board")
	default:
		return v5("project", project, scope)
	}
}
