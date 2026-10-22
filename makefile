.PHONY: build up kill depgate migrate e2e backend-test sandbox-readiness \
	infra.up infra.down infra.ps infra.logs

BIN ?= bin
SANDBOX_COMPOSE ?= infrastructure/sandbox/provision/compose.yaml
SANDBOX_PROVISION_BIN := $(BIN)/sandbox-provision
SANDBOX_READINESS_BIN := $(BIN)/sandbox-readiness

# Дополнительные аргументы команды sandbox-readiness (CI: --report=FILE).
SANDBOX_READINESS_ARGS ?=

build:
	npm run build --prefix ./web
	go build

up: build
	go run . serve

kill:
	kill -9 $$(lsof -t -i :8090 -sTCP:LISTEN)

# Импортный гейт Dependency Rule (Этап 8): application без прямых зависимостей
# от внешних/инфраструктурных пакетов. Дублирует тест deprule_test.go.
depgate:
	go test -count=1 -run TestApplicationDependencyRule .

# --- Песочница авто-проверки (rollout за DevOps/CI, п.3.1 A7-04) ------------
# Стек Postgres + S3: infrastructure/sandbox/provision/compose.yaml.
# Команды те же, что и в CI (.github/workflows/sandbox-readiness.yml) и в
# чеклисте docs/40-operations/sandbox-readiness.md.
#
# Коды возврата песочничных целей: 0 — ок, 2 — INFRA_DEGRADED (инфраструктура
# недоступна, это НЕ дефект кода), 1 — ошибка выполнения.

$(SANDBOX_PROVISION_BIN): $(wildcard cmd/sandbox-provision/*.go) $(wildcard infrastructure/sandbox/*.go) go.mod
	go build -o $@ ./cmd/sandbox-provision

$(SANDBOX_READINESS_BIN): $(wildcard cmd/sandbox-readiness/*.go) $(wildcard infrastructure/sandbox/*.go) go.mod
	go build -o $@ ./cmd/sandbox-readiness

# Подъём стека песочницы (Postgres + S3) и ожидание готовности.
infra.up: $(SANDBOX_PROVISION_BIN)
	$(SANDBOX_PROVISION_BIN) up

# Снятие стека (данные сохраняются; с томами — `down -v`).
infra.down: $(SANDBOX_PROVISION_BIN)
	$(SANDBOX_PROVISION_BIN) down

infra.ps:
	docker compose -f $(SANDBOX_COMPOSE) ps

infra.logs:
	docker compose -f $(SANDBOX_COMPOSE) logs -f --tail=100

# Миграции: DDL песочничного Postgres (SANDBOX_SCHEMA, по умолчанию
# server/testdata/todoapp/schema.sql). Требует поднятого стека.
migrate: $(SANDBOX_PROVISION_BIN)
	$(SANDBOX_PROVISION_BIN) migrate

# Интеграционный прогон: up → migrate → e2e-тесты → down (самозавершающаяся
# цель: ни одного зависшего сервиса после прогона).
e2e: $(SANDBOX_READINESS_BIN)
	$(SANDBOX_READINESS_BIN) e2e

# Полный чеклист acceptance criteria готовности песочницы.
# SANDBOX_READINESS_ARGS — доп. аргументы (например --report=readiness-report.md).
sandbox-readiness: $(SANDBOX_READINESS_BIN)
	$(SANDBOX_READINESS_BIN) check $(SANDBOX_READINESS_ARGS)

# --- Гейты CI (autotest.yml) ------------------------------------------------
# Канонический набор пакетов без temp/ (AGENTS.md, Handoff Gate). Единственный
# источник истины: и локальный запуск, и CI-джоб зовут одни и те же цели.
GO_PACKAGES := . ./agents/... ./application/... ./tools/... ./cmd/... ./server/ ./runner/ ./infrastructure/... ./domain/...

ci-build:
	go build $(GO_PACKAGES)

ci-vet:
	go vet $(GO_PACKAGES)

backend-test:
	go test -race -count=1 $(GO_PACKAGES)
