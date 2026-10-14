package adapters

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
//
// Адаптер порта `RuntimeLogger`: политика подмешивания (что и как) живёт в
// application/usecases, здесь — только конфигурация из окружения.

import (
	"os"
	"strconv"
	"strings"

	"ai/application/usecases"
)

// RuntimeLogger — порт агента с хвостом логов рантайма (usecases.RuntimeLogger).
// Реализуется *tools.FileOps (встроен в агентов-разработчиков, QA и DevOps),
// поэтому подключается сам собой — как AutoFixer.
type RuntimeLogger = usecases.RuntimeLogger

const (
	// AppLogFeedOnEnv — включение подмешивания (APP_LOG_AUTO_FEED).
	// 0/false/off/no — выключить.
	AppLogFeedOnEnv = "APP_LOG_AUTO_FEED"
	// AppLogMaxFeedRoundsEnv — лимит подмешиваний за один цикл.
	AppLogMaxFeedRoundsEnv = "APP_LOG_MAX_FEED_ROUNDS"
	// DefaultAppLogMaxFeedRounds — по умолчанию два: первый лог объясняет
	// проблему, второй подтверждает, что правка не помогла. Дальше модель
	// обычно начинает «качать» правки наугад, и жжение раундов вредит.
	DefaultAppLogMaxFeedRounds = 2
)

// AppLogFeedEnabled сообщает, включено ли подмешивание логов рантайма.
func AppLogFeedEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(AppLogFeedOnEnv))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// AppLogMaxFeedRounds — лимит подмешиваний логов за цикл.
func AppLogMaxFeedRounds() int {
	if v := strings.TrimSpace(os.Getenv(AppLogMaxFeedRoundsEnv)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return DefaultAppLogMaxFeedRounds
}
