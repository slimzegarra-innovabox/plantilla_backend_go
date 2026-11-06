-- 000004 — claves de idempotencia de los POST (ADR 0007).
--
-- Dueño de escritura: shared (middleware sharedhttp.Idempotent).
-- status NULL = la primera petición sigue en curso.

CREATE TABLE idempotency_keys (
    -- Alcance: usuario + método + ruta. Dos usuarios no comparten claves.
    scope        VARCHAR(400) NOT NULL,
    key          VARCHAR(255) NOT NULL,
    -- SHA-256 del cuerpo: la misma clave con otro cuerpo es un error del cliente.
    fingerprint  VARCHAR(64)  NOT NULL,
    status       INTEGER,
    headers      JSONB,
    body         BYTEA,
    -- Si el proceso cae con la petición en curso, la clave se libera al vencer.
    locked_until TIMESTAMPTZ  NOT NULL,
    expires_at   TIMESTAMPTZ  NOT NULL,
    created_at   TIMESTAMPTZ  NOT NULL,

    CONSTRAINT pk_idempotency_keys PRIMARY KEY (scope, key)
);

-- Limpieza de las claves vencidas.
CREATE INDEX idx_idempotency_keys_expires ON idempotency_keys (expires_at);
