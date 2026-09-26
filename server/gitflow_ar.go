// ЛЛМ-разрешение конфликтов слияния через LLM (Ф-9).
//
// Когда TrivialResolve оставляет сложные конфликты ( marqueurs <<<<<<..., =======, >>>>>>>
// ), сервер вызывает LLM для автоматического разрешения. Используется контекст:
// - описание эпика (Summary из Epic.Summary)
// - описания задач в порядке очереди (SequenceOrder)
// - содержимое конфликтующих файлов
//
// После LLM разрешения запускается верификация (acceptor): build, формат, анализ.
// Только при вердикте approve конфликты снимаются с доски.

package server

import (
	"ai/agents/acceptor"
	"ai/agents/planner"
	"ai/board"
	"ai/gitops"
	"ai/logging"
	"ai/models"
	"ai/workspace"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// resolveCtx — контекст одного цикла разрешения конфликтов через LLM.
type resolveCtx struct {
	epic    *board.Epic
	tasks   []*board.Task
	hard    []string // пути конфликтных файлов
	workdir string   // директория с worktree (файлы для чтения/записи)
	store   *board.Store
}

// resolveResult — результат разрешения конфликтов.
type resolveResult struct {
	cleared   bool   // конфликты сняты с доски?
	verifyErr string // причина неудачи верификации (для лога)
}

func newResolveCtx(epic *board.Epic, tasks []*board.Task, hard []string, workdir string, store *board.Store) *resolveCtx {
	return &resolveCtx{epic: epic, tasks: tasks, hard: hard, workdir: workdir, store: store}
}

// ResolveWithLLM — авторезолвинг конфликтов через агента-разработчика.
// Специализация выбирается по роли (backend/frontend/qa/devops), агент
// работает в worktree конфликта с инструментами и автoфиксом. При ошибке
// верификации ошибки возвращаются в промпт следующей попытки.
const resolveMaxAttempts = 3

func (r *resolveCtx) ResolveWithLLM(ctx context.Context, prov models.LLMProvider, project, role string) (*resolveResult, error) {
	if prov == nil || len(r.hard) == 0 {
		return nil, nil
	}

	var lastResult *resolveResult
	for attempt := 1; attempt <= resolveMaxAttempts; attempt++ {
		prompt := r.buildDeveloperPrompt()
		if attempt > 1 && lastResult != nil && lastResult.verifyErr != "" {
			prompt += fmt.Sprintf("\n\n=== ПРЕДЫДУЩАЯ ПОПЫТКА НЕ УДАЛАСЬ ===\nОшибки верификации:\n%s\n\nИсправь код и убедись, что сборка и проверки проходят.\n", lastResult.verifyErr)
		}

		agent := planner.SpecialistForRole(project, role, prompt)
		if sa, ok := agent.(interface{ SetOutputDir(string) }); ok {
			sa.SetOutputDir(r.workdir)
		}

		resp, err := prov.Generate(ctx, agent)
		if err != nil {
			return nil, fmt.Errorf("developer resolve generate (attempt %d): %w", attempt, err)
		}
		if resp == nil {
			return nil, fmt.Errorf("empty developer response (attempt %d)", attempt)
		}

		// Разработчик может писать файлы с префиксом "OUTPUTDIR/" —
		// переносим их в корень worktree.
		r.flattenOutputDir()

		markers, _ := gitops.HasConflictMarkers(r.workdir, r.hard)
		if len(markers) > 0 {
			lastResult = &resolveResult{cleared: false, verifyErr: fmt.Sprintf("остались маркеры конфликта в: %s", strings.Join(markers, ", "))}
			continue
		}

		if verErr := r.verify(); verErr != nil {
			lastResult = &resolveResult{cleared: false, verifyErr: verErr.Error()}
			continue
		}

		r.clearConflictField(r.hard)
		return &resolveResult{cleared: true}, nil
	}

	return lastResult, nil
}

// flattenOutputDir переносит файлы из подкаталога OUTPUTDIR/ в корень worktree.
// Разработчик иногда пишет пути с префиксом "OUTPUTDIR/" — это артефакт промпта.
func (r *resolveCtx) flattenOutputDir() {
	sub := filepath.Join(r.workdir, "OUTPUTDIR")
	entries, err := os.ReadDir(sub)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src := filepath.Join(sub, e.Name())
		dst := filepath.Join(r.workdir, e.Name())
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			logging.Warnf("gitflow: LLM resolve: flattenOutputDir: %s: %v", e.Name(), err)
		}
	}
}

// buildDeveloperPrompt формирует задание разработчику на резолвинг конфликтов:
// контекст эпика, задачи в порядке очереди, список конфликтных файлов.
func (r *resolveCtx) buildDeveloperPrompt() string {
	var b strings.Builder

	b.WriteString("Ты — разработчик, решающий конфликты слияния (merge conflict resolution) в git.\n")
	b.WriteString("В текущей директории (OutputDir) есть файлы с маркерами конфликта:\n")
	b.WriteString("<<<<<<< (наша ветка), ======= (разделитель), >>>>>>> (их ветка).\n")
	b.WriteString("ВАЖНО: при записи файлов используй ОТНОСИТЕЛЬНЫЕ пути без префикса OUTPUTDIR/ (например: Makefile, scripts/e2e.sh).\n\n")

	// --- Контекст эпика ---
	b.WriteString("=== ЭПИК ===\n")
	b.WriteString(fmt.Sprintf("ID: %s\n", r.epic.TaskID))
	b.WriteString(fmt.Sprintf("Title: %s\n", r.epic.Title))
	if r.epic.Description != "" {
		b.WriteString("Description: " + r.epic.Description + "\n")
	}
	if r.epic.Summary != "" {
		b.WriteString("Architecture summary: " + r.epic.Summary + "\n")
	}
	b.WriteString("\n")

	// --- Задачи в порядке выполнения ---
	if len(r.tasks) > 0 {
		b.WriteString("=== ЗАДАЧИ ЭПИКА (порядок выполнения) ===\n")
		sorted := make([]*board.Task, len(r.tasks))
		copy(sorted, r.tasks)
		sort.Slice(sorted, func(i, j int) bool {
			return int(sorted[i].SequenceOrder) < int(sorted[j].SequenceOrder)
		})
		for _, t := range sorted {
			b.WriteString(fmt.Sprintf("  %s | Seq: %d | %s", t.TaskID, t.SequenceOrder, t.Title))
			if t.AssignedRole != "" {
				b.WriteString(fmt.Sprintf(" | Role: %s", t.AssignedRole))
			}
			if t.Description != "" {
				b.WriteString(fmt.Sprintf("\n    %s", t.Description))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// --- Конфликтные файлы ---
	b.WriteString("=== КОНФЛИКТНЫЕ ФАЙЛЫ ===\n")
	for _, f := range r.hard {
		b.WriteString(fmt.Sprintf("  %s\n", f))
	}
	b.WriteString("\n")

	// --- Инструкции ---
	b.WriteString("=== ИНСТРУКЦИИ ===\n")
	b.WriteString("1. Прочитай конфликтные файлы (ReadFiles) и найди маркеры <<<<<<<, =======, >>>>>>>.\n")
	b.WriteString("2. Разреши ВСЕ конфликты, объединив изменения обеих веток так, чтобы требования задач и эпика были выполнены.\n")
	b.WriteString("3. Не удаляй чужой код без необходимости — объединяй логику.\n")
	b.WriteString("4. После правок прогони сборку и проверки (Run), доведи до зелёного состояния.\n")
	b.WriteString("5. Не оставляй маркеры конфликта в файлах.\n")
	b.WriteString("6. LOCK-ФАЙЛЫ (go.sum, composer.lock, package-lock.json, yarn.lock, pnpm-lock.yaml, cargo.lock, gemfile.lock, poetry.lock, pipfile.lock, packages.lock.json, project.assets.json) НЕ резолвь вручную: сначала разреши конфликт в манифесте (go.mod, composer.json, package.json, Cargo.toml, Gemfile, Pipfile), затем пересобери зависимости (go mod tidy, composer install, npm install, yarn install, pnpm install, cargo generate-lock-file, bundle install, poetry lock) — lock-файл пересоздаётся автоматически.\n")

	return b.String()
}

// collectFiles читает содержимое конфликтных файлов из worktree.
func (r *resolveCtx) collectFiles() (map[string]string, error) {
	out := make(map[string]string, len(r.hard))
	for _, f := range r.hard {
		full := filepath.Join(r.workdir, filepath.FromSlash(f))
		data, err := os.ReadFile(full)
		if err != nil {
			continue // файл может быть удалён merge — пропускаем
		}
		out[f] = string(data)
	}
	return out, nil
}

// buildPrompt строит промпт для LLM: эпик + задачи + файлы.
func (r *resolveCtx) buildPrompt(files map[string]string) string {
	var b strings.Builder

	b.WriteString("You are a git conflict resolution agent. Solve ALL conflict markers (" + "<<<<<<<<;" + " =======," + " >>>>>>>>\n in the ELL).\n\n")

	// --- Epic context ---
	b.WriteString("=== EPIC ===\n")
	b.WriteString(fmt.Sprintf("ID: %s\n", r.epic.TaskID))
	b.WriteString(fmt.Sprintf("Title: %s\n", r.epic.Title))
	if r.epic.Description != "" {
		b.WriteString("Description: " + r.epic.Description + "\n")
	}
	if r.epic.Summary != "" {
		b.WriteString("Architecture summary: " + r.epic.Summary + "\n")
	}
	if r.epic.AssignedRole != "" {
		b.WriteString("Role: " + r.epic.AssignedRole + "\n")
	}
	b.WriteString("\n")

	// --- Tasks in sequence order ---
	if len(r.tasks) > 0 {
		b.WriteString("=== EPIC TASKS (execution order) ===\n")
		sorted := make([]*board.Task, len(r.tasks))
		copy(sorted, r.tasks)
		sort.Slice(sorted, func(i, j int) bool {
			return int(sorted[i].SequenceOrder) < int(sorted[j].SequenceOrder)
		})
		for _, t := range sorted {
			b.WriteString(fmt.Sprintf("  ID: %s | Seq: %d | Title: %s", t.TaskID, t.SequenceOrder, t.Title))
			if t.AssignedRole != "" {
				b.WriteString(fmt.Sprintf(" | Role: %s", t.AssignedRole))
			}
			if t.Description != "" {
				b.WriteString(fmt.Sprintf(" | Desc: %s", t.Description))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// --- Conflict files ---
	b.WriteString("=== CONFLICTING FILES ===\n\n")
	for _, f := range r.hard {
		if content, ok := files[f]; ok {
			// Ограничить длину, чтобы не переполнить контекст LLM.
			if len(content) > 12288 {
				content = content[:12288] + "\n... (truncated to fit context)\n"
			}
			b.WriteString(fmt.Sprintf("FILE: %s\n", f))
			b.WriteString(content)
			b.WriteString("\n---\n")
		} else {
			b.WriteString(fmt.Sprintf("FILE: %s\n[FILE DELETED BY MERGE — skip]\n---\n", f))
		}
	}

	// --- Instructions ---
	b.WriteString("=== INSTRUCTIONS ===\n")
	b.WriteString("1. Read all epic and task descriptions CAREFULLY.\n")
	b.WriteString("2. Resolve EVERY conflict marker in EVERY listed file.\n")
	b.WriteString("3. Ensure ALL task requirements are fully satisfied in the resolved code.\n")
	b.WriteString("4. Only modify files listed above; do NOT touch files without conflict markers.\n\n")

	// --- Output format ---
	b.WriteString("=== OUTPUT FORMAT ===\n")
	b.WriteString("Return ONLY a pure JSON object mapping file paths to resolved contents.\n")
	b.WriteString("No markdown, no backticks, no commentary.\n\n")
	b.WriteString("Format:\n")
	b.WriteString("{\"file_path\": \"<resolved_content>\"}...")

	return b.String()
}

// writeResolved пишет содержимое из resolved map в файлы worktree.
func (r *resolveCtx) writeResolved(resolved map[string]string) error {
	for f, content := range resolved {
		full := filepath.Join(r.workdir, filepath.FromSlash(f))
		dir := filepath.Dir(full)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("mkdirall %s: %w", dir, err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			return fmt.Errorf("write %s: %w", f, err)
		}
	}
	return nil
}

// verify запускает обязательную приёмку: build, формат, анализ.
// Возвращает детализированную ошибку с полным выводом для LLM.
func (r *resolveCtx) verify() error {
	cfg := acceptor.Config{
		BuildTimeout: 3 * time.Minute,
		CheckFormat:  true,
		CheckAnalyze: true,
		CheckLSP:     false,
		InstallDeps:  false,
	}

	rep := acceptor.Accept(r.workdir, cfg)
	if rep.Verdict == acceptor.VerdictApprove {
		return nil
	}

	var parts []string

	// Собираем вывод ошибок по стадиям.
	if rep.Build.Output != "" {
		parts = append(parts, fmt.Sprintf("BUILD OUTPUT:\n%s", strings.TrimSpace(rep.Build.Output)))
	}
	if rep.Analyze != nil && rep.Analyze.Output != "" {
		parts = append(parts, fmt.Sprintf("ANALYZE OUTPUT:\n%s", strings.TrimSpace(rep.Analyze.Output)))
	}
	if rep.Format != nil && rep.Format.Output != "" {
		parts = append(parts, fmt.Sprintf("FORMAT OUTPUT:\n%s", strings.TrimSpace(rep.Format.Output)))
	}

	// Fallback на issues если вывод пуст.
	if len(parts) == 0 {
		for _, issue := range rep.Issues {
			if issue.Severity == "error" {
				parts = append(parts, issue.Text)
			}
		}
	}

	if len(parts) == 0 {
		return fmt.Errorf("acceptor reject: %s", rep.Summary)
	}

	return fmt.Errorf("acceptor reject:\n%s", strings.Join(parts, "\n\n"))
}

// clearConflictField снимает конфликтные файлы с доски.
func (r *resolveCtx) clearConflictField(resolved []string) {
	resSet := make(map[string]bool, len(resolved))
	for _, f := range resolved {
		resSet[f] = true
	}

	// Epic-level
	if len(r.epic.MergeConflictFiles) > 0 {
		all := true
		for _, f := range r.epic.MergeConflictFiles {
			if !resSet[f] {
				all = false
				break
			}
		}
		if all {
			r.epic.MergeConflictFiles = nil
			if r.store != nil {
				_ = r.store.SaveEpic(context.Background(), r.epic)
			}
		}
	}

	// Task-level
	for _, t := range r.tasks {
		if len(t.MergeConflictFiles) == 0 {
			continue
		}
		all := true
		for _, f := range t.MergeConflictFiles {
			if !resSet[f] {
				all = false
				break
			}
		}
		if all {
			t.MergeConflictFiles = nil
			if r.store != nil {
				_ = r.store.SaveTask(context.Background(), t)
			}
		}
	}
}

// attemptLLMResolve — полный цикл авторезолвина конфликтов через разработчика.
// Специализация выбирается по роли эпика/задачи. При успехе выполняет
// stage + commit + push. Возвращает true, если конфликты разрешены.
func (s *Server) attemptLLMResolve(ctx context.Context, project string, epic *board.Epic, workdir string, hard []string, inf workspace.Info, branch, role string) bool {
	if len(hard) == 0 {
		return false
	}

	store, err := s.boardStore(ctx, project)
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve: board store: %v", err)
		return false
	}
	defer store.Close()

	tasks, err := store.TasksByEpic(ctx, epic.TaskID)
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve: tasks: %v", err)
		return false
	}

	rCtx := newResolveCtx(epic, tasks, hard, workdir, store)

	prov, err := s.provider()
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve: provider: %v", err)
		return false
	}

	result, err := rCtx.ResolveWithLLM(ctx, prov, project, role)
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve: %v", err)
		return false
	}
	if result == nil {
		logging.For(project).Warnf("gitflow: LLM resolve: эпик %s — LLM недоступен", epic.TaskID)
		return false
	}
	if !result.cleared {
		reason := result.verifyErr
		if reason == "" {
			reason = "неизвестная причина"
		}
		logging.For(project).Warnf("gitflow: LLM resolve: эпик %s — не удалось разрешить: %s", epic.TaskID, reason)
		return false
	}

	wt := gitops.RepoFromState(s.gitExec, workdir, inf.GitRemote, branch, strings.TrimSpace(inf.GitBase))
	if err := wt.Stage(ctx, hard); err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve: stage: %v", err)
		return false
	}

	msg := fmt.Sprintf("эпик %s: LLM-резолв конфликтов (%d файлов)", epic.TaskID, len(hard))
	if err := wt.Commit(ctx, msg); err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve: commit: %v", err)
		return false
	}

	if pushURL := remotePushURL(inf); pushURL != "" {
		if err := wt.PushTo(ctx, pushURL); err != nil {
			logging.For(project).Warnf("gitflow: LLM resolve: push: %v", err)
			return false
		}
	}

	logging.For(project).Infof("gitflow: LLM resolve: эпик %s: %d файлов разрешено и зафиксировано", epic.TaskID, len(hard))
	return true
}

// tryTaskLLMResolve — авторезолвинг конфликта мёрджа задачи в релиз эпика.
// Создаёт worktree релизной ветки, вливает задачу, собирает конфликты,
// пробует TrivialResolve, затем LLM. Возвращает true при успехе.
func (s *Server) tryTaskLLMResolve(ctx context.Context, project string, task *board.Task, conflictFiles []string) bool {
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return false
	}
	repo, err := s.repoOf(ctx, project)
	if err != nil {
		return false
	}
	epicRef, err := s.reg.EpicBranch(project, task.EpicID)
	if err != nil {
		return false
	}
	taskRef, err := s.reg.TaskBranch(project, task.TaskID)
	if err != nil {
		return false
	}

	wtPath := filepath.Join(filepath.Dir(repo.Root), ".resolve-"+project+"-"+gitops.SanitizeBranchName(task.TaskID))
	if _, err := os.Stat(wtPath); err == nil {
		_ = repo.RemoveWorktree(ctx, wtPath)
	}
	wt, err := repo.AddWorktree(ctx, wtPath, epicRef.Branch)
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve задачи %s: worktree: %v", task.TaskID, err)
		return false
	}
	defer func() { _ = repo.RemoveWorktree(ctx, wtPath) }()

	_, _ = wt.MergeBranch(ctx, taskRef.Branch,
		fmt.Sprintf("задача %s: влитие в релиз эпика %s (LLM-резолв)", task.TaskID, task.EpicID))

	unmerged, err := wt.UnmergedFiles(ctx)
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve задачи %s: ls-files -u: %v", task.TaskID, err)
		return false
	}

	resolved, hard, err := gitops.TrivialResolve(wtPath, unmerged)
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve задачи %s: авто-резолв: %v", task.TaskID, err)
		return false
	}

	if len(hard) == 0 {
		if err := wt.Stage(ctx, resolved); err != nil {
			logging.For(project).Warnf("gitflow: LLM resolve задачи %s: stage: %v", task.TaskID, err)
			return false
		}
		msg := fmt.Sprintf("задача %s: авто-резолв в релизе эпика %s", task.TaskID, task.EpicID)
		if err := wt.Commit(ctx, msg); err != nil {
			logging.For(project).Warnf("gitflow: LLM resolve задачи %s: commit: %v", task.TaskID, err)
			return false
		}
		if pushURL := remotePushURL(inf); pushURL != "" {
			if err := wt.PushTo(ctx, pushURL); err != nil {
				logging.For(project).Warnf("gitflow: LLM resolve задачи %s: push: %v", task.TaskID, err)
				return false
			}
		}
		logging.For(project).Infof("gitflow: LLM resolve задачи %s: тривиальные конфликты авто-разрешены", task.TaskID)
		return true
	}

	store, err := s.boardStore(ctx, project)
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve задачи %s: board store: %v", task.TaskID, err)
		return false
	}
	epic, err := store.GetEpic(ctx, task.EpicID)
	store.Close()
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve задачи %s: epic: %v", task.TaskID, err)
		return false
	}

	if len(resolved) > 0 {
		_ = wt.Stage(ctx, resolved)
	}

	return s.attemptLLMResolve(ctx, project, epic, wtPath, hard, inf, epicRef.Branch, task.AssignedRole)
}

// handleTaskLLMResolve — REST-эндпоинт авторезолвина конфликта задачи через LLM.
// POST /api/projects/{id}/tasks/{tid}/resolve
func (s *Server) handleTaskLLMResolve(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")

	store, err := s.boardStore(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "доска недоступна: "+err.Error())
		return
	}
	defer store.Close()

	task, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "задача не найдена")
		return
	}
	if len(task.MergeConflictFiles) == 0 {
		writeErr(w, http.StatusConflict, "у задачи нет неразрешённых конфликтов")
		return
	}

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	resolved := s.tryTaskLLMResolve(r.Context(), project, task, task.MergeConflictFiles)
	if resolved {
		if len(task.MergeConflictFiles) > 0 {
			task.MergeConflictFiles = nil
			_ = store.SaveTask(r.Context(), task)
		}
		s.srvEmitBoard(project, "gitflow: LLM-резолв задачи")
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "resolved": true})
		return
	}

	writeJSON(w, http.StatusConflict, map[string]any{
		"status":  "failed",
		"files":   task.MergeConflictFiles,
		"message": "LLM не удалось разрешить конфликты",
	})
}

// handleEpicLLMResolve — REST-эндпоинт авторезолвина конфликта эпика через LLM.
// POST /api/projects/{id}/epics/{eid}/auto-resolve
func (s *Server) handleEpicLLMResolve(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	epicID := r.PathValue("eid")

	store, err := s.boardStore(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "доска недоступна: "+err.Error())
		return
	}
	epic, err := store.GetEpic(r.Context(), epicID)
	store.Close()
	if err != nil {
		writeErr(w, http.StatusNotFound, "эпик не найден")
		return
	}
	if len(epic.MergeConflictFiles) == 0 {
		writeErr(w, http.StatusConflict, "у эпика нет неразрешённых конфликтов")
		return
	}

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		writeErr(w, http.StatusBadRequest, "проект не найден или не git")
		return
	}
	repo, err := s.repoOf(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	epicRef, err := s.reg.EpicBranch(project, epicID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "ветка эпика не найдена")
		return
	}

	wtPath := filepath.Join(filepath.Dir(repo.Root), ".conflict-"+project+"-"+gitops.SanitizeBranchName(epicID))
	if _, err := os.Stat(wtPath); err == nil {
		_ = repo.RemoveWorktree(r.Context(), wtPath)
	}
	wt, err := repo.AddWorktree(r.Context(), wtPath, epicRef.Branch)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "worktree: "+err.Error())
		return
	}
	defer func() { _ = repo.RemoveWorktree(r.Context(), wtPath) }()

	_, _ = wt.MergeBranch(r.Context(), inf.GitBase,
		fmt.Sprintf("эпик %s: LLM-резолв с main", epicID))

	unmerged, err := wt.UnmergedFiles(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "ls-files -u: "+err.Error())
		return
	}

	resolved, hard, err := gitops.TrivialResolve(wtPath, unmerged)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "TrivialResolve: "+err.Error())
		return
	}

	if len(hard) == 0 {
		_ = wt.Stage(r.Context(), resolved)
		_ = wt.Commit(r.Context(), fmt.Sprintf("эпик %s: авто-резолв с main", epicID))
		if pushURL := remotePushURL(inf); pushURL != "" {
			_ = wt.PushTo(r.Context(), pushURL)
		}
		s.clearEpicMergeConflict(r.Context(), project, epicID)
		s.srvEmitBoard(project, "gitflow: LLM-резолв эпика")
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "resolved": true})
		return
	}

	if len(resolved) > 0 {
		_ = wt.Stage(r.Context(), resolved)
	}

	ok := s.attemptLLMResolve(r.Context(), project, epic, wtPath, hard, inf, epicRef.Branch, epic.AssignedRole)
	if ok {
		s.clearEpicMergeConflict(r.Context(), project, epicID)
		s.srvEmitBoard(project, "gitflow: LLM-резолв эпика")
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "resolved": true})
		return
	}

	writeJSON(w, http.StatusConflict, map[string]any{
		"status":  "failed",
		"files":   hard,
		"message": "LLM не удалось разрешить конфликты",
	})
}
