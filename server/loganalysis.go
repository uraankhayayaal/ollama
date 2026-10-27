package server

import (
	"fmt"
	"time"

	applogs "ai/application/logs"
	"ai/shared/logging"
)

const logToolMaxFiles = 3

// Разбор логов по запросу панели логов (кнопка «Разобрать»).
//
// Поток целиком: панель отдаёт файл/поиск/уровень → детерминированный
// дайджест (domain/log.BuildLogDigest) → дайджест уходит в промпт ассистента →
// модель отвечает в чате и при необходимости докапывает находку инструментом
// ReadProjectLogs. Сырой лог целиком в промпт не идёт никогда.
//
// Вся политика (выборка файлов, фильтры, дайджест, cooldown, контракт ответа)
// — в application/logs; здесь остаётся только серверная обвязка: проект из
// реестра, имя своего лога и адаптеры.

// logAnalysisReq/logAnalysis — алиасы типов разбора из application/logs.
type logAnalysisReq = applogs.Req
type logAnalysis = applogs.Analysis

// logAnalysisCooldown — окно, в котором повторный разбор того же лога не
// запускает модель заново. Кнопку легко кликнуть дважды, а каждый разбор —
// полный ход ассистента с промптом и инструментами.
const logAnalysisCooldown = 90 * time.Second

// buildLogAnalysis готовит разбор: проект из реестра + делегирование в
// application/logs (сбор и фильтрация логов, дайджест).
func (sess *Session) buildLogAnalysis(req logAnalysisReq) (logAnalysis, error) {
	inf, err := sess.srv.reg.Get(sess.project)
	if err != nil {
		return logAnalysis{}, fmt.Errorf("проект %s не найден", sess.project)
	}
	own := logging.ProjectLogName(sess.project) + ".log"
	return applogs.BuildAnalysis(inf.Root, sess.srv.logsDir(), own, req, logToolMaxFiles)
}

// markAnalyzed запоминает разбор. recent возвращает true, если такой же ключ
// разбирали недавно (в пределах cooldown).
func (sess *Session) markAnalyzed(k string) (recent bool) {
	return sess.logCooldown.Mark(k)
}

// LogDigestPrompt — блок промпта с дайджестом и контрактом ответа (обёртка над
// application/logs.DigestPrompt).
func LogDigestPrompt(a logAnalysis) string { return applogs.DigestPrompt(a) }
