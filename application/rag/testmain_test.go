package rag

import (
	"os"
	"testing"

	infrarag "ai/infrastructure/rag"
)

// TestMain подключает реальный обходчик проекта (в бою его ставит main.go):
// application-слой больше не импортирует infrastructure/rag, поэтому тесты,
// доходящие до IndexProjectRAG, задают его здесь (Этап 8, срез H3b).
func TestMain(m *testing.M) {
	SetProjectWalker(infrarag.WalkProject)
	os.Exit(m.Run())
}
