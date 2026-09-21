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
	"fmt"
	"time"

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
		Name: askToolName,
		Description: "Структурно спросить пользователя при неоднозначности: варианты выбора (один или несколько) + необязательный вариант «свой ответ» с полем ввода. Можно задать несколько вопросов сразу — они показываются пользователю пошагово. Рекомендуемый вариант помечай recommended=true. Инструмент блокирует генерацию до ответа пользователя; ответы вернутся в результате (status=answered).",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"questions": map[string]any{
					"type": "array",
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

	questions, err := parseAskQuestions(args)
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

// parseAskQuestions извлекает пачку вопросов из аргументов инструмента.
func parseAskQuestions(args map[string]any) ([]chat.AskQuestion, error) {
	raw, ok := args["questions"]
	if !ok {
		return nil, fmt.Errorf("нет ключа questions")
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("questions: нужен непустой массив")
	}
	seen := map[string]bool{}
	qs := make([]chat.AskQuestion, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("questions: элемент не объект")
		}
		q := chat.AskQuestion{
			ID:   strAny(m, "id"),
			Text: strAny(m, "text"),
		}
		if q.ID == "" || q.Text == "" {
			return nil, fmt.Errorf("questions: id и text обязательны")
		}
		if seen[q.ID] {
			return nil, fmt.Errorf("questions: дубликат id %q", q.ID)
		}
		seen[q.ID] = true
		switch chat.AskKind(strAny(m, "kind")) {
		case chat.AskSingle, chat.AskMulti:
			q.Kind = chat.AskKind(strAny(m, "kind"))
		case "":
			q.Kind = chat.AskSingle
		default:
			return nil, fmt.Errorf("questions[%s].kind: только single|multi", q.ID)
		}
		q.AllowCustom = boolAny(m, "allow_custom", true)
		optsRaw, ok := m["options"].([]any)
		if !ok || len(optsRaw) == 0 {
			return nil, fmt.Errorf("questions[%s].options: нужен непустой массив", q.ID)
		}
		optsSeen := map[string]bool{}
		for _, oi := range optsRaw {
			om, ok := oi.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("questions[%s].options: элемент не объект", q.ID)
			}
			o := chat.AskOption{
				ID:          strAny(om, "id"),
				Label:       strAny(om, "label"),
				Recommended: boolAny(om, "recommended", false),
			}
			if o.ID == "" || o.Label == "" {
				return nil, fmt.Errorf("questions[%s].options: id и label обязательны", q.ID)
			}
			if optsSeen[o.ID] {
				return nil, fmt.Errorf("questions[%s].options: дубликат id %q", q.ID, o.ID)
			}
			optsSeen[o.ID] = true
			q.Options = append(q.Options, o)
		}
		qs = append(qs, q)
	}
	return qs, nil
}

// strAny/boolAny — безопасные чтения из разобранных аргументов инструмента.
func strAny(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func boolAny(m map[string]any, k string, def bool) bool {
	if v, ok := m[k].(bool); ok {
		return v
	}
	return def
}

// --- реализация AskBackend на Session ---

// pendingAsk — ожидающая ответа пачка вопросов (один активный вопрос на
// сессию): askTool блокирует agendский цикл до ответа на все вопросы.
type pendingAsk struct {
	id        string
	questions []chat.AskQuestion
	answers   map[string]chat.AskAnswer // по question_id
	resolved  chan []chat.AskAnswer     // буфер 1 — разблокировка AskUser
}

// AskUser публикует вопросы в чат (role=ask) и блокируется до ответа на все
// из них. Один активный вопрос на сессию: повторный вызов при уже заданном —
// ошибка (модель должна сначала дождаться ответа).
func (sess *Session) AskUser(ctx context.Context, questions []chat.AskQuestion) ([]chat.AskAnswer, error) {
	askID := fmt.Sprintf("ask-%d", time.Now().UnixMilli())

	sess.mu.Lock()
	if sess.pendingAsk != nil {
		sess.mu.Unlock()
		return nil, fmt.Errorf("уже задан вопрос %s — дождись ответа или отмени его", sess.pendingAsk.id)
	}
	p := &pendingAsk{
		id:        askID,
		questions: questions,
		answers:   make(map[string]chat.AskAnswer),
		resolved:  make(chan []chat.AskAnswer, 1),
	}
	sess.pendingAsk = p
	sess.mu.Unlock()

	// Публикуем вопрос в чат (role=ask, payload — пачка вопросов): фронт
	// показывает карточку-вардин, WebSocket туда же доставит её live.
	sess.appendMsg(chat.Message{Role: chat.RoleAsk, Agent: "assistant", Ask: &chat.Ask{ID: askID, Questions: questions}})
	sess.log.Infof("assistant: задаёт вопрос %s (%d вопросов)", askID, len(questions))

	select {
	case ans := <-p.resolved:
		return ans, nil
	case <-ctx.Done():
		sess.mu.Lock()
		if sess.pendingAsk == p {
			sess.pendingAsk = nil
		}
		sess.mu.Unlock()
		return nil, ctx.Err()
	}
}

// AnswerAsk принимает ответ пользователя на один вопрос пачки. Возвращает
// счётчик ответивших/всего. Когда отвечены все вопросы — передаёт ответы
// разблокированному AskUser и помечает вопрос закрытым.
func (sess *Session) AnswerAsk(askID, questionID string, selected []string, custom string) (int, int, error) {
	sess.mu.Lock()
	p := sess.pendingAsk
	if p == nil || p.id != askID {
		sess.mu.Unlock()
		return 0, 0, fmt.Errorf("нет активного вопроса %s (может, уже отвечен или истёк по таймауту)", askID)
	}
	q := findAskQuestion(p.questions, questionID)
	if q == nil {
		sess.mu.Unlock()
		return 0, 0, fmt.Errorf("вопрос %s не найден в пачке %s", questionID, askID)
	}
	// Выбранные варианты должны существовать (custom — отдельное поле).
	for _, sid := range selected {
		if !hasAskOption(q, sid) {
			sess.mu.Unlock()
			return 0, 0, fmt.Errorf("вариант %q не существует в вопросе %s", sid, questionID)
		}
	}
	p.answers[questionID] = chat.AskAnswer{
		QuestionID: questionID,
		Selected:   selected,
		Custom:     custom,
	}
	answered, total := len(p.answers), len(p.questions)
	done := answered == total
	if done {
		sess.pendingAsk = nil
	}
	sess.mu.Unlock()

	if done {
		// Ответы в порядке вопросов — модель видит их так же, как задавала.
		out := make([]chat.AskAnswer, 0, total)
		for _, qq := range p.questions {
			out = append(out, p.answers[qq.ID])
		}
		p.resolved <- out
		sess.append(chat.RoleStatus, fmt.Sprintf("Получены ответы на %d вопрос(ов) ассистента: %s", total, summarizeAsk(p.questions, out)), "", "", nil)
		sess.log.Infof("assistant: вопрос %s отвечен (%d/%d)", askID, answered, total)
	} else {
		sess.log.Infof("assistant: вопрос %s: получен ответ на %d/%d", askID, answered, total)
	}
	return answered, total, nil
}

// findAskQuestion ищет вопрос по id в пачке.
func findAskQuestion(qs []chat.AskQuestion, id string) *chat.AskQuestion {
	for i := range qs {
		if qs[i].ID == id {
			return &qs[i]
		}
	}
	return nil
}

// hasAskOption сообщает, есть ли вариант с указанным id у вопроса.
func hasAskOption(q *chat.AskQuestion, id string) bool {
	for _, o := range q.Options {
		if o.ID == id {
			return true
		}
	}
	return false
}

// summarizeAsk — компактный протокол ответов для status-строки истории чата.
func summarizeAsk(qs []chat.AskQuestion, ans []chat.AskAnswer) string {
	byQ := make(map[string]string, len(ans))
	for _, a := range ans {
		s := ""
		for _, o := range findAskQuestion(qs, a.QuestionID).Options {
			for _, sid := range a.Selected {
				if o.ID == sid {
					s += o.Label + ", "
				}
			}
		}
		if a.Custom != "" {
			s += "свой ответ: " + a.Custom
		}
		if s == "" {
			s = "без выбора"
		}
		byQ[a.QuestionID] = s
	}
	var b string
	for _, q := range qs {
		b += fmt.Sprintf("%s: %s; ", q.Text, byQ[q.ID])
	}
	return b
}