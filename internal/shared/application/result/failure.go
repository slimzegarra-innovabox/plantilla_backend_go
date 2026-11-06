package result

import "errors"

// Failure es un fallo de negocio anticipado que viaja como error.
//
// Lo devuelven el model (invariantes) y, cuando traducen un caso previsible,
// los adapters. El caso de uso lo convierte en Result con FromError.
// Se declara como variable en el paquete apperr de cada módulo:
//
//	var ErrExampleNotFound = result.NewFailure(result.NotFound, "EXAMPLE_NOT_FOUND", "El ejemplo no existe.")
//
// Como cada Failure es un puntero único, errors.Is(err, apperr.ErrExampleNotFound)
// funciona aunque el error se haya envuelto con fmt.Errorf("...: %w", err).
type Failure struct {
	notification Notification
}

// NewFailure crea un fallo de negocio de severidad ERROR.
func NewFailure(category Category, code, message string) *Failure {
	return &Failure{notification: Error(category, code, message)}
}

// Error implementa error. Devuelve el código, que es estable y apto para logs.
func (f *Failure) Error() string { return f.notification.Code }

// Notification devuelve la notificación que representa el fallo.
func (f *Failure) Notification() Notification { return f.notification }

// AsFailure indica si err es (o envuelve) un fallo de negocio.
func AsFailure(err error) (*Failure, bool) {
	var f *Failure
	if errors.As(err, &f) {
		return f, true
	}
	return nil, false
}
