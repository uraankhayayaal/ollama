// Package chatassist содержит детерминированную политику диалога чат-ассистента:
// распознавание согласия/вопроса, антизацикливающую подсказку (якорь действия),
// классификацию «отчёта о записи на доске». Вынесено из HTTP-слоя
// (`server/chatassist.go`, `server/actions.go`, Этап 6, срез F3w): решения о том,
// как трактовать реплики пользователя и модели, — правило, а не серверная
// обвязка.
package chatassist

import (
	"strings"

	"ai/chat"
)

// ConfirmTokens — слова-согласия, по которым деструктивное действие ассистента
// считается подтверждённым пользователем в чате.
var ConfirmTokens = map[string]bool{
	"да": true, "подтверждаю": true, "подтверждаем": true,
	"ок": true, "окей": true, "о'к": true,
	"делай": true, "делаю": true, "делаем": true,
	"согласен": true, "согласна": true, "разрешаю": true,
	"давай": true, "конечно": true, "вали": true,
	"yes": true, "yep": true,
	// «lf» — русское «да», набранное при не переключённой раскладке
	// (д→l, а→f). Без него подтверждение «да/нет» терялось и ассистент
	// спрашивал по кругу.
	"lf": true,
}

// ConfirmAffirmative сообщает, содержит ли строка явное согласие на действие.
// Токенный матчинг вместо regexp: \b в Go работает только для ASCII и не
// видит кириллицу.
func ConfirmAffirmative(s string) bool {
	for _, tok := range strings.Fields(strings.ToLower(s)) {
		if ConfirmTokens[strings.Trim(tok, " \t,.;:!?—–«»\"'()")] {
			return true
		}
	}
	return false
}

// BareConfirm — согласие ЧИСТОЙ репликой («да», «давай», «lf»), а не словом
// внутри длинной фразы. Нужен там, где фраза трактуется как ответ на вопрос
// да/нет: «давай посмотрим, что там по безопасности» согласием на удаление не
// является, хотя ConfirmAffirmative её поймает.
func BareConfirm(s string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(s)))
	if len(fields) == 0 || len(fields) > 3 {
		return false
	}
	return ConfirmAffirmative(s)
}

// boardWriteClaimPhrases — узкий список формулировок, которыми ассистент (по
// системному промпту) отчитывается о ТОЛЬКО ЧТО выполненном действии на доске.
// Список намеренно узкий: он ловит отчёт об уже сделанном (первое лицо,
// совершенный вид), а не нейтральные упоминания («эпиков на доске нет») — иначе
// легко подавить честный ответ.
var boardWriteClaimPhrases = []string{
	"завёл эпик", "завела эпик", "создал эпик", "создала эпик",
	"завёл баг", "завела баг", "создал баг", "создала баг",
	"запустил оркестрацию", "запустила оркестрацию",
}

// LooksLikeBoardWriteClaim сообщает, похож ли текст модели на отчёт о
// выполненном действии на доске (регистр не важен).
func LooksLikeBoardWriteClaim(content string) bool {
	low := strings.ToLower(content)
	for _, p := range boardWriteClaimPhrases {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

// AskSummary собирает текст вопросов структурированного AskUser в одну строку.
func AskSummary(a *chat.Ask) string {
	if a == nil {
		return ""
	}
	var parts []string
	for _, q := range a.Questions {
		if t := strings.TrimSpace(q.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "; ")
}

// AsksSomething — выглядит ли реплика ассистента как вопрос пользователю
// (заканчивается «?» либо содержит просьбу подтвердить/выбрать).
func AsksSomething(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return false
	}
	if strings.HasSuffix(t, "?") {
		return true
	}
	for _, m := range []string{"подтвердите", "(да/нет)", "да/нет", "уточните", "выберите"} {
		if strings.Contains(t, m) {
			return true
		}
	}
	return false
}

// PrevUserDiffers — грубая эвристика «пользователь сменил тему»: ответ резко
// отличается от последнего вопроса ассистента. Нужна, чтобы не ругать модель на
// вопрос, когда пользователь честно переформулировал задачу.
func PrevUserDiffers(asked, answer string) bool {
	a, b := strings.ToLower(strings.TrimSpace(asked)), strings.ToLower(strings.TrimSpace(answer))
	if a == "" || b == "" {
		return false
	}
	// Ответ из 1–2 слов — это явно не новая тема, а реакция на вопрос.
	if len([]rune(b)) <= 24 {
		return false
	}
	return !strings.Contains(a, strings.SplitN(b, " ", 2)[0])
}

// ActionAnchor вычисляет ситуативную подсказку для промпта ассистента по
// истории чата (хронологический порядок; текущая реплика пользователя — уже
// добавлена последней записью, поэтому исключается). Пусто — подсказки нет.
//
// Проблема, которую снимает якорь: ассистент на каждом ходе заново решает, что
// делать, и не помнит, что уже спрашивал; пользователь отвечает «да», а модель
// снова задаёт тот же вопрос, и ни одного инструмента не вызывается.
func ActionAnchor(hist []chat.Message, question string) string {
	if len(hist) == 0 {
		return ""
	}
	// Текущая реплика пользователя — последняя запись; предыдущие собираем
	// в обратном порядке (свежие → старые).
	if last := hist[len(hist)-1]; last.Role == chat.RoleUser {
		hist = hist[:len(hist)-1]
	}

	// Ищем две последние содержательные реплики ассистента.
	var assistants []chat.Message
	for i := len(hist) - 1; i >= 0 && len(assistants) < 2; i-- {
		switch hist[i].Role {
		case chat.RoleAssistant:
			if strings.TrimSpace(hist[i].Content) != "" {
				assistants = append(assistants, hist[i])
			}
		case chat.RoleAsk:
			if t := AskSummary(hist[i].Ask); t != "" {
				assistants = append(assistants, chat.Message{Content: t})
			}
		}
	}
	if len(assistants) == 0 {
		return ""
	}
	asked := AsksSomething(assistants[0].Content)

	// Согласие: только ЧИСТАЯ короткая реплика («да», «давай», «lf»). Слово
	// согласия внутри длинной фразы («давай посмотрим, что там по
	// безопасности») согласием на действие не является.
	switch {
	case BareConfirm(question):
		if asked {
			return "СИТУАЦИЯ: пользователь ответил «да» на твой предыдущий вопрос — " +
				"согласие получено. ВЫПОЛНЯЙ действие в этом ходе (вызови нужный инструмент). " +
				"Не переспрашивай, не пересказывай сводку и не предлагай следующий шаг — выполняй."
		}
		return "СИТУАЦИЯ: пользователь дал согласие. ВЫПОЛНЯЙ согласованное действие в этом ходе. " +
			"Не переспрашивай подтверждение — оно уже получено."

	case len(assistants) >= 2 && asked && AsksSomething(assistants[1].Content) && !PrevUserDiffers(assistants[1].Content, question):
		// Два соседних вопроса ассистента подряд, пользователь отвечает — но не
		// согласием и не отказом (ответ не даёт явного сигнала): риск третьего
		// вопроса. Ограничиваем явно.
		return "ВНИМАНИЕ: ты уже задал вопрос во второй раз подряд и пользователь ответил, " +
			"но не подтвердил и не отказал. Третий вопрос недопустим: либо прими ответ как есть " +
			"и ВЫПОЛНИ действие, либо скажи одним предложением, чего конкретно не хватает."
	}
	return ""
}
