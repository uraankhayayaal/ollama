// Авто-шаги git-workflow (Ф-3/Ф-4): worktree задачи, авто-коммит, авто-MR,
// синхрон релизной ветки эпика с main. Выполняются серверными хуками доски
// (см. attachGitHooks) в фоне под пер-проектным mergeLock: не блокируют цикл
// оркестрации и не трогают рабочую копию основного клона.
package server

import (
	"ai/board"
	"ai/chat"
	"ai/gitops"
	"ai/logging"
	"ai/projects"
	"ai/workspace"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// taskWorktreePath возвращает каталог постоянного worktree ветки задачи —
// сосед основного клона (как MergeFeature/конфликтные worktree).
func (s *Server) taskWorktreePath(repoRoot, project, taskID string) string {
	return filepath.Join(filepath.Dir(repoRoot),
		".wt-task-"+project+"-"+gitops.SanitizeBranchName(taskID))
}

// taskGitignorePatterns — артефакты, которые не должны попасть в историю
// задачи. Промежуточные коммиты (Ф-6) делают `git add -A` на каждом раунде, а
// без списка он тащит в коммит всё, что появилось в рабочем дереве.
//
// `vendor/` в список НЕ входит: для Go его коммитят осознанно.
//
// Последние две группы добавлены после живого случая (mytrip, FEL-05): агент
// без npm в образе песочницы скачал Node в корень проекта, и `git add -A`
// закоммитил 46 МБ архива + 4287 файлов распакованного тулчейна.
//   - `*.tar.gz`/`*.tgz`/`*.zip`/`*.tar.*` — скачанные дистрибутивы;
//   - `node-v*/` — распакованный тулчейн Node (то, что агент распаковывает рядом
//     с архивом); версионный префикс отличает его от каталогов приложения.
var taskGitignorePatterns = []string{
	"node_modules/",
	"dist/",
	"build/",
	"__pycache__/",
	".venv/",
	"target/",
	"*.tsbuildinfo",
	"*.tar.gz",
	"*.tgz",
	"*.tar.xz",
	"*.tar.bz2",
	"*.zip",
	"node-v*/",
}

// taskGitignoreHeader — маркер нашего блока в .gitignore: по нему видно, что
// дописывала система, а что проект.
const taskGitignoreHeader = "# артефакты сборки и скачанные тулчейны (дописано оркестратором, Ф-6)"

// gitIgnoreFileName — имя служебного .gitignore оркестратора (используется
// гардом «phantom done»: он не считается доказательством работы агента).
const gitIgnoreFileName = ".gitignore"

// taskGitignore — полный файл для случая «.gitignore не было».
func taskGitignore() string {
	return taskGitignoreHeader + "\n" + strings.Join(taskGitignorePatterns, "\n") + "\n"
}

// ensureTaskGitignore создаёт .gitignore в корне worktree задачи (в т.ч.
// вложенного worktree сабмодуля) либо ДОПИСЫВАЕТ в существующий недостающие
// паттерны.
//
// Раньше файл создавался только когда его не было, и этого было мало: у
// mytrip .gitignore свой (frontend/node_modules/, pgdata/…) — корневого
// `node_modules/` в нём нет, а скачанный тулчейн под `node-v*/` не покрыт
// вообще. Дописывание ничего не удаляет и не переставляет: решения проекта
// остаются в силе, недостающие артефакты перестают попадать в `git add -A`.
// Идемпотентно (повторный вызов ничего не меняет). Файл коммитится вместе с
// веткой задачи — обычная правка, не хак.
func ensureTaskGitignore(project, taskID, root string) {
	if strings.TrimSpace(root) == "" {
		return
	}
	path := filepath.Join(root, gitIgnoreFileName)
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		logging.For(project).Warnf("gitflow: чтение .gitignore в worktree задачи %s: %v", taskID, err)
		return
	}
	existing := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
			existing[t] = true
		}
	}
	var missing []string
	for _, p := range taskGitignorePatterns {
		if !existing[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return
	}
	block := taskGitignoreHeader + "\n" + strings.Join(missing, "\n") + "\n"
	if len(raw) == 0 {
		if err := os.WriteFile(path, []byte(block), 0o644); err != nil {
			logging.For(project).Warnf("gitflow: .gitignore в worktree задачи %s: %v", taskID, err)
		}
		return
	}
	body := string(raw)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += "\n" + block
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		logging.For(project).Warnf("gitflow: дополнение .gitignore в worktree задачи %s: %v", taskID, err)
		return
	}
	logging.For(project).Infof("gitflow: задача %s: в .gitignore добавлены паттерны артефактов (было %d, добавил %d)",
		taskID, len(existing), len(missing))
}

// taskWorktree — Ф-3: при переводе задачи «в работу» создаёт постоянный
// worktree её ветки (ai/task/<id>), в который специалист получает OutputDir.
// Перед созданием worktree: синхронизация релизной ветки эпика с main и
// rebase ветки задачи на релизную ветку эпика (задача стартует от свежего
// кода). Ошибки не ломают переход статуса — логируются (специалист продолжит
// работать в общем клоне temp/<проект>, авто-коммит на done пропадёт).
func (s *Server) taskWorktree(ctx context.Context, project string, task *board.Task, store *board.Store) {
	if task == nil || task.TaskID == "" {
		return
	}
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return
	}
	taskRef, err := s.reg.TaskBranch(project, task.TaskID)
	if err != nil {
		logging.For(project).Detailf("gitflow: worktree задачи %s: ветки нет: %v — пропуск", task.TaskID, err)
		return
	}
	if strings.TrimSpace(taskRef.Worktree) != "" {
		return // уже создан
	}
	repo, err := s.repoOf(ctx, project)
	if err != nil {
		return
	}
	wtPath := s.taskWorktreePath(repo.Root, project, task.TaskID)

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	// 0) Актуальность main в индексе RAG: задача стартует, и её агент должен
	// искать по коду, который уже ушёл в main (релизы прошлых эпиков, в т.ч.
	// сделанные мимо releaseEpic — с пуша). Индексация по ref читает git, а не
	// рабочую копию, и выполняется в фоне (см. server/ragref.go).
	s.refreshRagIndexBranch(project, inf.GitBase)

	// 0) Синхронизация релизной ветки эпика с main (как перед релизом, но без
	// требования done): эпик получает свежий main, задача стартует от актуального
	// кода. Конфликты main ↔ релизная ветка не блокируют старт задачи — они
	// видны на доске (epic.MergeConflictFiles) и решаются флоу rebase/резолва.
	epicRef, err := s.reg.EpicBranch(project, task.EpicID)
	if err != nil {
		logging.For(project).Detailf("gitflow: worktree задачи %s: ветка эпика не найдена: %v — пропуск синхронизации", task.TaskID, err)
	} else {
		s.syncEpicMainForTask(ctx, project, task.EpicID, inf, repo, epicRef)

		// 0.1) Rebase ветки задачи на релизную ветку эпика: если предыдущая задача
		// влита в релиз, текущая задача должна стартовать от свежего кода.
		// Конфликт rebase не блокирует старт — специалист видит его в worktree.
		// Перед rebase снимаем worktree основного клона (если ветка задачи
		// используется им) — иначе git rebase откажется перемещать ветку.
		if wtList, err := repo.WorktreeList(ctx); err == nil {
			for _, wt := range wtList {
				if wt.Branch == taskRef.Branch {
					_ = repo.RemoveWorktree(ctx, wt.Path)
				}
			}
		}
		if err := repo.Rebase(ctx, taskRef.Branch, epicRef.Branch); err != nil {
			logging.For(project).Warnf("gitflow: rebase задачи %s на эпик %s: %v — специалист решит в worktree",
				task.TaskID, task.EpicID, err)
		}
	}

	// Чистим осиротевший worktree (прерванный прошлый цикл задачи).
	if _, err := os.Stat(wtPath); err == nil {
		logging.For(project).Warnf("gitflow: 删除я осиротевший worktree задачи %s (%s)", task.TaskID, wtPath)
		_ = repo.RemoveWorktree(ctx, wtPath)
	}
	// Основной клон может использовать ветку задачи (например, после rebase).
	// Переключаем его на ветку агента, чтобы git worktree add сработал.
	if inf.GitBranch != "" {
		if err := repo.Checkout(ctx, inf.GitBranch); err != nil {
			logging.For(project).Warnf("gitflow: worktree задачи %s: checkout %s: %v", task.TaskID, inf.GitBranch, err)
		}
	}
	wt, err := repo.AddWorktree(ctx, wtPath, taskRef.Branch)
	if err != nil {
		logging.For(project).Warnf("gitflow: worktree задачи %s: %v", task.TaskID, err)
		return
	}
	ensureTaskGitignore(project, task.TaskID, wtPath)
	if subs, err := gitops.EnsureSubmodules(ctx, s.gitExec, wt.Root); err != nil {
		_ = wt.RemoveWorktree(ctx, wtPath)
		logging.For(project).Warnf("gitflow: submodule worktree задачи %s: %v", task.TaskID, err)
		return
	} else {
		for _, sub := range subs {
			childName := submoduleProjectName(project, sub.Path)
			child, err := s.reg.Get(childName)
			if err != nil || child.Parent != project {
				continue
			}
			base, err := s.gitExec.Exec(ctx, sub.Root, "git", "rev-parse", "HEAD")
			if err != nil {
				_ = wt.RemoveWorktree(ctx, wtPath)
				logging.For(project).Warnf("gitflow: base сабмодуля %s задачи %s: %v", sub.Path, task.TaskID, err)
				return
			}
			branch := taskRef.Branch + "/submodule/" + gitops.SanitizeBranchName(filepath.ToSlash(sub.Path))
			if _, err := s.gitExec.Exec(ctx, wt.Root, "git", "submodule", "deinit", "-f", "--", sub.Path); err != nil {
				_ = wt.RemoveWorktree(ctx, wtPath)
				logging.For(project).Warnf("gitflow: deinit сабмодуля %s для task worktree: %v", sub.Path, err)
				return
			}
			if _, err := gitops.Worktree(ctx, s.gitExec, child.Root, branch, strings.TrimSpace(base), sub.Root); err != nil {
				_ = wt.RemoveWorktree(ctx, wtPath)
				logging.For(project).Warnf("gitflow: worktree сабмодуля %s задачи %s: %v", sub.Path, task.TaskID, err)
				return
			}
			ensureTaskGitignore(project, task.TaskID, sub.Root)
			if err := s.reg.SetTaskBranch(childName, childTaskKey(project, task.TaskID), workspace.BranchRef{
				Branch: branch, Base: strings.TrimSpace(base), Worktree: sub.Root,
			}); err != nil {
				_ = wt.RemoveWorktree(ctx, wtPath)
				logging.For(project).Warnf("gitflow: регистрация submodule task branch %s: %v", sub.Path, err)
				return
			}
		}
	}
	if err := s.reg.SetTaskBranch(project, task.TaskID, workspace.BranchRef{
		Branch: taskRef.Branch, Base: taskRef.Base, Worktree: wtPath,
	}); err != nil {
		_ = wt.RemoveWorktree(ctx, wtPath)
		logging.For(project).Warnf("gitflow: worktree задачи %s: реестр: %v", task.TaskID, err)
		return
	}
	s.invalidateDiffs(project)
	// Ф-6 (этап 2): точка отката «снести всё» — HEAD только что созданного
	// worktree задачи.
	s.recordTaskBaseSHA(ctx, project, task.TaskID, wtPath, store)
	// Индекс ветки задачи: worktree только что пересобран (rebase на релизную
	// ветку эпика, а эпик синхронизирован с main), а точки ветки задачи в
	// индексе ещё нет. Индексируем ИЗМЕНЕНИЯ ветки относительно main (остальное
	// покрывает индекс main) — дёшево и ровно то, что нужно выдаче поиска:
	// код самой задачи и слитых задач эпика. Без этого CodeSearch специалиста
	// видел бы только main и не нашёл бы код, собранный задачами эпика.
	s.refreshRagTaskBranch(project, taskRef.Branch, inf.GitBase)
	logging.For(project).Infof("gitflow: задача %s → worktree %s (%s)", task.TaskID, wtPath, taskRef.Branch)
	s.srvEmitBoard(project, "gitflow: worktree задачи")
}

// recordTaskBaseSHA запоминает HEAD worktree задачи как checkpoint.base_sha —
// точку ручного отката «снести всё» (Ф-6, этап 2). Значение не перетирается,
// если уже есть: база задачи — это код на момент её старта, а не текущий HEAD
// очередного прогона (после отката кода и повторного запуска).
func (s *Server) recordTaskBaseSHA(ctx context.Context, project, taskID, worktree string, store *board.Store) {
	if store == nil {
		return
	}
	head, err := gitops.RepoFromState(s.gitExec, worktree, "", "", "").HeadSHA(ctx)
	if err != nil {
		logging.For(project).Detailf("gitflow: base_sha задачи %s: %v", taskID, err)
		return
	}
	if err := store.PatchTask(ctx, taskID, func(t *board.Task) error {
		if t.Checkpoint == nil {
			t.Checkpoint = &board.TaskCheckpoint{}
		}
		if t.Checkpoint.BaseSHA == "" {
			t.Checkpoint.BaseSHA = head
		}
		return nil
	}); err != nil {
		logging.For(project).Warnf("gitflow: запись base_sha задачи %s: %v", taskID, err)
	}
}

// taskOutputDir возвращает каталог работы специалиста по задаче: для
// git-проектов — worktree ветки задачи (Ф-3), если он создан; иначе проектная
// копия temp/<проект>. Используется как хук SetOutputDir в KanbanRunner.
func (s *Server) taskOutputDir(project, taskID string) string {
	if ref, err := s.reg.TaskBranch(project, taskID); err == nil {
		if wt := strings.TrimSpace(ref.Worktree); wt != "" {
			return wt
		}
	}
	return projects.ProjectDir(project)
}

// epicWorktreePath — каталог постоянного worktree ветки эпика (4.1): сосед
// основного клона, как worktree задачи.
func (s *Server) epicWorktreePath(repoRoot, project, epicID string) string {
	return filepath.Join(filepath.Dir(repoRoot),
		".wt-epic-"+project+"-"+gitops.SanitizeBranchName(epicID))
}

// epicWorktree — Этап 4.1: ленивое создание (и повторное подключение)
// worktree релизной ветки эпика ai/epic/<id> — рабочей точки системного
// архитектора: он выстраивает структуру проекта 1-го уровня и коммитит её
// в ветку эпика (см. «ВЕТКА ЭПИКА И СТРУКТУРА ПРОЕКТА» в его промпте).
//
// Резолвер задаётся KanbanRunner через SetEpicOutputDir и вызывается
// архитектором ВНУТРИ submit_architecture_backlog — после публикации бэклога,
// когда EpicCreatedHook уже создал ветки эпиков. Возвращает "" — degrade
// (не-git-проект, ветки нет, git-ошибка): архитектор получит note и структуру
// не напишет, публикация бэклога при этом не срывается.
func (s *Server) epicWorktree(ctx context.Context, project, epicID string) string {
	if strings.TrimSpace(epicID) == "" {
		return ""
	}
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return ""
	}
	ref, err := s.reg.EpicBranch(project, epicID)
	if err != nil || strings.TrimSpace(ref.Branch) == "" {
		logging.For(project).Detailf("gitflow: worktree эпика %s: ветки нет: %v — пропуск", epicID, err)
		return ""
	}
	if wt := strings.TrimSpace(ref.Worktree); wt != "" {
		if _, err := os.Stat(wt); err == nil {
			return wt
		}
	}
	repo, err := s.repoOf(ctx, project)
	if err != nil {
		return ""
	}
	wtPath := s.epicWorktreePath(repo.Root, project, epicID)

	// Под локом: параллельные merge-операции снимают worktree перед checkout
	// релизной ветки — создание и снятие не должны гоняться.
	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	// Повторная проверка: пока ждали мьютекс, worktree мог создать другой поток.
	if ref, err := s.reg.EpicBranch(project, epicID); err == nil {
		if wt := strings.TrimSpace(ref.Worktree); wt != "" {
			if _, err := os.Stat(wt); err == nil {
				return wt
			}
		}
	}
	// Осиротевший каталог прошлого (прерванного) цикла: снимаем и создаём заново.
	if _, err := os.Stat(wtPath); err == nil {
		logging.For(project).Warnf("gitflow: удаляю осиротевший worktree эпика %s (%s)", epicID, wtPath)
		_ = repo.RemoveWorktree(ctx, wtPath)
		// git worktree remove отвечает ошибкой, если каталог не является
		// worktree (ручной остаток) — убираем его иначе worktree add упрётся
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
		return ""
	}
	ensureTaskGitignore(project, epicID, wtPath)
	if err := s.reg.SetEpicBranch(project, epicID, workspace.BranchRef{
		Branch: ref.Branch, Base: ref.Base, Worktree: wtPath,
	}); err != nil {
		_ = wt.RemoveWorktree(ctx, wtPath)
		logging.For(project).Warnf("gitflow: worktree эпика %s: реестр: %v", epicID, err)
		return ""
	}
	logging.For(project).Infof("gitflow: эпик %s → worktree %s (%s)", epicID, wtPath, ref.Branch)
	return wtPath
}

// dropEpicWorktreeLocked снимает worktree ветки эпика (4.1), если он создан:
// операциям, которым нужен СВОБОДНЫЙ checkout релизной ветки (MergeFeature и
// конфликтные worktree резолвов делают `git worktree add <путь> <ветка>`),
// иначе git отказывается: «ai/epic/… is already checked out at …».
// Незакоммиченные правки архитектора (промпт требует коммитить, но модель могла
// не успеть) фиксируются автокоммитом — структура проекта не теряется.
// Идемпотентно; вызывается ПОД mergeLock (свой лок здесь брать нельзя).
func (s *Server) dropEpicWorktreeLocked(ctx context.Context, project, epicID string) {
	if strings.TrimSpace(epicID) == "" {
		return
	}
	ref, err := s.reg.EpicBranch(project, epicID)
	if err != nil {
		return
	}
	wtPath := strings.TrimSpace(ref.Worktree)
	if wtPath == "" {
		return
	}
	if repo, rerr := s.repoOf(ctx, project); rerr == nil {
		if _, serr := os.Stat(wtPath); serr == nil {
			wt := gitops.RepoFromState(s.gitExec, wtPath, ref.Branch, ref.Base, "")
			if _, _, cerr := wt.CommitIfDirty(ctx,
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
	if werr := s.reg.SetEpicBranch(project, epicID, workspace.BranchRef{
		Branch: ref.Branch, Base: ref.Base,
	}); werr != nil {
		logging.For(project).Warnf("gitflow: worktree эпика %s: запись в реестр: %v", epicID, werr)
	}
	logging.For(project).Detailf("gitflow: worktree эпика %s снят (%s)", epicID, wtPath)
}

// commitTaskWorktree фиксирует незакоммиченные изменения специалиста в
// worktree ветки задачи (git add -A + commit) и публикует ветки worktree'ов
// сабмодулей (merge в базовую ветку сабмодуля + push + обновление gitlink).
// С Ф-6 промежуточными коммитами дерево на `done` обычно ЧИСТОЕ — тогда коммита
// нет (пустой коммит не создаём), но публикация веток сабмодулей всё равно
// выполняется. Ошибки логируются.
func (s *Server) commitTaskWorktree(ctx context.Context, project string, task *board.Task, worktree string) {
	wt := gitops.RepoFromState(s.gitExec, worktree, "", "", "")
	message := fmt.Sprintf("задача %s: работа специалиста (авто-коммит)", task.TaskID)
	subs, err := gitops.ListSubmodules(ctx, s.gitExec, worktree)
	if err != nil {
		logging.For(project).Warnf("gitflow: чтение сабмодулей worktree задачи %s: %v", task.TaskID, err)
		return
	}
	for _, sub := range subs {
		childName := submoduleProjectName(project, sub.Path)
		child, err := s.reg.Get(childName)
		if err != nil || child.Parent != project {
			continue
		}
		ref, err := s.reg.TaskBranch(childName, childTaskKey(project, task.TaskID))
		if err != nil {
			continue
		}
		subRoot := filepath.Join(worktree, sub.Path)
		taskRepo := gitops.RepoFromState(s.gitExec, subRoot, child.GitRemote, ref.Branch, ref.Base)
		dirty, err := taskRepo.Dirty(ctx)
		if err != nil {
			logging.For(project).Warnf("gitflow: status сабмодуля %s: %v", sub.Path, err)
			return
		}
		// Коммит — только по грязному дереву. А вот публикация ветки сабмодуля
		// (merge в его базовую ветку + push + обновление gitlink) нужна ВСЕГДА:
		// промежуточные коммиты (Ф-6) оставляют worktree сабмодуля чистым, хотя
		// ветка задачи уехала вперёд. Раньше чистое дерево давало continue, и
		// gitlink родителя указывал бы на коммит, которого нет в origin
		// сабмодуля, — MR с битым сабмодулем.
		if dirty {
			if err := taskRepo.Commit(ctx, message); err != nil {
				logging.For(project).Warnf("gitflow: commit сабмодуля %s: %v", sub.Path, err)
				return
			}
			logging.For(project).Infof("gitflow: задача %s: авто-коммит сабмодуля %s в %s", task.TaskID, sub.Path, subRoot)
		}
		if child.GitBranch != "" && ref.Branch != child.GitBranch {
			childRepo := gitops.RepoFromState(s.gitExec, child.Root, child.GitRemote, child.GitBranch, child.GitBase)
			if _, err := childRepo.MergeFeature(ctx, child.GitBranch, ref.Branch, gitops.MergeFeatureOptions{Message: message}); err != nil {
				logging.For(project).Warnf("gitflow: merge сабмодуля %s в %s: %v", sub.Path, child.GitBranch, err)
				return
			}
			if _, err := s.gitExec.Exec(ctx, child.Root, "git", "checkout", child.GitBranch); err != nil {
				logging.For(project).Warnf("gitflow: checkout feature branch сабмодуля %s: %v", sub.Path, err)
				return
			}
			if err := s.pushRepo(ctx, childRepo, child.GitRemote); err != nil {
				logging.For(project).Warnf("gitflow: push сабмодуля %s: %v", sub.Path, err)
				return
			}
			if _, err := s.gitExec.Exec(ctx, subRoot, "git", "reset", "--hard", child.GitBranch); err != nil {
				logging.For(project).Warnf("gitflow: обновление gitlink сабмодуля %s: %v", sub.Path, err)
				return
			}
		}
	}
	dirty, err := wt.Dirty(ctx)
	if err != nil || !dirty {
		return
	}
	if err := wt.Commit(ctx, message); err != nil {
		logging.For(project).Warnf("gitflow: авто-коммит задачи %s в %s: %v", task.TaskID, worktree, err)
		return
	}
	logging.For(project).Infof("gitflow: задача %s: авто-коммит в worktree %s", task.TaskID, worktree)
}

// removeTaskWorktree снимает worktree задачи (`git worktree remove --force` +
// удаление каталога) и очищает Worktree в side-реестре. Пустой worktree —
// no-op. Ошибки логируются (осиротевший каталог уберёт следующий цикл).
func (s *Server) removeTaskWorktree(project, taskID, worktree string) {
	if strings.TrimSpace(worktree) == "" {
		return
	}
	for _, child := range s.repositoriesForProject(project) {
		if child == project {
			continue
		}
		key := childTaskKey(project, taskID)
		if ref, err := s.reg.TaskBranch(child, key); err == nil && ref.Worktree != "" {
			if repo, err := s.repoOf(context.Background(), child); err == nil {
				_ = repo.RemoveWorktree(context.Background(), ref.Worktree)
			}
			_ = s.reg.DeleteTaskBranch(child, key)
			s.invalidateDiffs(child)
		}
	}
	if repo, err := s.repoOf(context.Background(), project); err == nil {
		if err := repo.RemoveWorktree(context.Background(), worktree); err != nil {
			logging.For(project).Warnf("gitflow: снятие worktree задачи %s: %v", taskID, err)
		}
	}
	if ref, err := s.reg.TaskBranch(project, taskID); err == nil {
		_ = s.reg.SetTaskBranch(project, taskID, workspace.BranchRef{Branch: ref.Branch, Base: ref.Base})
	}
	s.invalidateDiffs(project)
}

func childTaskKey(project, taskID string) string { return project + "::" + taskID }

// gitflowErrBoard собирает однострочное сообщение об ошибке git-шага для
// доски: при отказе GitHub по scope workflow — готовая инструкция (секреты
// скрыты, многострочный вывод git не тащится), иначе — первая строка ошибки,
// сжатая до 200 символов. Исходная причина целиком остаётся в логе проекта.
func gitflowErrBoard(prefix string, err error) string {
	if errors.Is(err, gitops.ErrWorkflowScope) {
		return prefix + ": " + gitops.ErrWorkflowScope.Error()
	}
	msg := gitops.RedactSecrets(err.Error())
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	return prefix + ": " + msg
}

// autoCommitAndMergeTask — Ф-2/Ф-3: авто-действия при переводе задачи в done:
//
//  1. авто-коммит незакоммиченных изменений worktree в ветку задачи;
//  2. авто-MR задачи (если remote/фордж доступны и MR ещё не создан);
//  3. штатный мёрдж ветки задачи в релизную ветку эпика (mergeTaskBranch);
//  4. снятие worktree задачи.
//
// Авто-MR создаётся ДО мёрджа в релиз эпика: после влития ветки между ней и
// релизной веткой не остаётся коммитов, и GitHub/GitLab отклоняют такой MR
// (422 «No commits between …»). Ветку задачи и базу MR пушит сам
// createTaskMROnce (ensureRemoteBase), поэтому порядок выполнения безопасен.
//
// Ошибки не ломают сам переход статуса (хук) — логируются, ручные кнопки
// остаются страховкой.
func (s *Server) autoCommitAndMergeTask(ctx context.Context, project string, task *board.Task, store *board.Store) {
	if task == nil || task.TaskID == "" {
		return
	}
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return
	}
	taskRef, err := s.reg.TaskBranch(project, task.TaskID)
	if err != nil {
		logging.For(project).Detailf("gitflow: авто-шаги %s: ветки нет: %v — пропуск", task.TaskID, err)
		return
	}

	// 1) Коммит в worktree ветки задачи (если специалист оставил изменения).
	worktree := strings.TrimSpace(taskRef.Worktree)
	if worktree != "" {
		s.commitTaskWorktree(ctx, project, task, worktree)
	}

	// 2) Авто-MR задачи ДО мёрджа в релиз эпика (см. комментарий функции):
	// пока ветка задачи несёт свои коммиты, фордж может открыть MR.
	if inf.GitRemote != "" {
		if mrURL, created, merr := s.createTaskMROnce(ctx, project, task.TaskID, task); merr != nil {
			logging.For(project).Warnf("gitflow: авто-MR задачи %s: %v", task.TaskID, merr)
			// Ошибка push/MR видна на доске: без неё коммиты задачи
			// остаются локальными, а пользователь видит только «done».
			s.srvEmitBoard(project, gitflowErrBoard(fmt.Sprintf("авто-MR задачи %s не создан", task.TaskID), merr))
		} else if created {
			logging.For(project).Infof("gitflow: задача %s → авто-MR %s", task.TaskID, mrURL)
		}
	}

	// 2.5) Задача без коммитов в ветке (например, QA-проверка) — уже влита
	// в релиз, мердж не нужен. Считаем смердженной, не блокируя процесс.
	epicRef, err := s.reg.EpicBranch(project, task.EpicID)
	if err == nil {
		if repo, rerr := s.repoOf(ctx, project); rerr == nil {
			if merged, _ := repo.MergedInto(ctx, epicRef.Branch, taskRef.Branch); merged {
				logging.For(project).Infof("gitflow: задача %s: ветка уже влита в релиз (нет коммитов) — считаем смердженной", task.TaskID)
				if worktree != "" {
					s.removeTaskWorktree(project, task.TaskID, worktree)
				}
				s.srvEmitBoard(project, "gitflow: задача без коммитов — смерджена")
				return
			}
		}
	}

	// 3) Штатный мёрдж done→релиз.
	res, err := s.mergeTaskBranch(ctx, project, task)
	if err != nil {
		var ce *gitops.MergeConflictError
		if errors.As(err, &ce) {
			logging.For(project).Warnf("gitflow: авто-мёрдж %s: конфликт в %s — пробуем LLM-авторезолвинг",
				task.TaskID, strings.Join(ce.Files, ", "))
			// Попытка авторезолвина через LLM: worktree релизной ветки + merge задачи.
			// LLM-резолв живёт под mergeLock (как в handleTaskLLMResolve): внутри он
			// снимает worktree эпика (4.1) перед checkout релизной ветки.
			lock := s.mergeLock(project)
			lock.Lock()
			resolved := s.tryTaskLLMResolve(ctx, project, task, ce.Files)
			lock.Unlock()
			if resolved {
				logging.For(project).Infof("gitflow: авто-мёрдж %s: конфликт разрешён LLM", task.TaskID)
				if len(task.MergeConflictFiles) > 0 {
					task.MergeConflictFiles = nil
					if serr := store.SaveTask(ctx, task); serr != nil {
						logging.For(project).Warnf("gitflow: авто-мёрдж %s: очистка конфликта: %v", task.TaskID, serr)
					}
				}
				if worktree != "" {
					s.removeTaskWorktree(project, task.TaskID, worktree)
				}
				s.srvEmitBoard(project, "gitflow: авто-мёрдж задачи: конфликт разрешён LLM")
				return
			}
			// LLM не справился — задача НЕ считается выполненной: откат в in_progress,
			// конфликт виден на доске, специалист решает сам.
			task.MergeConflictFiles = ce.Files
			task.Status = board.StatusInProgress
			if serr := store.SaveTask(ctx, task); serr != nil {
				logging.For(project).Warnf("gitflow: авто-мёрдж %s: запись конфликта на доске: %v", task.TaskID, serr)
			}
			if sess := s.session(project); sess != nil {
				sess.append(chat.RoleStatus,
					fmt.Sprintf("Задача %s: мёрдж в релиз эпика %s не прошёл — конфликт в файлах [%s]. Задача возвращена в in_progress: реши конфликт в своей ветке и повтори мёрдж.",
						task.TaskID, task.EpicID, strings.Join(ce.Files, ", ")), "", "", nil)
			}
		} else {
			logging.For(project).Warnf("gitflow: авто-мёрдж %s: %v", task.TaskID, err)
			// Worktree задачи снимаем в любом случае: правки уже в ветке
			// (авто-коммит выше), работа специалиста завершена.
			if worktree != "" {
				s.removeTaskWorktree(project, task.TaskID, worktree)
			}
			s.srvEmitBoard(project, gitflowErrBoard("авто-мёрдж задачи завершён с ошибкой", err))
			return
		}
		// Конфликт не разрешён: задача откачена в in_progress, worktree НЕ
		// снимаем — специалист решает конфликт в своей ветке и повторяет мёрдж.
		s.srvEmitBoard(project, "gitflow: авто-мёрдж задачи: конфликт, откат в in_progress")
		return
	}
	logging.For(project).Infof("gitflow: задача %s → done: авто-мёрдж в релиз эпика %s (already=%v)",
		task.TaskID, task.EpicID, res.AlreadyMerged)
	// Успешный мёрдж снимает признак конфликта с задачи (если он был).
	if len(task.MergeConflictFiles) > 0 {
		task.MergeConflictFiles = nil
		if serr := store.SaveTask(ctx, task); serr != nil {
			logging.For(project).Warnf("gitflow: авто-мёрдж %s: очистка конфликта: %v", task.TaskID, serr)
		}
	}

	// 4) Снимаем worktree задачи.
	if worktree != "" {
		s.removeTaskWorktree(project, task.TaskID, worktree)
	}
	s.srvEmitBoard(project, "gitflow: готовность задачи завершена (done → релиз)")
}

// syncEpicWithMain — Ф-4: авто-синхрон релизной ветки эпика с main. Запускается
// серверным хук'ом при переводе эпика в done, выполняется в фоне (не блокирует
// цикл оркестрации и статусный переход). После успешного синхрона клик
// «Залить в main» проходит без конфликтов («быстрый путь» handleReleaseEpic).
func (s *Server) syncEpicWithMain(ctx context.Context, project string, epic *board.Epic) {
	go func() {
		ctxi, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		s.syncEpicMainOnce(ctxi, project, epic)
	}()
}

// syncEpicMainOnce — собственно синхрон (под mergeLock). Если конфликтов с main
// нет — main вливается в релизную ветку быстрым путём (MergeFeature). Если есть
// — авто-резолв тривиальных блоков; сложные конфликты НЕ продвигают ветку,
// оставляя рабочую поверхность флоу rebase/резолва (интерактивному процессу
// ничего не ломаем).
func (s *Server) syncEpicMainOnce(ctx context.Context, project string, epic *board.Epic) {
	if epic == nil || epic.TaskID == "" {
		return
	}
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return
	}
	main := strings.TrimSpace(inf.GitBase)
	if main == "" {
		return
	}
	epicRef, err := s.reg.EpicBranch(project, epic.TaskID)
	if err != nil {
		return
	}
	repo, err := s.repoOf(ctx, project)
	if err != nil {
		return
	}

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	// 4.1: worktree ветки эпика (архитектор) держит релизную ветку
	// checked-out — MergeFeature не смог бы поставить её во временный worktree.
	s.dropEpicWorktreeLocked(ctx, project, epic.TaskID)

	res, err := repo.MergeFeature(ctx, epicRef.Branch, main, gitops.MergeFeatureOptions{
		Message: fmt.Sprintf("эпик %s: синхрон релизной ветки %s с main", epic.TaskID, epicRef.Branch),
		PushURL: remotePushURL(inf),
	})
	if err == nil {
		logging.For(project).Infof("gitflow: эпик %s: авто-синхрон с main (already=%v)", epic.TaskID, res.AlreadyMerged)
		s.clearEpicMergeConflict(ctx, project, epic.TaskID)
		s.srvEmitBoard(project, "gitflow: авто-синхрон эпика с main")
		return
	}
	var ce *gitops.MergeConflictError
	if !errors.As(err, &ce) {
		if errors.Is(err, context.Canceled) {
			return
		}
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: %v", epic.TaskID, err)
		return
	}
	s.autoResolveMainSync(ctx, project, epic, inf, repo, epicRef)
}

// autoResolveMainSync — продолжение авто-синхрона при конфликтах: настоящий
// merge main в постоянном worktree релизной ветки, авто-резолв тривиальных
// блоков, закрытие merge-коммита и push (всё на релизной ветке эпика). Сложные
// конфликты — отказ от продвижения: ветка остаётся нетронутой, работает
// интерактивный флоу handleEpicRebase/handleEpicResolve.
func (s *Server) autoResolveMainSync(ctx context.Context, project string, epic *board.Epic, inf workspace.Info, repo *gitops.Repo, epicRef workspace.BranchRef) {
	wtPath := filepath.Join(filepath.Dir(repo.Root),
		".conflict-"+project+"-"+gitops.SanitizeBranchName(epic.TaskID))
	if _, err := os.Stat(wtPath); err == nil {
		logging.For(project).Warnf("gitflow: удаляю осиротевший конфликтный worktree %s", wtPath)
		_ = repo.RemoveWorktree(ctx, wtPath)
	}
	// 4.1: релизную ветку эпика перед checkout освобождаем от worktree
	// архитектора (см. dropEpicWorktreeLocked).
	s.dropEpicWorktreeLocked(ctx, project, epic.TaskID)
	wt, err := repo.AddWorktree(ctx, wtPath, epicRef.Branch)
	if err != nil {
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: worktree: %v", epic.TaskID, err)
		return
	}
	removeWT := func() { _ = repo.RemoveWorktree(ctx, wtPath) }

	_, _ = wt.MergeBranch(ctx, inf.GitBase,
		fmt.Sprintf("эпик %s: влитие main (авто-резолв)", epic.TaskID))

	unmerged, err := wt.UnmergedFiles(ctx)
	if err != nil {
		removeWT()
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: ls-files -u: %v", epic.TaskID, err)
		return
	}
	resolved, hard, err := gitops.TrivialResolve(wtPath, unmerged)
	if err != nil {
		removeWT()
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: авто-резолв: %v", epic.TaskID, err)
		return
	}
	if len(hard) > 0 {
		// Попытка авторезолвина через LLM: контекст эпика + задачи + файлы.
		if s.attemptLLMResolve(ctx, project, epic, wtPath, hard, inf, epicRef.Branch, epic.AssignedRole) {
			removeWT()
			s.clearEpicMergeConflict(ctx, project, epic.TaskID)
			logging.For(project).Infof("gitflow: авто-синхрон эпика %s: сложные конфликты разрешены LLM", epic.TaskID)
			s.srvEmitBoard(project, "gitflow: авто-синхрон эпика: конфликты разрешены LLM")
			return
		}
		// LLM не справился — релизную ветку не трогаем, доступен ручной/модельный rebase + резолв + release (Ф-4).
		removeWT()
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: сложные конфликты [%s] — флоу rebase/резолв",
			epic.TaskID, strings.Join(hard, ", "))
		// Помечаем эпик на доске: конфликт main ↔ релизная ветка виден в UI и
		// ассистенту (флоу rebase/resolve остаётся точкой входа для резолва).
		epic.MergeConflictFiles = hard
		if store, err := s.boardStore(ctx, project); err == nil {
			if e, gerr := store.GetEpic(ctx, epic.TaskID); gerr == nil {
				e.MergeConflictFiles = hard
				if serr := store.SaveEpic(ctx, e); serr != nil {
					logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: запись конфликта: %v", epic.TaskID, serr)
				}
			}
			store.Close()
		}
		if sess := s.session(project); sess != nil {
			sess.append(chat.RoleStatus,
				fmt.Sprintf("Эпик %s: авто-синхрон релизной ветки с main упёрся в конфликт в файлах [%s]. Доступен флоу rebase/резолв (ResolveGitConflicts).",
					epic.TaskID, strings.Join(hard, ", ")), "", "", nil)
		}
		s.srvEmitBoard(project, "gitflow: авто-синхрон эпика: сложные конфликты")
		return
	}
	if err := wt.Stage(ctx, resolved); err != nil {
		removeWT()
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: git add резолвов: %v", epic.TaskID, err)
		return
	}
	if err := wt.CommitAllowEmpty(ctx,
		fmt.Sprintf("эпик %s: синхрон с main (авто-резолв %d файлов)", epic.TaskID, len(resolved))); err != nil {
		removeWT()
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: merge-коммит: %v", epic.TaskID, err)
		return
	}
	if pushURL := remotePushURL(inf); pushURL != "" {
		if err := wt.PushTo(ctx, pushURL); err != nil {
			removeWT()
			logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: push: %v", epic.TaskID, err)
			return
		}
	}
	removeWT()
	logging.For(project).Infof("gitflow: эпик %s: авто-синхрон с main: тривиальные конфликты авто-разрешены (%d файлов), ветка продвинута",
		epic.TaskID, len(resolved))
	s.clearEpicMergeConflict(ctx, project, epic.TaskID)
	s.srvEmitBoard(project, "gitflow: авто-синхрон эпика: конфликты разрешены")
}

// syncEpicMainForTask — синхронизация релизной ветки эпика с main перед стартом
// задачи (без требования done). Задача всегда стартует от свежего кода.
// Конфликты main ↔ релизная ветка не блокируют старт — они видны на доске.
// Вызывается под mergeLock (из taskWorktree) — dropEpicWorktreeLocked требует лока.
func (s *Server) syncEpicMainForTask(ctx context.Context, project, epicID string, inf workspace.Info, repo *gitops.Repo, epicRef workspace.BranchRef) {
	main := strings.TrimSpace(inf.GitBase)
	if main == "" {
		return
	}

	// 4.1: worktree ветки эпика (архитектор) мешает MergeFeature поставить
	// релизную ветку во временный worktree — снимаем (автокоммит правок).
	s.dropEpicWorktreeLocked(ctx, project, epicID)
	res, err := repo.MergeFeature(ctx, epicRef.Branch, main, gitops.MergeFeatureOptions{
		Message: fmt.Sprintf("эпик %s: синхрон с main перед стартом задачи", epicID),
		PushURL: remotePushURL(inf),
	})
	if err == nil {
		logging.For(project).Infof("gitflow: эпик %s: синхрон с main перед задачей (already=%v)", epicID, res.AlreadyMerged)
		s.clearEpicMergeConflict(ctx, project, epicID)
		return
	}

	var ce *gitops.MergeConflictError
	if !errors.As(err, &ce) {
		logging.For(project).Warnf("gitflow: синхрон эпика %s с main перед задачей: %v", epicID, err)
		return
	}

	// Конфликт: пробуем авто-резолв тривиальных блоков через autoResolveMainSync.
	s.autoResolveMainSync(ctx, project, &board.Epic{TaskSpec: board.TaskSpec{TaskID: epicID}}, inf, repo, epicRef)
}

// clearEpicMergeConflict снимает признак конфликта мёрджа с эпика на доске
// (успешный синхрон с main / релиз / резолв). Идемпотентно; ошибки логируются.
func (s *Server) clearEpicMergeConflict(ctx context.Context, project, epicID string) {
	s.setEpicMergeConflict(ctx, project, epicID, nil)
}

// setEpicMergeConflict ставит (files != nil) или снимает (files == nil) признак
// конфликта мёрджа релизной ветки эпика. Идемпотентно; ошибки логируются, но
// не ломают вызывающий поток: признак — витрина для UI, источник истины —
// результат следующего merge.
func (s *Server) setEpicMergeConflict(ctx context.Context, project, epicID string, files []string) {
	store, err := s.boardStore(ctx, project)
	if err != nil {
		return
	}
	defer store.Close()
	epic, err := store.GetEpic(ctx, epicID)
	if err != nil {
		return
	}
	if len(files) == 0 && len(epic.MergeConflictFiles) == 0 {
		return
	}
	epic.MergeConflictFiles = files
	if err := store.SaveEpic(ctx, epic); err != nil {
		logging.For(project).Warnf("gitflow: запись конфликта эпика %s: %v", epicID, err)
	}
}

// setEpicMergedIntoMain ставит или снимает признак «релизная ветка эпика влита
// в main» (merged_into_main). Идемпотентно; ошибки логируются.
func (s *Server) setEpicMergedIntoMain(ctx context.Context, project, epicID string, merged bool) {
	store, err := s.boardStore(ctx, project)
	if err != nil {
		return
	}
	defer store.Close()
	epic, err := store.GetEpic(ctx, epicID)
	if err != nil {
		return
	}
	if epic.MergedIntoMain == merged {
		return
	}
	epic.MergedIntoMain = merged
	if err := store.SaveEpic(ctx, epic); err != nil {
		logging.For(project).Warnf("gitflow: запись merged_into_main эпика %s: %v", epicID, err)
	}
}
