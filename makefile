.PHONY: qwen3-8b qwen3-coder-30b
qwen3-8b:
	docker-compose pull qwen3-8b


qwen3-coder-30b:
	docker-compose pull qwen3-coder-30b


.PHONY: backend-build backend-lint backend-test

backend-build:
	go build ./...

backend-lint:
	go vet ./...

backend-test:
	go test ./tools/ -run 'TestReadFiles' -v


.PHONY: infra.backend-build infra.backend-lint infra.backend-test

infra.backend-build:
	docker compose run --rm ollama go build ./...

infra.backend-lint:
	docker compose run --rm ollama go vet ./...

infra.backend-test:
	docker compose run --rm ollama go test ./tools/ -run 'TestReadFiles' -v
