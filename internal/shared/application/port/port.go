// Package port contiene los puertos comunes a todos los módulos.
// Los puertos específicos de un módulo viven en su propio application/port.
package port

import (
	"context"
	"time"
)

// TransactionManager ejecuta fn de forma atómica.
//
// El ctx que recibe fn lleva la transacción: los adapters que lo usen escriben
// dentro de ella. Si fn devuelve un error (incluido un *result.Failure) se hace
// rollback y el error se devuelve tal cual.
//
// La implementación puede reintentar fn ante errores transitorios de la base de
// datos, por lo que fn no debe hacer I/O externo (S3, correo, colas).
type TransactionManager interface {
	WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error
}

// Clock entrega la hora actual. Los casos de uso la obtienen de aquí y la pasan
// al model, que nunca llama a time.Now(): así las pruebas controlan el tiempo.
type Clock interface {
	Now() time.Time
}
