package domain

// Модель детерминированной приёмки собранного приложения. Типы лежат в
// домене, потому что отчёт приёмки — результат бизнес-проверки (вердикт,
// замечания, шаги исправления), а конкретные команды сборки/запуска остаются
// в инфраструктуре (acceptor).

import (
	"fmt"
	"strings"
)

// Verdict — результат приёмки.
type Verdict string

const (
	// VerdictApprove — приложение собирается, запускается и не содержит
	// ошибок в логах за время наблюдения.
	VerdictApprove Verdict = "approve"
	// VerdictReject — сборка/запуск не удались или в логах найдены
	// критические ошибки. Требуются исправления.
	VerdictReject Verdict = "reject"
)

// Stage — этап приёмки, на котором обнаружена проблема.
type Stage string

const (
	StageBuild   Stage = "build"
	StageRun     Stage = "run"
	StageFormat  Stage = "format"
	StageAnalyze Stage = "analyze"
	StageInstall Stage = "install"
	StageConfig  Stage = "config"
)

// Report — структурированный отчёт приёмки собранного приложения.
// Сериализуется в JSON (для отчёта в CLI) и в текст (для планировщика).
type Report struct {
	Project string         `json:"project"`
	Tool    string         `json:"tool"`
	Verdict Verdict        `json:"verdict"`
	Summary string         `json:"summary"`
	Build   BuildResult    `json:"build"`
	Install *InstallResult `json:"install,omitempty"`
	Run     *RunResult     `json:"run,omitempty"`
	Format  *CheckResult   `json:"format,omitempty"`
	Analyze *CheckResult   `json:"analyze,omitempty"`
	// LSP — результат точечной ЛСП-диагностики (нативные publishDiagnostics
	// языкового сервера). Замечания попадают в Issues как analyze-ошибки.
	LSP    *CheckResult `json:"lsp,omitempty"`
	Issues []Issue      `json:"issues"`
	// Projects — отчёты подпроектов монорепозитория (frontend/, server/).
	// Заполняется, когда корень проекта не имеет файловых маркеров, а его
	// прямые подкаталоги — имеют: каждый подпроект принимается отдельно.
	// Для одиночного проекта список пуст.
	Projects []*Report `json:"projects,omitempty"`
}

// BuildResult — результат сборки проекта.
type BuildResult struct {
	OK       bool   `json:"ok"`
	Command  string `json:"command"`
	Output   string `json:"output"`
	TimedOut bool   `json:"timed_out,omitempty"`
	Skipped  bool   `json:"skipped,omitempty"`
}

// RunResult — результат запуска приложения.
type RunResult struct {
	OK       bool   `json:"ok"`
	Command  string `json:"command"`
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
	// ServerMode — true, если процесс был прерван по таймауту (долгоживущий
	// сервис) и при этом не упал — запуск считается успешным.
	ServerMode bool `json:"server_mode"`
	// Skipped — true, если инструмент запуска отсутствует в окружении
	// (например, node не установлен) — это не дефект кода, шаг пропускается.
	Skipped bool `json:"skipped,omitempty"`
}

// InstallResult — результат установки зависимостей проекта.
type InstallResult struct {
	OK      bool   `json:"ok"`
	Command string `json:"command"`
	Output  string `json:"output"`
	// Skipped — устанавливать нечего (нет манифеста зависимостей) или
	// инструмент установки недоступен в окружении.
	Skipped bool `json:"skipped,omitempty"`
}

// CheckResult — результат проверки стилизатора (gofmt/prettier/black)
// или анализатора (go vet/eslint/ruff).
type CheckResult struct {
	OK      bool   `json:"ok"`
	Command string `json:"command"`
	Output  string `json:"output"`
	Tool    string `json:"tool"`
	// Skipped — инструмент не найден/не настроен для проекта: проверка не
	// выполнялась. Не является ошибкой приёмки.
	Skipped bool `json:"skipped,omitempty"`
}

// Issue — одно замечание приёмки.
type Issue struct {
	Stage    Stage  `json:"stage"`
	Severity string `json:"severity"` // "error" | "warning"
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Text     string `json:"text"`
}

// Вердикт отчёта нельзя вывести напрямую через %v интерфейс в текстовых
// логах удобнее строкой.
func (v Verdict) String() string { return string(v) }

// IssueFiles возвращает уникальные файлы, на которые указывают замечания
// приёмки (acceptor не ограничен областью видимости и может видеть весь
// модуль). Используется планировщиком для построения обновлённой области
// видимости задач исправления.
func (r *Report) IssueFiles() []string {
	if r == nil {
		return []string{}
	}
	seen := map[string]bool{}
	var files []string
	for _, iss := range r.Issues {
		f := strings.TrimSpace(iss.File)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		files = append(files, f)
	}
	return files
}

// IssuesText форматирует замечания в краткий текст для планировщика.
func (r *Report) IssuesText() string {
	if len(r.Issues) == 0 {
		return "замечаний не обнаружено"
	}
	var b strings.Builder
	for i, iss := range r.Issues {
		if i >= 30 {
			fmt.Fprintf(&b, "… и ещё %d замечаний\n", len(r.Issues)-30)
			break
		}
		loc := strings.TrimSpace(iss.File)
		if iss.Line > 0 {
			loc += ":" + fmt.Sprintf("%d", iss.Line)
		}
		if loc != "" {
			fmt.Fprintf(&b, "- [%s] %s %s\n", iss.Severity, loc, iss.Text)
		} else {
			fmt.Fprintf(&b, "- [%s] %s\n", iss.Severity, iss.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// FixPrompt собирает текст задания для планировщика исправлений: описывает
// результат сборки/запуска и список замечаний. Вывод этапов обрезается, чтобы
// не переполнять контекст.
func (r *Report) FixPrompt() string {
	// Монорепозиторий: заголовок и по одному блоку на подпроект, чтобы
	// планировщик знал, где именно чинить (файлы в замечаниях уже имеют
	// префикс подкаталога).
	if len(r.Projects) > 0 {
		var b strings.Builder
		b.WriteString("Монорепозиторий из подпроектов (приёмка каждого отдельная):\n")
		for _, pr := range r.Projects {
			fmt.Fprintf(&b, "- %s (%s): %s\n", pr.Project, pr.Tool, pr.Summary)
		}
		for _, pr := range r.Projects {
			b.WriteString("\n--- Подпроект: ")
			b.WriteString(pr.Project)
			b.WriteString(" ---\n")
			b.WriteString(pr.FixPrompt())
		}
		return b.String()
	}

	var b strings.Builder
	b.WriteString("Тип проекта: ")
	b.WriteString(r.Tool)
	b.WriteString(".\n")

	b.WriteString("\nСборка: ")
	if r.Build.Skipped {
		b.WriteString("не требуется (пропущена)")
	} else if r.Build.OK {
		b.WriteString("успешно")
	} else {
		b.WriteString("ОШИБКА")
	}
	if r.Build.Command != "" {
		fmt.Fprintf(&b, " (команда: %s)\n", r.Build.Command)
	} else {
		b.WriteString("\n")
	}

	if r.Install != nil {
		b.WriteString("\nУстановка зависимостей: ")
		switch {
		case r.Install.Skipped:
			b.WriteString("не требовалась/пропущена")
		case r.Install.OK:
			b.WriteString("успешно")
		default:
			b.WriteString("ОШИБКА")
		}
		fmt.Fprintf(&b, " (команда: %s)\n", r.Install.Command)
		if r.Install.Output != "" {
			fmt.Fprintf(&b, "Вывод установки:\n%s\n", r.Install.Output)
		}
	}
	if r.Build.Output != "" {
		fmt.Fprintf(&b, "\nВывод сборки:\n%s\n", r.Build.Output)
	}

	if r.Run != nil {
		b.WriteString("\nЗапуск: ")
		if r.Run.OK {
			b.WriteString("успешно")
			if r.Run.ServerMode {
				b.WriteString(" (процесс работал до таймаута наблюдения)")
			}
		} else {
			b.WriteString("ОШИБКА")
			if r.Run.ExitCode != 0 {
				fmt.Fprintf(&b, " (код выхода: %d)", r.Run.ExitCode)
			}
		}
		fmt.Fprintf(&b, " (команда: %s)\n", r.Run.Command)
		if r.Run.Output != "" {
			fmt.Fprintf(&b, "\nВывод запуска:\n%s\n", r.Run.Output)
		}
	}

	if r.Format != nil {
		b.WriteString("\nСтилизатор: ")
		if r.Format.Skipped {
			fmt.Fprintf(&b, "не найден (%s), проверка пропущена\n", r.Format.Command)
		} else if r.Format.OK {
			fmt.Fprintf(&b, "OK (%s)\n", r.Format.Tool)
		} else {
			fmt.Fprintf(&b, "нарушения (%s)\n", r.Format.Tool)
			if r.Format.Output != "" {
				fmt.Fprintf(&b, "\nВывод стилизатора:\n%s\n", r.Format.Output)
			}
		}
	}

	if r.Analyze != nil {
		b.WriteString("\nАнализатор: ")
		if r.Analyze.Skipped {
			fmt.Fprintf(&b, "не найден (%s), проверка пропущена\n", r.Analyze.Command)
		} else if r.Analyze.OK {
			fmt.Fprintf(&b, "OK (%s)\n", r.Analyze.Tool)
		} else {
			fmt.Fprintf(&b, "ОШИБКИ (%s)\n", r.Analyze.Tool)
			if r.Analyze.Output != "" {
				fmt.Fprintf(&b, "\nВывод анализатора:\n%s\n", r.Analyze.Output)
			}
		}
	}

	if r.LSP != nil {
		b.WriteString("\nЛСП-диагностика: ")
		if r.LSP.Skipped {
			fmt.Fprintf(&b, "не выполнена (%s)\n", r.LSP.Output)
		} else if r.LSP.OK {
			fmt.Fprintf(&b, "замечаний нет (%s)\n", r.LSP.Tool)
		} else {
			fmt.Fprintf(&b, "ОШИБКИ (%s)\n", r.LSP.Tool)
			// Точные строки уже в «Замечаниях» ниже — здесь не дублируем.
		}
	}

	if text := r.IssuesText(); text != "" && text != "замечаний не обнаружено" {
		fmt.Fprintf(&b, "\nЗамечания:\n%s\n", text)
	}

	return b.String()
}
