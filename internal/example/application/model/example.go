package model

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"myapp.v1.0.0/internal/example/application/apperr"
)

// Example es el objeto de negocio del módulo. Sus campos son privados: solo
// cambia a través de métodos que hacen cumplir sus reglas.
type Example struct {
	id        int64 // interno; 0 hasta que se persiste
	uuid      uuid.UUID
	name      ExampleName
	status    ExampleStatus
	version   int
	createdBy uuid.UUID
	createdAt time.Time
	updatedAt time.Time
}

//--------------------------------------------------------------
// CONSTRUCCIÓN
//--------------------------------------------------------------

// NewExample crea un example nuevo: siempre empieza en pending y versión 1.
// createdBy proviene del Principal autenticado.
func NewExample(name ExampleName, createdBy uuid.UUID, now time.Time) (*Example, error) {
	if name == (ExampleName{}) {
		return nil, errors.New("NewExample: el nombre no fue construido con NewExampleName")
	}
	if createdBy == uuid.Nil {
		return nil, errors.New("NewExample: createdBy es obligatorio")
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generar uuid de example: %w", err)
	}
	return &Example{
		uuid:      id,
		name:      name,
		status:    ExampleStatusPending,
		version:   1,
		createdBy: createdBy,
		createdAt: now,
		updatedAt: now,
	}, nil
}

// RestoreExample rehidrata un example desde persistencia, sin revalidar las reglas
// de creación. Solo lo usan los mappers de infrastructure/persistence, que ya
// validaron cada valor (ExampleName, ParseExampleStatus).
func RestoreExample(
	id int64,
	uuid uuid.UUID,
	name ExampleName,
	status ExampleStatus,
	version int,
	createdBy uuid.UUID,
	createdAt, updatedAt time.Time,
) *Example {
	return &Example{
		id:        id,
		uuid:      uuid,
		name:      name,
		status:    status,
		version:   version,
		createdBy: createdBy,
		createdAt: createdAt,
		updatedAt: updatedAt,
	}
}

//--------------------------------------------------------------
// COMPORTAMIENTO
//--------------------------------------------------------------

// Rename cambia el nombre.
func (e *Example) Rename(name ExampleName, now time.Time) {
	e.name = name
	e.updatedAt = now
}

// Activate pasa el example a active (desde pending o inactive).
func (e *Example) Activate(now time.Time) error { return e.changeStatus(ExampleStatusActive, now) }

// Deactivate pasa el example a inactive (solo desde active).
func (e *Example) Deactivate(now time.Time) error {
	return e.changeStatus(ExampleStatusInactive, now)
}

// CanBeManagedBy indica si un usuario puede modificar este example: quien lo creó o un
// administrador. Es una regla de negocio y se comprueba en cada escritura, porque el
// scope de la ruta solo dice qué puede hacer el usuario, no sobre qué registros.
// Fallo: apperr.ErrExampleForbidden.
func (e *Example) CanBeManagedBy(userUUID uuid.UUID, isAdmin bool) error {
	if isAdmin || e.createdBy == userUUID {
		return nil
	}
	return apperr.ErrExampleForbidden
}

// CanDelete indica si el example se puede eliminar: uno activo está en uso y se
// desactiva primero. Fallo: apperr.ErrExampleDeleteActive.
func (e *Example) CanDelete() error {
	if e.status == ExampleStatusActive {
		return apperr.ErrExampleDeleteActive
	}
	return nil
}

func (e *Example) changeStatus(next ExampleStatus, now time.Time) error {
	if err := e.status.transitionTo(next); err != nil {
		return err
	}
	e.status = next
	e.updatedAt = now
	return nil
}

//--------------------------------------------------------------
// GETTERS
//--------------------------------------------------------------

func (e *Example) ID() int64             { return e.id }
func (e *Example) UUID() uuid.UUID       { return e.uuid }
func (e *Example) Name() ExampleName     { return e.name }
func (e *Example) Status() ExampleStatus { return e.status }

// Version es la versión persistida que se leyó. La escritura en la base de datos
// solo se aplica si sigue siendo la misma (concurrencia optimista).
func (e *Example) Version() int         { return e.version }
func (e *Example) CreatedBy() uuid.UUID { return e.createdBy }
func (e *Example) CreatedAt() time.Time { return e.createdAt }
func (e *Example) UpdatedAt() time.Time { return e.updatedAt }
