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
	"os"
	"strconv"
	"strings"

	"ai/application/usecases"
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
)

// RuntimeLogger — необязательный интерфейс агента: хвост логов рантайма,
// накопленный инструментом ReadAppLogs. Реализуется *tools.FileOps (встроен в
// агентов-разработчиков, QA и DevOps через внедрённый *tools.FileOps), поэтому
// подключается сам собой — как AutoFixer.
// RuntimeLogger — порт агента с хвостом логов рантайма (usecases.RuntimeLogger).
type RuntimeLogger = usecases.RuntimeLogger

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

// appLogFeedMessage — алиас usecases.AppLogFeedMessage (политика в application).
func appLogFeedMessage(source string, lines []string, used, max int) string {
	return usecases.AppLogFeedMessage(source, lines, used, max)
}

// trimAppLogLines — алиас usecases.TrimAppLogLines (политика в application).
func trimAppLogLines(lines []string) []string {
	return usecases.TrimAppLogLines(lines)
}
