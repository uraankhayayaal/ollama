// Цены токенов и оценка стоимости запуска.
//
// Ф-1 PLAN-2026-09-19-todo-owerview-for-prom.md. Счётчик токенов проекта
// existed и до этого этапа, но денежной оценки не было: Web UI показывал
// «вход/выход» без понимания, во сколько это обошлось. Цены задаёт пользователь
// (у провайдеров они разные и меняются), поэтому:
//
//	LLM_PRICE_IN  — цена 1M входных токенов;
//	LLM_PRICE_OUT — цена 1M выходных токенов.
//
// Без обеих переменных цена неизвестна: Known() == false, а интерфейс обязан
// показывать токены без денежной оценки, а не выдуманный ноль. Это тот же
// обязательный degrade, что и для Qdrant/LSP.
package tokens

import (
	"os"
	"strconv"
	"strings"
)

// Валюта по умолчанию для подписи метрик стоимости.
const DefaultCurrency = "у.е."

// Pricing — цены за 1M токенов входа и выхода.
type Pricing struct {
	InPerMillion  float64
	OutPerMillion float64
	// Currency — подпись валюты для отображения и метрик.
	Currency string
}

// Known сообщает, заданы ли цены. Без цен стоимость не вычисляется.
func (p Pricing) Known() bool { return p.InPerMillion > 0 || p.OutPerMillion > 0 }

// Cost возвращает стоимость расхода в валюте пользователя. Неизвестная цена
// считается нулём (Known() == false отличает «бесплатно» от «не знаем»).
func (p Pricing) Cost(in, out int64) float64 {
	if !p.Known() {
		return 0
	}
	return p.CostIn(in) + p.CostOut(out)
}

// CostIn — стоимость входных токенов.
func (p Pricing) CostIn(in int64) float64 {
	if in <= 0 {
		return 0
	}
	return float64(in) / 1_000_000 * p.InPerMillion
}

// CostOut — стоимость выходных токенов.
func (p Pricing) CostOut(out int64) float64 {
	if out <= 0 {
		return 0
	}
	return float64(out) / 1_000_000 * p.OutPerMillion
}

// PricingFromEnv читает цены из окружения. Любой нечисловой/отрицательный
// элемент игнорируется: цена по умолчанию — «не задана», а не «нулевая».
func PricingFromEnv() Pricing {
	p := Pricing{
		InPerMillion:  envPrice("LLM_PRICE_IN"),
		OutPerMillion: envPrice("LLM_PRICE_OUT"),
	}
	if v := strings.TrimSpace(os.Getenv("LLM_CURRENCY")); v != "" {
		p.Currency = v
	} else {
		p.Currency = DefaultCurrency
	}
	return p
}

func envPrice(name string) float64 {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(v, ",", "."), 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
}
