package domain

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
)

// Этот файл держит git-понятия, которые нужны application-слою (gitflow/rag) и
// не являются инфраструктурными деталями: данные слияния веток, доменная ошибка
// конфликта, санитизация имени ветки, префикс фича-ветки задачи и утилиты
// маскирования секретов. infrastructure/git алиасит эти декларации (см.
// infrastructure/git/domain_alias.go), поэтому поведение реализации не меняется,
// но application перестаёт зависеть от infrastructure.

// MergeResult — исход слияния ветки в текущую.
type MergeResult struct {
	// Message — сообщение merge-коммита.
	Message string
	// FastForward — слияние было fast-forward. Всегда false: MergeBranch
	// использует --no-ff, чтобы фича-ветка оставалась видимой в истории.
	FastForward bool
	// AlreadyMerged — вливаемый источник уже является анцестором целевой
	// ветки (ничего менять не нужно). Заполняется MergeFeature.
	AlreadyMerged bool
}

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

// TaskBranchPrefix — префикс фича-ветки задачи (ai/task/<id>). Единственный
// источник правды: сервер строит по нему ветки, а InTaskBranch проверяет по
// нему, что рабочая копия принадлежит задаче.
const TaskBranchPrefix = "ai/task/"

// SanitizeBranchName приводит произвольный ID (эпика/задачи из LLM-схем) к
// допустимому имени git-ветки: буквы/цифры (в т.ч. юникод) сохраняются,
// остальное (пробелы, ~^:?*[\\ и пр.) заменяется на '_'; добиваются точки,
// запрещённые подстроки. Санитизация консервативна: даже странные ID не
// сломают `git branch`.
func SanitizeBranchName(id string) string {
	s := strings.TrimSpace(id)
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	out = strings.Trim(out, "_")
	// git check-ref-format запрещает подстроку ".." — заменяем на '_'.
	for strings.Contains(out, "..") {
		out = strings.ReplaceAll(out, "..", "_")
	}
	// Запрещённые слова git check-ref-format.
	switch out {
	case "", ".":
		return "id"
	}
	return out
}

// ErrWorkflowScope — GitHub отклонил push: у токена форджа (GITHUB_TOKEN)
// нет scope `workflow`, а ветка меняет `.github/workflows/*`. Перехватывается
// вызывающим слоем (авто-MR/авто-мёрдж), чтобы показать на доске понятную
// инструкцию вместо сырого вывода git.
var ErrWorkflowScope = errors.New("у токена форджа нет scope `workflow`: GitHub отклоняет push веток с правками .github/workflows/* — добавь scope `workflow` к GITHUB_TOKEN")

// tokenInURL маскирует userinfo credentialed URL push
// (https://x-access-token:<токен>@host/…) — пароль userinfo не должен
// попадать в логи проекта и ответы REST.
var tokenInURL = regexp.MustCompile(`(://[^:/@\s]+):[^@\s]+@`)

// knownTokens маскирует известные префиксы токенов GitHub/GitLab, если они
// попали в текст не в URL (например, в stderr git или сообщении API-клиента).
var knownTokens = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{16,}|glpat-[A-Za-z0-9_-]{16,}`)

// RedactSecrets скрывает секреты в тексте git-команд и вывода git: пароль в
// userinfo credentialed URL и известные префиксы токенов GitHub/GitLab.
// Вызывается в CLIExecutor, чтобы токен push не оседал в логах и ошибках.
func RedactSecrets(s string) string {
	s = tokenInURL.ReplaceAllString(s, "$1:***@")
	return knownTokens.ReplaceAllString(s, "***")
}
