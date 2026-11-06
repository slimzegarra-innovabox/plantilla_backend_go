-- 000003 — búsqueda por nombre en examples (GET /api/v1/examples?q=…).
--
-- ILIKE '%texto%' no puede usar un índice B-tree; un índice GIN de trigramas sí
-- (con textos de 3 caracteres o más: el borde HTTP exige ese mínimo).
-- pg_trgm viene con PostgreSQL (contrib) y está permitida en RDS/Aurora; crear la
-- extensión requiere el rol dueño de la base (rds_superuser en RDS).
--
-- Se agrega en una migración nueva: la 000001 ya está integrada y no se edita (§12.2).

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX idx_examples_name_trgm ON examples USING gin (name gin_trgm_ops);
