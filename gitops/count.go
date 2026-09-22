// Счётчик коммитов в ветке (Ф-2): `git rev-list --count <base>..<branch>`.
//
// Признак «в ветке есть коммиты» нужен git-статусу доски: ветка может быть
// создана (ai/epic/<id>/ai/task/<id>) без единого коммита (эпик/задача добавлены
// на доску, специалист ещё не работал) — «Создать MR» в этом случае бессмысленен
// и на фронте не показывается.
package gitops

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// CountCommits возвращает число коммитов ветки branch относительно базы base
// (`git rev-list --count <base>..<branch>`). 0 означает «в ветке нет своих
// коммитов»: точка отхода совпадает с вершиной ветки, сливать нечего.
func (r *Repo) CountCommits(ctx context.Context, base, branch string) (int, error) {
	if r == nil || r.Root == "" {
		return 0, fmt.Errorf("gitops: пустой Repo")
	}
	if strings.TrimSpace(base) == "" || strings.TrimSpace(branch) == "" {
		return 0, fmt.Errorf("gitops: требуются база и ветка")
	}
	out, err := r.ex.Exec(ctx, r.Root, "git", "rev-list", "--count", base+".."+branch)
	if err != nil {
		return 0, fmt.Errorf("gitops: git rev-list --count %s..%s: %w", base, branch, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("gitops: rev-list --count: нечисловой вывод %q: %w", out, err)
	}
	return n, nil
}