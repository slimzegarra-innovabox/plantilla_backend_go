package persistence

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// Códigos SQLSTATE de PostgreSQL que la plantilla distingue.
const (
	sqlStateUniqueViolation      = "23505"
	sqlStateForeignKeyViolation  = "23503"
	sqlStateSerializationFailure = "40001"
	sqlStateDeadlockDetected     = "40P01"
	sqlStateQueryCanceled        = "57014" // statement_timeout (o cancelación)
	sqlStateLockNotAvailable     = "55P03" // lock_timeout
)

// SQLState devuelve el código SQLSTATE si err es (o envuelve) un error de PostgreSQL.
// Se lee de la cadena de errores, nunca del texto del mensaje.
func SQLState(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, true
	}
	return "", false
}

// IsUniqueViolation indica si err es una violación de unicidad sobre constraint.
// Con constraint == "" acepta cualquier constraint único.
func IsUniqueViolation(err error, constraint string) bool {
	return isViolation(err, sqlStateUniqueViolation, constraint)
}

// IsForeignKeyViolation indica si err es una violación de FK sobre constraint.
// Con constraint == "" acepta cualquier FK.
func IsForeignKeyViolation(err error, constraint string) bool {
	return isViolation(err, sqlStateForeignKeyViolation, constraint)
}

// IsDataConflict indica si err es una violación de unicidad o de FK que ningún
// adapter tradujo. El borde HTTP la responde como 409 DATA_CONFLICT (red de seguridad).
func IsDataConflict(err error) bool {
	code, ok := SQLState(err)
	return ok && (code == sqlStateUniqueViolation || code == sqlStateForeignKeyViolation)
}

// IsTransient indica si err es un fallo de concurrencia que se resuelve reintentando.
func IsTransient(err error) bool {
	code, ok := SQLState(err)
	return ok && (code == sqlStateSerializationFailure || code == sqlStateDeadlockDetected)
}

// IsTimeout indica que PostgreSQL abortó la sentencia por un plazo de sesión
// (DB_STATEMENT_TIMEOUT o DB_LOCK_TIMEOUT). No es un bug: la base tardó demasiado.
// El borde HTTP la responde como 503 SERVICE_TIMEOUT, igual que un context vencido.
func IsTimeout(err error) bool {
	code, ok := SQLState(err)
	return ok && (code == sqlStateQueryCanceled || code == sqlStateLockNotAvailable)
}

func isViolation(err error, code, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}
