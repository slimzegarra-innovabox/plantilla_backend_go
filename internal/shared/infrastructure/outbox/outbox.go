// Package outbox implementa el patrón transactional outbox (ADR 0006).
//
// Problema: si un caso de uso guarda un cambio y después publica una tarea, un fallo
// entre ambos pasos pierde la tarea; si la publica antes, puede salir una tarea de un
// cambio que nunca se guardó. Y publicar dentro de la transacción está prohibido
// (§6.2: el cierre se puede reintentar y la cola no participa del rollback).
//
// Solución:
//   - Writer escribe el mensaje en la tabla outbox DENTRO de la transacción del caso
//     de uso: el cambio y el mensaje se confirman o se descartan juntos.
//   - Dispatcher (cmd/worker) toma los pendientes y los publica DESPUÉS, fuera de toda
//     transacción, con reintentos y backoff. La entrega es al menos una vez: el id del
//     mensaje viaja al consumidor (TaskID de Asynq) para deduplicar.
//
// Como los demás clientes de shared/infrastructure (ADR 0003), no implementa un puerto:
// el módulo define el suyo (p. ej. ExampleEventPublisher) y su adapter delega aquí.
package outbox

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Message es un mensaje listo para publicar.
type Message struct {
	ID      uuid.UUID
	Topic   string
	Payload json.RawMessage
	// Headers lleva el contexto de traza W3C de la operación que originó el mensaje.
	Headers map[string]string
}

// entity es la fila de la tabla outbox. El dueño de escritura es shared (§5.3).
type entity struct {
	ID          int64           `gorm:"column:id;primaryKey"`
	UUID        uuid.UUID       `gorm:"column:uuid"`
	Topic       string          `gorm:"column:topic"`
	Payload     json.RawMessage `gorm:"column:payload;type:jsonb"`
	Headers     json.RawMessage `gorm:"column:headers;type:jsonb"`
	CreatedAt   time.Time       `gorm:"column:created_at;autoCreateTime:false"`
	AvailableAt time.Time       `gorm:"column:available_at"`
	Attempts    int             `gorm:"column:attempts"`
	LastError   *string         `gorm:"column:last_error"`
	PublishedAt *time.Time      `gorm:"column:published_at"`
	DeadAt      *time.Time      `gorm:"column:dead_at"`
}

func (entity) TableName() string { return "outbox" }
