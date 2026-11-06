-- 000006 — orden por updatedAt en GET /api/v1/examples.
--
-- Sin índice, sort=updatedAt recorre y ordena toda la tabla en cada petición: con un
-- millón de filas, 272 ms y 90 MB de temporales en disco. Un coste así agota el pool
-- (docs/load-baseline.md).
--
-- CONCURRENTLY no bloquea las escrituras, y por eso va en su propia migración, sola:
-- no puede correr dentro de una transacción ni junto a otra sentencia.

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_examples_updated ON examples (updated_at DESC, id DESC);
