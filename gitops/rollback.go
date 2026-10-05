package gitops

// Ручной откат задачи (Ф-6, этапы 3 и 5,
// PLAN-2026-10-05-todo-kanban-rollback.md): возврат worktree задачи к опорной
// точке её git-истории — «к последней рабочей версии» (last_good_sha), «снести
// всё» (base_sha) или к произвольному коммиту.
//
// Почему git, а не stash или снапшот: единица отката — коммит, который агент и
// так делает каждый раунд (runner/wipcommit.go). Откат переводит ветку задачи
// назад, поэтому работа агента продолжается с откатанного кода, а отменённые
// коммиты остаются в reflog.
//
// Ограничения, важные для безопасности:
//
//   - Только из worktree ВЕТКИ ЗАДАЧИ (InTaskBranch). Общий клон проекта
//     откатывать нельзя: его делят все шаги плана, и откат задачи снёс бы
//     чужую работу.
//   - `git clean -fdq` БЕЗ `-x`: игнорируемые файлы (node_modules, .venv,
//     кэши сборки) — не артефакты прогона, сносить их нельзя, а их
//     пересоздание дорого.
//   - Сабмодули `reset --hard` не трогает: gitlink в index остаётся новым, а
//     рабочее дерево сабмодуля — старым. Поэтому откат рекурсивный и
//     ориентируется на gitlink'и ЦЕЛЕВОГО коммита родителя (то есть на то
//     состояние, которое было у задачи в тот момент).
//   - Откат необратим в рабочем дереве (коммиты остаются только в reflog) —
//     поэтому вызывающий обязан подтвердить действие у человека.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrNoSuchCommit — цель отката не существует. Отдельная ошибка (а не просто
// «git упал»), чтобы вызывающий отличил опечатку человека в SHA (400, его вина)
// от сбоя git (502). Основание: несуществующий ref не должен «откатить куда-нибудь».
var ErrNoSuchCommit = errors.New("gitops: нет такого коммита")

// RollbackResult — что сделал откат (для ответа API и записи в чат).
type RollbackResult struct {
	// SHA — коммит, к которому откатились.
	SHA string
	// Submodules — пути сабмодулей (через «/» для вложенных), откатанных
	// рекурсивно.
	Submodules []string
	// GitlinksStaged — true, если gitlink'ы сабмодулей поставлены в индекс
	// родителя (иначе возврат сабмодуля остался бы незамеченным коммитом).
	GitlinksStaged bool
}

// RollbackWorktree возвращает рабочее дерево к коммиту sha: сбрасывает ветку и
// индекс (`reset --hard`), убирает незакоммиченные правки и неотслеживаемые
// файлы. Рекурсивно откатывает вложенные worktree сабмодулей и ставит
// обновлённые gitlink'ы в индекс родителя.
//
// Требует, чтобы рабочее дерево стояло на ветке задачи; иначе отказ.
func RollbackWorktree(ctx context.Context, ex Executor, root, sha string) (*RollbackResult, error) {
	if ex == nil {
		return nil, fmt.Errorf("gitops: не задан исполнитель")
	}
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("gitops: пустой каталог отката")
	}
	repo := RepoFromState(ex, root, "", "", "")
	inTask, err := repo.InTaskBranch(ctx)
	if err != nil {
		return nil, err
	}
	if !inTask {
		return nil, fmt.Errorf("gitops: откат разрешён только в worktree ветки задачи (%s)", TaskBranchPrefix)
	}
	target := strings.TrimSpace(sha)
	if target == "" {
		return nil, fmt.Errorf("gitops: не задан коммит отката")
	}
	// Коммит должен существовать: иначе reset ушёл бы в никуда (или, что
	// хуже, откатил бы дерево к предку из-за опечатки в SHA).
	if _, err := repo.revParse(ctx, target+"^{commit}"); err != nil {
		return nil, fmt.Errorf("коммит отката %s: %w", target, errors.Join(ErrNoSuchCommit, err))
	}

	if err := resetWorktree(ctx, ex, root, target); err != nil {
		return nil, err
	}
	// reset --hard приводит и индекс, и рабочее дерево к коммиту; `checkout --
	// .` после него обычно пустая операция, но страхует от рассинхрона
	// индекса (например, после прерванной операции), из-за которого reset
	// отказал бы, а дерево осталось бы грязным.
	if _, err := ex.Exec(ctx, root, "git", "checkout", "--", "."); err != nil {
		return nil, fmt.Errorf("gitops: сброс изменений: %w", err)
	}

	res := &RollbackResult{SHA: target}
	subs, err := rollbackSubmodules(ctx, ex, root, target, "")
	if err != nil {
		return res, err
	}
	res.Submodules = subs
	if len(subs) > 0 {
		// Gitlink'ы теперь смотрят на откатанные коммиты — пусть это видно в
		// индексе родителя: иначе ближайший коммит раунда (`git add -A`) не
		// заметит расхождения, а `git status` покажет «чисто».
		args := append([]string{"git", "add", "--"}, subs...)
		if _, err := ex.Exec(ctx, root, args...); err != nil {
			return res, fmt.Errorf("gitops: staging gitlink'ов: %w", err)
		}
		res.GitlinksStaged = true
	}
	return res, nil
}

// rollbackSubmodules откатывает вложенные worktree сабмодулей репозитория root
// к gitlink'ам, записанным на коммите ref этого репозитория, и возвращает
// список откатанных путей (для вложенных — через «/»). Гард ветки задачи
// здесь НЕ проверяется: внутри сабмодуля мы уже в ветке задачи, а база
// сабмодуля может быть любой веткой (см. server.taskWorktree).
func rollbackSubmodules(ctx context.Context, ex Executor, root, ref, prefix string) ([]string, error) {
	subs, err := ListSubmodules(ctx, ex, root)
	if err != nil {
		return nil, err
	}
	var done []string
	for _, sub := range subs {
		subRoot := sub.Root
		if strings.TrimSpace(subRoot) == "" {
			subRoot = filepath.Join(root, sub.Path)
		}
		// Куда откатывать сабмодуль: ровно тот коммит, который записан в
		// gitlink'е на ЦЕЛЕВОМ коммите родителя. Это и есть «состояние задачи
		// на тот момент» — вложенный reset того же коммита его не заменит.
		subSHA := subRevision(ctx, ex, root, ref, sub.Path)
		if subSHA == "" {
			// Сабмодуль на этом коммите не зафиксирован: откатывать нечего.
			continue
		}
		if err := resetWorktree(ctx, ex, subRoot, subSHA); err != nil {
			return done, fmt.Errorf("gitops: откат сабмодуля %s%s: %w", prefix, sub.Path, err)
		}
		name := prefix + sub.Path
		deep, err := rollbackSubmodules(ctx, ex, subRoot, subSHA, name+"/")
		if err != nil {
			return done, err
		}
		done = append(done, name)
		done = append(done, deep...)
	}
	return done, nil
}

// resetWorktree возвращает рабочее дерево к коммиту и убирает неотслеживаемые
// файлы (`clean` без -x: игнорируемое вроде node_modules должно уцелеть).
func resetWorktree(ctx context.Context, ex Executor, root, sha string) error {
	if _, err := ex.Exec(ctx, root, "git", "reset", "--hard", sha); err != nil {
		return fmt.Errorf("gitops: reset --hard %s: %w", sha, err)
	}
	if _, err := ex.Exec(ctx, root, "git", "clean", "-fdq"); err != nil {
		return fmt.Errorf("gitops: очистка неотслеживаемых файлов: %w", err)
	}
	return nil
}

// subRevision возвращает коммит из gitlink'а сабмодуля path на коммите ref
// репозитория root (`git rev-parse <ref>:<path>`). Пустая строка — сабмодуль
// на этом коммите не зафиксирован или путь не разрешается (удалённый каталог).
func subRevision(ctx context.Context, ex Executor, root, ref, path string) string {
	if ex == nil || strings.TrimSpace(path) == "" {
		return ""
	}
	if strings.TrimSpace(ref) == "" {
		ref = "HEAD"
	}
	out, err := ex.Exec(ctx, root, "git", "rev-parse", ref+":"+path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
