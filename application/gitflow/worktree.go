package gitflow

// Layout и артефактная политика git-workflow (Ф-3/Ф-6), вынесенные из
// HTTP-слоя (`server/gitflow_auto.go`, Этап 6 «Интерфейсы и адаптеры»).
//
// Здесь только чистые правила: где живут постоянные worktree задач и эпиков,
// какие артефакты сборки никогда не должны попасть в историю задачи и как
// показывать ошибку git-шага на доске. Сами git-операции и жизненный цикл
// worktree остаются в `server`.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"ai/domain"
	"ai/logging"
)

// TaskWorktreePath возвращает каталог постоянного worktree ветки задачи —
// сосед основного клона (как MergeFeature/конфликтные worktree).
func TaskWorktreePath(repoRoot, project, taskID string) string {
	return filepath.Join(filepath.Dir(repoRoot),
		".wt-task-"+project+"-"+domain.SanitizeBranchName(taskID))
}

// EpicWorktreePath — каталог постоянного worktree релизной ветки эпика
// (4.1): сосед основного клона, как worktree задачи.
func EpicWorktreePath(repoRoot, project, epicID string) string {
	return filepath.Join(filepath.Dir(repoRoot),
		".wt-epic-"+project+"-"+domain.SanitizeBranchName(epicID))
}

// ChildTaskKey — ключ задачи дочернего (сабмодульного) проекта в общем
// реестре веток.
func ChildTaskKey(project, taskID string) string { return project + "::" + taskID }

// GitignorePatterns — артефакты, которые не должны попасть в историю задачи.
// Промежуточные коммиты (Ф-6) делают `git add -A` на каждом раунде, а без
// списка он тащит в коммит всё, что появилось в рабочем дереве.
//
// `vendor/` в список НЕ входит: для Go его коммитят осознанно.
//
// Последние две группы добавлены после живого случая (mytrip, FEL-05): агент
// без npm в образе песочницы скачал Node в корень проекта, и `git add -A`
// закоммитил 46 МБ архива + 4287 файлов распакованного тулчейна.
//   - `*.tar.gz`/`*.tgz`/`*.zip`/`*.tar.*` — скачанные дистрибутивы;
//   - `node-v*/` — распакованный тулчейн Node (то, что агент распаковывает рядом
//     с архивом); версионный префикс отличает его от каталогов приложения.
var GitignorePatterns = []string{
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

// GitignoreHeader — маркер блока оркестратора в .gitignore: по нему видно,
// что дописывала система, а что проект.
const GitignoreHeader = "# артефакты сборки и скачанные тулчейны (дописано оркестратором, Ф-6)"

// GitignoreFileName — имя служебного .gitignore оркестратора (используется
// гардом «phantom done»: он не считается доказательством работы агента).
const GitignoreFileName = ".gitignore"

// TaskGitignore — полный файл для случая «.gitignore не было».
func TaskGitignore() string {
	return GitignoreHeader + "\n" + strings.Join(GitignorePatterns, "\n") + "\n"
}

// EnsureTaskGitignore создаёт .gitignore в корне worktree задачи (в т.ч.
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
func EnsureTaskGitignore(project, taskID, root string) {
	if strings.TrimSpace(root) == "" {
		return
	}
	path := filepath.Join(root, GitignoreFileName)
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
	for _, p := range GitignorePatterns {
		if !existing[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return
	}
	block := GitignoreHeader + "\n" + strings.Join(missing, "\n") + "\n"
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

// GitflowErrBoard собирает однострочное сообщение об ошибке git-шага для
// доски: при отказе GitHub по scope workflow — готовая инструкция (секреты
// скрыты, многострочный вывод git не тащится), иначе — первая строка ошибки,
// сжатая до 200 символов. Исходная причина целиком остаётся в логе проекта.
func GitflowErrBoard(prefix string, err error) string {
	if errors.Is(err, domain.ErrWorkflowScope) {
		return prefix + ": " + domain.ErrWorkflowScope.Error()
	}
	msg := domain.RedactSecrets(err.Error())
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	return prefix + ": " + msg
}
