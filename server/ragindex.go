// Фоновая индексация RAG проекта (см. PLAN-2026-09-24-done-architect-intelligence.md, Ф-5, Р-2;
// версия по веткам — PLAN-2026-09-27-done-branch-aware-rag.md).
//
// Архитектор видит через RagIndexStatus, что проект не проиндексирован, и по
// промпту предлагает пользователю (AskUser) построить индекс в фоне. Мост
// IndexBackground (server/actions.go) вызывает Session.IndexBackground —
// индексация уходит в горутину и НЕ блокирует агентский цикл: архитектор
// продолжает проектирование через ReadFiles/ReadMap/LSP, а результат
// (файлы/чанки) отчитывается в лог проекта и chat.RoleStatus. Повторный
// запуск идемпотентен (IndexProject помечает прежние версии чанков устаревшими
// и грузит новые с тем же chunk_id) и защищён флагом сессии (повтор при уже
// идущей индексации отклоняется). Индексация ветко-осознанная: чанки получают
// branch/commit_sha, поэтому CodeSearch в ветке агента видит свою ветку и
// актуальный main, но не изменения соседних эпиков.

package server

import (
	"context"
	"errors"
	"io"
	"strings"

	apprag "ai/application/rag"
	"ai/chat"
	"ai/infrastructure/rag"
	"ai/projects"
)

// ragIndexSink — приёмник отчётов фоновой индексации (порт
// application/rag.IndexSink) поверх лога проекта и чата сессии.
type ragIndexSink struct{ sess *Session }

func (r ragIndexSink) Info(msg string)   { r.sess.log.Infof("%s", msg) }
func (r ragIndexSink) Warn(msg string)   { r.sess.log.Warnf("%s", msg) }
func (r ragIndexSink) Status(msg string) { r.sess.append(chat.RoleStatus, msg, "", "", nil) }

// projectIndexer — узкий интерфейс векторной памяти для полной индексации
// проекта (реализует *rag.Client; выделен для hermetic-тестов без сети).
// Алиас порта application/rag.Indexer (срез F3k).
type projectIndexer = apprag.Indexer

// projectIndexerCloser — индексатор, который нужно закрыть после работы.
// *rag.Client реализует обе части (Close закрывает gRPC-соединение).
type projectIndexerCloser interface {
	projectIndexer
	Close() error
}

// buildProjectIndexer — фабрика клиента RAG для фоновой индексации. Замена
// (пакетная переменная) позволяет hermetic-тестам подставить фейковый
// индексатор без сети/Qdrant.
var buildProjectIndexer = func() (projectIndexerCloser, error) {
	c := rag.NewClientSafe(rag.Config{})
	if c == nil {
		return nil, errors.New("клиент RAG не создан (проверь QDRANT_ADDR и EMBEDDING_MODEL)")
	}
	return c, nil
}

// IndexBackground запускает фоновую индексацию RAG-памяти проекта (Ф-5, Р-2).
// Возвращается сразу (в отдельной горутине): агентский цикл не блокируется.
// Повторный вызов при уже идущей индексации — ошибка (один прогон на сессию).
// branch — необязательная ветка:
//
//   - пусто — индексируется рабочий каталог проекта под его текущей веткой
//     (rag.DetectIndexOptions);
//   - задана — индексируется СОДЕРЖИМОЕ ветки из git (по ref), а не рабочая
//     копия: checkout проекта стоит на ветке агента (ai/<имя>) и кода main в
//     нём нет, поэтому подписывать его как main нельзя (см. ragref.go).
func (sess *Session) IndexBackground(ctx context.Context, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch != "" {
		return sess.IndexRefBackground(ctx, branch)
	}
	dir := sess.projectDir()
	return sess.IndexDirBackground(ctx, dir, "проект")
}

// projectDir — каталог рабочей копии проекта из реестра (фолбэк projects.ProjectDir).
func (sess *Session) projectDir() string {
	if inf, err := sess.srv.reg.Get(sess.project); err == nil && inf.Root != "" {
		return inf.Root
	}
	return projects.ProjectDir(sess.project)
}

// newProjectIndexer — фабрика индексатора для фонового прогона поверх
// buildProjectIndexer (переменная подменяется в hermetic-тестах). Возвращает
// индексатор и его closer — оркестратор application/rag закроет его сам.
func (sess *Session) newProjectIndexer() (apprag.Indexer, io.Closer, error) {
	c, err := buildProjectIndexer()
	if err != nil {
		return nil, nil, err
	}
	return c, c, nil
}

// startIndexBackground — запуск фоновой индексации через оркестратор сессии:
// single-flight по ветке, горутина и отчёты — в application/rag.
// BackgroundIndexer. note — описание источника для логов, branch — ветка.
func (sess *Session) startIndexBackground(note, branch string, run apprag.IndexRun) error {
	return sess.bgIndex.Start(note, branch, run)
}

// branchSuffix — « (ветка X)» для сообщений о фоновой индексации (нужен
// серверным хендлерам для ответа пользователю).
func branchSuffix(branch string) string {
	if b := strings.TrimSpace(branch); b != "" {
		return " (ветка " + b + ")"
	}
	return ""
}

// IndexDirBackground индексирует произвольный каталог проекта (temp/<имя> или
// worktree задачи) под веткой, определённой по его состоянию git. what —
// подпись источника для логов («проект», «worktree задачи T-01»).
func (sess *Session) IndexDirBackground(ctx context.Context, dir, what string) error {
	what = strings.TrimSpace(what)
	if what == "" {
		what = "проект"
	}
	if strings.TrimSpace(dir) == "" {
		return errors.New("не определён каталог проекта для индексации")
	}
	// Ветка/коммит определяются до запуска: они попадают в сообщение о старте,
	// а каталог мог исчезнуть уже после (типовая гонка с очисткой temp/).
	opts := rag.DetectIndexOptions(dir)
	job := apprag.DirJob{Project: sess.project, Dir: dir, What: what, Opts: opts, Sink: ragIndexSink{sess: sess}}
	return sess.startIndexBackground(what, opts.Branch,
		func(bgctx context.Context, indexer apprag.Indexer) (*rag.IndexResult, string, bool, error) {
			return job.Run(bgctx, indexer)
		})
}
