.PHONY: build up

build:
	npm run build --prefix ./web
	go build

up: build
	go run . serve

kill:
	kill -9 $$(lsof -t -i :8090 -sTCP:LISTEN)