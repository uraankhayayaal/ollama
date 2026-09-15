package runner

import (
	"os"
	"strconv"
)

// Сжатие истории диалога (Скорость 3): длинный контекст агентского цикла
// (nudge-подсказки, результаты инструментов) раздувает запрос до десятков
// тысяч токенов и дорого обходится провайдеру. Перед каждым запросом к модели
// история сжимается под символьный бюджет: сохраняются системные сообщения,
// первая user-постановка задачи (head) и «хвост» последних сообщений; старейшие
// полные пары assistant+tool из середины выбрасываются детерминированно.
//
// Бюджет задаётся переменной окружения CODEGEN_HISTORY_BUDGET (символы,
// приблизительно: 1 токен ≈ 4 символа): 0 — сжатие отключено (по умолчанию
// выключено — флаг управления; ручное включение "80000" даёт ~20k токенов).
// Сжатие безопасно для resume-возобновления: оно детерминировано и сохраняет
// head (система + задача) и актуальное состояние последних раундов.

// historyBudget возвращает символьный бюджет истории (CODEGEN_HISTORY_BUDGET,
// 0 = сжатие выключено).
func historyBudget() int {
	if v := os.Getenv("CODEGEN_HISTORY_BUDGET"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// estimateLen оценивает «вес» истории в символах (содержимое + аргументы
// вызовов инструментов).
func estimateLen(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
		for _, tc := range m.ToolCalls {
			n += len(tc.Name) + len(tc.Arguments)
		}
	}
	return n
}

// CompressHistory сжимает историю под символьный бюджет. Возвращает ту же
// историю без изменений, если она уже укладывается в бюджет (в т.ч. при
// budget<=0). Граница отбрасываемой «середины» выбирается так, чтобы хвост
// не начинался с tool-сообщения: пара assistant(toolcalls)+tool-результаты
// сохраняется/отбрасывается целиком, и модель в хвосте не получает вызовов
// без их результатов.
func CompressHistory(msgs []Message, budget int) []Message {
	if len(msgs) <= 2 || budget <= 0 || estimateLen(msgs) <= budget {
		return msgs
	}

	// head: системные сообщения и первая user-постановка задачи. Всё, что
	// после первой user-сообщения, — кандидат на отбрасывание.
	head := len(msgs)
	for i, m := range msgs {
		if m.Role == "user" {
			head = i + 1
			break
		}
	}

	// Хвост: жадно включаем сообщения с конца, пока их вес укладывается в
	// остаток бюджета после головы.
	tailBudget := budget - estimateLen(msgs[:head])
	tailStart := len(msgs)
	for i := len(msgs) - 1; i >= head; i-- {
		if estimateLen(msgs[i:]) > tailBudget {
			break
		}
		tailStart = i
	}
	// Граница хвоста не должна открываться tool-сообщением (это результат
	// отброшенного вызова): отодвигаем границу левее, включая пару целиком.
	for tailStart > head && msgs[tailStart].Role == "tool" {
		tailStart--
	}
	// Хвоста не осталось (голова или отдельный хвост не умещаются) — отдаём
	// только голову: это максимально возможное сжатие без потери задачи.
	if tailStart == len(msgs) {
		return append([]Message{}, msgs[:head]...)
	}

	compressed := make([]Message, 0, head+len(msgs)-tailStart)
	compressed = append(compressed, msgs[:head]...)
	compressed = append(compressed, msgs[tailStart:]...)
	return compressed
}