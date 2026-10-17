package kafkarelay

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"ai/shared/logging"
)

// stepPrefix — префикс ключа счётчика шагов в Redis.
const stepPrefix = "obs:step:"

// stepTTL — время жизни счётчика: больше, чем жизнь любого продуктивного эпика,
// и достаточно мало, чтобы ключи завершённых ранов уходили сами.
const stepTTL = 90 * 24 * time.Hour

// stepCounter — монотонный номер шага внутри run_id. Кросс-сессионный ран
// (эпик) требует общего счётчика, поэтому источник — Redis (INCR); при его
// недоступности деградация до локального счётчика процесса с предупреждением
// в лог (порядок внутри процесса сохраняется, после рестарта нумерация
// начинается заново — приемлемо для деградации).
type stepCounter struct {
	client *redis.Client

	redisDown atomic.Bool
	warnOnce  sync.Once

	mu  sync.Mutex
	mem map[string]int64
}

func newStepCounter(addr, password string, db int) *stepCounter {
	c := &stepCounter{mem: make(map[string]int64)}
	if strings.TrimSpace(addr) != "" {
		c.client = redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})
	}
	return c
}

// next возвращает следующий номер шага рана (с 1).
func (c *stepCounter) next(ctx context.Context, runID string) int64 {
	if c.client != nil && !c.redisDown.Load() {
		cctx, cancel := context.WithTimeout(ctx, time.Second)
		key := stepPrefix + runID
		pipe := c.client.Pipeline()
		incr := pipe.Incr(cctx, key)
		pipe.Expire(cctx, key, stepTTL)
		_, err := pipe.Exec(cctx)
		cancel()
		if err == nil {
			return incr.Val()
		}
		c.redisDown.Store(true)
		c.warnOnce.Do(func() {
			logging.Warnf("kafkarelay: Redis недоступен, номера шагов временно локальные: %v", err)
		})
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mem[runID]++
	return c.mem[runID]
}

func (c *stepCounter) close() error {
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}
