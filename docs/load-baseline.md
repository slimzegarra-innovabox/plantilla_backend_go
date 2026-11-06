# Línea base de carga

`make load` levanta PostgreSQL y la API, carga datos y ejecuta `test/load/examples.js` con k6.

```bash
make load                               # 300 rps, 60 s, 100 000 filas
make load SEED=1000000 RATE=1000        # otra carga u otro volumen
make load MODE=ramp                     # sube hasta encontrar la saturación
```

**La mezcla:** 50 % `GET` por uuid, 25 % listados, 10 % búsquedas, 10 % creaciones con `Idempotency-Key` y 5 % modificaciones. Con `HTTP_RATE_LIMIT_RPS=0`, sin Redis ni OTel.

**Cómo leer estas cifras:** sirven para **comparar versiones en la misma máquina**, no como capacidad de producción. k6, la API y PostgreSQL comparten aquí 8 núcleos y 15 GB. Antes de un cambio que toque consultas o el camino HTTP, conviene repetir la medición y comparar.

## Resultados (2026-11-05, commit de este documento)

p95 por operación, con 1 millón de filas:

| Carga | `GET` | Listado | Búsqueda | Crear | Modificar | Respuestas inesperadas |
|---|---|---|---|---|---|---|
| 300 rps | 1,3 ms | 21 ms | 20 ms | 6 ms | 3,5 ms | 0 % |
| 1 000 rps | 30 ms | 88 ms | 109 ms | 70 ms | 47 ms | 0 % |
| 1 500 rps (saturado: atiende ~1 340 rps) | 1,2 s | 2,0 s | 2,0 s | 2,7 s | 2,1 s | 0 % |

**Bajo saturación no hay errores:** las peticiones se ponen lentas, pero todas responden lo esperado. El límite real en producción lo fijan las réplicas y el tamaño de la base; `DB_MAX_OPEN_CONNS` (20) es el primer parámetro a revisar.

## Lo que encontró esta prueba

**`COUNT(*)` en cada listado colapsaba todo el sistema.** Con 100 000 filas no se notaba. Con 1 millón, a solo 300 rps, el conteo recorría la tabla en cada listado y agotaba el pool de conexiones, de modo que **todas** las operaciones hacían cola:

| p95 a 300 rps, 1 millón de filas | Antes | Con `CountUpTo` |
|---|---|---|
| `GET` por uuid | 5,16 s | 1,25 ms |
| Listado | 8,39 s | 20,9 ms |
| Búsqueda | 7,93 s | 20,4 ms |
| Crear | 10,11 s | 5,9 ms |
| Modificar | 8,69 s | 3,5 ms |

Como la paginación no pasa de `MaxOffset`, contar más allá no sirve: `sharedpersistence.CountUpTo` cuenta con un tope (`shareddto.MaxCount`). **Consecuencia en el contrato:** `total_elements` vale como máximo 100 100, y en el tope significa «100 100 o más».

**Lección:** probar siempre con el volumen de datos esperado. Con pocas filas, este problema era invisible.

## Lo que no mide

- Latencia de red, balanceador y TLS.
- PostgreSQL con disco y memoria de producción.
- Con OTel o con Redis y el outbox activos.
- Varias réplicas de la API.
