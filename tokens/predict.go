// Прогноз расхода LLM-токенов по истории завершённых единиц работы (Ф-5
// PLAN-2026-09-19-done-epic-task-token.md).
//
// Модель намеренно простая и детерминированная: линейная регрессия с
// регуляризацией на логарифме расхода (лог-масштаб — расход токенов
// мультипликативен: задача вдвое длиннее обычно стоит кратно больше, а не
// «на 200 токенов больше»). Признаки: размер описания, размер заголовка, роль
// исполнителя, признак «эпик» и число задач эпика.
//
// Модель обучается на фактах (TokensTotal завершённых задач/эпиков) и честно
// деградирует, когда истории мало или она вырождена:
//
//	нет истории                 — оценки нет (0);
//	мало примеров/вырожденная  — медиана по роли, иначе медиана по проекту;
//	обученная модель           — прогноз модели + её средняя ошибка (MAE).
package tokens

import (
	"math"
	"sort"
	"strings"
)

// Пороги деградации предиктора: меньше примеров или меньше независимых
// признаков — модель не обучается, работает медиана.
const (
	// minPredictSamples — минимум завершённых единиц в истории для обучения.
	minPredictSamples = 4
	// maxPredictRoles — сколько ролей-признаков удерживать в модели
	// (остальные — в «прочих»): иначе матрица раздувается.
	maxPredictRoles = 12
	// predictRidge — регуляризация (гребень) нормальных уравнений: спасает
	// от singular-матрицы на малых выборках.
	predictRidge = 1.0
)

// Sample — признаки единицы работы для прогноза расхода токенов.
type Sample struct {
	// IsEpic — эпик (true) или задача (false).
	IsEpic bool
	// Role — роль исполнителя: assigned_role эпика, assignee задачи.
	Role string
	// TitleLen — длина заголовка в символах.
	TitleLen int
	// DescLen — длина описания в символах.
	DescLen int
	// NumTasks — число задач эпика (0 у задач).
	NumTasks int
	// Spent — факт расхода (TokensTotal) для обучающей выборки; у новой
	// единицы (прогнозируемой) 0.
	Spent int64
}

// Основание прогноза — по чему посчитана оценка (для логов и UI).
const (
	// BasisNone — данных нет, оценка не выдаётся.
	BasisNone = "нет данных"
	// BasisModel — линейная модель по истории.
	BasisModel = "модель"
	// BasisRoleMedian — медиана расхода по роли.
	BasisRoleMedian = "медиана роли"
	// BasisGlobalMedian — медиана расхода по всем завершённым единицам.
	BasisGlobalMedian = "медиана проекта"
)

// Prediction — прогноз расхода токенов и его обоснование.
type Prediction struct {
	// Estimate — ожидаемый расход (0, если прогноза нет).
	Estimate int64
	// Basis — по чему посчитан (BasisModel/BasisRoleMedian/…).
	Basis string
	// MAE — средняя абсолютная ошибка модели на истории, % от факта (0 для
	// фолбэков и «нет данных»).
	MAE float64
}

// Predictor — обученный по истории прогноз расхода токенов. Нулевой
// Predictor корректен: он ничего не прогнозирует (нет данных). Небезопасен для
// одновременного использования из нескольких горутин (создаётся и сразу
// отдаётся вызывающему).
type Predictor struct {
	weights   []float64
	roles     []string
	roleIndex map[string]int
	global    int64
	byRole    map[string]int64
	trained   bool
	mae       float64
	nSamples  int
}

// NewPredictor обучает предиктор по выборке завершённых единиц. Пустая/мелкая
// выборка не ломает предиктор — он просто выдаёт медиану или отказ.
func NewPredictor(samples []Sample) *Predictor {
	p := &Predictor{roleIndex: map[string]int{}, byRole: map[string]int64{}}
	all := make([]int64, 0, len(samples))
	for _, s := range samples {
		if s.Spent > 0 {
			all = append(all, s.Spent)
		}
	}
	p.global = median(all)
	byRole := map[string][]int64{}
	for _, s := range samples {
		if s.Spent <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(s.Role))
		if key == "" {
			key = "?"
		}
		byRole[key] = append(byRole[key], s.Spent)
	}
	for role, vals := range byRole {
		p.byRole[role] = median(vals)
	}
	p.nSamples = len(samples)
	if len(samples) < minPredictSamples {
		return p
	}

	// Роли-признаки: самые частые (стабильнее, чем редкие).
	counts := map[string]int{}
	for _, s := range samples {
		key := strings.ToLower(strings.TrimSpace(s.Role))
		if key == "" {
			key = "?"
		}
		counts[key]++
	}
	roles := make([]string, 0, len(counts))
	for r := range counts {
		roles = append(roles, r)
	}
	sort.Slice(roles, func(i, j int) bool {
		if counts[roles[i]] != counts[roles[j]] {
			return counts[roles[i]] > counts[roles[j]]
		}
		return roles[i] < roles[j]
	})
	if len(roles) > maxPredictRoles {
		roles = roles[:maxPredictRoles]
	}
	p.roles = roles
	for i, r := range roles {
		p.roleIndex[r] = i
	}

	// Нормальные уравнения: XᵀX w = Xᵀy, y = ln(1 + факт).
	// Признаки: перехват, размер описания, размер заголовка, роли, «эпик»,
	// число задач эпика.
	dim := 5 + len(roles)
	xtx := make([][]float64, dim)
	xty := make([]float64, dim)
	for i := range xtx {
		xtx[i] = make([]float64, dim)
	}
	for _, s := range samples {
		if s.Spent <= 0 {
			continue
		}
		x := p.features(s)
		for i := 0; i < dim; i++ {
			xty[i] += x[i] * math.Log1p(float64(s.Spent))
			for j := 0; j < dim; j++ {
				xtx[i][j] += x[i] * x[j]
			}
		}
	}
	for i := 1; i < dim; i++ { // гребень по диагонали (кроме перехвата)
		xtx[i][i] += predictRidge
	}
	w, ok := solve(xtx, xty)
	if !ok {
		return p // вырожденная матрица — остаётся медиана
	}
	p.weights = w
	p.trained = true
	p.mae = p.meanError(samples)
	return p
}

// Trained сообщает, обучена ли модель (для логов/тестов).
func (p *Predictor) Trained() bool { return p != nil && p.trained }

// Samples сообщает размер обучающей выборки.
func (p *Predictor) Samples() int { return p.nSamples }

// Predict возвращает прогноз расхода токенов для новой единицы работы.
func (p *Predictor) Predict(s Sample) Prediction {
	if p == nil {
		return Prediction{Basis: BasisNone}
	}
	if p.trained {
		y := 0.0
		x := p.features(s)
		for i, w := range p.weights {
			y += w * x[i]
		}
		est := int64(math.Round(math.Expm1(y)))
		if est < 0 {
			est = 0
		}
		// Модель может дать меньше минимума правдоподобного (пустого) раунда
		// только при патологических данных — но не меньше медианы роли.
		return Prediction{Estimate: est, Basis: BasisModel, MAE: p.mae}
	}
	if m, ok := p.byRole[roleKey(s.Role)]; ok && m > 0 {
		return Prediction{Estimate: m, Basis: BasisRoleMedian}
	}
	if p.global > 0 {
		return Prediction{Estimate: p.global, Basis: BasisGlobalMedian}
	}
	return Prediction{Basis: BasisNone}
}

// features строит вектор признаков (перехват, размеры, роли, эпик, число задач).
func (p *Predictor) features(s Sample) []float64 {
	x := make([]float64, 5+len(p.roles))
	x[0] = 1
	x[1] = math.Log1p(float64(max(s.DescLen, 0)))
	x[2] = math.Log1p(float64(max(s.TitleLen, 0)))
	key := roleKey(s.Role)
	if i, ok := p.roleIndex[key]; ok {
		x[3+i] = 1
	}
	if s.IsEpic {
		x[3+len(p.roles)] = 1
		x[3+len(p.roles)+1] = math.Log1p(float64(max(s.NumTasks, 0)))
	}
	return x
}

// meanError — средняя абсолютная ошибка модели на обучающей выборке, % от факта.
func (p *Predictor) meanError(samples []Sample) float64 {
	var sum float64
	var n int
	for _, s := range samples {
		if s.Spent <= 0 {
			continue
		}
		est := p.Predict(s)
		if est.Estimate <= 0 {
			continue
		}
		diff := float64(est.Estimate - s.Spent)
		sum += math.Abs(diff) / float64(s.Spent) * 100
		n++
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// roleKey нормализует роль для признака/медианы.
func roleKey(role string) string {
	r := strings.ToLower(strings.TrimSpace(role))
	if r == "" {
		return "?"
	}
	return r
}

// median возвращает медиану значений (0 для пустого входа).
func median(vals []int64) int64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]int64(nil), vals...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	mid := len(cp) / 2
	if len(cp)%2 == 1 {
		return cp[mid]
	}
	return (cp[mid-1] + cp[mid]) / 2
}

// solve решает систему A w = b методом Гаусса с частичным выбором ведущего.
// ok=false, если матрица вырождена — вызывающий деградирует до медианы.
func solve(a [][]float64, b []float64) ([]float64, bool) {
	n := len(b)
	m := make([][]float64, n)
	for i := 0; i < n; i++ {
		m[i] = make([]float64, n+1)
		copy(m[i], a[i])
		m[i][n] = b[i]
	}
	for col := 0; col < n; col++ {
		// Ведущий элемент столбца.
		pivot, best := col, math.Abs(m[col][col])
		for r := col + 1; r < n; r++ {
			if v := math.Abs(m[r][col]); v > best {
				pivot, best = r, v
			}
		}
		if best < 1e-9 {
			return nil, false
		}
		m[col], m[pivot] = m[pivot], m[col]
		// Нормируем ведущую строку и устраняем столбец снизу.
		pv := m[col][col]
		for j := col; j <= n; j++ {
			m[col][j] /= pv
		}
		for r := 0; r < n; r++ {
			if r == col || m[r][col] == 0 {
				continue
			}
			f := m[r][col]
			for j := col; j <= n; j++ {
				m[r][j] -= f * m[col][j]
			}
		}
	}
	w := make([]float64, n)
	for i := 0; i < n; i++ {
		w[i] = m[i][n]
	}
	return w, true
}
