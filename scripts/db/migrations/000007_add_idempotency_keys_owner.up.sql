-- 000007 — dueño de cada reserva de idempotencia (ADR 0007).
--
-- Una petición que supera su reserva no debe pisar la de otra que la tomó después.
-- Admite NULL porque las reservas en curso durante el despliegue no lo tienen: esas
-- peticiones no podrán guardar su respuesta, y su clave se reutiliza al vencer.

ALTER TABLE idempotency_keys ADD COLUMN owner UUID;
