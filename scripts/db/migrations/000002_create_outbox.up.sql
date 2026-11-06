-- 000002 — tabla outbox (ADR 0006): mensajes que se publican después del commit.
--
-- El caso de uso escribe aquí en SU transacción (sharedoutbox.Writer): el cambio de
-- negocio y el mensaje se guardan juntos o no se guarda ninguno. El despachador de
-- cmd/worker los publica después, con entrega al menos una vez.
--
-- Dueño de escritura: shared (todos los módulos escriben a través de sharedoutbox).

CREATE TABLE outbox (
    id           BIGSERIAL    PRIMARY KEY,
    -- Id del mensaje: viaja al consumidor como TaskID de Asynq (deduplicación).
    uuid         UUID         NOT NULL,
    topic        VARCHAR(200) NOT NULL,
    payload      JSONB        NOT NULL,
    -- Contexto de traza W3C (traceparent…) de la petición que originó el mensaje.
    headers      JSONB        NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ  NOT NULL,
    -- Próximo intento: lease del despachador o backoff tras un fallo.
    available_at TIMESTAMPTZ  NOT NULL,
    attempts     INTEGER      NOT NULL DEFAULT 0,
    last_error   TEXT,
    published_at TIMESTAMPTZ,
    -- Agotó los intentos: requiere intervención (ver docs/outbox.md).
    dead_at      TIMESTAMPTZ,

    CONSTRAINT uq_outbox_uuid      UNIQUE (uuid),
    CONSTRAINT ck_outbox_attempts  CHECK (attempts >= 0),
    CONSTRAINT ck_outbox_topic     CHECK (topic <> '')
);

-- Solo los pendientes: el índice queda pequeño aunque la tabla tenga historial.
CREATE INDEX idx_outbox_pending ON outbox (available_at, id)
    WHERE published_at IS NULL AND dead_at IS NULL;

-- Limpieza de publicados antiguos.
CREATE INDEX idx_outbox_published ON outbox (published_at)
    WHERE published_at IS NOT NULL;
