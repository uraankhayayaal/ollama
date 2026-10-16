-- Схема БД todo-приложения. Идентична константе schema в main.go;
-- хранится отдельно как человекочитаемая справка и для ручного psql.
CREATE TABLE IF NOT EXISTS todos (
    id         BIGSERIAL PRIMARY KEY,
    title      TEXT NOT NULL,
    done       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
