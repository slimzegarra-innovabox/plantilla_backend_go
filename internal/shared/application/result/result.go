package result

// Result es la salida de un caso de uso: el dato si tuvo éxito, o al menos una
// notificación de severidad ERROR si falló por una razón de negocio.
//
// Los fallos técnicos NO se representan aquí: viajan como el segundo valor de
// retorno (error) de Execute y los convierte en 500 el borde HTTP.
type Result[T any] struct {
	data          T
	notifications []Notification
	ok            bool
}

// Ok crea un resultado exitoso. extra permite acompañarlo de avisos (INFO/WARNING).
func Ok[T any](data T, extra ...Notification) Result[T] {
	return Result[T]{data: data, notifications: extra, ok: true}
}

// Fail crea un resultado fallido. La primera notificación determina la categoría.
func Fail[T any](n Notification, more ...Notification) Result[T] {
	ns := make([]Notification, 0, 1+len(more))
	ns = append(ns, n)
	ns = append(ns, more...)
	return Result[T]{notifications: ns}
}

// FromError traduce un error al par de retorno de un caso de uso:
//
//   - err es (o envuelve) un *Failure → (Fail, nil): fallo de negocio.
//   - cualquier otro error           → (Result vacío, err): fallo técnico, se propaga.
//
// Es la única forma permitida de convertir un error en un fallo de negocio.
// Nunca se crea un Fail a partir de un error desconocido.
func FromError[T any](err error) (Result[T], error) {
	if f, ok := AsFailure(err); ok {
		return Fail[T](f.Notification()), nil
	}
	return Result[T]{}, err
}

// IsSuccess indica si el caso de uso terminó con éxito.
func (r Result[T]) IsSuccess() bool { return r.ok }

// Data devuelve el dato. En un resultado fallido es el valor cero de T.
func (r Result[T]) Data() T { return r.data }

// Notifications devuelve las notificaciones. Nunca es nil.
func (r Result[T]) Notifications() []Notification {
	if r.notifications == nil {
		return []Notification{}
	}
	return r.notifications
}
