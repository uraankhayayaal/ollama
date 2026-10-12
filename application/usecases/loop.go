package usecases

import (
	"ai/domain"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Debugf — приёмник диагностики детектора петель. Внешний слой (runner)
// подключает к нему свой логгер, чтобы application не зависел от инфраструктуры.
var Debugf = func(format string, args ...any) {}

// CallSignature строит нормализованный ключ вызова инструмента (имя +
// нормализованные аргументы) для детекции повторяющихся циклов. encoding/json
// сортирует ключи map, поэтому одинаковые по смыслу аргументы дают тот же ключ.
func CallSignature(name string, args map[string]any) string {
	b, err := json.Marshal(args)
	if err != nil {
		return name
	}
	return name + " " + string(b)
}

// Детектор зацикливания агентского цикла и разрыв петли («задача с другой
// стороны»).
//
// Мягкие подсказки (loopMessage, toolFailMessage, progressNudgeMessage)
// работают только с ПОНИМАЮЩЕЙ моделью. Агент, который не понял свою ошибку
// (не разобрал текст провала, не сумел переформулировать шаг), игнорирует их
// и повторяет тот же вызов, пока не выжигает весь бюджет раундов
// (REVIEW_MAX_ROUNDS в живом .env = 500), после чего цикл уходит в resume с
// той же историей — то есть «зациклился и не предпринял никаких других
// действий».
//
// Поэтому петля фиксируется объективно (по счётчикам, без согласия модели) и
// обрабатывается решением, а не ещё одной подсказкой:
//
//  1. BreakLoop — разрыв петли внутри цикла: зацикленный хвост истории
//     схлопывается, модели уходит структурный дайджест («что уже пробовал,
//     что не сработало, что запрещено, идти с другой стороны»), счётчики
//     перевзводятся — после разрыва пороги считаются заново;
//  2. повтор запрещённого вызова после разрыва — приговор: вернуть Looped
//     (исчерпание разрывов Looped не возвращает: жечь бюджет дальше незачем);
//  3. вызывающий код решает судьбу задачи «с другой стороны»:
//     LayeredProvider эскалирует цикл на большую модель, а оркестратор
//     честно роняет шаг вместо тихого «готово» (см. AgentResponse.Looped).
type loopConfig struct {
	// sigRepeats — сколько раз один и тот же вызов (имя+аргументы) считается
	// петлёй.
	sigRepeats int
	// failStreak — сколько раундов подряд без единого успешного вызова.
	failStreak int
	// noWrite — сколько раундов подряд без мутирующего вызова (чтение
	// разных файлов — тоже «ничего не сделано» для задачи, где надо писать).
	noWrite int
	// strikes — сколько повторов запрещённого вызова после разрыва прощаем.
	strikes int
	// breaks — сколько разрывов петли разрешено за один цикл.
	breaks int
	// digestMax — сколько строк «что уже пробовал» уходит в дайджест.
	digestMax int
	// stateRepeats — сколько раундов подряд проект может НЕ меняться при одной
	// и той же падающей проверке (семантический хеш состояния).
	stateRepeats int
	// stateWarn — на каком повторе неподвижной проверки харнес вмешивается
	// СИЛЬНЫМ предупреждением, не дожидаясь разрыва петли. Должно быть меньше
	// stateRepeats: цель — дать модели chance осознать проблему ДО того, как
	// история будет схлопнута.
	stateWarn int
	// verifyFails — сколько раундов подряд одна и та же проверка (Run с тестом,
	// сборкой или линтом) может падать с тем же выводом до принудительного
	// вмешательства харнеса (verify guard).
	verifyFails int
	// verifyBlockRounds — на сколько раундов после вмешательства блокируется
	// повторный запуск заведомо падающей проверки.
	verifyBlockRounds int
}

// Пороги детектора зацикливания по умолчанию. Переопределяются переменными
// окружения (см. loopConfigFromEnv), чтобы петлю можно было обострить или
// ослабить без правки кода.
const (
	loopSigRepeatsDefault = 3
	loopFailStreakDefault = 5
	loopNoWriteDefault    = 16
	loopStrikesDefault    = 2
	loopBreaksDefault     = 2
	loopDigestMaxDefault  = 8
	// loopStateRepeatsDefault — одинаковая падающая проверка N раундов подряд.
	loopStateRepeatsDefault = 3
	// loopStateWarnDefault — на этом повторе харнес вмешивается предупреждением
	// (за раунд до разрыва петли): состояние не меняется, а модель ещё может
	// свернуть направление сама.
	loopStateWarnDefault = 2
	// loopVerifyFailsDefault — столько одинаковых падений проверки подряд
	// включает принудительное вмешательство.
	loopVerifyFailsDefault = 5
	// loopVerifyBlockRoundsDefault — блокировка повторного запуска проверки.
	loopVerifyBlockRoundsDefault = 1
	// LoopDigestResult — сколько символов результата инструмента попадает
	// в дайджест (ошибка нужна модели, чтобы понять причину провала).
	LoopDigestResult = 240
	// loopMinKeepTail — минимальное число сообщений, которое разрыв обязан
	// оставить в истории (защита от схлопывания системного промпта/задачи).
	loopMinKeepTail = 2
	// loopTaskMax — сколько символов постановки задачи перепечатывается после
	// разрыва. Задача печатается заново САМОЙ ПОСЛЕДНЕЙ, чтобы модель читала
	// её как актуальную, а не как древний кусок истории.
	loopTaskMax = 8000
	// loopRereadMax — сколько файлов перечислять для перечитывания.
	loopRereadMax = 10
)

// loopConfigFromEnv читает пороги детектора из окружения.
func loopConfigFromEnv() loopConfig {
	num := func(key string, def int) int {
		v := strings.TrimSpace(os.Getenv(key))
		if v == "" {
			return def
		}
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		return def
	}
	return loopConfig{
		sigRepeats:        num("LOOP_SIG_REPEATS", loopSigRepeatsDefault),
		failStreak:        num("LOOP_FAIL_STREAK", loopFailStreakDefault),
		noWrite:           num("LOOP_NO_WRITE", loopNoWriteDefault),
		strikes:           num("LOOP_MAX_STRIKES", loopStrikesDefault),
		breaks:            num("LOOP_MAX_BREAKS", loopBreaksDefault),
		digestMax:         num("LOOP_DIGEST_MAX", loopDigestMaxDefault),
		stateRepeats:      num("LOOP_STATE_REPEATS", loopStateRepeatsDefault),
		stateWarn:         num("LOOP_STATE_WARN", loopStateWarnDefault),
		verifyFails:       num("LOOP_VERIFY_FAILS", loopVerifyFailsDefault),
		verifyBlockRounds: num("LOOP_VERIFY_BLOCK_ROUNDS", loopVerifyBlockRoundsDefault),
	}
}

// LoopRoundCall — один вызов инструмента раунда с признаками результата.
type LoopRoundCall struct {
	Name   string
	Sig    string
	Failed bool
	Result string
	// Paths — пути из аргументов вызова: по ним собирается список файлов для
	// перечитывания при разрыве петли.
	Paths []string
	// State — семантический отпечаток проверки этого вызова (пусто, если
	// вызов не проверка или проверка прошла). Одинаковый State при неизменном
	// проекте = петля независимо от аргументов.
	State string
	// StateSample — нормализованный текст падения (для сообщений модели).
	StateSample string
	// StateLabel — команда проверки (для сообщений модели).
	StateLabel string
	// Verify/VerifyCmd — вызов был проверкой и какой была её команда. State
	// пуст у ПРОШЕДШЕЙ проверки, поэтому признак «проверка была» держим
	// отдельно: он нужен State Tracking (agent_state=running_tests и сдвиг
	// last_good_sha), который не заглядывает в содержимое вывода.
	Verify    bool
	VerifyCmd string
	// Blocked — вызов не выполнялся: харнес его заблокировал (verify guard).
	Blocked bool
}

// LoopDetector копит признаки петли по раундам агентского цикла и хранит
// «что уже пробовал» для дайджеста разрыва.
type LoopDetector struct {
	cfg loopConfig

	// parallelSafe — какие инструменты безопасно выполнять параллельно (не
	// мутируют проект). Инъекция из внешнего слоя: application не знает имён
	// инструментов инфраструктуры.
	parallelSafe func(string) bool

	// sigRound — счётчик сигнатуры с момента последнего разрыва (по нему
	// принимается решение о петле); сбрасывается в BreakLoop.
	sigRound map[string]int
	// sigTotal — счётчик сигнатуры за весь цикл (для дайджеста: важно, что
	// модель пыталась это и раньше, до разрыва).
	sigTotal map[string]int
	// lastResult — последний результат по сигнатуре (усечённый) — что
	// именно не сработало.
	lastResult map[string]string
	// toolRound/toolFails — счётчики инструмента с момента разрыва (всего
	// вызовов и провалов): нужны, когда петля выглядит как перебор АРГУМЕНТОВ
	// одного инструмента (сигнатуры разные, повтора нет).
	toolRound map[string]int
	toolFails map[string]int
	// toolLastError — последний провал инструмента (усечённый).
	toolLastError map[string]string
	// bannedSigs/bannedTools — что запрещено повторять: конкретные вызовы
	// (сигнатуры) либо инструмент целиком (когда модель перебирает аргументы).
	bannedSigs  map[string]bool
	bannedTools map[string]bool

	// touched — файлы, к которым агент уже обращался (в порядке первого
	// обращения). После разрыва петли они перечисляются модели для
	// перечитывания: состояние проекта могло уехать после её же правок.
	touched      map[string]bool
	touchedOrder []string

	// failStreak/noWrite — длины текущих серий раундов.
	failStreak int
	noWrite    int
	// rounds — сколько раундов учтено детектором.
	rounds int
	// strikes — повторы запрещённого вызова после разрывов.
	strikes int
	// breaks — выполненные разрывы петли.
	breaks int

	// Семантический отпечаток состояния (state hash). Ключевая идея: петля — это не
	// повтор одного вызова, а отсутствие изменений. Агент может сдвигать
	// строки в ReadFiles, менять ключи в grep и подставлять новые аргументы —
	// сигнатуры у вызовов разные, но проверка, которую он гоняет, та же. Поэтому
	// отпечаток раунда считается по НОРМАЛИЗОВАННОЙ САМОЙ ПРОВЕРКЕ
	// (см. VerifyIdentity), а не по тексту команд и не по выводу: grep меняет
	// вывод каждый раунд и обошёл бы отпечаток по выводу за секунду.
	//
	// Счётчики — ПО ПРОВЕРКЕ, а не по последней: чередование двух падающих
	// проверок (то и другое) не должно обнулять отсчёт каждой.
	stateRuns   map[string]int
	stateSample map[string]string
	stateLabel  map[string]string
	stateWarned map[string]bool
	// verifyErr/verifyRuns — текущая падающая проверка и число раундов подряд с
	// её выводом БЕЗ учёта правок (verify guard: сеть на случай, когда агент
	// меняет файлы, но проверка падает ровно так же).
	verifyErr    string
	verifyRuns   int
	verifyLabel  string
	verifySample string
	// blockedSigs/blockedIdents — проверки, повторный запуск которых харнес
	// временно блокирует (см. blockedVerify); verifyBlock — сколько раундов ещё
	// осталось. Блокировка по нормализованной проверке переживает подмену
	// аргументов: `| grep -A5` после `| grep -B3 -A20` — та же проверка.
	blockedSigs   map[string]string
	blockedIdents map[string]string
	verifyBlock   int
	// injection — приготовленное сообщение о принудительном вмешательстве;
	// забирается вызывающим через TakeInjection.
	injection string
}

// NewLoopDetector создаёт детектор с порогами из окружения. parallelSafe —
// предикат «этот инструмент не мутирует проект» (для счётчика раундов без
// правок); nil трактуется как «всё мутирует», что безопаснее.
func NewLoopDetector(parallelSafe func(string) bool) *LoopDetector {
	if parallelSafe == nil {
		parallelSafe = func(string) bool { return false }
	}
	return &LoopDetector{
		cfg:           loopConfigFromEnv(),
		parallelSafe:  parallelSafe,
		sigRound:      make(map[string]int),
		sigTotal:      make(map[string]int),
		lastResult:    make(map[string]string),
		toolRound:     make(map[string]int),
		toolFails:     make(map[string]int),
		toolLastError: make(map[string]string),
		bannedSigs:    make(map[string]bool),
		bannedTools:   make(map[string]bool),
		touched:       make(map[string]bool),
		stateRuns:     make(map[string]int),
		stateSample:   make(map[string]string),
		stateLabel:    make(map[string]string),
		stateWarned:   make(map[string]bool),
		blockedSigs:   make(map[string]string),
		blockedIdents: make(map[string]string),
	}
}

// BannedSignature сообщает, что вызов запрещён предыдущим разрывом петли:
// либо конкретный вызов (повторялась сигнатура), либо инструмент целиком
// (модель перебирала аргументы — повторов по сигнатуре не набиралось).
func (d *LoopDetector) BannedSignature(sig, name string) bool {
	return d.bannedSigs[sig] || d.bannedTools[name]
}

// RegisterStrike фиксирует повтор запрещённого вызова. Возвращает true, когда
// прощение исчерпано и пора вернуть приговор (AgentResponse.Looped): модель
// получила явный запрет и всё равно повторила то же самое — продолжать цикл
// бессмысленно.
func (d *LoopDetector) RegisterStrike() bool {
	d.strikes++
	return d.strikes >= d.cfg.strikes
}

// ObserveRound принимает результаты раунда и возвращает вердикт детектора:
// непустая строка — петля подтверждена, она же идёт в AgentResponse.LoopReason.
func (d *LoopDetector) ObserveRound(calls []LoopRoundCall) string {
	d.rounds++

	succeeded := false
	wrote := false
	for _, c := range calls {
		d.sigRound[c.Sig]++
		d.sigTotal[c.Sig]++
		d.lastResult[c.Sig] = c.Result
		d.toolRound[c.Name]++
		for _, p := range c.Paths {
			if !d.touched[p] {
				d.touched[p] = true
				d.touchedOrder = append(d.touchedOrder, p)
			}
		}
		if c.Failed {
			d.toolFails[c.Name]++
			d.toolLastError[c.Name] = c.Result
			continue
		}
		// Прогресс — успешный вызов, который ещё не стал повтором:
		// иначе «прочитал 20 файлов по кругу» считался бы успехом.
		if d.sigRound[c.Sig] < d.cfg.sigRepeats {
			succeeded = true
		}
		if !d.parallelSafe(c.Name) {
			wrote = true
		}
	}

	if succeeded {
		d.failStreak = 0
	} else {
		d.failStreak++
	}
	if wrote {
		d.noWrite = 0
	} else {
		d.noWrite++
	}

	// Семантический отпечаток состояния: падающие проверки раунда против
	// прошлого. Если агент что-то изменил (wrote) — состояние другое, отсчёт
	// начинается заново, даже если проверка падает так же. Блокировка проверки
	// самим харнесом отсчёт НЕ сбрасывает: это не «агент перестал проверять»,
	// а наше собственное решение, и сбрасывать по нему нельзя — иначе петля
	// вечно перезапускалась бы счётчиком.
	state := roundState(calls)
	switch {
	case state == "" && !blockedVerify(calls):
		d.resetState()
	case wrote:
		d.resetState()
	default:
		d.stateRuns[state]++
		if sample := stateSampleOf(calls, state); sample != "" {
			d.stateSample[state] = sample
		}
		if label := stateLabelOf(calls, state); label != "" {
			d.stateLabel[state] = label
		}
	}
	if inj := d.stateWarning(state); inj != "" {
		d.injection = inj
	}
	d.noteVerify(calls, state)

	// 1. Один и тот же вызов с одними и теми же аргументами.
	if sig, n := d.worstSignature(); n >= d.cfg.sigRepeats {
		return fmt.Sprintf("вызов %s повторён %d раз с одними и теми же аргументами", SignatureLabel(sig), n)
	}
	// 2. Ни одного успешного действия раунд за раундом.
	if d.failStreak >= d.cfg.failStreak {
		return fmt.Sprintf("последние %d раундов подряд не дали ни одного успешного действия", d.failStreak)
	}
	// 3. Только чтение: задача требует изменений, а агент только изучает.
	if d.noWrite >= d.cfg.noWrite {
		return fmt.Sprintf("последние %d раундов подряд были только чтением — ни одного изменяющего действия", d.noWrite)
	}
	// 4. Состояние не меняется: проверка падает, а модель лишь подкручивает
	// аргументы запуска (строки чтения, ключи grep, флаги вывода).
	if ident, n := d.worstState(); n >= d.cfg.stateRepeats {
		return fmt.Sprintf("состояние проекта не меняется %d раундов подряд: проверка %s падает с тем же результатом (%s)",
			n, ident, truncateWords(d.stateSample[ident], 120))
	}
	return ""
}

// resetState обнуляет отсчёты неподвижного состояния: раунд с реальной
// правкой или без падающей проверки — это другой этап работы.
func (d *LoopDetector) resetState() {
	d.stateRuns = make(map[string]int)
	d.stateSample = make(map[string]string)
	d.stateLabel = make(map[string]string)
	d.stateWarned = make(map[string]bool)
}

// worstState возвращает проверку с наибольшим числом повторов без изменений и
// это число.
func (d *LoopDetector) worstState() (string, int) {
	worst, n := "", 0
	for ident, c := range d.stateRuns {
		if c > n {
			worst, n = ident, c
		}
	}
	return worst, n
}

// stateWarning готовит СИЛЬНОЕ предупреждение модели, когда состояние стоит
// (stateWarn повторов), но до разрыва петли (stateRepeats) ещё не дошло.
// Предупреждение уходит в СЛЕДУЮЩИЙ запрос к модели (см. TakeInjection в
// runner.go) и временно блокирует повторный запуск этой проверки — иначе раунд
// сгорает на бесполезном перезапуске. Повторяться может: счётчик сбрасывается
// вместе с состоянием.
func (d *LoopDetector) stateWarning(state string) string {
	warn := d.cfg.stateWarn
	if warn <= 0 {
		return ""
	}
	if d.cfg.stateRepeats > 0 && warn >= d.cfg.stateRepeats {
		// Предупреждение должно прийти раньше разрыва, иначе оно бессмысленно.
		warn = d.cfg.stateRepeats - 1
	}
	if warn <= 0 || state == "" || d.stateRuns[state] != warn || d.stateWarned[state] {
		return ""
	}
	d.stateWarned[state] = true
	if d.cfg.verifyBlockRounds > 0 {
		// Блокируем по нормализованной проверке: смена grep-флагов не должна
		// открыть дорогу тому же бесполезному перезапуску.
		d.blockedIdents[state] = d.stateSample[state]
		d.verifyBlock = d.cfg.verifyBlockRounds
	}
	return LoopVerifyInjection(d.stateLabel[state], state, d.stateSample[state], warn)
}

// stateSampleOf/stateLabelOf достают текст падения и команду проверки с
// отпечатком.
func stateSampleOf(calls []LoopRoundCall, state string) string {
	for _, c := range calls {
		if !c.Blocked && c.State == state {
			return c.StateSample
		}
	}
	return ""
}

func stateLabelOf(calls []LoopRoundCall, state string) string {
	for _, c := range calls {
		if !c.Blocked && c.State == state {
			return c.StateLabel
		}
	}
	return ""
}

// blockedVerify сообщает, что в раунде не было выполненной проверки, но был
// заблокированный вызов: отсчёт состояния в этом случае не сбрасывается.
func blockedVerify(calls []LoopRoundCall) bool {
	blocked := false
	for _, c := range calls {
		if c.Blocked {
			blocked = true
			continue
		}
		if c.State != "" {
			return false
		}
	}
	return blocked
}

// roundState собирает семантический отпечаток раунда по проверкам: несколько
// проверок склеиваются, поэтому «тесты упали» и «сборка упала» — разные
// состояния.
func roundState(calls []LoopRoundCall) string {
	var parts []string
	for _, c := range calls {
		if c.Blocked || c.State == "" {
			continue
		}
		parts = append(parts, c.State)
	}
	return strings.Join(parts, "|")
}

// noteVerify считает серию одинаковых падений проверки и при достижении
// порога готовит принудительное вмешательство: сообщение модели с диагнозом и
// временную блокировку повторного запуска этой проверки. Счётчик после
// срабатывания обнуляется — вмешательство может повториться.
func (d *LoopDetector) noteVerify(calls []LoopRoundCall, state string) {
	var call *LoopRoundCall
	for i := range calls {
		if calls[i].State != "" && !calls[i].Blocked {
			call = &calls[i]
			break
		}
	}
	if call == nil {
		return
	}
	if state != d.verifyErr {
		d.verifyErr, d.verifyRuns = state, 1
		d.verifyLabel, d.verifySample = call.StateLabel, call.StateSample
		return
	}
	d.verifyRuns++
	if d.verifyRuns < d.cfg.verifyFails {
		return
	}

	runs := d.verifyRuns
	d.verifyRuns = 0
	if d.cfg.verifyBlockRounds > 0 {
		d.blockedSigs[call.Sig] = call.StateSample
		if call.State != "" {
			d.blockedIdents[call.State] = call.StateSample
		}
		d.verifyBlock = d.cfg.verifyBlockRounds
	}
	// Предупреждение по неподвижному состоянию важнее: у него правильный порог
	// (до разрыва петли). Вмешательства в один раунд не дублируем.
	if d.injection == "" {
		d.injection = LoopVerifyInjection(call.StateLabel, call.State, call.StateSample, runs)
	}
}

// TakeInjection забирает подготовленное сообщение о принудительном вмешательстве.
func (d *LoopDetector) TakeInjection() string {
	msg := d.injection
	d.injection = ""
	return msg
}

// BlockedVerifyCall сообщает, что харнес временно не выполняет этот вызов:
// проверка уже столько раундов подряд падала с тем же результатом, что
// повторный запуск только жжёт раунд. Блокировка по НОРМАЛИЗОВАННОЙ проверке,
// поэтому смена аргументов (строки, grep-флаги, `--reporter`) её не обходит.
func (d *LoopDetector) BlockedVerifyCall(name string, args map[string]any) (string, bool) {
	sig := CallSignature(name, args)
	sample, ok := d.blockedSigs[sig]
	if !ok {
		cmd := VerifyCommandOf(name, args)
		if cmd == "" || !LoopIsVerificationCommand(cmd) {
			return "", false
		}
		ident := VerifyIdentity(cmd)
		sample, ok = d.blockedIdents[ident]
		if !ok {
			return "", false
		}
	}
	return "Харнес заблокировал повторный запуск проверки: она уже падала несколько раундов подряд с тем же результатом (" + truncateWords(sample, 160) + "). " +
		"Повторный запуск ничего не изменит — даже если поменять фильтр вывода. Не запускай её снова: сначала найди причину в коде или в самой проверке " +
		"(тест и его ожидания, асинхронные ожидания и таймауты, моки, окружение), исправь и только потом запусти проверку снова.", true
}

// ExpireBlocks уменьшает срок действия временных блокировок проверок.
func (d *LoopDetector) ExpireBlocks() {
	if d.verifyBlock <= 0 {
		return
	}
	d.verifyBlock--
	if d.verifyBlock == 0 {
		d.blockedSigs = make(map[string]string)
		d.blockedIdents = make(map[string]string)
	}
}

// ToolErrorResult оформляет сообщение харнеса результатом инструмента в том же
// JSON-виде, что и настоящие инструменты: модель и проверка toolResultFailed
// видят status=error, а не текст в обход протокола.
func ToolErrorResult(message string) []byte {
	buf, err := json.Marshal(map[string]string{"status": "error", "message": message})
	if err != nil {
		return []byte(`{"status":"error","message":"внутренняя ошибка харнеса"}`)
	}
	return buf
}

// truncateWords обрезает текст по границе слова.
func truncateWords(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndexAny(cut, " \n\t"); i > max/2 {
		cut = cut[:i]
	}
	return cut + "…"
}

// worstSignature возвращает сигнатуру с максимальным числом повторов и это
// число (с момента последнего разрыва).
func (d *LoopDetector) worstSignature() (string, int) {
	worst, n := "", 0
	for sig, c := range d.sigRound {
		if c > n {
			worst, n = sig, c
		}
	}
	return worst, n
}

// CanBreak сообщает, остался ли лимит разрывов петли.
func (d *LoopDetector) CanBreak() bool {
	return d.breaks < d.cfg.breaks
}

// Strikes — число повторов запрещённого вызова (для лога вызывающего кода).
func (d *LoopDetector) Strikes() int { return d.strikes }

// Breaks — сколько разрывов петли уже сделано.
func (d *LoopDetector) Breaks() int { return d.breaks }

// MaxBreaks — разрешённое число разрывов петли за цикл.
func (d *LoopDetector) MaxBreaks() int { return d.cfg.breaks }

// BreakLoop применяет разрыв: запрещает повтор того, что модель долбит,
// перевзводит счётчики раунда и возвращает текст дайджеста для модели.
// taken — количество вызовов, попавших под запрет (для лога).
//
// Под запрет идут сигнатуры с пороговым числом повторов. Если повторов по
// сигнатуре нет (модель перебирает АРГУМЕНТЫ одного инструмента — каждый
// вызов уникален), запрещается сам инструмент: иначе дайджест остался бы без
// конкретики, а модель продолжила бы перебор.
func (d *LoopDetector) BreakLoop(reason, required string) (msg string, taken int) {
	d.breaks++

	sigs := make([]string, 0, len(d.sigRound))
	for sig, n := range d.sigRound {
		if n >= d.cfg.sigRepeats {
			sigs = append(sigs, sig)
		}
	}
	sort.Strings(sigs)
	if len(sigs) > d.cfg.digestMax {
		sigs = sigs[:d.cfg.digestMax]
	}
	for _, sig := range sigs {
		d.bannedSigs[sig] = true
	}
	taken = len(sigs)

	lookup := func(sig string) (int, string) { return d.sigTotal[sig], d.lastResult[sig] }
	if len(sigs) == 0 {
		// Повторов по сигнатуре нет — модель перебирает аргументы одного
		// инструмента. Запрещаем инструменты, которые с момента разрыва НИ
		// РАЗУ не дали успеха: с ними больше нечего пробовать.
		names := make([]string, 0, len(d.toolFails))
		for name, fails := range d.toolFails {
			if fails >= 1 && fails == d.toolRound[name] {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		if len(names) > d.cfg.digestMax {
			names = names[:d.cfg.digestMax]
		}
		for _, name := range names {
			d.bannedTools[name] = true
			sigs = append(sigs, name)
		}
		taken = len(sigs)
		lookup = func(name string) (int, string) { return d.toolRound[name], d.toolLastError[name] }
	}

	// Перевзводим счётчики: после разрыва пороги считаются заново, иначе первая
	// же попытка повторить запрещённое тут же «петля».
	d.sigRound = make(map[string]int)
	d.toolRound = make(map[string]int)
	d.toolFails = make(map[string]int)
	d.failStreak, d.noWrite = 0, 0
	// Отпечаток состояния и серия падений проверки тоже обнуляются: после
	// разрыва модель получила перезапуск и обязана начать с чистого листа, а
	// старые блокировки проверок больше неактуальны.
	d.resetState()
	d.verifyErr, d.verifyRuns, d.verifyBlock = "", 0, 0
	d.blockedSigs = make(map[string]string)
	d.blockedIdents = make(map[string]string)

	return loopBreakMessage(reason, sigs, lookup, required), taken
}

// SignatureLabel превращает ключ сигнатуры в читаемый вид: «WriteFiles {…}».
func SignatureLabel(sig string) string {
	name, args, found := strings.Cut(sig, " ")
	if !found {
		return name
	}
	return name + " " + truncate(args, LoopDigestResult)
}

// loopBreakMessage собирает дайджест разрыва: что модель уже делала и не
// сработало, что запрещено повторять и как продолжить с другой стороны.
// Формулировки намеренно в императиве и с запретом — модель должна получить
// не «совет», а новые правила работы.
func loopBreakMessage(reason string, banned []string, lookup func(string) (int, string), required string) string {
	var b strings.Builder
	b.WriteString("СТОП: ты попал в повторяющийся цикл и не предпринял других действий.\n")
	b.WriteString("Причина: ")
	b.WriteString(reason)
	b.WriteString(".\n\n")

	if len(banned) > 0 {
		b.WriteString("Что ты уже делал и что НЕ сработало (больше так делать нельзя):\n")
		for _, sig := range banned {
			total, res := lookup(sig)
			label := SignatureLabel(sig)
			if res == "" {
				res = "результат без изменений"
			}
			fmt.Fprintf(&b, "- %s — вызовов: %d; последний результат: %s\n", label, total, truncate(res, LoopDigestResult))
		}
		b.WriteString("\n")
	}

	b.WriteString("Продолжи С ДРУГОЙ СТОРОНЫ. Запрещено повторять перечисленные вызовы и подбирать к ним варианты наугад.\n")
	b.WriteString("Выбери один из вариантов и выполни его сейчас:\n")
	b.WriteString("1. Собери недостающую информацию ДРУГИМ способом: другой инструмент, другой файл/источник, другой запрос — и сделай вывод из полученного.\n")
	b.WriteString("2. Разбей работу на части и проверь гипотезу минимальным изменением вместо повторения неудачного шага целиком.\n")
	b.WriteString("3. Если задачу выполнить невозможно — не повторяй попытки: заверши ответ по требуемой схеме и прямо укажи, что именно не удалось и почему (какой вызов, какая ошибка).\n")
	if required != "" {
		fmt.Fprintf(&b, "Обязательное действие всё ещё не выполнено: вызови %q — с другими, корректными данными.\n", required)
	}
	return b.String()
}

// LoopCollapsedMessages схлопывает зацикленный хвост истории: оставляет
// messages[:keep], чтобы модель перестала видеть паттерн, который она
// повторяет, и получала чистый старт от последней точки прогресса.
// prefixLen — число начальных системных/user-сообщений задания: их нельзя
// терять даже если петля началась на первом же раунде.
func LoopCollapsedMessages(messages []domain.Message, keep, prefixLen int) []domain.Message {
	if keep < prefixLen {
		keep = prefixLen
	}
	if keep < loopMinKeepTail {
		keep = loopMinKeepTail
	}
	if keep > len(messages) {
		keep = len(messages)
	}
	if keep == len(messages) {
		return messages
	}
	Debugf("RUNNER: разрыв петли — схлопываю %d сообщений зацикленного хвоста истории (остаюсь на последней точке прогресса)", len(messages)-keep)
	return append([]domain.Message{}, messages[:keep]...)
}

// LoopPrefixLen считает ведущие системные и пользовательские сообщения —
// постановку задачи, которая переживает разрыв петли.
func LoopPrefixLen(messages []domain.Message) int {
	n := 0
	for _, m := range messages {
		if m.Role != "system" && m.Role != "user" {
			break
		}
		n++
	}
	if n < loopMinKeepTail {
		return loopMinKeepTail
	}
	return n
}

// LoopTaskStatement собирает постановку задачи из ведущих сообщений: системный
// промпт переживает разрыв и в перепечатке не нуждается, а вот пользовательские
// сообщения (обычно это и есть задание) печатаются заново.
func LoopTaskStatement(prefix []domain.Message) string {
	var b strings.Builder
	for _, m := range prefix {
		if m.Role != "user" {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(content)
	}
	return b.String()
}

// TouchedFiles — до loopRereadMax путей, к которым агент уже обращался. Хвост
// списка важнее начала: последние правки модели вероятнее всего и нужно
// перечитать.
func (d *LoopDetector) TouchedFiles() []string {
	files := d.touchedOrder
	if len(files) > loopRereadMax {
		files = files[len(files)-loopRereadMax:]
	}
	out := make([]string, len(files))
	copy(out, files)
	return out
}

// LoopRestartMessage собирает указание начать задачу заново: повторная
// постановка (последним сообщением, чтобы она читалась как актуальная), список
// уже тронутых файлов для перечитывания и прямое требование первого действия.
//
// Без этого разрыв давал модели свободу продолжить «по памяти»: постановка
// задачи осталась далеко в начале длинной истории, а состояние файлов — тем
// более. Теперь после разрыва модель обязана вернуться к заданию и увидеть
// реальное содержимое файлов ДО новых правок.
func LoopRestartMessage(task string, files []string, required string) string {
	var b strings.Builder
	b.WriteString("\n\nНАЧНИ ЗАДАЧУ ЗАНОВО С ЭТОГО МОМЕНТА. Предыдущие попытки не считаются верными.\n")

	if strings.TrimSpace(task) != "" {
		b.WriteString("\nПОСТАНОВКА ЗАДАЧИ (перечитай целиком):\n")
		b.WriteString(truncate(strings.TrimSpace(task), loopTaskMax))
		b.WriteString("\n")
	}

	if len(files) > 0 {
		b.WriteString("\nФайлы, которые ты уже трогал — перечитай их заново, они в состоянии после твоих же правок:\n")
		for _, f := range files {
			b.WriteString("- " + f + "\n")
		}
	}

	b.WriteString("\nПЕРВЫЙ ДЕЙСТВИЕ РАУНДА: перечитай эти файлы инструментами чтения")
	if len(files) == 0 {
		b.WriteString(" (List/ReadMap, чтобы сориентироваться в проекте)")
	}
	b.WriteString(" и сверься с постановкой выше. Только после этого продолжай работу — ")
	b.WriteString("другим способом, чем запрещённый выше вызов.\n")
	if required != "" {
		fmt.Fprintf(&b, "Нужное незавершённое действие: вызови %q с корректными данными.\n", required)
	}
	b.WriteString("Если после перечитывания задача окажется невыполнимой — не повторяй вызовы: закончи по требуемой схеме и напиши, что именно не удалось и почему.\n")
	return b.String()
}

// LoopCallPaths вытаскивает из аргументов вызова пути файлов/каталогов —
// достаточно знать, какие файлы агент уже открывал, чтобы предложить ему
// перечитать именно их. Содержимое (ключ content) и команды (key command) сюда
// не попадают: это не пути.
func LoopCallPaths(args any) []string {
	var out []string
	var walk func(v any, key string, depth int)
	walk = func(v any, key string, depth int) {
		if depth > 4 {
			return
		}
		switch t := v.(type) {
		case map[string]any:
			for k, val := range t {
				walk(val, k, depth+1)
			}
		case map[string]string:
			for k, val := range t {
				walk(val, k, depth+1)
			}
		case []any:
			for _, val := range t {
				walk(val, key, depth+1)
			}
		case []map[string]any:
			for _, m := range t {
				walk(m, key, depth+1)
			}
		case string:
			if loopPathKey(key) && loopPathLooksLikePath(t) {
				out = append(out, t)
			}
		}
	}
	walk(args, "", 0)
	return out
}

// loopPathKey — ключ аргумента, под которым лежит путь (а не текст).
// Реальные инструменты называют их по-разному: path/paths (ReadFiles, List,
// ReadMap), files[].filename (WriteFiles), file (LspCheck, LspNav).
func loopPathKey(key string) bool {
	k := strings.ToLower(key)
	switch k {
	case "dir", "dirs", "directory":
		return true
	}
	return strings.Contains(k, "path") || strings.Contains(k, "file")
}

// loopPathLooksLikePath отсеивает мусор: слишком длинные строки, многострочный
// текст и URL — это не пути. Короткое слово без пробелов («server») —
// допустимое имя каталога, фразы с пробелами — нет.
func loopPathLooksLikePath(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 160 || strings.ContainsAny(s, "\n\r") {
		return false
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return false
	}
	if strings.Contains(s, "/") || strings.Contains(s, ".") {
		return true
	}
	return !strings.Contains(s, " ")
}
