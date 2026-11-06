-- 000001 — tabla examples (módulo de ejemplo de la plantilla)
--
-- Convenciones que ilustra (CLAUDE.md §7.1 y §12.2):
--   - id BIGSERIAL interno + uuid externo (los contratos HTTP solo exponen uuid)
--   - version para concurrencia optimista
--   - constraints con nombre explícito: los adapters los referencian
--   - los DEFAULT coinciden con el model (estado inicial 'pending')

CREATE TABLE examples (
    id          BIGSERIAL    PRIMARY KEY,
    uuid        UUID         NOT NULL DEFAULT gen_random_uuid(),
    name        VARCHAR(150) NOT NULL,
    status      VARCHAR(20)  NOT NULL DEFAULT 'pending',
    version     INTEGER      NOT NULL DEFAULT 1,
    created_by  UUID         NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),

    CONSTRAINT uq_examples_uuid    UNIQUE (uuid),
    CONSTRAINT ck_examples_status  CHECK (status IN ('pending', 'active', 'inactive')),
    CONSTRAINT ck_examples_version CHECK (version >= 1)
);

-- El nombre es único sin distinguir mayúsculas. El adapter traduce la violación
-- de este índice a EXAMPLE_NAME_TAKEN (409).
CREATE UNIQUE INDEX uq_examples_name ON examples (lower(name));

-- Soporta el listado: filtro por estado y orden estable por fecha de creación.
CREATE INDEX idx_examples_status_created ON examples (status, created_at DESC, id DESC);
CREATE INDEX idx_examples_created        ON examples (created_at DESC, id DESC);
