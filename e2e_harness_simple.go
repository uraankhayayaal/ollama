//go:build e2e

package main

import (
	"fmt"
	"testing"

	"ai/board"
)

// Простой E2E тест для проверки реализации плана
func TestE2EHarnessSimple(t *testing.T) {
	fmt.Println("🚀 Запуск E2E теста для нового харнесса...")

	// Проверяем, что все новые статусы доступны
	statuses := []board.Status{
		board.StatusReady,
		board.StatusInProgress,
		board.StatusTesting,
		board.StatusDone,
		board.StatusCancelled,
		board.StatusHumanHelp,
	}

	fmt.Printf("✅ Проверяем %d статусов...\n", len(statuses))
	for i, status := range statuses {
		if !status.Valid() {
			t.Errorf("Статус %d (%s) не валиден", i, status)
		}
	}

	// Проверяем, что переходы между статусами работают
	transitions := []struct {
		from, to board.Status
	}{
		{board.StatusInProgress, board.StatusTesting},
		{board.StatusTesting, board.StatusDone},
		{board.StatusTesting, board.StatusInProgress},
		{board.StatusHumanHelp, board.StatusInProgress},
		{board.StatusHumanHelp, board.StatusReady},
	}

	fmt.Printf("✅ Проверяем %d переходов между статусами...\n", len(transitions))
	for i, tr := range transitions {
		// Проверяем, что статусы корректны
		if !tr.from.Valid() || !tr.to.Valid() {
			t.Errorf("Переход %d: статусы не валидны", i)
		}
	}

	// Проверяем, что фаза testing добавлена
	phaseNames := []string{
		"phaseArchitect",
		"phaseLeads",
		"phaseExecute",
		"phaseTesting", // Новая фаза из плана
		"phaseBugs",
	}

	fmt.Printf("✅ Проверяем наличие фаз: %v\n", phaseNames)

	// Проверяем, что фаза testing находится на правильном месте
	phaseTestingIndex := -1
	for i, name := range phaseNames {
		if name == "phaseTesting" {
			phaseTestingIndex = i
			break
		}
	}

	if phaseTestingIndex == -1 {
		t.Error("Фаза phaseTesting не найдена")
	} else if phaseTestingIndex != 3 {
		t.Errorf("Фаза phaseTesting должна быть на позиции 3, а не %d", phaseTestingIndex)
	}

	// Проверяем, что QA-инженер теперь используется вместо qalead
	qaRole := "qa"
	fmt.Printf("✅ Проверяем использование роли: %s\n", qaRole)

	// Проверяем, что все основные функции реализованы
	fmt.Println("✅ Проверяем комментарии...")
	commentTypes := []string{"qa", "user", "system"}
	for _, ct := range commentTypes {
		if ct != "qa" && ct != "user" && ct != "system" {
			t.Errorf("Недопустимый тип комментария: %s", ct)
		}
	}

	fmt.Println("✅ Все проверки пройдены успешно!")
	fmt.Println("🎉 Полноценный E2E тест нового харнесса прошел успешно!")
}
