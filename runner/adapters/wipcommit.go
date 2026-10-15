package adapters

// Ф-6: промежуточные коммиты задачи — единица ручного отката.
//
// Правки агента раньше фиксировались единственным коммитом на `done`
// (server/gitflow_auto.go), поэтому внутри прогона рабочее дерево оставалось
// грязным целиком: откатить задачу к рабочему состоянию было нечем, а
// `git stash` как единица отката не годится — он прячет изменения и не
// трогает untracked-файлы (те самые новые файлы, что создал агент).
//
// Здесь раунд, в котором агент менял файлы, закрывается коммитом. Побочный
// эффект не менее важен: git-история worktree задачи становится журналом
// трассировки прогона (раунд + список файлов), поэтому отдельный
// подписчик «события → Redis» для State Tracking не нужен.
//
// Коммит делает агент, а не раннер: раннер не знает ни проект, ни ветку, и
// не должен знать про git. Хук получает список файлов из того же ОДНОГО дрена
// очереди, что авто-лечение LSP и реиндексация RAG, — иначе хуки конкурировали
// бы за очередь.
//
// Адаптер порта `WIPCommitter`: формат сообщения коммита и конфигурация из
// окружения — здесь, фиксация средствами git — на стороне агента.

import (
	"context"
	"os"
	"strconv"
	"strings"

	"ai/application/usecases"
	"ai/infrastructure/git"
)

// WIPCommitOnEnv — env-флаг промежуточных коммитов (KANBAN_WIP_COMMIT).
// По умолчанию включён; выключается 0/false/off/no.
const WIPCommitOnEnv = "KANBAN_WIP_COMMIT"

// WIPCommitFilesMax — сколько путей перечислять в теле сообщения коммита.
// Список нужен человеку при разборе истории, а не модели, поэтому длинный
// хвост усекается счётчиком остатка.
const WIPCommitFilesMax = 12

// wipCommitFilesMaxEnv — env-override лимита файлов в сообщении коммита.
const wipCommitFilesMaxEnv = "KANBAN_WIP_COMMIT_FILES"

// WIPCommitter — необязательный интерфейс агента для фиксации правок раунда
// (Ф-6). Реализуется агентами-разработчиками поверх git.Repo рабочего
// каталога. Отказываться от реализации можно молча: без неё промежуточные
// коммиты просто не делаются, поведение прежнее.
type WIPCommitter = usecases.WIPCommitter

// WipRepo — состояние промежуточных коммитов рабочего каталога
// агента-разработчика. Сабмодули читаются один раз (при первом коммите) и
// кэшируются: набор в ходе прогона не меняется, а git-вызов на каждом раунде
// лишний. Нулевое значение готово к использованию; поле живёт в агенте,
// git-логика — здесь.
type WipRepo struct {
	subs   []git.Submodule
	loaded bool
}

// CommitWipRound фиксирует правки раунда промежуточным коммитом в рабочей
// копии dir (Ф-6). Гард области: коммит создаётся только в изолированной копии
// задачи (git.InTaskBranch); общий клон temp/<проект>, где агент пишет в режиме
// `plan`, стоит на ветке ai/<проект> — там коммит не делается (клон делят все
// шаги плана). Сабмодули добавляются, чтобы `git add -A` не закоммитил
// изменённый gitlink на коммит, которого нет на remote сабмодуля.
//
// Возвращает (sha, committed, err); пустой dir → (".", false, nil) без
// обращения к git. Ошибка не должна ронять генерацию: вызывающий логирует её,
// а правки остаются в рабочем дереве до следующего раунда.
func (r *WipRepo) CommitWipRound(ctx context.Context, dir string, touched []string, round int) (string, bool, error) {
	if dir == "" {
		return "", false, nil
	}
	repo := git.RepoFromState(&git.CLIExecutor{}, dir, "", "", "")
	inTask, err := repo.InTaskBranch(ctx)
	if err != nil {
		return "", false, err
	}
	if !inTask {
		return "", false, nil
	}
	if !r.loaded {
		subs, err := git.ListSubmodules(ctx, &git.CLIExecutor{}, dir)
		if err != nil {
			return "", false, err
		}
		r.subs = subs
		r.loaded = true
	}
	repo.Submodules = r.subs
	return repo.CommitIfDirty(ctx, WipRoundMessage(round, touched))
}

// WIPCommitEnabled сообщает, включены ли промежуточные коммиты.
func WIPCommitEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(WIPCommitOnEnv))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// wipCommitFilesLimit — сколько файлов показывать в сообщении коммита.
func wipCommitFilesLimit() int {
	if v := strings.TrimSpace(os.Getenv(wipCommitFilesMaxEnv)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return WIPCommitFilesMax
}

// WipRoundMessage собирает сообщение промежуточного коммита.
//
// Идентификатор задачи в сообщение не входит намеренно: он и так зашит в
// ветку (ai/task/<id>) и в имя worktree (.wt-task-<проект>-<id>), а репортёр
// раннера не отдаёт scope на чтение. Обогащение сообщения агентом, фазой и
// последней ошибкой проверки — задача этапа 2 (поля State Tracking).
func WipRoundMessage(round int, touched []string) string {
	limit := wipCommitFilesLimit()
	shown := touched
	rest := 0
	if len(shown) > limit {
		rest = len(shown) - limit
		shown = shown[:limit]
	}

	var b strings.Builder
	b.WriteString("wip: раунд ")
	b.WriteString(strconv.Itoa(round))
	if n := len(touched); n > 0 {
		b.WriteString(" (файлов: ")
		b.WriteString(strconv.Itoa(n))
		b.WriteString(")")
	}
	for _, f := range shown {
		b.WriteString("\n")
		b.WriteString(f)
	}
	if rest > 0 {
		b.WriteString("\n…ещё ")
		b.WriteString(strconv.Itoa(rest))
	}
	return b.String()
}
