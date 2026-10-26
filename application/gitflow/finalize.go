package gitflow

// Авто-шаги задачи при переводе в done (Ф-2/Ф-3), вынесенные из HTTP-слоя
// (`server/gitflow_auto.go`, Этап 6 «Интерфейсы и адаптеры»).
//
// Порядок шагов:
//  1. авто-коммит незакоммиченных изменений worktree в ветку задачи;
//  2. авто-MR задачи (если remote/фордж доступны и MR ещё не создан);
//  3. штатный мёрдж ветки задачи в релизную ветку эпика (MergeTaskBranch);
//  4. снятие worktree задачи.
//
// Авто-MR создаётся ДО мёрджа: после влития ветки между ней и релизной веткой
// не остаётся коммитов, и GitHub/GitLab отклоняют такой MR (422 «No commits
// between …»). Ошибки не ломают сам переход статуса — логируются, ручные
// кнопки остаются страховкой. Все побочные эффекты (git, доска, сессия, шина)
// подключаются портом TaskFinalizerHost.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ai/domain"
	"ai/shared/logging"
)

// ErrMergeConflict — мёрдж ветки задачи не прошёл из-за конфликта; список
// файлов — в MergeOutcome.ConflictFiles.
var ErrMergeConflict = errors.New("конфликт слияния")

// BranchRef — ветка/worktree задачи из side-реестра.
type BranchRef struct {
	Branch   string
	Base     string
	Worktree string
}

// MergeOutcome — результат мёрджа ветки задачи в релиз эпика.
type MergeOutcome struct {
	AlreadyMerged bool
	ConflictFiles []string
}

// Project — свойства проекта, влияющие на авто-шаги.
type Project struct {
	IsGit     bool
	HasRemote bool
}

// TaskFinalizerHost — операции хоста (реализуется сервером: git, реестр
// веток, доска, сессия, шина доски), нужные авто-шагам «задача → done».
type TaskFinalizerHost interface {
	// Project — git-проект? есть remote?
	Project() Project
	// TaskBranch — ветка/worktree задачи; ok=false — ветки нет.
	TaskBranch(taskID string) (BranchRef, bool)
	// CommitWorktree — авто-коммит изменений worktree задачи (+сабмодули).
	CommitWorktree(ctx context.Context, task *domain.Task, worktree string)
	// CreateTaskMR — авто-MR задачи; created=false — MR уже был или пропущен.
	CreateTaskMR(ctx context.Context, task *domain.Task) (url string, created bool, err error)
	// BranchAlreadyMerged — ветка задачи уже влита в релиз эпика (нет своих
	// коммитов); ok=false — влитие определить не удалось.
	BranchAlreadyMerged(ctx context.Context, epicID, taskBranch string) (merged, ok bool)
	// MergeTaskBranch — штатный мёрдж ветки задачи в релиз эпика. Конфликт —
	// ErrMergeConflict и MergeOutcome.ConflictFiles.
	MergeTaskBranch(ctx context.Context, task *domain.Task) (MergeOutcome, error)
	// ResolveTaskConflict — авторезолв конфликта через LLM (хост сам берёт
	// mergeLock проекта).
	ResolveTaskConflict(ctx context.Context, task *domain.Task, files []string) bool
	// SaveTask — запись задачи на доску.
	SaveTask(ctx context.Context, task *domain.Task) error
	// RemoveTaskWorktree — снятие worktree задачи.
	RemoveTaskWorktree(taskID, worktree string)
	// EmitBoard — сообщение на доску.
	EmitBoard(msg string)
	// AppendStatus — статусное сообщение в чат проекта.
	AppendStatus(msg string)
}

// TaskFinalizer выполняет авто-шаги завершения задачи.
type TaskFinalizer struct {
	Host TaskFinalizerHost
}

// Finalize — полный цикл авто-шагов при переводе задачи в done.
func (f *TaskFinalizer) Finalize(ctx context.Context, project string, task *domain.Task) {
	if task == nil || task.TaskID == "" {
		return
	}
	proj := f.Host.Project()
	if !proj.IsGit {
		return
	}
	taskRef, ok := f.Host.TaskBranch(task.TaskID)
	if !ok {
		logging.For(project).Detailf("gitflow: авто-шаги %s: ветки нет — пропуск", task.TaskID)
		return
	}
	worktree := strings.TrimSpace(taskRef.Worktree)

	// 1) Коммит в worktree ветки задачи (если специалист оставил изменения).
	if worktree != "" {
		f.Host.CommitWorktree(ctx, task, worktree)
	}

	// 2) Авто-MR задачи ДО мёрджа в релиз эпика: пока ветка задачи несёт свои
	// коммиты, фордж может открыть MR.
	if proj.HasRemote {
		if mrURL, created, err := f.Host.CreateTaskMR(ctx, task); err != nil {
			logging.For(project).Warnf("gitflow: авто-MR задачи %s: %v", task.TaskID, err)
			// Ошибка push/MR видна на доске: без неё коммиты задачи
			// остаются локальными, а пользователь видит только «done».
			f.Host.EmitBoard(GitflowErrBoard(fmt.Sprintf("авто-MR задачи %s не создан", task.TaskID), err))
		} else if created {
			logging.For(project).Infof("gitflow: задача %s → авто-MR %s", task.TaskID, mrURL)
		}
	}

	// 2.5) Задача без коммитов в ветке (например, QA-проверка) — уже влита в
	// релиз, мердж не нужен. Считаем смердженной, не блокируя процесс.
	if merged, ok := f.Host.BranchAlreadyMerged(ctx, task.EpicID, taskRef.Branch); ok && merged {
		logging.For(project).Infof("gitflow: задача %s: ветка уже влита в релиз (нет коммитов) — считаем смердженной", task.TaskID)
		if worktree != "" {
			f.Host.RemoveTaskWorktree(task.TaskID, worktree)
		}
		f.Host.EmitBoard("gitflow: задача без коммитов — смерджена")
		return
	}

	// 3) Штатный мёрдж done→релиз.
	outcome, err := f.Host.MergeTaskBranch(ctx, task)
	if err != nil {
		if errors.Is(err, ErrMergeConflict) {
			logging.For(project).Warnf("gitflow: авто-мёрдж %s: конфликт в %s — пробуем LLM-авторезолвинг",
				task.TaskID, strings.Join(outcome.ConflictFiles, ", "))
			if f.Host.ResolveTaskConflict(ctx, task, outcome.ConflictFiles) {
				logging.For(project).Infof("gitflow: авто-мёрдж %s: конфликт разрешён LLM", task.TaskID)
				f.clearConflict(ctx, project, task)
				if worktree != "" {
					f.Host.RemoveTaskWorktree(task.TaskID, worktree)
				}
				f.Host.EmitBoard("gitflow: авто-мёрдж задачи: конфликт разрешён LLM")
				return
			}
			// LLM не справился — задача НЕ считается выполненной: откат в
			// development, конфликт виден на доске, специалист решает сам.
			task.MergeConflictFiles = outcome.ConflictFiles
			task.Status = domain.StatusDevelopment
			if serr := f.Host.SaveTask(ctx, task); serr != nil {
				logging.For(project).Warnf("gitflow: авто-мёрдж %s: запись конфликта на доске: %v", task.TaskID, serr)
			}
			f.Host.AppendStatus(fmt.Sprintf("Задача %s: мёрдж в релиз эпика %s не прошёл — конфликт в файлах [%s]. Задача возвращена в development: реши конфликт в своей ветке и повтори мёрдж.",
				task.TaskID, task.EpicID, strings.Join(outcome.ConflictFiles, ", ")))
			// Конфликт не разрешён: задача откачена в development, worktree НЕ
			// снимаем — специалист решает конфликт в своей ветке.
			f.Host.EmitBoard("gitflow: авто-мёрдж задачи: конфликт, откат в development")
			return
		}
		logging.For(project).Warnf("gitflow: авто-мёрдж %s: %v", task.TaskID, err)
		// Worktree задачи снимаем в любом случае: правки уже в ветке
		// (авто-коммит выше), работа специалиста завершена.
		if worktree != "" {
			f.Host.RemoveTaskWorktree(task.TaskID, worktree)
		}
		f.Host.EmitBoard(GitflowErrBoard("авто-мёрдж задачи завершён с ошибкой", err))
		return
	}

	logging.For(project).Infof("gitflow: задача %s → done: авто-мёрдж в релиз эпика %s (already=%v)",
		task.TaskID, task.EpicID, outcome.AlreadyMerged)
	// Успешный мёрдж снимает признак конфликта с задачи (если он был).
	f.clearConflict(ctx, project, task)

	// 4) Снимаем worktree задачи.
	if worktree != "" {
		f.Host.RemoveTaskWorktree(task.TaskID, worktree)
	}
	f.Host.EmitBoard("gitflow: готовность задачи завершена (done → релиз)")
}

// clearConflict снимает признак конфликта с задачи и сохраняет её на доске.
func (f *TaskFinalizer) clearConflict(ctx context.Context, project string, task *domain.Task) {
	if len(task.MergeConflictFiles) == 0 {
		return
	}
	task.MergeConflictFiles = nil
	if err := f.Host.SaveTask(ctx, task); err != nil {
		logging.For(project).Warnf("gitflow: авто-мёрдж %s: очистка конфликта: %v", task.TaskID, err)
	}
}
