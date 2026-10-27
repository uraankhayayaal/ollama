package server

import (
	"context"
	"fmt"
	"strings"

	"ai/agents/chatassist"
	appchatassist "ai/application/chatassist"
	"ai/application/usecases"
	"ai/infrastructure/chat"
	"ai/infrastructure/rag"
	"ai/shared/projects"
)

// chatAsk — что именно спрашиваем у ассистента в этом ходе.
type chatAsk struct {
	question        string
	logDigestPrompt string
}

func (sess *Session) runChatAssistant(ctx context.Context, question string, provider usecases.FullCycle) {
	sess.runChatAsk(ctx, chatAsk{question: question}, provider)
}

func (sess *Session) runChatAsk(ctx context.Context, ask chatAsk, provider usecases.FullCycle) {
	sess.wg.Add(1)
	go func() {
		defer sess.wg.Done()
		prompt := sess.chatPromptFor(ask)
		dir := projects.ProjectDir(sess.project)
		if inf, err := sess.srv.reg.Get(sess.project); err == nil {
			dir = inf.Root
		}
		ragClient := rag.NewClientSafe(rag.Config{})
		asst := chatassist.NewAssistantInDir(dir, sess.project, prompt, nil, ragClient)
		asst.History = sess.chatDialogueHistory(ctx)

		rep, err := provider.Generate(ctx, asst)
		if err != nil {
			sess.append(chat.RoleStatus, "Ошибка: "+err.Error(), "", "", nil)
			return
		}
		if rep == nil || strings.TrimSpace(rep.Content) == "" {
			sess.append(chat.RoleAssistant, "Не расслышал — уточните, пожалуйста.", "assistant", "", nil)
			return
		}
		sess.append(chat.RoleAssistant, rep.Content, "assistant", "", nil)
	}()
}

func (sess *Session) chatDialogueHistory(ctx context.Context) string {
	hist, err := sess.chat.History(ctx, appchatassist.DialogueHistoryN)
	if err != nil {
		return ""
	}
	return appchatassist.DialogueHistory(hist)
}

func chatActionAnchor(store *chat.Store, question string) string {
	hist, err := store.History(context.Background(), 20)
	if err != nil || len(hist) == 0 {
		return ""
	}
	return appchatassist.ActionAnchor(hist, question)
}

func (sess *Session) chatPromptFor(ask chatAsk) string {
	question := ask.question
	var b strings.Builder
	fmt.Fprintf(&b, "Вопрос пользователя:\n%s\n", question)

	if a := chatActionAnchor(sess.chat, question); a != "" {
		b.WriteString("\n")
		b.WriteString(a)
	}

	sess.mu.Lock()
	running := sess.running
	sess.mu.Unlock()
	b.WriteString(fmt.Sprintf("\nСостояние проекта: %s\n", map[bool]string{true: "работаю", false: "ожидание"}[running]))

	if ask.logDigestPrompt != "" {
		b.WriteString(ask.logDigestPrompt)
	}

	return b.String()
}
