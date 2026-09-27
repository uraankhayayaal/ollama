package acceptor

import (
	"sort"
	"strings"
)

// plan.go — единый источник правды о командах проверки проекта (Ф-3).
//
// Проблема, которую он снимает: команды приёмки и команды, которые QA-инженер
// запускает в своём цикле, жили отдельно. Приёмка знала про Makefile, env
// ACCEPT_* и автодетект по типу проекта, а промпт QA описывал это прозой
// («при цели test — запускай её, иначе go test ./...»). Стоило правителю
// забыть одну оговорку — QA писал тесты командой, которую приёмка потом не
// запускала, и «зелёные тесты» ничего не значили.
//
// Здесь ровно та логика, что и в acceptOne (makefileLocate → makeCommand →
// kind.buildCommand/analyzeCommand), только в виде читаемого плана вместо
// немедленного запуска. Расхождение с acceptOne проверяется тестом
// TestVerifyPlanMatchesAcceptCommands — при «починке» одной стороны тест
// упадёт.

// VerifyPlan — команды проверки одного проекта, одинаковые для приёмки и для
// QA-цикла. Dir — корень проекта (монорепо: подпроект), MakefileDir — каталог
// с корневым Makefile (nil, если Makefile нет).
type VerifyPlan struct {
	Dir         string
	Kind        string
	MakefileDir string

	// Build — команда сборки; Analyze — команда статического анализа (пустая,
	// если анализатор не найден: шаг пропускается, а не падает).
	Build   string
	Analyze string
	// Test — команда запуска автотестов (env ACCEPT_TEST_CMD → make test →
	// kind.testCommand). Пустая — запускать нечем: у K8s-манифестов или
	// каталога без манифеста зависимостей тестов нет, и это НЕ дефект.
	Test string
	// Lint — команда форматирования/стиля, пустая если нечего проверять.
	Lint string
	// Start — команда запуска сервиса: та же, что у приёмки (env
	// ACCEPT_RUN_CMD → make run → kind.runCommand). Приёмка выполняет её
	// сам; агенту она нужна, чтобы поднять приложение для проверки в рантайме
	// (у ReadAppLogs есть свой автодетект, но он может ошибиться на монорепо).
	Start string
	// Tool — имя инструмента автотестов (go test, npm test, pytest, phpunit)
	// для отчёта и подсказки по установке; пустое, если тестов нет.
	Tool string
}

// MakeCommand — команда запуска make-цели в корне Makefile (пустая, если
// Makefile или цели нет). Агент запускает её из каталога Makefile.
func (p VerifyPlan) MakeCommand(target string) string {
	if p.MakefileDir == "" {
		return ""
	}
	return makeCommand(map[string]bool{target: true}, target)
}

// TestHint — подсказка для агента о том, как запускать Test: если команда не
// самозавершающаяся, нужен таймаут и ожидаемый вывод.
func (p VerifyPlan) TestHint() string {
	if p.Test == "" {
		return "команда запуска тестов не определилась — выведи её сам по стеку и укажи в отчёте, почему автоопределение не сработало"
	}
	if strings.HasPrefix(p.Test, "make ") {
		return "запусти в каталоге Makefile: " + p.Test + " (цель должна быть самозавершающейся, иначе приёмка зависнет на таймауте)"
	}
	return "запусти с таймаутом: " + p.Test + " (сервис может не завершиться сам — тогда останови его и смотри логи через ReadAppLogs)"
}

// VerifyPlanFor — план проверки для каталога проекта dir в корне приёмки
// root. Приоритет тот же, что в acceptOne: env ACCEPT_* → Makefile →
// автодетект по типу проекта. Только чтение файлов, ничего не запускает.
func VerifyPlanFor(root, dir string, cfg Config) VerifyPlan {
	if cfg == (Config{}) {
		cfg = LoadConfig()
	}
	kind := DetectKind(dir)
	mkDir, mk := makefileLocate(root, dir)
	p := VerifyPlan{
		Dir:         dir,
		Kind:        string(kind),
		MakefileDir: mkDir,
		Build:       firstNonEmpty(cfg.BuildCmd, makeCommand(mk, "build"), kind.buildCommand(dir)),
		Test:        firstNonEmpty(cfg.TestCmd, makeCommand(mk, "test"), kind.runCommand(dir)),
	}
	// Test: env → make test → автодетект. Tool едет вместе с победившим
	// источником, иначе в отчёте появится «go test» для команды, которой
	// никто не запускал.
	tc, autoTool := kind.testCommand(dir)
	switch {
	case strings.TrimSpace(cfg.TestCmd) != "":
		p.Test, p.Tool = cfg.TestCmd, cfg.TestCmd
	case makeCommand(mk, "test") != "":
		p.Test, p.Tool = makeCommand(mk, "test"), "make test"
	default:
		p.Test, p.Tool = tc, autoTool
	}
	// Analyze/Lint повторяют приоритет acceptOne: env → make-цель (analyze
	// включает make test как запасной вариант) → автодетект по типу проекта.
	ac, _ := kind.analyzeCommand(dir)
	mac, _ := makeAnalyzeCommand(mk)
	p.Analyze = firstNonEmpty(cfg.AnalyzeCmd, mac, ac)
	fc, _ := kind.formatCommand(dir)
	p.Lint = firstNonEmpty(cfg.FormatCmd, makeCommand(mk, "lint"), fc)
	if mirror := makeInfraMirror(mk, p.Build); p.Lint == "" {
		// Стилистатор в проекте не настроен — для Go-сервисов приёмка всё
		// равно проверяет сборку контейнерно; показываем именно это.
		p.Lint = mirror
	}
	p.Start = firstNonEmpty(cfg.RunCmd, makeCommand(mk, "run"), kind.runCommand(dir))
	return p
}

// TestTargets — make-цели, найденные в корневом Makefile (для диагностики:
// когда цель есть, но команда отличается от ожидаемой агентом).
func (p VerifyPlan) TestTargets() []string {
	if p.MakefileDir == "" {
		return nil
	}
	_, mk := makefileLocate(p.MakefileDir, p.MakefileDir)
	var out []string
	for t := range mk {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// HasTarget — есть ли такая цель в корневом Makefile (для подсказок агенту:
// «make e2e» лучше набора отдельных команд, но сказать об этом можно только
// зная, что цель есть).
func (p VerifyPlan) HasTarget(target string) bool {
	if p.MakefileDir == "" {
		return false
	}
	_, mk := makefileLocate(p.MakefileDir, p.MakefileDir)
	return mk[target]
}
