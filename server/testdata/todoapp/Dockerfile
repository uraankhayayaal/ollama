# Многостадийная сборка TODO-приложения (Go + PostgreSQL).
#
# Стадия сборки несёт компилятор и модули, финальный образ — минимальный
# (alpine) и запускается от непривилегированного пользователя. Секреты в образ
# не зашиваются: DATABASE_URL приходит из окружения (см. compose.yaml).
#
# Это артефакт, который на живом прогоне пишет DevOps-инженер.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/todoapp .

FROM alpine:3.20
RUN adduser -D -u 10001 app
USER app
COPY --from=build /out/todoapp /usr/local/bin/todoapp
ENV ADDR=0.0.0.0:8080
EXPOSE 8080
ENTRYPOINT ["todoapp"]
