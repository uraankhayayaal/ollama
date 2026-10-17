# PLAN-2026-10-17: Kafka-наблюдаемость агентного цикла (agent-harness-metrics)

Интеграция с целевым контрактом
[agent-harness-observability](https://github.com/uraankhayayaal/agent-harness-observability):
шаги агентного цикла публикуются в Kafka-топик `agent-harness-metrics` как JSON
`AgentStep`, Kafka-key = `run_id` (строгий UUID).

## Статус: код готов (wip — не прогнан e2e на живой Kafka)

## Контракт приёмщика

`AgentStep{run_id, harness_name, agent_version, step_number>=1, thought,
tool_called, status(success|error), latency_ms>=0, tokens_used>=0,
cost_usd>=0}`; невалидные сообщения — в DLQ. Референс-продюсер: `segmentio/
kafka-go`, acks=all, snappy, батч 16384, лингер 10 мс.

## Реализация

Пакет `infrastructure/kafkarelay/` (клиент `github.com/segmentio/kafka-go`):

| Файл | Содержимое |
|---|---|
| `config.go` | env-конфиг: `KAFKA_BROKERS` (пусто → выкл), `KAFKA_TOPIC`, `KAFKA_ACKS`, `KAFKA_COMPRESSION`, `KAFKA_BATCH_SIZE`, `KAFKA_LINGER_MS`, `HARNESS_NAME`, `AGENT_VERSION` |
| `step.go` | `AgentStep` + `Validate` (зеркало контракта) |
| `runid.go` | детерминированные run_id (UUIDv5, ns `f877313e-…`): эпики кросс-сессионно, ассистент — параллельный ран проекта |
| `assembler.go` | конечный автомат раундов: токены → текст → тулы; первый шаг раунда забирает thought+токены, параллельные вызовы — с нулём; message-only шаг — по границе раунда/дебаунсу 5 с; `role=user`/дельты/логи игнорируются |
| `counter.go` | `step_number`: Redis INCR `obs:step:<run_id>` (TTL 90 д), фолбэк in-memory |
| `relay.go` | writer, очередь с drop при переполнении (агент не блокируется), тикер флаша, `SessionSink(project, mode, epicOf)` |

Проводка (только `outer`, depgate не затронут):

- `main.go` — `Relay: kafkarelay.FromEnv()` в `server.Config`;
- `server/server.go` — поле `Relay`, дренаж (`Close`) при завершении `Run`;
- `server/session.go` — `relaySink` (оркестрация) на `sess.router`, `assistantSink`
  (ассистент); task-скоуп резолвится в эпик через `board.Store.GetTask`;
- `server/chatassist.go` — `sess.assistantSink(ev)` в начале chat-репортёра.

Публикация только в режиме `serve`; без `KAFKA_BROKERS` всё выключено (nil).

## Маппинг scope → run_id

| Scope | RunID |
|---|---|
| `epic:<id>` | v5(ns, "epic", id) — кросс-сессионно |
| `task:<id>` | v5 эпика задачи (fallback — сама задача) |
| прочий/пустой | v5(project, scope) |
| ассистент | v5("assistant", project) — свой параллельный ран |

## Проверки

- Юнит: `step_test`, `runid_test`, `assembler_test`, `relay_test`
  (полный путь, дроп при переполнении, Close-дренаж, miniredis-счётчик,
  `FromEnv` без брокеров → nil) — `-race` чисто.
- Канонический `go build/vet/test`, `make depgate`, `gofmt` — чисто
  (`TestAcceptBuild*` — известный флак GNU Make 3.81).

## Осталось (wip)

- [ ] e2e на живой Kafka (docker-compose приёмщика: cp-kafka :9092, Kafdrop):
  `KAFKA_BROKERS=localhost:9092 go run . serve`, оркестрация, проверка ключа и
  валидности в Kafdrop/DLQ.
