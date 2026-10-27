package server

import (
	apprag "ai/application/rag"
	"ai/infrastructure/rag"
	"os"
	"testing"
)

// TestMain подключает RAG-инъекцию фонового индексатора.
func TestMain(m *testing.M) {
	apprag.SetProjectWalker(rag.WalkProject)
	os.Exit(m.Run())
}
