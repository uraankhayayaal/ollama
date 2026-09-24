// Мосты-инструменты ассистента к серверным git/канбан-действиям (Ф-3).
//
// Пакет tools не может импортировать server (цикл импортов), поэтому
// инструменты действий (KanbanStart/TaskMerge/EpicRelease/BranchReject)
// реализуют tools.Tool прямо здесь и добавляются в набор ассистента на
// стороне сервера через Set.Add (runChatAssistant), а не через общий реестр.
//
// Подтверждение (Р-3/Р-4): деструктивные операции (мёрдж, релиз, откат ветки)
// оборачиваются единой проверкой явного согласия из чата — если в последнем
// пользовательском сообщении нет слова-согласия, инструмент возвращает
// status=confirm, и модель (по правилу в системном промпте) задаёт
// пользователю вопрос «Подтвердите: ...? (да/нет)». Инфраструктурных HITL-
// затворов для этого не вводится: статус confirm не считается ошибкой
// агентского цикла (runner.toolResultFailed), т.е. раунды не жгутся.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ai/chat"
	"ai/tools"
)

// Имена мостов-инструментов ассистента (вне общего реестра tools).
const (
	actionKanbanStart     = "KanbanStart"
	actionTaskMerge       = "TaskMerge"
	actionEpicRelease     = "EpicRelease"
	actionBranchReject    = "BranchReject"
	actionIndexBackground = "IndexBackground"
)

// ActionsBackend — серверные действия, к которым мосты-инструменты обращаются
// через интерфейс (реализует *Session). Отделяет инструменты от HTTP-хендлеров:
// Execute инструмента не должен знать про декодирование тел и коды ответов.
type ActionsBackend interface {
	// KanbanStart запускает/возобновляет оркестрацию по текущей доске
	// (аналог POST /api/projects/{id}/continue). Безопасно — подтверждения
	// не требует.
	KanbanStart(ctx context.Context) error
	// TaskMerge вливает ветку задачи в релизную ветку её эпика (аналог
	// POST /api/projects/{id}/tasks/{tid}/merge). Деструктивно.
	TaskMerge(ctx context.Context, taskID string) (string, error)
	// EpicRelease вливает релизную ветку эпика в main (аналог
	// POST /api/projects/{id}/epics/{eid}/release). Деструктивно.
	EpicRelease(ctx context.Context, epicID string) (string, error)
	// BranchReject отклоняет текущую фича-ветку git-проекта (аналог
	// POST /api/projects/{id}/reject-branch). Деструктивно.
	BranchReject(ctx context.Context) error
	// IndexBackground запускает фоновую индексацию RAG-памяти проекта
	// (walk + IndexProject, Ф-5, Р-2). Безопасно — подтверждения не требует:
	// прогон идемпотентен (IndexProject сперва очищает точки проекта), не
	// блокирует агентский цикл — вернуться должна сразу.
	IndexBackground(ctx context.Context) error
	// ActionConfirmed сообщает, подтвердил ли пользователь действие в чате
	// (последнее user-сообщение содержит явное согласие, Р-3). Деструктивные
	// мосты вызывают её ПЕРЕД выполнением и возвращают status=confirm иначе.
	ActionConfirmed(ctx context.Context) bool
}

// actionTool — инструмент-мост к серверному действию (Ф-3). Единая обёртка
// поведения: не деструктивные действия выполняются сразу; деструктивные —
// сначала запрашивают подтверждение из чата (ActionsBackend.ActionConfirmed)
// и при его отсутствии возвращают status=confirm вместо выполнения, чтобы
// модель спросила у пользователя «да/нет».
type actionTool struct {
	name        string
	description string
	args        map[string]any // JSON Schema properties
	required    []string
	destructive bool
	run         func(ctx context.Context, args map[string]any) (map[string]any, error)
	b           ActionsBackend
}

func (t *actionTool) Name() string { return t.name }

func (t *actionTool) Definition() tools.ToolDefinition {
	props := t.args
	if props == nil {
		props = map[string]any{}
	}
	return tools.ToolDefinition{
		Name:        t.name,
		Description: t.description,
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             t.required,
			"additionalProperties": false,
		},
	}
}

func (t *actionTool) Execute(args map[string]any) ([]byte, error) {
	ctx := context.Background()
	if t.destructive && !t.b.ActionConfirmed(ctx) {
		return json.Marshal(map[string]any{
			"status":  "confirm",
			"action":  t.name,
			"message": "Действие деструктивное — сначала спроси пользователя: «Подтвердите ...? (да/нет)». Выполняй ТОЛЬКО после явного «да/подтверждаю/ок/делай» в чате.",
		})
	}
	body, err := t.run(ctx, args)
	if err != nil {
		return json.Marshal(map[string]any{"status": "error", "action": t.name, "message": err.Error()})
	}
	body["status"] = "success"
	body["action"] = t.name
	return json.Marshal(body)
}

// actionTools конструирует мосты-инструменты server-действий поверх
// ActionsBackend (Ф-3): KanbanStart — запуск оркестрации по доске (безопасно),
// TaskMerge/EpicRelease/BranchReject — деструктивные git-операции с единой
// обёрткой подтверждения из чата (Р-3). Отделён от Session, чтобы hermetic-
// тесты могли гонять определения над фейковым backend без сети/git.
func actionTools(b ActionsBackend) []tools.Tool {
	return []tools.Tool{
		&actionTool{
			name: actionKanbanStart, b: b,
			description: "Запустить или возобновить оркестрацию по Kanban-доске проекта (продолжить выполнение задач доски). Безопасное действие, подтверждения не требует.",
			run: func(ctx context.Context, _ map[string]any) (map[string]any, error) {
				if err := b.KanbanStart(ctx); err != nil {
					return nil, err
				}
				return map[string]any{"message": "Оркестрация запущена по доске проекта."}, nil
			},
		},
		&actionTool{
			name: actionTaskMerge, b: b, destructive: true,
			description: "Влить ветку задачи в релизную ветку её эпика (git merge + push). Деструктивно — требует подтверждения пользователем в чате.",
			args: map[string]any{
				"task_id": map[string]any{"type": "string", "description": "ID задачи на доске (например CHAT-01, T-01)"},
			},
			required: []string{"task_id"},
			run: func(ctx context.Context, args map[string]any) (map[string]any, error) {
				msg, err := b.TaskMerge(ctx, actionArg(args, "task_id"))
				if err != nil {
					return nil, err
				}
				return map[string]any{"message": msg}, nil
			},
		},
		&actionTool{
			name: actionEpicRelease, b: b, destructive: true,
			description: "Влить релизную ветку эпика в main (git merge + push; кнопка «Залить в main»). Работает только для эпика со статусом done. Деструктивно — требует подтверждения пользователем в чате.",
			args: map[string]any{
				"epic_id": map[string]any{"type": "string", "description": "ID эпика на доске (например CHAT-01, ARCH-01)"},
			},
			required: []string{"epic_id"},
			run: func(ctx context.Context, args map[string]any) (map[string]any, error) {
				msg, err := b.EpicRelease(ctx, actionArg(args, "epic_id"))
				if err != nil {
					return nil, err
				}
				return map[string]any{"message": msg}, nil
			},
		},
		&actionTool{
			name: actionBranchReject, b: b, destructive: true,
			description: "Отклонить текущую фича-ветку git-проекта: удалить ветку на remote и локально, вернуть рабочую копию на базу (gitops.RejectBranch). Деструктивно — требует подтверждения пользователем в чате.",
			run: func(ctx context.Context, _ map[string]any) (map[string]any, error) {
				if err := b.BranchReject(ctx); err != nil {
					return nil, err
				}
				return map[string]any{"message": "Фича-ветка отклонена, рабочая копия возвращена на базу."}, nil
			},
		},
	}
}

// newIndexBackgroundTool — безопасный мост «построить RAG-индекс в фоне»
// (Ф-5, Р-2). Без подтверждения: индексация идемпотентна (IndexProject сперва
// удаляет точки проекта) и не блокирует цикл — executes сразу, работа идёт
// параллельно в Session.IndexBackground, результат отчитывается в чат/лог.
func newIndexBackgroundTool(b ActionsBackend) *actionTool {
	return &actionTool{
		name: actionIndexBackground, b: b,
		description: "Построить/обновить RAG-индекс проекта в фоне (семантическая память для CodeSearch). Вызов не блокирует проектирование: индексация идёт параллельно и идемпотентна. Пока индекс строится, работай ReadMap/ReadFiles/LSP; после завершения в чате появится статус-отчёт (файлы/чанки).",
		run: func(ctx context.Context, _ map[string]any) (map[string]any, error) {
			if err := b.IndexBackground(ctx); err != nil {
				return nil, err
			}
			return map[string]any{
				"message": "Фоновая индексация RAG запущена: продолжай проектирование (ReadFiles/ReadMap/LSP), CodeSearch заработает после построения индекса.",
			}, nil
		},
	}
}

// serverActionTools возвращает мосты-инструменты server-действий, которые
// runChatAssistant добавляет в набор ассистента (Ф-3): см. actionTools.
// AskUser (Ф-1 «спроси пользователя») добавляется рядом, поверх AskBackend.
func (sess *Session) serverActionTools() []tools.Tool {
	ts := actionTools(sess)
	ts = append(ts, &askTool{b: sess})
	return ts
}

// actionArg — строковый аргумент инструмента действий.
func actionArg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}

// --- реализация ActionsBackend на Session ---

// KanbanStart запускает/возобновляет оркестрацию по текущей доске (общая
// механика handleContinue). Раннер работает в board-only режиме: новые эпики
// не создаются, но эпики без задач декомпозируются лидами; при отсутствии
// работы уходит в режим ожидания.
func (sess *Session) KanbanStart(ctx context.Context) error {
	prov, err := sess.srv.provider()
	if err != nil {
		return fmt.Errorf("LLM-провайдер не настроен: %v", err)
	}
	taskText, err := sess.continueTaskText(ctx)
	if err != nil {
		return err
	}
	return sess.start(ctx, taskText, prov)
}

// TaskMerge вливает ветку задачи в релизную ветку её эпика. Возвращает
// человекочитаемый результат для ответа модели. Деструктивно — подтверждение
// обеспечивает обёртка actionTool.
func (sess *Session) TaskMerge(ctx context.Context, taskID string) (string, error) {
	store, err := sess.srv.boardStore(ctx, sess.project)
	if err != nil {
		return "", err
	}
	defer store.Close()
	task, err := store.GetTask(ctx, taskID)
	if err != nil {
		return "", fmt.Errorf("задача %s не найдена", taskID)
	}
	res, err := sess.srv.mergeTaskBranch(ctx, sess.project, task)
	if err != nil {
		return "", err
	}
	branch := sess.srv.epicBranch(sess.project, task)
	sess.log.Infof("gitflow: ассистент мёрджит задачу %s → %s (already=%v)", taskID, branch, res.AlreadyMerged)
	sess.append(chat.RoleStatus,
		fmt.Sprintf("Задача %s влита в релизную ветку %s", taskID, branch), "", "", nil)
	sess.emitBoard("ассистент: мёрдж задачи")
	if res.AlreadyMerged {
		return fmt.Sprintf("Задача %s уже влита в релизную ветку %s", taskID, branch), nil
	}
	return fmt.Sprintf("Задача %s влита в релизную ветку %s", taskID, branch), nil
}

// EpicRelease вливает релизную ветку эпика в main. Возвращает результат для
// ответа модели. Деструктивно — подтверждение обеспечивает обёртка actionTool.
func (sess *Session) EpicRelease(ctx context.Context, epicID string) (string, error) {
	res, source, _ /* main */, err := sess.srv.releaseEpic(ctx, sess.project, epicID)
	if err != nil {
		return "", err
	}
	sess.log.Infof("gitflow: ассистент релизит эпик %s → main (already=%v)", epicID, res.AlreadyMerged)
	sess.emitBoard("ассистент: релиз эпика в main")
	if res.AlreadyMerged {
		return fmt.Sprintf("Релизная ветка %s эпика %s уже влита в main", source, epicID), nil
	}
	return fmt.Sprintf("Эпик %s: релизная ветка %s влита в main", epicID, source), nil
}

// BranchReject отклоняет текущую фича-ветку git-проекта: удаляет на remote и
// локально, возвращает рабочую копию на базу; сбрасывает кэш диффа. Деструк-
// тивно — подтверждение обеспечивает обёртка actionTool.
func (sess *Session) BranchReject(ctx context.Context) error {
	repo, err := sess.srv.repoOf(ctx, sess.project)
	if err != nil {
		return err
	}
	if err := repo.RejectBranch(ctx); err != nil {
		return err
	}
	// Рабочая копия сброшена на базу — кэш диффа устарел.
	sess.srv.diffMu.Lock()
	delete(sess.srv.diffs, sess.project)
	sess.srv.diffMu.Unlock()

	sess.log.Infof("gitflow: ассистент отклоняет ветку %s, копия возвращена на %s", repo.Branch, repo.Base)
	sess.append(chat.RoleStatus,
		fmt.Sprintf("Ветка %s отклонена, рабочая копия возвращена на %s", repo.Branch, repo.Base),
		"", "", nil)
	sess.emitBoard("ассистент: отклонение ветки")
	return nil
}

// ActionConfirmed сообщает, содержит ли последнее пользовательское сообщение
// чата явное согласие на деструктивное действие (Р-3). Токенный матчинг
// вместо regexp: \b в Go работает только для ASCII и не видит кириллицу.
func (sess *Session) ActionConfirmed(ctx context.Context) bool {
	msgs, err := sess.chat.History(ctx, 20)
	if err != nil {
		return false
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != chat.RoleUser {
			continue
		}
		return confirmAffirmative(msgs[i].Content)
	}
	return false
}

// confirmTokens — слова-согласия, по которым деструктивное действие
// ассистента считается подтверждённым пользователем в чате.
var confirmTokens = map[string]bool{
	"да": true, "подтверждаю": true, "подтверждаем": true,
	"ок": true, "окей": true, "о'к": true,
	"делай": true, "делаю": true, "делаем": true,
	"согласен": true, "согласна": true, "разрешаю": true,
	"давай": true, "конечно": true, "вали": true,
	"yes": true, "yep": true,
}

// confirmAffirmative сообщает, содержит ли строка явное согласие на действие.
func confirmAffirmative(s string) bool {
	for _, tok := range strings.Fields(strings.ToLower(s)) {
		if confirmTokens[strings.Trim(tok, " \t,.;:!?—–«»\"'()")] {
			return true
		}
	}
	return false
}
