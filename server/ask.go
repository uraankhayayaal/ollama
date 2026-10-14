// Мост-инструмент AskUser: структурированный вопрос ассистента пользователю.
//
// Когда (Ф-1 «спроси пользователя»): ассистент видит неоднозначность и вместо
// домысливания задаёт пользователю структурированный вопрос — варианты ответа
// (один выбор/множественный), необязательное поле своего ответа и пометку
// рекомендуемого варианта. Пачка вопросов (несколько сразу) показывается
// пользователю пошагово (Ф-2) и отвечается через REST:
//
//	POST /api/projects/{id}/ask/{askID}/answer  {question_id, selected, custom}
//
// Инструмент БЛОКИРУЕТ агентский цикл до ответа пользователя — как HITL-затвор
// (waitGate): модель не генерирует продолжение, пока вопросы не отвечены.
// Ответы возвращаются модели как результат инструмента (status=answered),
// а в историю чата пишется протокольная status-строка с итогом (НЕ синтетиче-
// ское user-сообщение — Р-5: оно попало бы в модель как её собственный ход и
// сломало бы диалог).
//
// Инструмент живёт вне общего реестра tools (цикл импортов, как actionTools
// в actions.go): реализует tools.Tool прямо здесь и добавляется в набор
// ассистента на стороне сервера через serverActionTools (runChatAssistant).
package server

import (
	"context"
	"encoding/json"
	"time"

	appask "ai/application/ask"
	"ai/chat"
	"ai/tools"
)

// askToolName — имя моста-инструмента AskUser.
const askToolName = "AskUser"

// AskBackend — серверная часть структурированного вопроса: инструмент через
// интерфейс обращается к сессии (реализует *Session), отделение от HTTP —
// как у ActionsBackend в actions.go.
type AskBackend interface {
	// AskUser задаёт пользователю пачку вопросов и блокируется до ответа на
	// все из них. Возвращает ответы в порядке вопросов.
	AskUser(ctx context.Context, questions []chat.AskQuestion) ([]chat.AskAnswer, error)
}

// askTool — инструмент-мост AskUser (реализация tools.Tool). Ответ пользователя
// возвращается модели в аргументах следующего хода, поэтому интерфейс LLM
// ничего не нужно менять.
type askTool struct{ b AskBackend }

func (t *askTool) Name() string { return askToolName }

func (t *askTool) Definition() tools.ToolDefinition {
	return tools.ToolDefinition{
		Name:        askToolName,
		Description: "Структурно спросить пользователя при неоднозначности: варианты выбора (один или несколько) + необязательный вариант «свой ответ» с полем ввода. Можно задать несколько вопросов сразу — они показываются пользователю пошагово. Рекомендуемый вариант помечай recommended=true. Инструмент блокирует генерацию до ответа пользователя; ответы вернутся в результате (status=answered).",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"questions": map[string]any{
					"type":        "array",
					"description": "Пачка вопросов к пользователю (порядок = порядок шагов)",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"id":           map[string]any{"type": "string", "description": "Уникальный id вопроса (в пределах пачки)"},
							"text":         map[string]any{"type": "string", "description": "Текст вопроса"},
							"kind":         map[string]any{"type": "string", "enum": []any{"single", "multi"}, "description": "single — один выбор (клик по варианту = ответ), multi — несколько (чекбоксы + подтверждение)"},
							"allow_custom": map[string]any{"type": "boolean", "description": "Показывать ли вариант «свой ответ» с полем ввода (по умолчанию true)"},
							"options": map[string]any{
								"type": "array",
								"items": map[string]any{
									"type":                 "object",
									"additionalProperties": false,
									"properties": map[string]any{
										"id":          map[string]any{"type": "string"},
										"label":       map[string]any{"type": "string", "description": "Текст варианта"},
										"recommended": map[string]any{"type": "boolean", "description": "Предпочтительный вариант (в UI — бейдж «рекомендую»); решение всё равно за пользователем"},
									},
									"required": []any{"id", "label"},
								},
							},
						},
						"required": []any{"id", "text", "kind", "options"},
					},
				},
			},
			"required": []any{"questions"},
		},
	}
}

func (t *askTool) Execute(args map[string]any) ([]byte, error) {
	// Разумный ориентир для блокирующего вопроса: модель не должна «подвиснуть»
	// навсегда, если пользователь покинул беседу. Ответ после таймаута
	// возвращается как status=tool_timeout — модель продолжает диалог сама.
	ctx, cancel := context.WithTimeout(context.Background(), askAnswerTimeout)
	defer cancel()

	questions, err := appask.ParseQuestions(args)
	if err != nil {
		return json.Marshal(map[string]any{"status": "error", "ask_user": "invalid_arguments", "message": err.Error()})
	}
	ans, err := t.b.AskUser(ctx, questions)
	if err != nil {
		return json.Marshal(map[string]any{"status": "error", "message": err.Error()})
	}
	return json.Marshal(map[string]any{"status": "answered", "answers": ans})
}

// askAnswerTimeout — лимит ожидания ответа пользователя на пачку вопросов.
// После таймаута инструмент возвращает ошибку, и модель продолжает диалог
// (например, отвечает по умолчанию). Вопросы при этом остаются в истории чата.
const askAnswerTimeout = 15 * time.Minute

// --- реализация AskBackend на Session ---

// askAnnouncer — адаптер порта appask.Announcer поверх сессии: публикует
// карточку-вардинг в чат, протокольную статус-строку в историю и лог.
type askAnnouncer struct{ sess *Session }

func (a askAnnouncer) Ask(ask chat.Ask) {
	a.sess.appendMsg(chat.Message{Role: chat.RoleAsk, Agent: "assistant", Ask: &ask})
}

func (a askAnnouncer) Status(msg string) {
	a.sess.append(chat.RoleStatus, msg, "", "", nil)
}

func (a askAnnouncer) Info(msg string) {
	a.sess.log.Infof("%s", msg)
}

// AskUser публикует вопросы в чат и блокируется до ответа на все из них
// (делегирует состояние appask.Pending).
func (sess *Session) AskUser(ctx context.Context, questions []chat.AskQuestion) ([]chat.AskAnswer, error) {
	return sess.pending.Ask(ctx, questions)
}

// AnswerAsk принимает ответ пользователя на один вопрос пачки. Возвращает
// счётчик ответивших/всего; по последнему ответу разблокирует AskUser.
func (sess *Session) AnswerAsk(askID, questionID string, selected []string, custom string) (int, int, error) {
	return sess.pending.Answer(askID, questionID, selected, custom)
}
