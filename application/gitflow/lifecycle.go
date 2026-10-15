// Жизненный цикл постоянных worktree задачи и эпика (Ф-3/4.1), вынесенный из
// HTTP-слоя (`server/gitflow_auto.go`, Этап 6 «Интерфейсы и адаптеры»).
//
// Здесь политика: когда и как создаётся worktree ветки задачи (синхрон релизной
// ветки эпика, rebase на эпик, подъём worktree сабмодулей) и ленивый worktree
// релизной ветки эпика под архитектора; когда worktree эпика снимается перед
// операциями, которым нужен свободный checkout релизной ветки. Сам git —
// infrastructure/git; доступ к реестру веток, доске, RAG-хукам и локу — через
// порт WorktreeHost.
package gitflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ai/domain"
	"ai/logging"
)

// WorktreeProject — срез info проекта, нужный жизненному циклу worktree.
type WorktreeProject struct {
	IsGit     bool
	Root      string
	GitBranch string
	GitBase   string
}

// WorktreeTask — задача, для которой создаётся worktree.
type WorktreeTask struct {
	ID     string
	EpicID string
}

// WorktreeHost — окружение менеджера worktree (реализуется сервером: реестр
// веток, git-репозиторий, RAG-хуки, merge-lock, доска).
type WorktreeHost interface {
	// Info — свойства проекта (git? root, ветка агента, база).
	Info(project string) (WorktreeProject, error)
	// Repo — рабочая копия основного клона проекта.
	Repo(ctx context.Context, project string) (WorktreeRepo, error)
	// EnsureSubmodules — поднять worktree сабмодулей в каталоге dir.
	EnsureSubmodules(ctx context.Context, dir string) ([]Submodule, error)
	// SubmoduleBase — SHA HEAD сабмодуля (rev-parse HEAD) в каталоге dir.
	SubmoduleBase(ctx context.Context, dir string) (string, error)
	// DeinitSubmodule — `git submodule deinit -f` сабмодуля path в каталоге dir.
	DeinitSubmodule(ctx context.Context, dir, path string) error
	// SubmoduleWorktree — worktree ветки branch (base) сабмодуля в каталоге.
	SubmoduleWorktree(ctx context.Context, dir, branch, base, dest string) error
	// CommitWorktree — авто-коммит правок рабочей копии dir под сообщением.
	CommitWorktree(ctx context.Context, dir, msg string) error
	// TaskBranch/EpicBranch — ветка/worktree из side-реестра.
	TaskBranch(project, taskID string) (BranchRef, error)
	EpicBranch(project, epicID string) (BranchRef, error)
	// SetTaskBranch/SetEpicBranch — запись ветки/worktree в side-реестр.
	SetTaskBranch(project, taskID string, ref BranchRef) error
	SetEpicBranch(project, epicID string, ref BranchRef) error
	// ChildProject — дочерний (сабмодульный) проект по пути сабмодуля;
	// ok=false — проекта нет или он не принадлежит project.
	ChildProject(project, subPath string) (name, root string, ok bool)
	// Locked — выполнить fn под пер-проектным merge-lock.
	Locked(project string, fn func())
	// RefreshRagIndexBranch — фоновая переиндексация ветки (main) по ref.
	RefreshRagIndexBranch(project, base string)
	// RefreshRagTaskBranch — фоновая индексация изменений ветки задачи.
	RefreshRagTaskBranch(project, branch, base string)
	// SyncEpicMainForTask — синхрон релизной ветки эпика с main перед задачей.
	SyncEpicMainForTask(ctx context.Context, project, epicID string)
	// InvalidateDiffs — сброс кэша диффов проекта.
	InvalidateDiffs(project string)
	// RecordTaskBaseSHA — точка отката «снести всё» (checkpoint.base_sha).
	RecordTaskBaseSHA(ctx context.Context, project, taskID, worktree string)
	// EmitBoard — сообщение на доску.
	EmitBoard(project, msg string)
}

// WorktreeManager — политика жизненного цикла worktree задачи и эпика.
type WorktreeManager struct {
	Host WorktreeHost
}

// TaskWorktree — Ф-3: при переводе задачи «в работу» создаёт постоянный worktree
// её ветки (ai/task/<id>), в который специалист получает OutputDir. Перед
// созданием: синхронизация релизной ветки эпика с main и rebase ветки задачи на
// эпик (задача стартует от свежего кода). Ошибки не ломают переход статуса —
// логируются (специалист продолжит работать в общем клоне, авто-коммит на done
// пропадёт).
func (m WorktreeManager) TaskWorktree(ctx context.Context, project string, t WorktreeTask) {
	if t.ID == "" {
		return
	}
	inf, err := m.Host.Info(project)
	if err != nil || !inf.IsGit {
		return
	}
	taskRef, err := m.Host.TaskBranch(project, t.ID)
	if err != nil {
		logging.For(project).Detailf("gitflow: worktree задачи %s: ветки нет: %v — пропуск", t.ID, err)
		return
	}
	if strings.TrimSpace(taskRef.Worktree) != "" {
		return // уже создан
	}
	repo, err := m.Host.Repo(ctx, project)
	if err != nil {
		return
	}
	wtPath := TaskWorktreePath(repo.Root(), project, t.ID)

	m.Host.Locked(project, func() {
		// 0) Актуальность main в индексе RAG: задача стартует, и её агент должен
		// искать по коду, который уже ушёл в main.
		m.Host.RefreshRagIndexBranch(project, inf.GitBase)

		// 0) Синхронизация релизной ветки эпика с main (без требования done):
		// эпик получает свежий main, задача стартует от актуального кода.
		// Конфликты main ↔ релизная ветка не блокируют старт — они видны на
		// доске (epic.MergeConflictFiles) и решаются флоу rebase/резолва.
		epicRef, err := m.Host.EpicBranch(project, t.EpicID)
		if err != nil {
			logging.For(project).Detailf("gitflow: worktree задачи %s: ветка эпика не найдена: %v — пропуск синхронизации", t.ID, err)
		} else {
			m.Host.SyncEpicMainForTask(ctx, project, t.EpicID)

			// 0.1) Rebase ветки задачи на релизную ветку эпика: если предыдущая
			// задача влита в релиз, текущая должна стартовать от свежего кода.
			// Конфликт rebase не блокирует старт — специалист видит его в
			// worktree. Перед rebase снимаем worktree основного клона, если он
			// держит ветку задачи (иначе git rebase откажется её перемещать).
			if wtList, err := repo.WorktreeList(ctx); err == nil {
				for _, wt := range wtList {
					if wt.Branch == taskRef.Branch {
						_ = repo.RemoveWorktree(ctx, wt.Path)
					}
				}
			}
			if err := repo.Rebase(ctx, taskRef.Branch, epicRef.Branch); err != nil {
				logging.For(project).Warnf("gitflow: rebase задачи %s на эпик %s: %v — специалист решит в worktree",
					t.ID, t.EpicID, err)
			}
		}

		// Чистим осиротевший worktree (прерванный прошлый цикл задачи).
		if _, err := os.Stat(wtPath); err == nil {
			logging.For(project).Warnf("gitflow: удаляю осиротевший worktree задачи %s (%s)", t.ID, wtPath)
			_ = repo.RemoveWorktree(ctx, wtPath)
		}
		// Основной клон может использовать ветку задачи (например, после
		// rebase) — переключаем на ветку агента, чтобы git worktree add сработал.
		if inf.GitBranch != "" {
			if err := repo.Checkout(ctx, inf.GitBranch); err != nil {
				logging.For(project).Warnf("gitflow: worktree задачи %s: checkout %s: %v", t.ID, inf.GitBranch, err)
			}
		}
		wt, err := repo.AddWorktree(ctx, wtPath, taskRef.Branch)
		if err != nil {
			logging.For(project).Warnf("gitflow: worktree задачи %s: %v", t.ID, err)
			return
		}
		EnsureTaskGitignore(project, t.ID, wtPath)
		if subs, err := m.Host.EnsureSubmodules(ctx, wt.Root()); err != nil {
			_ = wt.RemoveWorktree(ctx, wtPath)
			logging.For(project).Warnf("gitflow: submodule worktree задачи %s: %v", t.ID, err)
			return
		} else {
			for _, sub := range subs {
				childName, childRoot, ok := m.Host.ChildProject(project, sub.Path)
				if !ok {
					continue
				}
				base, err := m.Host.SubmoduleBase(ctx, sub.Root)
				if err != nil {
					_ = wt.RemoveWorktree(ctx, wtPath)
					logging.For(project).Warnf("gitflow: base сабмодуля %s задачи %s: %v", sub.Path, t.ID, err)
					return
				}
				branch := taskRef.Branch + "/submodule/" + domain.SanitizeBranchName(filepath.ToSlash(sub.Path))
				if err := m.Host.DeinitSubmodule(ctx, wt.Root(), sub.Path); err != nil {
					_ = wt.RemoveWorktree(ctx, wtPath)
					logging.For(project).Warnf("gitflow: deinit сабмодуля %s для task worktree: %v", sub.Path, err)
					return
				}
				if err := m.Host.SubmoduleWorktree(ctx, childRoot, branch, strings.TrimSpace(base), sub.Root); err != nil {
					_ = wt.RemoveWorktree(ctx, wtPath)
					logging.For(project).Warnf("gitflow: worktree сабмодуля %s задачи %s: %v", sub.Path, t.ID, err)
					return
				}
				EnsureTaskGitignore(project, t.ID, sub.Root)
				if err := m.Host.SetTaskBranch(childName, ChildTaskKey(project, t.ID), BranchRef{
					Branch: branch, Base: strings.TrimSpace(base), Worktree: sub.Root,
				}); err != nil {
					_ = wt.RemoveWorktree(ctx, wtPath)
					logging.For(project).Warnf("gitflow: регистрация submodule task branch %s: %v", sub.Path, err)
					return
				}
			}
		}
		if err := m.Host.SetTaskBranch(project, t.ID, BranchRef{
			Branch: taskRef.Branch, Base: taskRef.Base, Worktree: wtPath,
		}); err != nil {
			_ = wt.RemoveWorktree(ctx, wtPath)
			logging.For(project).Warnf("gitflow: worktree задачи %s: реестр: %v", t.ID, err)
			return
		}
		m.Host.InvalidateDiffs(project)
		// Ф-6 (этап 2): точка отката «снести всё» — HEAD нового worktree задачи.
		m.Host.RecordTaskBaseSHA(ctx, project, t.ID, wtPath)
		// Индекс ветки задачи: worktree только что пересобран (rebase на релизную
		// ветку эпика, эпик синхронизирован с main). Индексируем ИЗМЕНЕНИЯ ветки
		// относительно main — покрывает код самой задачи и слитых задач эпика.
		m.Host.RefreshRagTaskBranch(project, taskRef.Branch, inf.GitBase)
		logging.For(project).Infof("gitflow: задача %s → worktree %s (%s)", t.ID, wtPath, taskRef.Branch)
		m.Host.EmitBoard(project, "gitflow: worktree задачи")
	})
}

// EpicWorktree — Этап 4.1: ленивое создание (и повторное подключение) worktree
// релизной ветки эпика ai/epic/<id> — рабочей точки системного архитектора.
// Возвращает "" — degrade (не-git-проект, ветки нет, git-ошибка).
func (m WorktreeManager) EpicWorktree(ctx context.Context, project, epicID string) string {
	if strings.TrimSpace(epicID) == "" {
		return ""
	}
	inf, err := m.Host.Info(project)
	if err != nil || !inf.IsGit {
		return ""
	}
	ref, err := m.Host.EpicBranch(project, epicID)
	if err != nil || strings.TrimSpace(ref.Branch) == "" {
		logging.For(project).Detailf("gitflow: worktree эпика %s: ветки нет: %v — пропуск", epicID, err)
		return ""
	}
	if wt := strings.TrimSpace(ref.Worktree); wt != "" {
		if _, err := os.Stat(wt); err == nil {
			return wt
		}
	}
	repo, err := m.Host.Repo(ctx, project)
	if err != nil {
		return ""
	}
	wtPath := EpicWorktreePath(repo.Root(), project, epicID)

	result := ""
	// Под локом: параллельные merge-операции снимают worktree перед checkout
	// релизной ветки — создание и снятие не должны гоняться.
	m.Host.Locked(project, func() {
		// Повторная проверка: пока ждали мьютекс, worktree мог создать другой поток.
		if ref, err := m.Host.EpicBranch(project, epicID); err == nil {
			if wt := strings.TrimSpace(ref.Worktree); wt != "" {
				if _, err := os.Stat(wt); err == nil {
					result = wt
					return
				}
			}
		}
		// Осиротевший каталог прошлого (прерванного) цикла: снимаем и создаём заново.
		if _, err := os.Stat(wtPath); err == nil {
			logging.For(project).Warnf("gitflow: удаляю осиротевший worktree эпика %s (%s)", epicID, wtPath)
			_ = repo.RemoveWorktree(ctx, wtPath)
			// git worktree remove отвечает ошибкой, если каталог не является
			// worktree (ручной остаток) — убираем его, иначе worktree add упрётся
			// в «уже существует».
			_ = os.RemoveAll(wtPath)
		}
		// Основной клон может держать ветку эпика (после rebase/merge) —
		// переключаем его на ветку агента, иначе git worktree add откажется.
		if inf.GitBranch != "" {
			if err := repo.Checkout(ctx, inf.GitBranch); err != nil {
				logging.For(project).Warnf("gitflow: worktree эпика %s: checkout %s: %v", epicID, inf.GitBranch, err)
			}
		}
		wt, err := repo.AddWorktree(ctx, wtPath, ref.Branch)
		if err != nil {
			logging.For(project).Warnf("gitflow: worktree эпика %s: %v", epicID, err)
			return
		}
		EnsureTaskGitignore(project, epicID, wtPath)
		if err := m.Host.SetEpicBranch(project, epicID, BranchRef{
			Branch: ref.Branch, Base: ref.Base, Worktree: wtPath,
		}); err != nil {
			_ = wt.RemoveWorktree(ctx, wtPath)
			logging.For(project).Warnf("gitflow: worktree эпика %s: реестр: %v", epicID, err)
			return
		}
		logging.For(project).Infof("gitflow: эпик %s → worktree %s (%s)", epicID, wtPath, ref.Branch)
		result = wtPath
	})
	return result
}

// DropEpicWorktreeLocked снимает worktree ветки эпика (4.1), если он создан:
// операциям, которым нужен СВОБОДНЫЙ checkout релизной ветки, иначе git
// отказывается («ai/epic/… is already checked out at …»). Незакоммиченные
// правки архитектора фиксируются автокоммитом — структура проекта не теряется.
// Идемпотентно; вызывается ПОД mergeLock (свой лок здесь брать нельзя).
func (m WorktreeManager) DropEpicWorktreeLocked(ctx context.Context, project, epicID string) {
	if strings.TrimSpace(epicID) == "" {
		return
	}
	ref, err := m.Host.EpicBranch(project, epicID)
	if err != nil {
		return
	}
	wtPath := strings.TrimSpace(ref.Worktree)
	if wtPath == "" {
		return
	}
	if repo, rerr := m.Host.Repo(ctx, project); rerr == nil {
		if _, serr := os.Stat(wtPath); serr == nil {
			if cerr := m.Host.CommitWorktree(ctx, wtPath,
				fmt.Sprintf("эпик %s: структура проекта (авто-коммит)", epicID)); cerr != nil {
				logging.For(project).Warnf("gitflow: worktree эпика %s: автокоммит перед снятием: %v", epicID, cerr)
			}
			if werr := repo.RemoveWorktree(ctx, wtPath); werr != nil {
				logging.For(project).Warnf("gitflow: снятие worktree эпика %s: %v — удаляю каталог", epicID, werr)
				_ = os.RemoveAll(wtPath)
			}
		}
	} else {
		logging.For(project).Warnf("gitflow: worktree эпика %s: клон недоступен: %v", epicID, rerr)
	}
	if werr := m.Host.SetEpicBranch(project, epicID, BranchRef{
		Branch: ref.Branch, Base: ref.Base,
	}); werr != nil {
		logging.For(project).Warnf("gitflow: worktree эпика %s: запись в реестр: %v", epicID, werr)
	}
	logging.For(project).Detailf("gitflow: worktree эпика %s снят (%s)", epicID, wtPath)
}
