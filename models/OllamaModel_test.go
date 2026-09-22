package models

import (
	"ai/runner"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// Отмена контекста (остановка оркестрации пользователем, graceful shutdown,
// таймаут шага) должна оставаться различимой через errors.Is: server/session
// по этому отличает «оркестрация остановлена» от «прервана ошибкой» и не пугает
// пользователя статусом error при обычной остановке.
func TestChatStreamCanceledContextIsWrappable(t *testing.T) {
	u, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("url: %v", err)
	}
	p := &OllamaProvider{client: api.NewClient(u, http.DefaultClient), model: "test", settings: ModelSettings{InputTokens: 1024}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = p.ChatStream(ctx, &testAgent{}, []runner.Message{{Role: "user", Content: "привет"}}, nil)
	if err == nil {
		t.Fatal("ожидалась ошибка отменённого контекста")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ошибка должна разворачиваться в context.Canceled, got %v", err)
	}
	if !strings.Contains(err.Error(), "Ошибка выполнения Chat") {
		t.Fatalf("контекст сообщения провайдера потерян: %v", err)
	}
}
