package acceptor

import (
	"ai/logging"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Accept выполняет приёмку собранного приложения в директории dir:
// определяет тип проекта, собирает его, запускает на cfg.RunTimeout,
// анализирует вывод на предмет ошибок и возвращает структурированный отчёт.
//
// Приёмка полностью детерминирована: не требует вызова LLM. Если приложение
// не проходит приёмку, исполнитель плана передаёт отчёт планировщику для
// составления шагов исправления.
//
// Поддерживаются монорепозитории «фронтенд + бэкенд»: если в корне проекта
// нет файловых маркеров (go.mod/package.json/…), а в прямых подкаталогах
// (например frontend/ и server/) они есть — каждый подпроект принимается
// отдельно, со своей сборкой и своими проверками, а результаты объединяются
// в один отчёт (см. Report.Projects).
func Accept(dir string, cfg Config) *Report {
	projects := DetectProjects(dir)

	// Явно заданные команды через конфиг позволяют принимать проект вообще
	// без файловых маркеров (см. TestAcceptOverriddenCommands): такой каталог
	// трактуем как одиночный (синтетический) проект в корне.
	if len(projects) == 0 &&
		(strings.TrimSpace(cfg.BuildCmd) != "" || strings.TrimSpace(cfg.RunCmd) != "") {
		projects = []ProjectRoot{{Dir: dir, Kind: KindUnknown}}
	}

	switch {
	case len(projects) == 0:
		return unknownProjectReport(dir)
	case len(projects) == 1:
		return acceptOne(dir, dir, projects[0].Kind, cfg)
	default:
		return acceptMany(dir, projects, cfg)
	}
}

// unknownProjectReport собирает отчёт для каталога без опознанного типа
// проекта и без явно заданных команд: принять такое нечего.
func unknownProjectReport(dir string) *Report {
	rep := &Report{
		Project: filepath.Base(dir),
		Verdict: VerdictReject,
		Tool:    string(KindUnknown),
	}
	rep.Issues = append(rep.Issues, Issue{
		Stage:    StageConfig,
		Severity: "error",
		Text:     "не удалось определить тип проекта (нет go.mod, composer.json, package.json, requirements.txt и т.п.) — задайте ACCEPT_BUILD_CMD/ACCEPT_RUN_CMD",
	})
	rep.Summary = summarize(rep)
	return rep
}

// acceptMany принимает монорепозиторий: каждый подпроект (фронтенд/бэкенд)
// приёмка проходит по отдельности со своей сборкой и проверками. Итоговый
// вердикт — approve только если приняты все подпроекты; замечания объединяются
// с префиксом подкаталога (frontend/…, server/…), чтобы планировщик
// исправлений знал, где чинить.
func acceptMany(root string, projects []ProjectRoot, cfg Config) *Report {
	rep := &Report{
		Project: filepath.Base(root),
		Tool:    "монорепозиторий",
		Verdict: VerdictApprove,
	}

	for _, p := range projects {
		sub := acceptOne(root, p.Dir, p.Kind, cfg)
		sub.Project = p.Rel
		rep.Projects = append(rep.Projects, sub)

		if sub.Verdict == VerdictReject {
			rep.Verdict = VerdictReject
		}
		for _, iss := range sub.Issues {
			if iss.File != "" {
				// Анализатор берёт файлы из вывода инструментов (например
				// "./main.go") — нормализуем перед добавлением префикса
				// подкаталога, чтобы получилось "server/main.go", а не
				// "server/./main.go".
				iss.File = p.Rel + "/" + strings.TrimPrefix(filepath.ToSlash(iss.File), "./")
			}
			rep.Issues = append(rep.Issues, iss)
		}
		logging.Infof("[приёмка] %s: подпроект %q (%s): вердикт %s",
			rep.Project, p.Rel, p.Kind, sub.Verdict)
	}

	rep.Summary = summarize(rep)
	return rep
}

// acceptOne — приёмка одного проекта с известным типом (kind). Вся логика
// одиночной приёмки: установка зависимостей, сборка, стилизатор, анализатор,
// запуск и анализ логов.
//
// Приоритет команд (Р-5 PLAN-2026-09-24-todo-makefile.md): env ACCEPT_* →
// цель корневого Makefile проекта (`make build`/`make run`/`make lint`/`make
// test`) → автодетект по типу. root — корень приёмки (для монорепо — верхняя
// директория, в которой ищется Makefile), dir — директория конкретного проекта.
func acceptOne(root, dir string, kind Kind, cfg Config) *Report {
	rep := &Report{
		Project: filepath.Base(dir),
		Verdict: VerdictApprove,
		Tool:    string(kind),
	}

	// Makefile проекта (поиск от подпроекта вверх до корня приёмки) — единый
	// контракт целей команд для субагентов и приёмки: при наличии цели она
	// приоритетнее автодетекта по типу, env ACCEPT_* — над Makefile.
	mkDir, mk := makefileLocate(root, dir)

	buildCmd := cfg.BuildCmd
	if strings.TrimSpace(buildCmd) == "" {
		if mc := makeCommand(mk, "build"); mc != "" {
			buildCmd = mc
		} else {
			buildCmd = kind.buildCommand(dir)
		}
	}
	runCmd := cfg.RunCmd
	if strings.TrimSpace(runCmd) == "" {
		if mc := makeCommand(mk, "run"); mc != "" {
			runCmd = mc
		} else {
			runCmd = kind.runCommand(dir)
		}
	}

	// Долгоживущие процессы и каталоги: make-цели исполняем в директории
	// расположения Makefile (монорепо — корень, цели сами делают cd), обычные
	// команды — в директории подпроекта.
	buildDir := dir
	if strings.HasPrefix(buildCmd, "make ") && mkDir != "" {
		buildDir = mkDir
	}
	runDir := dir
	if strings.HasPrefix(runCmd, "make ") && mkDir != "" {
		runDir = mkDir
	}

	// Неизвестный тип проекта и нет явно заданных команд — принимать нечего:
	// без маркеров (go.mod/package.json/…) приёмка бессмысленна.
	if kind == KindUnknown && strings.TrimSpace(buildCmd) == "" && strings.TrimSpace(runCmd) == "" {
		return unknownProjectReport(dir)
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
				logging.Detailf("[приёмка] %s: установка %q пропущена (инструмент недоступен)", rep.Project, installCmd)
			} else if timedOut || code != 0 {
				msg := "установка зависимостей завершилась с ошибкой"
				if timedOut {
					msg = "установка зависимостей превысила таймаут"
				}
				rep.Verdict = VerdictReject
				rep.Issues = append(rep.Issues, Issue{Stage: StageInstall, Severity: "error", Text: msg})
				logging.Warnf("[приёмка] %s: установка %q -> ошибка (code=%d timedout=%v)", rep.Project, installCmd, code, timedOut)
			} else {
				rep.Install.OK = true
				logging.Detailf("[приёмка] %s: установка %q -> OK", rep.Project, installCmd)
			}
		}
	}

	if strings.TrimSpace(buildCmd) == "" {
		rep.Build.Skipped = true
		logging.Detailf("[приёмка] %s: сборка не требуется (%s)", rep.Project, rep.Tool)
	} else {
		out, code, timedOut, _ := runCommand(buildDir, buildCmd, cfg.BuildTimeout)
		rep.Build = BuildResult{
			OK:       code == 0 && !timedOut,
			Command:  buildCmd,
			Output:   trimOutput(out, cfg.MaxLog),
			TimedOut: timedOut,
		}
		if !rep.Build.OK && toolMissing(out, code) {
			// Инструмент сборки (npm/go/pip) отсутствует в окружении: это не
			// дефект кода, а свойство хоста — шаг может быть повторён в
			// контейнерном тулчейне инфра-зеркала Makefile (infra.<цель> =
			// docker compose run). Зеркала нет — пропускаем с предупреждением
			// по той же конвенции, что и для установки зависимостей.
			if mirror := makeInfraMirror(mk, buildCmd); mirror != "" {
				mout, mcode, mtimedOut, _ := runCommand(buildDir, mirror, cfg.BuildTimeout)
				logging.Infof("[приёмка] %s: сборка через инфра-зеркало %q (хост-инструмент недоступен)", rep.Project, mirror)
				out, code, timedOut = mout, mcode, mtimedOut
				rep.Build = BuildResult{
					OK:       code == 0 && !timedOut,
					Command:  mirror,
					Output:   trimOutput(out, cfg.MaxLog),
					TimedOut: timedOut,
				}
			}
		}
		if !rep.Build.OK && toolMissing(out, code) {
			rep.Build.Skipped = true
			rep.Build.Output = "инструмент сборки недоступен в окружении — шаг пропущен"
			rep.Issues = append(rep.Issues, Issue{
				Stage:    StageBuild,
				Severity: "warning",
				Text:     "инструмент сборки недоступен — шаг пропущен",
			})
			logging.Warnf("[приёмка] %s: сборка %q пропущена (инструмент недоступен)", rep.Project, rep.Build.Command)
		} else if !rep.Build.OK {
			issues, _ := analyzeOutput(StageBuild, out)
			rep.Issues = append(rep.Issues, issues...)
			msg := "сборка завершилась с ошибкой"
			if timedOut {
				msg = "сборка превысила таймаут"
			}
			rep.Issues = append(rep.Issues, Issue{Stage: StageBuild, Severity: "error", Text: msg})
		}
		logging.Infof("[приёмка] %s: сборка %q -> ok=%v skipped=%v", rep.Project, rep.Build.Command, rep.Build.OK, rep.Build.Skipped)
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
			if mc := makeCommand(mk, "lint"); mc != "" {
				formatCmd, tool = mc, "make lint"
			} else {
				formatCmd, tool = kind.formatCommand(dir)
				if formatCmd == "" {
					tool = "не найден"
				}
			}
		}
		formatDir := dir
		if strings.HasPrefix(formatCmd, "make ") && mkDir != "" {
			formatDir = mkDir
		}
		formatted, fmtIssues := runFormatCheck(formatDir, cfg, formatCmd, tool)
		if r, mi := retryInfraMirror(mk, formatDir, cfg, formatted, runFormatCheck); r != formatted {
			formatted, fmtIssues = r, mi
		}
		rep.Format = formatted
		rep.Issues = append(rep.Issues, fmtIssues...)
		logging.Detailf("[приёмка] %s: стилизатор %q -> %s", rep.Project, formatCmd, formatStatus(rep.Format))
	}

	// Проверка анализатора (go vet/eslint/ruff). Находки — ошибки: ведут к
	// вердикту reject.
	if cfg.CheckAnalyze {
		analyzeCmd, tool := cfg.AnalyzeCmd, "по команде ACCEPT_ANALYZE_CMD"
		if strings.TrimSpace(analyzeCmd) == "" {
			if mc, mt := makeAnalyzeCommand(mk); mc != "" {
				analyzeCmd, tool = mc, mt
			} else {
				analyzeCmd, tool = kind.analyzeCommand(dir)
				if analyzeCmd == "" {
					tool = "не найден"
				}
			}
		}
		analyzeDir := dir
		if strings.HasPrefix(analyzeCmd, "make ") && mkDir != "" {
			analyzeDir = mkDir
		}
		analyzed, anIssues := runAnalyzeCheck(analyzeDir, cfg, analyzeCmd, tool)
		if r, mi := retryInfraMirror(mk, analyzeDir, cfg, analyzed, runAnalyzeCheck); r != analyzed {
			analyzed, anIssues = r, mi
		}
		rep.Analyze = analyzed
		rep.Issues = append(rep.Issues, anIssues...)
		if !rep.Analyze.OK && !rep.Analyze.Skipped {
			rep.Verdict = VerdictReject
		}
		logging.Detailf("[приёмка] %s: анализатор %q -> %s", rep.Project, analyzeCmd, formatStatus(rep.Analyze))
	}

	// Точечная ЛСП-диагностика (нативные publishDiagnostics языкового сервера):
	// отдаёт планировщику точные файлы/строки, из которых строятся точечные
	// scope для шагов исправления. Замечания — analyze-ошибки (reject);
	// сервер не установлен — пропускаем без сбоя.
	if cfg.CheckLSP {
		lsp, lspIssues := runLSPCheck(dir)
		rep.LSP = lsp
		rep.Issues = append(rep.Issues, lspIssues...)
		if !lsp.OK && !lsp.Skipped {
			rep.Verdict = VerdictReject
		}
		logging.Detailf("[приёмка] %s: ЛСП-диагностика %q -> %s", rep.Project, lsp.Tool, formatStatus(lsp))
	}

	if strings.TrimSpace(runCmd) == "" {
		// Точка входа не найдена (например, проект — библиотека): приёмка
		// по сборке считается успешной, но помечаем предупреждение.
		rep.Issues = append(rep.Issues, Issue{
			Stage:    StageConfig,
			Severity: "warning",
			Text:     "точка входа для запуска не найдена — приёмка выполнена только по сборке",
		})
		logging.Warnf("[приёмка] %s: точка входа для запуска не найдена, приёмка по сборке", rep.Project)
		rep.Summary = summarize(rep)
		return rep
	}

	out, code, timedOut, _ := runCommand(runDir, runCmd, cfg.RunTimeout)
	run := &RunResult{
		Command:    runCmd,
		Output:     trimOutput(out, cfg.MaxLog),
		ExitCode:   code,
		ServerMode: timedOut,
		OK:         false,
	}
	if toolMissing(out, code) {
		// Инструмент запуска (node/npm) отсутствует в окружении: пропускаем
		// запуск с предупреждением, как и для установки/сборки. Инфра-зеркало
		// для запуска НЕ применяется (долгоживущие процессы в контейнере
		// не завершаются сами).
		run.Skipped = true
		run.OK = true
		run.Output = "инструмент запуска недоступен в окружении — шаг пропущен"
		rep.Run = run
		rep.Issues = append(rep.Issues, Issue{
			Stage:    StageRun,
			Severity: "warning",
			Text:     "инструмент запуска недоступен — шаг пропущен",
		})
		logging.Warnf("[приёмка] %s: запуск %q пропущен (инструмент недоступен)", rep.Project, runCmd)
		rep.Summary = summarize(rep)
		return rep
	}
	if timedOut {
		// Долгоживущий процесс (сервер) жив дольше таймаута и завершён
		// принудительно. Ошибка от runCommand здесь — это всегда ctx.Err()
		// от таймаута, а не падение приложения, поэтому запуск считается
		// успешным, если в логах нет критичных маркеров (проверяем ниже).
		run.OK = true
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

	logging.Infof("[приёмка] %s: запуск %q -> ok=%v code=%d timedout=%v issues=%d",
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

// makeToolMissingRE — маркер недоступного инструмента внутри make-рецепта:
// make возвращает собственный код 2, но в выводе печатает «Error/Ошибка 127».
var makeToolMissingRE = regexp.MustCompile(`(?m)(Ошибка|Error) 127\b`)

// toolMissing определяет, что команда упала на недоступном инструменте хоста
// (isToolMissing), в т.ч. при исполнении через make (makeToolMissingRE).
func toolMissing(out string, code int) bool {
	return isToolMissing(out, code) || makeToolMissingRE.MatchString(out)
}

// retryInfraMirror перезапускает прикладную проверку через зеркальную
// инфра-цель ('make infra.<цель>' = docker compose run) в случаях, когда
// хост-инструмент недоступен (см. isToolMissing). Возвращает исходный
// результат, если зеркала нет, или если и зеркальная команда недоступна:
// тогда шаг, как и раньше, остаётся пропущенным с предупреждением
// (Р-6 PLAN-2026-09-24-todo-makefile.md; для run зеркало не используется).
func retryInfraMirror(targets map[string]bool, execDir string, cfg Config, res *CheckResult, run func(string, Config, string, string) (*CheckResult, []Issue)) (*CheckResult, []Issue) {
	if res == nil || res.Command == "" || !strings.HasPrefix(res.Command, "make ") {
		return res, nil
	}
	missing := res.Skipped && strings.Contains(res.Output, "недоступен")
	if !missing && !res.OK && toolMissing(res.Output, 0) {
		missing = true
	}
	if !missing {
		return res, nil
	}
	mirror := makeInfraMirror(targets, res.Command)
	if mirror == "" {
		return res, nil
	}
	mres, mIssues := run(execDir, cfg, mirror, strings.TrimPrefix(mirror, "make "))
	if mres == nil {
		return res, nil
	}
	if mres.Skipped && strings.Contains(mres.Output, "недоступен") {
		return res, nil
	}
	if !mres.OK && toolMissing(mres.Output, 0) {
		// Зеркало тоже недоступно — оставляем исходный пропущенный результат.
		return res, nil
	}
	return mres, mIssues
}

// summarize составляет краткую однострочную сводку результата приёмки.
func summarize(rep *Report) string {
	// Монорепозиторий: сводка собирается из сводок подпроектов, каждый —
	// со своим префиксом (frontend/server), чтобы было видно, где что упало.
	if len(rep.Projects) > 0 {
		var parts []string
		for _, pr := range rep.Projects {
			parts = append(parts, pr.Project+": "+pr.Summary)
		}
		return strings.Join(parts, "; ")
	}

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
	if rep.LSP != nil {
		switch {
		case rep.LSP.Skipped:
			parts = append(parts, "ЛСП: пропущен")
		case rep.LSP.OK:
			parts = append(parts, "ЛСП OK")
		default:
			parts = append(parts, "ЛСП: ОШИБКА")
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
