package main

import (
	"context"
	"errors"

	"github.com/hibiken/asynq"

	"myapp.v1.0.0/internal/example"
	"myapp.v1.0.0/internal/shared/infrastructure/external/sqs"
	"myapp.v1.0.0/internal/shared/infrastructure/module"
)

// registerTasks registra los handlers de tareas de cada módulo.
// Un módulo con tareas expone RegisterTasks(mux, deps) junto a su Register y se
// agrega aquí con una línea: <modulo>.RegisterTasks(mux, deps).
func registerTasks(mux *asynq.ServeMux, deps module.Deps) {
	example.RegisterTasks(mux, deps)
}

// errNoSQSHandler hace visible que SQS_ENABLED=true sin un handler configurado:
// los mensajes no se borran y terminan en la DLQ en lugar de perderse.
var errNoSQSHandler = errors.New("no hay handler de SQS configurado (cmd/worker/tasks.go)")

// sqsHandler devuelve el handler de los mensajes de SQS_QUEUE_URL.
// Una cola tiene un único tipo de mensaje: reemplazar por el handler del módulo dueño,
// p. ej. return orders.SQSHandler(deps).
func sqsHandler(_ module.Deps) sqs.Handler {
	return func(context.Context, string) error { return errNoSQSHandler }
}
