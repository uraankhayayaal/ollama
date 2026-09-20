// Вливание фича-ветки задачи в релизную ветку эпика (Ф-2).
//
// MergePerformance — главный примитив Ф-2: ветка `ai/task/<id>` → релизная
// ветка `ai/epic/<eid>`. Принципиальное ограничение то же, что и в Ф-1 —
// рабочая копия и текущая ветка основного клона НЕ трогаются. Сам merge (это
// реальный merge-коммит в релизной ветке) выполняется во временном git
// worktree: add → merge --no-ff → push → remove. Ветки живут в одном
// репозитории (branch-in-place), поэтому результат merge виден в основном
// клоне сразу после снятия worktree.
package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MergeConflictError — слияние feature в release невозможно из-за конфликтов.
// Files содержит конфликтующие пути, полученные через merge-tree БЕЗ изменения
// рабочей копии. Это вход для Ф-4 (авто-резолв + приёмка) — тут только прячем
// список наружу.
type MergeConflictError struct {
	Feature string   // вливаемый источник (ai/task/<id>)
	Files   []string // конфликтующие пути
}

func (e *MergeConflictError) Error() string {
	return "gitops: конфликт при вливании " + e.Feature + ": " + strings.Join(e.Files, ", ")
}

// MergeFeatureOptions — параметры вливания фича-ветки в релизную.
type MergeFeatureOptions struct {
	// Message — сообщение merge-коммита (обязательно, в HEAD попадает как
	// «Merge branch 'ai/task/<id>' ...» — связывает коммит с задачей).
	Message string
	// PushURL — remote-URL, куда после слияния пушится релизная ветка.
	// Пустая строка — без push (например, локальный клон без remote).
	PushURL string
}

// MergeFeature вливает фича-ветку feature в релизную ветку release в общем
// клоне r.Root. Возвращаемые случаи:
//
//   - *MergeConflictError — merge-tree выявил конфликтующие пути (рабочая
//     копия и ветки не тронуты);
//   - MergeResult.AlreadyMerged — feature уже анцесторна release (идемпотентность);
//   - MergeResult{} — success: merge-коммит создан (--no-ff), release продвинута
//     и (если задан PushURL) запушена на remote.
func (r *Repo) MergeFeature(ctx context.Context, release, feature string, opts MergeFeatureOptions) (*MergeResult, error) {
	if r == nil || r.Root == "" {
		return nil, fmt.Errorf("gitops: пустой Repo")
	}
	if strings.TrimSpace(release) == "" || strings.TrimSpace(feature) == "" {
		return nil, fmt.Errorf("gitops: требуются релизная и фича-ветки")
	}
	msg := strings.TrimSpace(opts.Message)
	if msg == "" {
		return nil, fmt.Errorf("gitops: пустое сообщение merge-коммита")
	}

	// Идемпотентность: задача уже влита в релиз — повторный «мёрдж» не
	// должен плодить пустые merge-коммиты (например, после done→merge хука
	// по одному разу на результат).
	merged, err := r.MergedInto(ctx, release, feature)
	if err != nil {
		return nil, err
	}
	if merged {
		return &MergeResult{Message: msg, AlreadyMerged: true}, nil
	}

	// Прогноз конфликтов через merge-tree БЕЗ изменения рабочей копии.
	base, err := r.MergeBase(ctx, release, feature)
	if err != nil {
		return nil, err
	}
	files, err := r.MergeTree(ctx, base, release, feature)
	if err != nil {
		return nil, err
	}
	if len(files) > 0 {
		return nil, &MergeConflictError{Feature: feature, Files: files}
	}

	// Сам merge — во временном worktree (рабочая копия клона не трогается).
	wtPath := filepath.Join(filepath.Dir(r.Root), ".wt-"+SanitizeBranchName(feature))
	cleanup := true
	defer func() {
		if !cleanup {
			return
		}
		_, _ = r.ex.Exec(ctx, r.Root, "git", "worktree", "remove", "--force", wtPath)
		_ = os.RemoveAll(wtPath)
	}()

	if _, err := r.ex.Exec(ctx, r.Root, "git", "worktree", "add", wtPath, release); err != nil {
		return nil, fmt.Errorf("gitops: worktree %s: %w", release, err)
	}
	wt := &Repo{Root: wtPath, Branch: release, Base: release, ex: r.ex}
	if _, err := wt.MergeBranch(ctx, feature, msg); err != nil {
		return nil, fmt.Errorf("gitops: слияние %s → %s: %w", feature, release, err)
	}
	if pushURL := strings.TrimSpace(opts.PushURL); pushURL != "" {
		if err := wt.PushTo(ctx, pushURL); err != nil {
			return nil, fmt.Errorf("gitops: push %s: %w", release, err)
		}
	}
	// Снимаем worktree: ветка осталась в общем репозитории клона.
	if _, err := r.ex.Exec(ctx, r.Root, "git", "worktree", "remove", "--force", wtPath); err != nil {
		return nil, fmt.Errorf("gitops: снятие worktree: %w", err)
	}
	if err := os.RemoveAll(wtPath); err != nil {
		return nil, fmt.Errorf("gitops: удаление каталога worktree: %w", err)
	}
	cleanup = false
	return &MergeResult{Message: msg}, nil
}

// MergedInto сообщает, является ли ветка feat анцестором ветки into, то есть
// уже влита в неё: merge-base(into, feat) совпадает с tip(feat). Используется
// как идемпотентность в MergeFeature и будет использоваться статусным
// эндпоинтом Ф-3 (GET .../epics/:eid/release).
func (r *Repo) MergedInto(ctx context.Context, into, feat string) (bool, error) {
	if r == nil || r.Root == "" {
		return false, fmt.Errorf("gitops: пустой Repo")
	}
	base, err := r.MergeBase(ctx, into, feat)
	if err != nil {
		return false, err
	}
	tip, err := r.revParse(ctx, feat)
	if err != nil {
		return false, err
	}
	return base == tip, nil
}

// revParse возвращает полный SHA ref-а (git rev-parse <ref>).
func (r *Repo) revParse(ctx context.Context, ref string) (string, error) {
	out, err := r.ex.Exec(ctx, r.Root, "git", "rev-parse", ref)
	if err != nil {
		return "", fmt.Errorf("gitops: git rev-parse %s: %w", ref, err)
	}
	v := strings.TrimSpace(out)
	if v == "" {
		return "", fmt.Errorf("gitops: git rev-parse %s: пустой вывод", ref)
	}
	return v, nil
}
