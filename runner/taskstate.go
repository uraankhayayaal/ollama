package runner

// Ф-6 (этап 2): State Tracking — доска задачи знает, чем занят агент прямо
// сейчас, и где последняя рабочая точка для ручного отката.
//
// Источник сигнала один на всё: пост-раундовый блок хуков в runner.go, где уже
// известно и что агент менял (TakeTouched), и чем закончились проверки раунда
// (loopStateFromCall). Отдельный подписчик «события прогона → Redis» для этого
// не нужен: git-история worktree задачи уже хранит и коммиты раундов, и
// прохождение проверок (см. State ниже).

import (
	"strings"

	"ai/board"
)

// roundStateErrorMax — сколько символов вывода упавшей проверки писать в
// last_error задачи. Хватает, чтобы понять, что сломалось, и не раздувает
// запись доски (нормализованный вывод тестов бывает на десятки тысяч символов).
const roundStateErrorMax = 600

// RoundState — состояние задачи по итогам одного раунда агента.
type RoundState struct {
	// Round — номер раунда (1-based), для журнала.
	Round int
	// Agent — роль/имя специалиста, который делал раунд. Раннер агента по
	// имени не знает (в agents.Agent имени нет), поэтому поле заполняет сам
	// репортёр.
	Agent string
	// State — agent_state: writing_code/running_tests/fixing_errors/idle.
	State string
	// VerifyRan/VerifyFailed — была ли в раунде проверка и падала ли она.
	VerifyRan    bool
	VerifyFailed bool
	// VerifyCommand — нормализованная команда последней проверки раунда.
	VerifyCommand string
	// VerifyOutput — усечённый вывод последней УПАВШЕЙ проверки раунда.
	VerifyOutput string
	// CommitSHA/Committed — коммит промежуточной фиксации раунда.
	CommitSHA string
	Committed bool
	// Touched — сколько файлов изменено в раунде.
	Touched int
}

// RoundStateReporter — необязательный интерфейс агента для записи состояния
// задачи на доску (Ф-6). Реализует агент-разработчик поверх board.Store.
// Отсутствие интерфейса — не ошибка: доска просто не знает runtime-состояния.
type RoundStateReporter interface {
	// ReportRoundState вызывается один раз на раунд, ПОСЛЕ промежуточного
	// коммита (в том же хуке TakeTouched). Ошибки не возвращаются: запись
	// состояния не должна ронять генерацию.
	ReportRoundState(RoundState)
}

// RoundStateErrorText — короткое описание упавшей проверки для last_error
// задачи. Экспортируется: агент-репортёр пишет это в запись задачи на доске.
func RoundStateErrorText(rs RoundState) string {
	parts := make([]string, 0, 3)
	if rs.VerifyCommand != "" {
		parts = append(parts, rs.VerifyCommand)
	}
	if out := strings.TrimSpace(rs.VerifyOutput); out != "" {
		parts = append(parts, out)
	}
	if len(parts) == 0 {
		return ""
	}
	return Truncate(strings.Join(parts, " — "), roundStateErrorMax)
}

// agentStateFor — agent_state по итогам раунда. Приоритет: упавшая проверка
// (агент чинит ошибки) → прошедшая проверка (прогоняет тесты) → изменённые
// файлы (пишет код) → ничего не делал.
func agentStateFor(verifyRan, verifyFailed bool, touched int) string {
	switch {
	case verifyFailed:
		return board.AgentStateFixingErrors
	case verifyRan:
		return board.AgentStateRunningTests
	case touched > 0:
		return board.AgentStateWritingCode
	default:
		return board.AgentStateIdle
	}
}

// roundVerify собирает по раунду итог проверок: была ли проверка, падала ли и
// что она вывела.
//
// Признак «падала» — ЛЮБАЯ упавшая проверка раунда, даже если последняя
// проверка зелёная: «зелёный линт» не отменяет красный тест, а last_good_sha
// по такой конвенции не сдвинется на раунде, где что-то было сломано.
func roundVerify(calls []loopRoundCall) (ran, failed bool, cmd, sample string) {
	for _, c := range calls {
		if !c.verify {
			continue
		}
		ran = true
		cmd = c.verifyCmd
		if c.state != "" {
			failed = true
			sample = c.stateSample
			if sample == "" {
				sample = c.stateLabel
			}
		}
	}
	return ran, failed, cmd, sample
}

// newRoundState собирает состояние раунда из вызовов инструментов и результата
// промежуточного коммита (Ф-6, этап 2).
func newRoundState(round int, calls []loopRoundCall, touched []string, sha string, committed bool) RoundState {
	ran, failed, cmd, sample := roundVerify(calls)
	return RoundState{
		Round:         round,
		State:         agentStateFor(ran, failed, len(touched)),
		VerifyRan:     ran,
		VerifyFailed:  failed,
		VerifyCommand: cmd,
		VerifyOutput:  sample,
		CommitSHA:     sha,
		Committed:     committed,
		Touched:       len(touched),
	}
}
