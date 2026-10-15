// Синхронизация рабочей копии агентов (temp/<имя> на ветке ai/<имя>) с main,
// вынесенная из HTTP-слоя (`server/ragref.go`, Этап 6 «Интерфейсы и адаптеры»).
//
// Зачем: рабочую копию читают архитектор, лиды и баг-эксперт в статусе
// «анализ»; без подтягивания main смена эпика показывала код до релизов
// предыдущих эпиков. Синхрон не должен терять незакоммиченные правки агентов и
// не должен оставлять конфликтные маркеры: грязное дерево пропускается, а
// конфликтный merge откатывается (`git merge --abort`). Возвращаем true, только
// если копия реально обновилась (HEAD сдвинулся) — иначе переиндексация её
// ветки была бы напрасной (стоит эмбеддингов всего проекта).
package rag

import (
	"context"
	"fmt"
	"strings"

	"ai/domain"
	"ai/domain/services"
)

// SyncProject — срез info проекта, нужный синхронизации рабочей копии.
type SyncProject struct {
	// Base — главная ветка (обычно main), Branch — ветка рабочей копии.
	Base   string
	Branch string
	// Root — рабочий каталог (для git rev-parse/merge --abort).
	Root string
	// Checkout — текущая checkout-ветка репозитория ("" — неизвестно).
	Checkout string
}

// SyncRepo — узкий порт рабочей копии, нужный синхронизации (реализуется
// *infrastructure/git.Repo).
type SyncRepo interface {
	Dirty(ctx context.Context) (bool, error)
	MergeBranch(ctx context.Context, branch, message string) (*domain.MergeResult, error)
}

// SyncSink — приёмник сообщений синхронизации (привязан к проекту адаптером).
type SyncSink interface {
	Detail(format string, args ...any)
	Warn(format string, args ...any)
	Info(format string, args ...any)
}

// WorkingCopySyncer — политика синхронизации рабочей копии с main.
type WorkingCopySyncer struct {
	Exec services.Executor
	Sink SyncSink
}

// Sync подтягивает рабочую копию агентов к актуальному main: `git merge --no-ff`
// main в ветку проекта. Пропуск (только лог) при грязном дереве, отсутствии
// main или занятом checkout; конфликтный merge откатывается. Возвращает true,
// только если HEAD сдвинулся.
func (w WorkingCopySyncer) Sync(ctx context.Context, inf SyncProject, repo SyncRepo) bool {
	main := strings.TrimSpace(inf.Base)
	agentBranch := strings.TrimSpace(inf.Branch)
	if repo == nil || main == "" || agentBranch == "" || main == agentBranch {
		return false
	}
	if inf.Checkout != "" && inf.Checkout != agentBranch {
		// checkout занят другой веткой (например, служебный checkout перед
		// созданием worktree) — переключение делает вызывающий код.
		w.Sink.Detail("rag: синхрон рабочей копии с main пропущен — checkout на ветке %s", inf.Checkout)
		return false
	}
	dirty, err := repo.Dirty(ctx)
	if err != nil {
		w.Sink.Detail("rag: синхрон рабочей копии с main: %v", err)
		return false
	}
	if dirty {
		w.Sink.Detail("rag: синхрон рабочей копии с main пропущен — есть незакоммиченные правки")
		return false
	}
	headBefore := headSHA(ctx, w.Exec, inf.Root)
	if _, err := repo.MergeBranch(ctx, main, fmt.Sprintf("%s: синхрон рабочей копии с %s", agentBranch, main)); err != nil {
		// Конфликт: возвращаем дерево к состоянию до merge, иначе следующий
		// агент увидит конфликтные маркеры, а индекс — мусор.
		if aerr := abortMerge(ctx, w.Exec, inf.Root); aerr != nil {
			w.Sink.Detail("rag: git merge --abort: %v", aerr)
		}
		w.Sink.Warn("rag: синхрон рабочей копии с %s: %v", main, err)
		return false
	}
	if head := headSHA(ctx, w.Exec, inf.Root); headBefore != "" && head == headBefore {
		w.Sink.Detail("rag: рабочая копия %s уже актуальна относительно %s", agentBranch, main)
		return false
	}
	w.Sink.Info("gitflow: рабочая копия %s синхронизирована с %s", agentBranch, main)
	return true
}

// headSHA — текущий коммит рабочей копии (пустая строка, если не git или нет
// коммитов).
func headSHA(ctx context.Context, ex services.Executor, dir string) string {
	if ex == nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	out, err := ex.Exec(ctx, dir, "git", "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// abortMerge откатывает неудачный merge в рабочей копии. Ошибка возвращается
// вызывающему для лога: состояние merge и так лучше, чем паника.
func abortMerge(ctx context.Context, ex services.Executor, dir string) error {
	if ex == nil || strings.TrimSpace(dir) == "" {
		return nil
	}
	_, err := ex.Exec(ctx, dir, "git", "merge", "--abort")
	return err
}
