package acceptor

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"
)

// Accept выполняет приёмку собранного приложения в директории dir:
// определяет тип проекта, собирает его, запускает на cfg.RunTimeout,
// анализирует вывод на предмет ошибок и возвращает структурированный отчёт.
//
// Приёмка полностью детерминирована: не требует вызова LLM. Если приложение
// не проходит приёмку, исполнитель плана передаёт отчёт планировщику для
// составления шагов исправления.
func Accept(dir string, cfg Config) *Report {
	rep := &Report{
		Project: filepath.Base(dir),
		Verdict: VerdictApprove,
	}

	kind := DetectKind(dir)
	rep.Tool = string(kind)

	buildCmd := cfg.BuildCmd
	if strings.TrimSpace(buildCmd) == "" {
		buildCmd = kind.buildCommand(dir)
	}
	runCmd := cfg.RunCmd
	if strings.TrimSpace(runCmd) == "" {
		runCmd = kind.runCommand(dir)
	}

	// Неизвестный тип проекта и нет явно заданных команд — принимать нечего:
	// без маркеров (go.mod/package.json/…) приёмка бессмысленна.
	if kind == KindUnknown && strings.TrimSpace(buildCmd) == "" && strings.TrimSpace(runCmd) == "" {
		rep.Verdict = VerdictReject
		rep.Issues = append(rep.Issues, Issue{
			Stage:    StageConfig,
			Severity: "error",
			Text:     "не удалось определить тип проекта (нет go.mod, package.json, requirements.txt и т.п.) — задайте ACCEPT_BUILD_CMD/ACCEPT_RUN_CMD",
		})
		rep.Summary = summarize(rep)
		return rep
	}

	// Установка зависимостей перед сборкой. Недоступный инструмент установки —
	// не ошибка (шаг пропускается), а вот упавшая установка ведёт к reject.
	if cfg.InstallDeps {
		installCmd := cfg.InstallCmd
		if strings.TrimSpace(installCmd) == "" {
			installCmd, _ = kind.installCommand(dir)
		}
		if strings.TrimSpace(installCmd) != "" {
			out, code, timedOut, _ := runCommand(dir, installCmd, cfg.InstallTimeout)
			rep.Install = &InstallResult{
				Command: installCmd,
				Output:  trimOutput(out, cfg.MaxLog),
			}
			if isToolMissing(out, code) {
				rep.Install.Skipped = true
				rep.Install.Output = "инструмент установки недоступен в окружении — шаг пропущен"
				rep.Issues = append(rep.Issues, Issue{
					Stage:    StageInstall,
					Severity: "warning",
					Text:     "инструмент установки зависимостей недоступен — шаг пропущен",
				})
				log.Printf("[Accept] %s: установка %q пропущена (инструмент недоступен)", rep.Project, installCmd)
			} else if timedOut || code != 0 {
				msg := "установка зависимостей завершилась с ошибкой"
				if timedOut {
					msg = "установка зависимостей превысила таймаут"
				}
				rep.Verdict = VerdictReject
				rep.Issues = append(rep.Issues, Issue{Stage: StageInstall, Severity: "error", Text: msg})
				log.Printf("[Accept] %s: установка %q -> ошибка (code=%d timedout=%v)", rep.Project, installCmd, code, timedOut)
			} else {
				rep.Install.OK = true
				log.Printf("[Accept] %s: установка %q -> OK", rep.Project, installCmd)
			}
		}
	}

	if strings.TrimSpace(buildCmd) == "" {
		rep.Build.Skipped = true
		log.Printf("[Accept] %s: сборка не требуется (%s)", rep.Project, rep.Tool)
	} else {
		out, code, timedOut, err := runCommand(dir, buildCmd, cfg.BuildTimeout)
		rep.Build = BuildResult{
			OK:       code == 0 && !timedOut,
			Command:  buildCmd,
			Output:   trimOutput(out, cfg.MaxLog),
			TimedOut: timedOut,
		}
		if !rep.Build.OK {
			issues, _ := analyzeOutput(StageBuild, out)
			rep.Issues = append(rep.Issues, issues...)
			msg := "сборка завершилась с ошибкой"
			if timedOut {
				msg = "сборка превысила таймаут"
			}
			if err != nil && timedOut {
				rep.Issues = append(rep.Issues, Issue{Stage: StageBuild, Severity: "error", Text: msg})
			} else if !timedOut {
				rep.Issues = append(rep.Issues, Issue{Stage: StageBuild, Severity: "error", Text: msg})
			}
		}
		log.Printf("[Accept] %s: сборка %q -> ok=%v", rep.Project, buildCmd, rep.Build.OK)
	}

	// Если сборка уже упала — запуск не имеет смысла: фиксируем вердикт и
	// не тратим время на обречённый запуск.
	if !rep.Build.OK && !rep.Build.Skipped {
		rep.Verdict = VerdictReject
		rep.Summary = summarize(rep)
		return rep
	}

	// Проверка стилизатора (gofmt/prettier/black). Нарушения формата — только
	// предупреждения: вердикт они не меняют, но попадают в отчет и план
	// исправлений.
	if cfg.CheckFormat {
		formatCmd, tool := cfg.FormatCmd, "по команде ACCEPT_FORMAT_CMD"
		if strings.TrimSpace(formatCmd) == "" {
			formatCmd, tool = kind.formatCommand(dir)
			if formatCmd == "" {
				tool = "не найден"
			}
		}
		formatted, fmtIssues := runFormatCheck(dir, cfg, formatCmd, tool)
		rep.Format = formatted
		rep.Issues = append(rep.Issues, fmtIssues...)
		log.Printf("[Accept] %s: стилизатор %q -> %s", rep.Project, formatCmd, formatStatus(rep.Format))
	}

	// Проверка анализатора (go vet/eslint/ruff). Находки — ошибки: ведут к
	// вердикту reject.
	if cfg.CheckAnalyze {
		analyzeCmd, tool := cfg.AnalyzeCmd, "по команде ACCEPT_ANALYZE_CMD"
		if strings.TrimSpace(analyzeCmd) == "" {
			analyzeCmd, tool = kind.analyzeCommand(dir)
			if analyzeCmd == "" {
				tool = "не найден"
			}
		}
		analyzed, anIssues := runAnalyzeCheck(dir, cfg, analyzeCmd, tool)
		rep.Analyze = analyzed
		rep.Issues = append(rep.Issues, anIssues...)
		if !rep.Analyze.OK && !rep.Analyze.Skipped {
			rep.Verdict = VerdictReject
		}
		log.Printf("[Accept] %s: анализатор %q -> %s", rep.Project, analyzeCmd, formatStatus(rep.Analyze))
	}

	if strings.TrimSpace(runCmd) == "" {
		// Точка входа не найдена (например, проект — библиотека): приёмка
		// по сборке считается успешной, но помечаем предупреждение.
		rep.Issues = append(rep.Issues, Issue{
			Stage:    StageConfig,
			Severity: "warning",
			Text:     "точка входа для запуска не найдена — приёмка выполнена только по сборке",
		})
		log.Printf("[Accept] %s: точка входа для запуска не найдена, приёмка по сборке", rep.Project)
		rep.Summary = summarize(rep)
		return rep
	}

	out, code, timedOut, err := runCommand(dir, runCmd, cfg.RunTimeout)
	run := &RunResult{
		Command:    runCmd,
		Output:     trimOutput(out, cfg.MaxLog),
		ExitCode:   code,
		ServerMode: timedOut,
		OK:         false,
	}
	if timedOut {
		if err == nil {
			run.OK = true
		}
	} else {
		run.OK = code == 0
	}

	issues, critical := analyzeOutput(StageRun, out)
	// В server-режиме (процесс жив до таймаута) запуск считается успешным,
	// если в логах нет критичных маркеров падения.
	if timedOut && critical {
		run.OK = false
	}
	// Процесс завершился нулевым кодом, но в логе паника/Traceback — считаем
	// запуск проваленным.
	if !timedOut && code == 0 && critical {
		run.OK = false
	}
	rep.Run = run
	rep.Issues = append(rep.Issues, issues...)

	log.Printf("[Accept] %s: запуск %q -> ok=%v code=%d timedout=%v issues=%d",
		rep.Project, runCmd, run.OK, run.ExitCode, timedOut, len(issues))

	if !run.OK {
		rep.Verdict = VerdictReject
		if len(issues) == 0 {
			rep.Issues = append(rep.Issues, Issue{
				Stage:    StageRun,
				Severity: "error",
				Text:     "запуск завершился с ошибкой",
			})
		}
	}

	rep.Summary = summarize(rep)
	return rep
}

// summarize составляет краткую однострочную сводку результата приёмки.
func summarize(rep *Report) string {
	var parts []string
	if rep.Install != nil {
		switch {
		case rep.Install.Skipped:
			parts = append(parts, "установка пропущена")
		case rep.Install.OK:
			parts = append(parts, "установка OK")
		default:
			parts = append(parts, "установка ОШИБКА")
		}
	}
	if rep.Build.Skipped {
		parts = append(parts, "сборка пропущена")
	} else if rep.Build.OK {
		parts = append(parts, "сборка OK")
	} else {
		parts = append(parts, "сборка ОШИБКА")
	}
	if rep.Run != nil {
		if rep.Run.OK {
			parts = append(parts, "запуск OK")
		} else {
			parts = append(parts, "запуск ОШИБКА")
		}
	}
	if rep.Format != nil {
		switch {
		case rep.Format.Skipped:
			parts = append(parts, "стиль: пропущен")
		case rep.Format.OK:
			parts = append(parts, "стиль OK")
		default:
			parts = append(parts, "стиль: нарушен")
		}
	}
	if rep.Analyze != nil {
		switch {
		case rep.Analyze.Skipped:
			parts = append(parts, "анализ: пропущен")
		case rep.Analyze.OK:
			parts = append(parts, "анализ OK")
		default:
			parts = append(parts, "анализ: ОШИБКА")
		}
	}
	n := len(rep.Issues)
	if n == 0 {
		parts = append(parts, "замечаний нет")
	} else {
		parts = append(parts, fmt.Sprintf("замечаний: %d", n))
	}
	return strings.Join(parts, ", ")
}
