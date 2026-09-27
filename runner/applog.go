package runner

// Ф-2: логи рантайма в цикл самокоррекции.
//
// Инструмент ReadAppLogs (tools/applog.go) умеет запустить приложение и
// вернуть его логи. Но модель после этого раунда обычно продолжает писать код
// «по памяти»: она видела зелёные тесты и не видела, что сервер упал с
// «listen tcp :8080: address already in use». Ровно это и съедает раунды.
//
// Поэтому после каждого раунда раннер забирает накопленный хвост логов
// рантайма и подмешивает его модели СКРЫТЫМ user-промптом — тем же механизмом,
// что и диагностики LSP в autofix.go, но с другим содержимым и своим лимитом
// итераций.
//
// Отличие от LSP-автофикса принципиально: файлы меняются каждый раунд, а
// рантайм надо ещё разобрать. Поэтому
//   - повтор одинаковых строк подавляется (filterNewDiags — общий помощник);
//   - лимит итераций отдельный (APP_LOG_MAX_FEED_ROUNDS, по умолчанию 2), и
//     он НЕ расходуется заново, пока модель не починит приложение;
//   - логи никогда не показываются как «ошибка» сами по себе: стектрейс в
//     логе — это подсказка, а не вердикт.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	// appLogFeedOnEnv — включение подмешивания (APP_LOG_AUTO_FEED).
	// 0/false/off/no — выключить.
	appLogFeedOnEnv = "APP_LOG_AUTO_FEED"
	// appLogMaxFeedRoundsEnv — лимит подмешиваний за один цикл.
	appLogMaxFeedRoundsEnv = "APP_LOG_MAX_FEED_ROUNDS"
	// defaultAppLogMaxFeedRounds — по умолчанию два: первый лог объясняет
	// проблему, второй подтверждает, что правка не помогла. Дальше модель
	// обычно начинает «качать» правки наугад, и жжение раундов вредит.
	defaultAppLogMaxFeedRounds = 2
	// appLogFeedMaxLines — сколько строк лога уходит в промпт. Логи рантайма
	// самые шумные (HTTP-запросы, прогресс-бары сборки), и 2000 строк съели бы
	// контекст раунда целиком.
	appLogFeedMaxLines = 60
	// appLogFeedMaxChars — потолок по символам: одна строка трейсера Go с
	// locals бывает в десятки килобайт.
	appLogFeedMaxChars = 6000
)

// RuntimeLogger — необязательный интерфейс агента: хвост логов рантайма,
// накопленный инструментом ReadAppLogs. Реализуется *tools.FileOps (встроен в
// агентов-разработчиков, QA и DevOps через внедрённый *tools.FileOps), поэтому
// подключается сам собой — как AutoFixer.
type RuntimeLogger interface {
	// TakeAppLogTail забирает и очищает накопленные строки логов рантайма.
	// «Забирает» — чтобы один и тот же лог не подмешивался в двух раундах
	// подряд (иначе модель зациклится на одном и том же тексте).
	TakeAppLogTail() (source string, lines []string)
}

// appLogFeedEnabled сообщает, включено ли подмешивание логов рантайма.
func appLogFeedEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(appLogFeedOnEnv))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// appLogMaxFeedRounds — лимит подмешиваний логов за цикл.
func appLogMaxFeedRounds() int {
	if v := strings.TrimSpace(os.Getenv(appLogMaxFeedRoundsEnv)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultAppLogMaxFeedRounds
}

// appLogFeedMessage собирает скрытый user-промпт по логам рантайма.
func appLogFeedMessage(source string, lines []string, used, max int) string {
	var b strings.Builder
	b.WriteString("ПРИЛОЖЕНИЕ В РАНТАЙМЕ ВЫДАЛО ЛОГИ (инструмент ReadAppLogs, источник ")
	b.WriteString(orDefault(source, "local"))
	b.WriteString("). Это не сообщение об ошибке твоей работы — это фактическое поведение запущенного приложения. ")
	b.WriteString("Осмотри строки ниже и, если в них есть настоящая причина (исключение, «address already in use», ")
	b.WriteString("ошибка миграции/БД, неожиданный код ответа, падение при старте), исправь её в коде ДО того, как объявишь задачу выполненной. ")
	b.WriteString("Если логи чистые, просто продолжай работу. Не пересказывай логи в ответе и не вызывай ReadAppLogs повторно ")
	b.WriteString("ради тех же строк — они уже у тебя есть.\n\n")
	b.WriteString("--- логи рантайма ---\n")
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n--- конец логов ---")
	fmt.Fprintf(&b, "\n(подсказка %d из %d)", used, max)
	return b.String()
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// trimAppLogLines оставляет последние appLogFeedMaxLines строк и обрезает по
// символам. Обрезка идёт с начала внутри лимита строк: при переполнении
// символами начало лога неинтересно, а конец содержит причину.
func trimAppLogLines(lines []string) []string {
	if len(lines) > appLogFeedMaxLines {
		lines = lines[len(lines)-appLogFeedMaxLines:]
	}
	budget := appLogFeedMaxChars
	for i, l := range lines {
		if len(l) > budget {
			return lines[i:]
		}
		budget -= len(l) + 1
	}
	return lines
}
