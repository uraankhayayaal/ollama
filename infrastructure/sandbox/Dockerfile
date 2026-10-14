# Dev-образ песочницы выполнения (Ф-4, tools/sandbox.go).
#
# Зачем он нужен при том, что для большинства проектов подходит стоковый образ
# по стеку (golang:1.24, node:22, python:3.12, php:8.3-cli): монорепозитории, где
# в одной приёмке участвуют backend на Go и frontend на Node, и проекты с
# Makefile-целями вида `make build`, которым нужен целый тулчейн (make, git,
# ripgrep, jq) — ровно то, чего нет в минимальных языковых образах.
#
# Принципы, по которым образ совпадает с тем, что делает dockerArgs:
#   - непривилегированный пользователь с UID/GID хоста (аргументы --user в
#     docker run всё равно подставляются, но USER здесь нужен для ручного
#     `docker compose run` и для проверки образа вручную);
#   - HOME и кэши (Go/npm/pip) — в /tmp, а не в /workspace: иначе git и npm
#     оставят ~/.gitconfig и ~/.npmrc прямо в проекте, и они попадут в diff,
#     который потом ревьюит человек;
#   - тулчейн без дев-серверов: то, что поднимают долгоживущие процессы, в образ
#     не ставится намеренно (node-gyp без python3-dev не нужен: образ для
#     запуска тестов и сборки, а не для установки нативных модулей).

FROM golang:1.24-bookworm

ARG TARGET_UID=1000
ARG TARGET_GID=1000
# Объявлен именно ARG, а не только compose.args: в RUN ниже стоит `set -eux`,
# и без объявления обращение к переменной падает с «parameter not set» —
# то есть образ не собирался вообще ни разу.
ARG CODEGEN_SANDBOX_PLAYWRIGHT=0

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    TZ=UTC \
    HOME=/tmp \
    XDG_CACHE_HOME=/tmp/.cache \
    GOCACHE=/tmp/.cache/go-build \
    GOMODCACHE=/tmp/go/pkg/mod \
    GOPATH=/tmp/go \
    npm_config_cache=/tmp/.npm \
    PIP_CACHE_DIR=/tmp/.cache/pip \
    PLAYWRIGHT_BROWSERS_PATH=/tmp/ms-playwright \
    GIT_CONFIG_GLOBAL=/tmp/.gitconfig \
    GIT_AUTHOR_NAME="AI Sandbox" \
    GIT_AUTHOR_EMAIL="sandbox@localhost" \
    GIT_COMMITTER_NAME="AI Sandbox" \
    GIT_COMMITTER_EMAIL="sandbox@localhost" \
    CI=true

# Один слой apt-get: apt сам не умеет чистить в том же слое при удалении.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        make \
        jq \
        less \
        ripgrep \
        unzip \
        xz-utils \
        zip \
        # Рантаймы для монорепо (Go-тулчейн уже в базовом образе).
        nodejs \
        npm \
        python3 \
        python3-pip \
        python3-venv \
        php-cli \
        php-mbstring \
        php-xml \
        unzip \
    ; \
    rm -rf /var/lib/apt/lists/*

# Playwright + его системные зависимости ставятся отдельным слоем и только
# при CODEGEN_SANDBOX_PLAYWRIGHT=1: базовый образ нужен чаще без браузеров, а
# скачивание браузеров весит больше всего остального вместе взятого.
RUN set -eux; \
    if [ "${CODEGEN_SANDBOX_PLAYWRIGHT}" = "1" ]; then \
        npx --yes playwright@1.49.0 install --with-deps chromium; \
    fi

# Каталоги кэшей заранее с правильным владельцем: иначе первый же запуск под
# UID хоста упрётся в «permission denied» на /tmp/go. HOME не создаётся
# отдельным каталогом — dockerArgs и compose накладывают на /tmp tmpfs, и
# каталога /tmp/home под ней просто не было бы.
RUN set -eux; \
    mkdir -p "$XDG_CACHE_HOME" "$GOMODCACHE" "$GOPATH" \
        "$npm_config_cache" "$PIP_CACHE_DIR" "$PLAYWRIGHT_BROWSERS_PATH"; \
    groupadd -g "$TARGET_GID" sandbox 2>/dev/null || true; \
    useradd -m -u "$TARGET_UID" -g "$TARGET_GID" sandbox 2>/dev/null || true; \
    chown -R "$TARGET_UID:$TARGET_GID" /tmp

USER ${TARGET_UID}:${TARGET_GID}
WORKDIR /workspace

# Образ используется как исполняемая среда (docker run sh -c '<команда>'), а не
# как сервис, поэтому ENTRYPOINT намеренно отсутствует: иначе compose и docker
# подставили бы shell дважды и «make test» превратился бы в запуск make внутри
# make.
CMD ["sh"]
