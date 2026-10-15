.PHONY: build up depgate

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