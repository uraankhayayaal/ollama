// Package kafkarelay — продюсер шагов агентного цикла в Apache Kafka для
// внешнего дашборда наблюдаемости (agent-harness-observability).
//
// События runevents превращаются в «шаги» (траектория одного запуска) и
// публикуются в топик agent-harness-metrics с ключом run_id: все шаги одного
// рана попадают в одну партицию и сохраняют порядок. Контракт сообщения
// зеркалит backend/internal/domain.AgentStep приёмщика: JSON, run_id — строгий
// UUID, step_number >= 1, status success|error (иначе сообщение уходит в DLQ
// у приёмщика).
//
// Политика отказоустойчивости: публикация асинхронная, очередь ограничена;
// когда Kafka недоступна или очередь переполнена — шаги отбрасываются
// (drop) со счётчиком в лог. Агентский цикл никогда не блокируется
// наблюдаемостью. Выключена по умолчанию: без KAFKA_BROKERS FromEnv
// возвращает nil.
package kafkarelay

import (
	"os"
	"strconv"
	"strings"
	"time"

	"ai/shared/logging"
)

// Дефолты, согласованные с .env.example приёмщика.
const (
	defaultTopic     = "agent-harness-metrics"
	defaultAcks      = "all"
	defaultComp      = "snappy"
	defaultBatchSize = 16384
	defaultLinger    = 10 * time.Millisecond

	defaultQueueSize     = 2048
	defaultFlushInterval = 5 * time.Second
	defaultWriteTimeout  = 5 * time.Second
)

// Config — параметры продюсера.
type Config struct {
	// Brokers — адреса брокеров через запятую; пусто — интеграция выключена.
	Brokers []string
	Topic   string
	// Acks — all | one | none; Compression — snappy | lz4 | zstd | gzip | none.
	Acks        string
	Compression string
	BatchSize   int
	Linger      time.Duration
	// WriteTimeout — бюджет одной записи в брокер (внутри WriteMessages).
	WriteTimeout time.Duration
	// FlushInterval — дебаунс публикации message-only шагов (финальный ответ
	// модели без вызова инструмента публикуется, когда раунд не продолжился).
	FlushInterval time.Duration
	QueueSize     int

	HarnessName  string
	AgentVersion string

	RedisAddr     string
	RedisPassword string
	RedisDB       int
}

// ConfigFromEnv читает настройки из окружения. ok=false — интеграция
// выключена (KAFKA_BROKERS пуст).
func ConfigFromEnv() (Config, bool) {
	brokers := splitList(os.Getenv("KAFKA_BROKERS"))
	if len(brokers) == 0 {
		return Config{}, false
	}
	cfg := Config{
		Brokers:       brokers,
		Topic:         envOr("KAFKA_TOPIC", defaultTopic),
		Acks:          envOr("KAFKA_ACKS", defaultAcks),
		Compression:   envOr("KAFKA_COMPRESSION", defaultComp),
		BatchSize:     envInt("KAFKA_BATCH_SIZE", defaultBatchSize),
		Linger:        time.Duration(envInt("KAFKA_LINGER_MS", 10)) * time.Millisecond,
		WriteTimeout:  defaultWriteTimeout,
		FlushInterval: defaultFlushInterval,
		QueueSize:     defaultQueueSize,
		HarnessName:   envOr("HARNESS_NAME", "ollama-harness"),
		AgentVersion:  envOr("AGENT_VERSION", "dev"),
	}
	cfg.RedisAddr = os.Getenv("REDIS_ADDR")
	if cfg.RedisAddr == "" {
		cfg.RedisAddr = "localhost:56379"
	}
	cfg.RedisPassword = os.Getenv("REDIS_PASSWORD")
	if v := os.Getenv("REDIS_DB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.RedisDB = n
		}
	}
	return cfg, true
}

// FromEnv создаёт рлей по окружению; nil — интеграция выключена или
// брокер/настройки некорректны (подробности в логе, сервер стартует всегда).
func FromEnv() *Relay {
	cfg, ok := ConfigFromEnv()
	if !ok {
		return nil
	}
	relay, err := New(cfg)
	if err != nil {
		logging.Warnf("kafkarelay: публикация шагов в Kafka отключена: %v", err)
		return nil
	}
	logging.Infof("kafkarelay: шаги агента → %s/%s (harness=%s, version=%s)",
		strings.Join(cfg.Brokers, ","), cfg.Topic, cfg.HarnessName, cfg.AgentVersion)
	return relay
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
