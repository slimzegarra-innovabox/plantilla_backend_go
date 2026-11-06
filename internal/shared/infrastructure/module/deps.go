// Package module define las dependencias que las raíces de composición (cmd/api,
// cmd/worker) entregan a cada módulo de negocio.
package module

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	sharedport "myapp.v1.0.0/internal/shared/application/port"
	"myapp.v1.0.0/internal/shared/infrastructure/external/asynq"
	"myapp.v1.0.0/internal/shared/infrastructure/external/brevo"
	"myapp.v1.0.0/internal/shared/infrastructure/external/s3"
	sharedhttp "myapp.v1.0.0/internal/shared/infrastructure/httpapi"
	"myapp.v1.0.0/internal/shared/infrastructure/outbox"
)

// Deps son las dependencias compartidas. Un módulo usa solo las que necesita.
//
// Los clientes opcionales son nil cuando su integración está desactivada
// (S3_ENABLED, BREVO_ENABLED, REDIS_ENABLED). Un módulo que necesita uno lo exige en
// su Register con MustHave, de modo que el proceso no arranca a medio configurar.
type Deps struct {
	DB    *gorm.DB
	Tx    sharedport.TransactionManager
	Clock sharedport.Clock
	// Outbox escribe mensajes en la transacción del caso de uso (ADR 0006). Es nil con
	// REDIS_ENABLED=false: sin destino, los mensajes se acumularían para siempre. Un
	// módulo cuyos eventos sean imprescindibles lo exige con MustHave; si son
	// opcionales, usa un adapter que los descarte (ver example/infrastructure/messaging).
	Outbox *outbox.Writer
	// Auth e Idempotent solo existen en cmd/api. Idempotent va en la ruta de un POST que
	// crea recursos, después de auth: g.POST("", write, deps.Idempotent, h.Create).
	Auth       *sharedhttp.Authenticator
	Idempotent gin.HandlerFunc

	Storage *s3.Client
	Mail    *brevo.Client
	Tasks   *asynq.Publisher
}

// MustHave detiene el arranque si falta una dependencia opcional que el módulo necesita.
// Se llama al inicio de Register: MustHave("orders", "S3_ENABLED", deps.Storage != nil).
func MustHave(moduleName, flag string, present bool) {
	if !present {
		panic("módulo " + moduleName + ": requiere " + flag + "=true")
	}
}
