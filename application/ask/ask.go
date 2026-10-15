// Пакет ask — серверная логика структурированного вопроса ассистента
// (инструмент AskUser, Ф-1 «спроси пользователя»): разбор и валидация пачки
// вопросов из аргументов инструмента и состояние одного активного вопроса на
// сессию (блокирующий цикл AskUser↔AnswerAsk).
//
// Пакет не зависит от HTTP и от пакета server: публикация карточки-вардинга в
// чат, протокольной статус-строки и лога идёт через порт Announcer; сами
// domain.AskQuestion/AskAnswer — общие доменные типы.
package ask

import (
	"context"
	"fmt"
	"sync"
	"time"

	"ai/domain"
)

// Announcer — вывод состояния вопроса во внешний мир: карточка-вардинг в чат
// (role=ask), протокольная статус-строка в историю и лог сервера.
type Announcer interface {
	Ask(a domain.Ask)
	Status(msg string)
	Info(msg string)
}

// Pending — один активный структурированный вопрос на сессию: Ask блокируется
// до ответа на все вопросы пачки. Потокобезопасен.
type Pending struct {
	mu    sync.Mutex
	cur   *waiting
	newID func() string
	ann   Announcer
}

// waiting — ожидающая ответа пачка вопросов.
type waiting struct {
	id        string
	questions []domain.AskQuestion
	answers   map[string]domain.AskAnswer // по question_id
	resolved  chan []domain.AskAnswer     // буфер 1 — разблокировка Ask
}

// NewPending создаёт состояние вопросов с заданным выводом. Идентификатор
// вопроса генерируется по умолчанию (ask-<мс>).
func NewPending(ann Announcer) *Pending {
	return &Pending{ann: ann, newID: defaultAskID}
}

func defaultAskID() string {
	return fmt.Sprintf("ask-%d", time.Now().UnixMilli())
}

// Ask публикует вопросы в чат и блокируется до ответа на все из них. Один
// активный вопрос на сессию: повторный вызов при уже заданном — ошибка (модель
// должна сначала дождаться ответа или отменить вопрос по контексту).
func (p *Pending) Ask(ctx context.Context, questions []domain.AskQuestion) ([]domain.AskAnswer, error) {
	cur := &waiting{
		id:        p.newID(),
		questions: questions,
		answers:   make(map[string]domain.AskAnswer),
		resolved:  make(chan []domain.AskAnswer, 1),
	}

	p.mu.Lock()
	if p.cur != nil {
		id := p.cur.id
		p.mu.Unlock()
		return nil, fmt.Errorf("уже задан вопрос %s — дождись ответа или отмени его", id)
	}
	p.cur = cur
	p.mu.Unlock()

	p.ann.Ask(domain.Ask{ID: cur.id, Questions: questions})
	p.ann.Info(fmt.Sprintf("assistant: задаёт вопрос %s (%d вопросов)", cur.id, len(questions)))

	select {
	case ans := <-cur.resolved:
		return ans, nil
	case <-ctx.Done():
		p.mu.Lock()
		if p.cur == cur {
			p.cur = nil
		}
		p.mu.Unlock()
		return nil, ctx.Err()
	}
}

// Answer принимает ответ пользователя на один вопрос пачки. Возвращает счётчик
// ответивших/всего. Когда отвечены все вопросы — передаёт ответы
// разблокированному Ask и помечает вопрос закрытым.
func (p *Pending) Answer(askID, questionID string, selected []string, custom string) (int, int, error) {
	p.mu.Lock()
	cur := p.cur
	if cur == nil || cur.id != askID {
		p.mu.Unlock()
		return 0, 0, fmt.Errorf("нет активного вопроса %s (может, уже отвечен или истёк по таймауту)", askID)
	}
	q := findQuestion(cur.questions, questionID)
	if q == nil {
		p.mu.Unlock()
		return 0, 0, fmt.Errorf("вопрос %s не найден в пачке %s", questionID, askID)
	}
	// Выбранные варианты должны существовать (custom — отдельное поле).
	for _, sid := range selected {
		if !hasOption(q, sid) {
			p.mu.Unlock()
			return 0, 0, fmt.Errorf("вариант %q не существует в вопросе %s", sid, questionID)
		}
	}
	cur.answers[questionID] = domain.AskAnswer{
		QuestionID: questionID,
		Selected:   selected,
		Custom:     custom,
	}
	answered, total := len(cur.answers), len(cur.questions)
	done := answered == total
	if done {
		p.cur = nil
	}
	p.mu.Unlock()

	if done {
		// Ответы в порядке вопросов — модель видит их так же, как задавала.
		out := make([]domain.AskAnswer, 0, total)
		for _, qq := range cur.questions {
			out = append(out, cur.answers[qq.ID])
		}
		cur.resolved <- out
		p.ann.Status(fmt.Sprintf("Получены ответы на %d вопрос(ов) ассистента: %s", total, Summarize(cur.questions, out)))
		p.ann.Info(fmt.Sprintf("assistant: вопрос %s отвечен (%d/%d)", askID, answered, total))
	} else {
		p.ann.Info(fmt.Sprintf("assistant: вопрос %s: получен ответ на %d/%d", askID, answered, total))
	}
	return answered, total, nil
}

// ParseQuestions извлекает и валидирует пачку вопросов из аргументов
// инструмента AskUser.
func ParseQuestions(args map[string]any) ([]domain.AskQuestion, error) {
	raw, ok := args["questions"]
	if !ok {
		return nil, fmt.Errorf("нет ключа questions")
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("questions: нужен непустой массив")
	}
	seen := map[string]bool{}
	qs := make([]domain.AskQuestion, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("questions: элемент не объект")
		}
		q := domain.AskQuestion{
			ID:   StrAny(m, "id"),
			Text: StrAny(m, "text"),
		}
		if q.ID == "" || q.Text == "" {
			return nil, fmt.Errorf("questions: id и text обязательны")
		}
		if seen[q.ID] {
			return nil, fmt.Errorf("questions: дубликат id %q", q.ID)
		}
		seen[q.ID] = true
		switch domain.AskKind(StrAny(m, "kind")) {
		case domain.AskSingle, domain.AskMulti:
			q.Kind = domain.AskKind(StrAny(m, "kind"))
		case "":
			q.Kind = domain.AskSingle
		default:
			return nil, fmt.Errorf("questions[%s].kind: только single|multi", q.ID)
		}
		q.AllowCustom = BoolAny(m, "allow_custom", true)
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
			o := domain.AskOption{
				ID:          StrAny(om, "id"),
				Label:       StrAny(om, "label"),
				Recommended: BoolAny(om, "recommended", false),
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

// StrAny/BoolAny — безопасные чтения из разобранных аргументов инструмента.
func StrAny(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func BoolAny(m map[string]any, k string, def bool) bool {
	if v, ok := m[k].(bool); ok {
		return v
	}
	return def
}

// findQuestion ищет вопрос по id в пачке.
func findQuestion(qs []domain.AskQuestion, id string) *domain.AskQuestion {
	for i := range qs {
		if qs[i].ID == id {
			return &qs[i]
		}
	}
	return nil
}

// hasOption сообщает, есть ли вариант с указанным id у вопроса.
func hasOption(q *domain.AskQuestion, id string) bool {
	for _, o := range q.Options {
		if o.ID == id {
			return true
		}
	}
	return false
}

// Summarize — компактный протокол ответов для status-строки истории чата.
func Summarize(qs []domain.AskQuestion, ans []domain.AskAnswer) string {
	byQ := make(map[string]string, len(ans))
	for _, a := range ans {
		s := ""
		for _, o := range findQuestion(qs, a.QuestionID).Options {
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
