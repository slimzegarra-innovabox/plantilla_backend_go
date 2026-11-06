-- 000005 — índice de mensajes muertos del outbox.
--
-- La métrica outbox.dead corre en cada recolección y en cada réplica. Sin este índice
-- recorre todo el historial de publicados. Es parcial: normalmente queda vacío.
--
-- CONCURRENTLY no bloquea las escrituras, y por eso va en su propia migración, sola:
-- no puede correr dentro de una transacción ni junto a otra sentencia.

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_outbox_dead ON outbox (id) WHERE dead_at IS NOT NULL;
