// Package clock implementa sharedport.Clock con el reloj del sistema.
package clock

import "time"

// System devuelve la hora actual en UTC, truncada a microsegundos: la precisión
// de TIMESTAMPTZ en PostgreSQL. Así el valor que devuelve una escritura es
// idéntico al que se lee después de la base de datos.
type System struct{}

// Now implementa sharedport.Clock.
func (System) Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
