// Package gitflow — use-cases git-workflow (эпик = релизная ветка, задача =
// фича-ветка), вынесенные из HTTP-слоя (Этап 6, «Интерфейсы и адаптеры»).
//
// Здесь живёт бизнес-логика, у которой нет HTTP-зависимостей: сам цикл
// разрешения конфликтов слияния через агента-разработчика. Побочные эффекты
// (git, приёмка, доска, LLM) подключаются через порты, реализуемые
// инфраструктурой (`server`).
package gitflow

// ЛЛМ-разрешение конфликтов слияния через LLM (Ф-9).
//
// Когда TrivialResolve оставляет сложные конфликты (маркеры <<<<<<<,
// =======, >>>>>>>), вызывается агент-разработчик в worktree конфликта:
// контекст — описание эпика, задачи в порядке очереди и сами конфликтные
// файлы. Агент правит файлы инструментами, после чего запускается
// детерминированная приёмка (acceptor). Только при её успехе конфликты
// снимаются с доски.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ai/domain"
	"ai/domain/services"
	"ai/shared/logging"
)

// Result — итог одного цикла разрешения конфликтов.
type Result struct {
	// Cleared — конфликты сняты с доски (агент отработал и приёмка прошла).
	Cleared bool
	// VerifyErr — причина неудачи последней попытки (для лога/сообщения).
	VerifyErr string
}

// Store — часть доски, нужная, чтобы снять конфликтные файлы по завершении.
// Реализуется `board.Store`/`services.BoardStore`.
type Store interface {
	SaveEpic(ctx context.Context, epic *domain.Epic) error
	SaveTask(ctx context.Context, task *domain.Task) error
}

// Developer — фабрика и исполнитель агента-разработчика, решающего конфликты.
// Реализуется специалистом `planner.SpecialistForRole` + LLM-провайдером.
type Developer interface {
	// SpecialistForRole собирает агента нужной специализации с заданием.
	SpecialistForRole(project, role, prompt string) services.Agent
	// Generate прогоняет агента и возвращает его ответ.
	Generate(ctx context.Context, agent services.Agent) (*domain.AgentResponse, error)
}

// Acceptor — детерминированная приёмка директории (сборка, формат, анализ).
// Реализуется `acceptor.Accept` с фиксированной конфигурацией.
type Acceptor interface {
	Accept(dir string) *domain.Report
}

// Deps — внешние зависимости цикла разрешения конфликтов.
type Deps struct {
	Store     Store
	Developer Developer
	Acceptor  Acceptor
	// Markers ищет оставшиеся маркеры конфликта в файлах worktree
	// (`git.HasConflictMarkers`).
	Markers func(dir string, files []string) ([]string, error)
}

// Resolver — контекст одного цикла разрешения конфликтов через агента.
type Resolver struct {
	epic    *domain.Epic
	tasks   []*domain.Task
	hard    []string // пути конфликтных файлов
	workdir string   // директория worktree (файлы для чтения/записи)
	deps    Deps
}

// NewResolver собирает контекст цикла.
func NewResolver(epic *domain.Epic, tasks []*domain.Task, hard []string, workdir string, deps Deps) *Resolver {
	return &Resolver{epic: epic, tasks: tasks, hard: hard, workdir: workdir, deps: deps}
}

// ResolveMaxAttempts — бюджет попыток разрешения: каждый новый прогон агента
// получает в промпт ошибки приёмки предыдущего.
const ResolveMaxAttempts = 3

// ResolveWithLLM — авторезолвинг конфликтов через агента-разработчика.
// Специализация выбирается по роли (backend/frontend/qa/devops), агент
// работает в worktree конфликта с инструментами и автофиксом. При ошибке
// верификации ошибки возвращаются в промпт следующей попытки. Возвращает
// nil, если разрешать нечего (пустой список файлов).
func (r *Resolver) ResolveWithLLM(ctx context.Context, project, role string) (*Result, error) {
	if len(r.hard) == 0 {
		return nil, nil
	}

	var lastResult *Result
	for attempt := 1; attempt <= ResolveMaxAttempts; attempt++ {
		prompt := r.buildDeveloperPrompt()
		if attempt > 1 && lastResult != nil && lastResult.VerifyErr != "" {
			prompt += fmt.Sprintf("\n\n=== ПРЕДЫДУЩАЯ ПОПЫТКА НЕ УДАЛАСЬ ===\nОшибки верификации:\n%s\n\nИсправь код и убедись, что сборка и проверки проходят.\n", lastResult.VerifyErr)
		}

		agent := r.deps.Developer.SpecialistForRole(project, role, prompt)
		if sa, ok := agent.(interface{ SetOutputDir(string) }); ok {
			sa.SetOutputDir(r.workdir)
		}

		resp, err := r.deps.Developer.Generate(ctx, agent)
		if err != nil {
			return nil, fmt.Errorf("developer resolve generate (attempt %d): %w", attempt, err)
		}
		if resp == nil {
			return nil, fmt.Errorf("empty developer response (attempt %d)", attempt)
		}

		// Разработчик может писать файлы с префиксом "OUTPUTDIR/" —
		// переносим их в корень worktree.
		r.flattenOutputDir()

		markers, _ := r.deps.Markers(r.workdir, r.hard)
		if len(markers) > 0 {
			lastResult = &Result{Cleared: false, VerifyErr: fmt.Sprintf("остались маркеры конфликта в: %s", strings.Join(markers, ", "))}
			continue
		}

		if verErr := r.verify(); verErr != nil {
			lastResult = &Result{Cleared: false, VerifyErr: verErr.Error()}
			continue
		}

		r.clearConflictField(r.hard)
		return &Result{Cleared: true}, nil
	}

	return lastResult, nil
}

// flattenOutputDir переносит файлы из подкаталога OUTPUTDIR/ в корень worktree.
// Разработчик иногда пишет пути с префиксом "OUTPUTDIR/" — это артефакт промпта.
func (r *Resolver) flattenOutputDir() {
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
func (r *Resolver) buildDeveloperPrompt() string {
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
		sorted := make([]*domain.Task, len(r.tasks))
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

// verify запускает обязательную приёмку: build, формат, анализ.
// Возвращает детализированную ошибку с полным выводом для агента.
func (r *Resolver) verify() error {
	rep := r.deps.Acceptor.Accept(r.workdir)
	if rep.Verdict == domain.VerdictApprove {
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
func (r *Resolver) clearConflictField(resolved []string) {
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
			if r.deps.Store != nil {
				_ = r.deps.Store.SaveEpic(context.Background(), r.epic)
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
			if r.deps.Store != nil {
				_ = r.deps.Store.SaveTask(context.Background(), t)
			}
		}
	}
}
