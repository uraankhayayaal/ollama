package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"ai/forges"
	"ai/logging"
)

// FileOps — разделяемое состояние файловых инструментов генератора кода
// (WriteFiles, ReadFiles, DeleteFiles, Run, List, AppendFile).
// Держит рабочую директорию (OutputDir) и политики записи, общие для всех
// выбранных агентом инструментов.
type FileOps struct {
	// OutputDir — единственная директория, внутри которой разрешена работа
	// инструментов (защита от выхода за пределы через ".." или абсолютные пути).
	OutputDir string
	// Project — ИМЯ проекта для RAG (payload project_name) и логов. Задаётся
	// оркестрацией явно: OutputDir у специалиста — worktree задачи
	// (temp/.wt-task-<проект>-<id>), и имя из его basename указывало бы на
	// несуществующий проект (пустая выдача CodeSearch, чанки в чужой индекс).
	// Пусто — фолбэк на basename OutputDir (консольные/простые проекты).
	Project string
	// MaxFiles — максимальное число файлов, которое можно записать за запуск.
	// 0 или отрицательное — без лимита.
	MaxFiles int
	// NoOverwrite — запрещает перезаписывать уже существующие файлы.
	NoOverwrite bool
	// Scope — области работы (файлы/директории проекта), в рамках которых
	// разрешены операции. Пустой — без ограничений (весь OutputDir).
	Scope []string
	// scopeMatch — скомпилированный matcher областей; nil — без ограничений.
	scopeMatch *forges.ScopeMatcher
	// WriteAllowlist — allowlist ЗАПИСИ (отдельно от Scope): разрешённые пути
	// для мутаций (Write/Append/Delete/Patch). Scope ограничивает и чтение,
	// а архитектору (этап 4.2 плана) нужен свободный доступ к чтению кода при
	// жёстко сжатой области записи — поэтому проверка раздельная. Пустой —
	// без ограничений (запись определяется Scope, как раньше).
	WriteAllowlist []string
	// writeAllowMatch — скомпилированный allowlist записи; nil/пусто — без
	// ограничений.
	writeAllowMatch *forges.ScopeMatcher
	// WriteReadmeOnly — true: записывающие инструменты (Write/Append/Delete)
	// разрешены ТОЛЬКО для файлов readme* в корне OutputDir. Чтение (ReadFiles,
	// List) не ограничивается. Ставится лидам, чтобы они могли вести план
	// проекта в readme, но не могли писать/удалять код.
	WriteReadmeOnly bool
	// WriteTestsOnly — true: записывающие инструменты (Write/Append/Delete)
	// разрешены ТОЛЬКО для тестовых файлов (isTestPath: *_test.go, *.test.*,
	// *.spec.*, test_*.py, каталоги test/tests/__tests__/e2e и конфиги
	// раннеров) плюс явных дополнительных путей WriteTestsExtra (например,
	// файл-отчёт QA). Чтение (ReadFiles, List) не ограничивается. Ставится
	// QA-инженеру: тестировщик пишет и правит тесты, но НЕ код продукта —
	// найденный дефект он возвращает разработчику комментарием к задаче и
	// статусом in_progress.
	WriteTestsOnly bool
	// WriteTestsExtra — дополнительные точные пути (относительные slash),
	// разрешённые к записи в режиме WriteTestsOnly (файл-отчёт QA). Сравнение
	// как в ScopeMatcher: точный файл или директория-префикс.
	WriteTestsExtra []string
	// writeTestsExtraMatch — скомпилированный WriteTestsExtra; nil — нет
	// дополнительных путей.
	writeTestsExtraMatch *forges.ScopeMatcher
	// written — счётчик записанных файлов (разделяется инструментами).
	written int
	// touched — относительные (slash) пути файлов, затронутых мутацией с
	// момента последнего вызова LspAutoFix. Используется Ф-2 (авто-
	// самоисправление): после раунда с записями раннер проверяет эти файлы
	// LspCheck и подмешивает модели скрытый промпт при ошибках. Ведётся под
	// touchedMu; сама мутация сериализуется пер-проектной блокировкой.
	touched   []string
	touchedMu sync.Mutex
}

// SetScope задаёт области работы для инструментов (нормализует записи через
// forges.CompileScope). Вызов с пустым/nil-слайсом снимает ограничения.
func (ops *FileOps) SetScope(scope []string) {
	ops.Scope = scope
	ops.scopeMatch = forges.CompileScope(scope)
}

// SetWriteAllowlist задаёт allowlist записи: разрешённые пути мутаций
// (Write/Append/Delete/Patch), независимых от Scope. Пустой/nil-слайс снимает
// ограничение (запись определяется Scope). Гард области (этап 4.2 плана) живёт
// в инструменте, а не только в промпте: модель, выйдшая за allowlist, получает
// ошибку, а не молчаливое отклонение.
func (ops *FileOps) SetWriteAllowlist(paths []string) {
	ops.WriteAllowlist = paths
	ops.writeAllowMatch = forges.CompileScope(paths)
}

// SetOutputDir переключает рабочую директорию инструментов на лету. Используется
// оркестрацией (Ф-3): у git-проектов специалист работает в отдельном worktree
// ветки задачи, а не в общей проектной копии. Раннер зовёт метод через интерфейс
// { SetOutputDir(string) } у агента (developer/devops/qaengineer встраивают
// *FileOps), поэтому сигнатура — часть публичного контракта.
// При смене директории сбрасывает touched: файлы из предыдущей ветки могут
// отсутствовать в новой, а переиндексация несуществующих файлов — ошибка.
func (ops *FileOps) SetOutputDir(dir string) {
	ops.OutputDir = dir
	ops.touchedMu.Lock()
	ops.touched = nil
	ops.touchedMu.Unlock()
}

// SetProjectName задаёт имя проекта для RAG-инструментов и переиндексации.
// Оркестрация зовёт метод через интерфейс { SetProjectName(string) } у агента
// вместе с SetOutputDir: у git-проектов специалист работает в worktree задачи,
// имя которого не совпадает с именем проекта в индексе. Пустое имя снимает
// явное значение (фолбэк — basename OutputDir).
func (ops *FileOps) SetProjectName(name string) {
	ops.Project = strings.TrimSpace(name)
}

// ProjectName — имя проекта для RAG: явное Project, иначе basename OutputDir
// (temp/<имя> → "<имя>"; пустой OutputDir — "").
func (ops *FileOps) ProjectName() string {
	if ops == nil {
		return ""
	}
	if name := strings.TrimSpace(ops.Project); name != "" {
		return name
	}
	if ops.OutputDir == "" {
		return ""
	}
	return filepath.Base(filepath.Clean(ops.OutputDir))
}

// WorkingDir — текущая рабочая директория файловых операций (OutputDir).
// Ленивый nil-безопасный аксессор для инструментов, вынесенных в подпакеты
// (tools/rag): OutputDir может меняться на лету (SetOutputDir при переходе
// специалиста в worktree), поэтому подпакеты читают его через интерфейс, а не
// запоминают значение при сборке набора инструментов.
func (ops *FileOps) WorkingDir() string {
	if ops == nil {
		return ""
	}
	return ops.OutputDir
}

// allowed проверяет, разрешён ли файл (относительный slash-путь) областью
// работы. Без установленного scope разрешено всё.
func (ops *FileOps) allowed(rel string) bool {
	if ops.scopeMatch == nil || ops.scopeMatch.Empty() {
		return true
	}
	return ops.scopeMatch.Allow(rel)
}

// dirHasScope сообщает, стоит ли заходить в директорию при обходе
// (внутри неё есть файлы из области работы), даже если сама директория
// в область не входит.
func (ops *FileOps) dirHasScope(rel string) bool {
	if ops.scopeMatch == nil {
		return true
	}
	return ops.scopeMatch.HasInside(rel)
}

// writeAllowed проверяет, разрешена ли ЗАПИСЬ файла (относительный
// slash-путь). В режиме «только readme» (WriteReadmeOnly) разрешены ТОЛЬКО
// файлы readme* в корне OutputDir независимо от Scope (чтение при этом
// продолжает ограничиваться Scope). В режиме «только тесты» (WriteTestsOnly)
// разрешены только тестовые файлы (isTestPath) и дополнительные пути
// WriteTestsExtra. Иначе — allowlist записи (WriteAllowlist, если задан) И
// область работы: обе проверки должны пройти.
func (ops *FileOps) writeAllowed(rel string) bool {
	if ops.WriteReadmeOnly {
		// Разрешён только файл readme* в корне проекта (без вложенных
		// каталогов), независимо от регистра (README.md, readme.md, ...).
		slug := filepath.ToSlash(filepath.Clean(rel))
		if slug == "." || slug == "" || strings.Contains(slug, "/") {
			return false
		}
		return strings.HasPrefix(strings.ToLower(slug), "readme")
	}
	if ops.WriteTestsOnly && !ops.testsWriteAllowed(rel) {
		return false
	}
	if ops.writeAllowMatch != nil && !ops.writeAllowMatch.Empty() && !ops.writeAllowMatch.Allow(rel) {
		return false
	}
	return ops.allowed(rel)
}

// testsWriteAllowed проверяет путь в режиме WriteTestsOnly: тестовый файл
// (isTestPath) либо путь из дополнительного allowlist WriteTestsExtra
// (например, файл-отчёт QA в корне проекта).
func (ops *FileOps) testsWriteAllowed(rel string) bool {
	if isTestPath(rel) {
		return true
	}
	if ops.writeTestsExtraMatch != nil && !ops.writeTestsExtraMatch.Empty() {
		return ops.writeTestsExtraMatch.Allow(rel)
	}
	return false
}

// writeDenyMsg — точная причина отказа записи (для модели): при гарде allowlist
// (этап 4.2 плана) перечисляет разрешённые пути — модель видит границы и может
// исправиться, а не наступать на них вслепую. Без allowlist — прежнее сообщение
// про scope (обратная совместимость).
func (ops *FileOps) writeDenyMsg(name string) string {
	if ops.WriteTestsOnly {
		msg := fmt.Sprintf("QA-инженер пишет только тесты: файл %q не является тестовым. Найденный дефект верни разработчику комментарием к задаче (BoardAddComment) и статусом in_progress (BoardSetTaskStatus)", name)
		if len(ops.WriteTestsExtra) > 0 {
			msg += fmt.Sprintf("; дополнительно разрешено: %s", strings.Join(ops.WriteTestsExtra, ", "))
		}
		return msg
	}
	if ops.writeAllowMatch != nil && !ops.writeAllowMatch.Empty() {
		return fmt.Sprintf("файл %q вне allowlist записи (разрешено: %s)", name, strings.Join(ops.WriteAllowlist, ", "))
	}
	return fmt.Sprintf("файл %q вне области работы (scope: %v)", name, ops.Scope)
}

// relPath возвращает относительный slash-путь файла внутри OutputDir.
func (ops *FileOps) relPath(full string) string {
	rel, err := filepath.Rel(ops.OutputDir, full)
	if err != nil {
		return filepath.ToSlash(full)
	}
	return filepath.ToSlash(rel)
}

// ResolvePath приводит относительный путь к абсолютному в пределах OutputDir
// и защищает от выхода за границу через ".." или абсолютные пути.
func (ops *FileOps) ResolvePath(name string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("абсолютный путь запрещён: %s", name)
	}
	full := filepath.Join(ops.OutputDir, cleaned)
	root := filepath.Clean(ops.OutputDir)
	if !strings.HasPrefix(full, root+string(filepath.Separator)) && full != root {
		return "", fmt.Errorf("путь выходит за пределы OutputDir: %s", name)
	}
	return full, nil
}

// Write создаёт директории при необходимости и записывает файл.
// Мутация сериализуется пер-проектной блокировкой (см. filelock.go): любые две
// записи в каталог проекта выполняются строго последовательно, исключая гонки
// между параллельными шагами волны плана.
func (ops *FileOps) Write(name, content string) error {
	return withProjectLock(ops.OutputDir, func() error {
		return ops.writeLocked(name, content)
	})
}

// writeLocked — тело Write без пер-проектной блокировки. Вызывается только из
// Write (под блокировкой) и тестами напрямую не предполагается.
func (ops *FileOps) writeLocked(name, content string) error {
	full, err := ops.ResolvePath(name)
	if err != nil {
		return err
	}
	if !ops.writeAllowed(ops.relPath(full)) {
		return fmt.Errorf("%s", ops.writeDenyMsg(name))
	}

	if ops.MaxFiles > 0 && ops.written >= ops.MaxFiles {
		return fmt.Errorf("превышен лимит записанных файлов (%d)", ops.MaxFiles)
	}
	if ops.NoOverwrite {
		if _, err := os.Stat(full); err == nil {
			return fmt.Errorf("файл уже существует (%s), перезапись запрещена (CODEGEN_NO_OVERWRITE=true)", name)
		}
	}

	if err := ensureParentDirs(full); err != nil {
		return err
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		return err
	}
	ops.written++
	ops.recordTouched(full)
	logging.Detailf("[WriteFiles] записано %q -> %q", name, full)
	return nil
}

// AppendTo прибавляет текст в конец существующего файла (без полной
// перезаписи). Используется инструментом AppendFile для точечных правок.
// Сериализуется той же пер-проектной блокировкой, что и Write.
func (ops *FileOps) AppendTo(name, content string) error {
	return withProjectLock(ops.OutputDir, func() error {
		return ops.appendToLocked(name, content)
	})
}

// appendToLocked — тело AppendTo без пер-проектной блокировки.
func (ops *FileOps) appendToLocked(name, content string) error {
	full, err := ops.ResolvePath(name)
	if err != nil {
		return err
	}
	if !ops.writeAllowed(ops.relPath(full)) {
		return fmt.Errorf("%s", ops.writeDenyMsg(name))
	}
	if err := ensureParentDirs(full); err != nil {
		return err
	}
	f, err := os.OpenFile(full, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return err
	}
	ops.recordTouched(full)
	logging.Detailf("[AppendFile] дополнен %q -> %q", name, full)
	return nil
}

// ensureParentDirs создаёт родительские каталоги для будущей записи файла
// full и «лечит» файлы-заглушки, блокирующие эти каталоги.
//
// У модели нет инструмента создания директорий: чтобы «создать структуру»,
// она часто вызывает WriteFiles с путём каталога и пустым содержимым. На диске
// тогда появляется ПУСТОЙ ФАЙЛ с именем каталога (например server/internal),
// занимающий имя будущей папки. Любая последующая запись внутрь падает с
// ENOTDIR: «файлы в папке не меняются», шаги циклически повторяются. Эта
// функция устраняет такое состояние: пустая заглушка удаляется, вместо неё
// создаются каталоги. Непустой файл НЕ трогается — это реальный код, а не
// заглушка.
func ensureParentDirs(full string) error {
	dir := filepath.Dir(full)
	for {
		err := os.MkdirAll(dir, 0755)
		if err == nil {
			return nil
		}
		blocker := deepestExistingPath(dir)
		if blocker == "" {
			return fmt.Errorf("не удалось создать директорию %s: %v", dir, err)
		}
		st, serr := os.Lstat(blocker)
		if serr != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("не удалось создать директорию %s: путь %s занят", dir, blocker)
		}
		if st.Size() > 0 {
			return fmt.Errorf("не удалось создать директорию %s: путь %s занят непустым файлом", dir, blocker)
		}
		logging.Warnf("[tools] устранена пустая файловая заглушка %q, блокировавшая запись (директория была «создана» пустым файлом)", blocker)
		if err := os.Remove(blocker); err != nil {
			return fmt.Errorf("не удалось создать директорию %s: %v", dir, err)
		}
		// Продолжаем цикл: MkdirAll(dir) повторяется уже без блокера.
	}
}

// deepestExistingPath возвращает самый глубокий существующий путь на оси
// dir .. корня файловой системы. Если это регулярный файл на месте каталога —
// это и есть предполагаемый блокер (пустая заглушка).
func deepestExistingPath(dir string) string {
	p := dir
	for {
		if _, err := os.Lstat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return ""
		}
		p = parent
	}
}

// ReadResult читает файл и возвращает результат-статус.
func (ops *FileOps) ReadResult(name string) map[string]string {
	full, err := ops.ResolvePath(name)
	if err != nil {
		return map[string]string{"filename": name, "status": "error", "message": err.Error()}
	}
	if !ops.allowed(ops.relPath(full)) {
		return map[string]string{"filename": name, "status": "error", "message": "файл вне области работы (scope)"}
	}
	content, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{"filename": name, "status": "error", "message": "файл не существует в текущей ветке (возможно, он был в предыдущей ветке или ещё не создан)"}
		}
		return map[string]string{"filename": name, "status": "error", "message": err.Error()}
	}
	return map[string]string{"filename": name, "status": "success", "content": string(content)}
}

// Remove удаляет файл или папку и возвращает (результат-статус, ошибку).
// Сериализуется пер-проектной блокировкой: удаление не должно пересекаться
// с параллельной записью в тот же каталог проекта.
func (ops *FileOps) Remove(name string) (map[string]string, error) {
	var res map[string]string
	err := withProjectLock(ops.OutputDir, func() error {
		var rerr error
		res, rerr = ops.removeLocked(name)
		return rerr
	})
	return res, err
}

// removeLocked — тело Remove без пер-проектной блокировки.
func (ops *FileOps) removeLocked(name string) (map[string]string, error) {
	full, err := ops.ResolvePath(name)
	if err != nil {
		return nil, err
	}
	if !ops.writeAllowed(ops.relPath(full)) {
		return map[string]string{"path": name, "status": "error", "message": ops.writeDenyMsg(name)}, nil
	}
	if _, err := os.Stat(full); os.IsNotExist(err) {
		return map[string]string{"path": name, "status": "error", "message": "файл или папка не существует"}, nil
	}
	if err := os.RemoveAll(full); err != nil {
		return nil, err
	}
	ops.recordTouched(full)
	return map[string]string{"path": name, "status": "success"}, nil
}
