package usecase_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/example/application/apperr"
	"myapp.v1.0.0/internal/example/application/dto"
	"myapp.v1.0.0/internal/example/application/model"
	"myapp.v1.0.0/internal/example/application/port/porttest"
	"myapp.v1.0.0/internal/example/application/usecase"
	shareddto "myapp.v1.0.0/internal/shared/application/dto"
	sharedporttest "myapp.v1.0.0/internal/shared/application/port/porttest"
	"myapp.v1.0.0/internal/shared/application/result"
)

var (
	t0      = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	ctx     = context.Background()
	errBoom = errors.New("connection refused")
)

// admin ejecuta las pruebas que no son sobre autorización: la regla se prueba aparte.
var admin = dto.ExampleActor{UserUUID: uuid.New(), IsAdmin: true}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

// assertFailure comprueba que el resultado es el fallo de negocio esperado y no un error técnico.
func assertFailure[T any](t *testing.T, res result.Result[T], err error, want *result.Failure) {
	t.Helper()
	require.NoError(t, err, "un fallo de negocio no es un error técnico")
	require.False(t, res.IsSuccess())
	assert.Equal(t, want.Notification(), res.Notifications()[0])
}

func seed(t *testing.T, repo *porttest.InMemoryExampleRepository, name string, status model.ExampleStatus) *model.Example {
	t.Helper()
	return repo.Seed(model.RestoreExample(0, uuid.New(), porttest.MustName(t, name), status, 1, uuid.New(), t0, t0))
}

//--------------------------------------------------------------
// CREATE
//--------------------------------------------------------------

func TestCreateExample(t *testing.T) {
	repo := porttest.NewInMemoryExampleRepository()
	uc := usecase.NewCreateExampleUseCase(repo, fixedClock{t0})
	by := uuid.New()

	res, err := uc.Execute(ctx, dto.CreateExampleCommand{Name: "  Demo  ", CreatedBy: by})

	require.NoError(t, err)
	require.True(t, res.IsSuccess())
	data := res.Data()
	assert.Equal(t, "Demo", data.Name)
	assert.Equal(t, model.ExampleStatusPending, data.Status)
	assert.Equal(t, 1, data.Version)
	assert.Equal(t, by, data.CreatedBy)
	assert.Equal(t, t0, data.CreatedAt)
	assert.Equal(t, 1, repo.Calls["Create"])
}

func TestCreateExampleFailures(t *testing.T) {
	tests := map[string]struct {
		name string
		want *result.Failure
	}{
		"nombre vacío":  {"", apperr.ErrExampleNameRequired},
		"nombre corto":  {"a", apperr.ErrExampleNameTooShort},
		"nombre largo":  {strings.Repeat("a", 151), apperr.ErrExampleNameTooLong},
		"nombre tomado": {"existente", apperr.ErrExampleNameTaken},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			repo := porttest.NewInMemoryExampleRepository()
			seed(t, repo, "Existente", model.ExampleStatusPending)
			uc := usecase.NewCreateExampleUseCase(repo, fixedClock{t0})

			res, err := uc.Execute(ctx, dto.CreateExampleCommand{Name: tt.name, CreatedBy: uuid.New()})

			assertFailure(t, res, err, tt.want)
		})
	}
}

func TestCreateExampleInvalidNameDoesNotPersist(t *testing.T) {
	repo := porttest.NewInMemoryExampleRepository()

	_, _ = usecase.NewCreateExampleUseCase(repo, fixedClock{t0}).Execute(ctx, dto.CreateExampleCommand{Name: "a", CreatedBy: uuid.New()})

	assert.Zero(t, repo.Calls["Create"])
}

//--------------------------------------------------------------
// GET
//--------------------------------------------------------------

func TestGetExample(t *testing.T) {
	repo := porttest.NewInMemoryExampleRepository()
	e := seed(t, repo, "Demo", model.ExampleStatusActive)

	res, err := usecase.NewGetExampleUseCase(repo).Execute(ctx, dto.GetExampleQuery{UUID: e.UUID()})

	require.NoError(t, err)
	require.True(t, res.IsSuccess())
	assert.Equal(t, e.UUID(), res.Data().UUID)
	assert.Equal(t, model.ExampleStatusActive, res.Data().Status)
}

func TestGetExampleNotFound(t *testing.T) {
	res, err := usecase.NewGetExampleUseCase(porttest.NewInMemoryExampleRepository()).
		Execute(ctx, dto.GetExampleQuery{UUID: uuid.New()})

	assertFailure(t, res, err, apperr.ErrExampleNotFound)
}

//--------------------------------------------------------------
// LIST
//--------------------------------------------------------------

func TestListExamples(t *testing.T) {
	repo := porttest.NewInMemoryExampleRepository()
	seed(t, repo, "Uno", model.ExampleStatusActive)
	seed(t, repo, "Dos", model.ExampleStatusPending)
	seed(t, repo, "Tres", model.ExampleStatusActive)
	uc := usecase.NewListExamplesUseCase(repo)

	res, err := uc.Execute(ctx, dto.ListExamplesQuery{Status: "active", Page: shareddto.NewPageQuery(1, 10)})

	require.NoError(t, err)
	require.True(t, res.IsSuccess())
	assert.EqualValues(t, 2, res.Data().TotalElements)
	for _, d := range res.Data().Items {
		assert.Equal(t, model.ExampleStatusActive, d.Status)
	}

	all, err := uc.Execute(ctx, dto.ListExamplesQuery{Page: shareddto.NewPageQuery(1, 10)})
	require.NoError(t, err)
	assert.EqualValues(t, 3, all.Data().TotalElements)
}

func TestListExamplesInvalidStatus(t *testing.T) {
	res, err := usecase.NewListExamplesUseCase(porttest.NewInMemoryExampleRepository()).
		Execute(ctx, dto.ListExamplesQuery{Status: "deleted", Page: shareddto.NewPageQuery(1, 10)})

	assertFailure(t, res, err, apperr.ErrExampleStatusInvalid)
}

//--------------------------------------------------------------
// UPDATE
//--------------------------------------------------------------

func TestUpdateExample(t *testing.T) {
	repo := porttest.NewInMemoryExampleRepository()
	e := seed(t, repo, "Viejo", model.ExampleStatusPending)
	later := t0.Add(time.Hour)

	res, err := usecase.NewUpdateExampleUseCase(repo, fixedClock{later}).
		Execute(ctx, dto.UpdateExampleCommand{Actor: admin, UUID: e.UUID(), Name: "Nuevo", Version: 1})

	require.NoError(t, err)
	require.True(t, res.IsSuccess())
	assert.Equal(t, "Nuevo", res.Data().Name)
	assert.Equal(t, 2, res.Data().Version)
	assert.Equal(t, later, res.Data().UpdatedAt)
}

func TestUpdateExampleFailures(t *testing.T) {
	tests := map[string]struct {
		cmd  func(target *model.Example) dto.UpdateExampleCommand
		want *result.Failure
	}{
		"nombre inválido": {func(e *model.Example) dto.UpdateExampleCommand {
			return dto.UpdateExampleCommand{Actor: admin, UUID: e.UUID(), Name: "a", Version: 1}
		}, apperr.ErrExampleNameTooShort},
		"no existe": {func(*model.Example) dto.UpdateExampleCommand {
			return dto.UpdateExampleCommand{Actor: admin, UUID: uuid.New(), Name: "Nuevo", Version: 1}
		}, apperr.ErrExampleNotFound},
		"versión vieja": {func(e *model.Example) dto.UpdateExampleCommand {
			return dto.UpdateExampleCommand{Actor: admin, UUID: e.UUID(), Name: "Nuevo", Version: 0}
		}, apperr.ErrExampleVersionConflict},
		"nombre de otro": {func(e *model.Example) dto.UpdateExampleCommand {
			return dto.UpdateExampleCommand{Actor: admin, UUID: e.UUID(), Name: "OTRO", Version: 1}
		}, apperr.ErrExampleNameTaken},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			repo := porttest.NewInMemoryExampleRepository()
			target := seed(t, repo, "Objetivo", model.ExampleStatusPending)
			seed(t, repo, "Otro", model.ExampleStatusPending)

			res, err := usecase.NewUpdateExampleUseCase(repo, fixedClock{t0}).Execute(ctx, tt.cmd(target))

			assertFailure(t, res, err, tt.want)
		})
	}
}

//--------------------------------------------------------------
// CHANGE STATUS
//--------------------------------------------------------------

func TestChangeExampleStatus(t *testing.T) {
	tests := map[string]struct {
		from   model.ExampleStatus
		action dto.ExampleStatusAction
		want   model.ExampleStatus
		fail   *result.Failure
	}{
		"activar pending":    {model.ExampleStatusPending, dto.ActionActivate, model.ExampleStatusActive, nil},
		"desactivar active":  {model.ExampleStatusActive, dto.ActionDeactivate, model.ExampleStatusInactive, nil},
		"reactivar inactive": {model.ExampleStatusInactive, dto.ActionActivate, model.ExampleStatusActive, nil},
		"desactivar pending": {model.ExampleStatusPending, dto.ActionDeactivate, "", apperr.ErrExampleInvalidTransition},
		"activar active":     {model.ExampleStatusActive, dto.ActionActivate, "", apperr.ErrExampleInvalidTransition},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			repo := porttest.NewInMemoryExampleRepository()
			e := seed(t, repo, "Demo", tt.from)
			uc := usecase.NewChangeExampleStatusUseCase(repo, &porttest.RecordingExampleEventPublisher{}, &sharedporttest.TransactionManager{}, fixedClock{t0})

			res, err := uc.Execute(ctx, dto.ChangeExampleStatusCommand{Actor: admin, UUID: e.UUID(), Action: tt.action})

			if tt.fail != nil {
				assertFailure(t, res, err, tt.fail)
				assert.Zero(t, repo.Calls["Update"], "una transición inválida no escribe")
				return
			}
			require.NoError(t, err)
			require.True(t, res.IsSuccess())
			assert.Equal(t, tt.want, res.Data().Status)
			assert.Equal(t, 2, res.Data().Version)
		})
	}
}

func TestChangeExampleStatusPublishesTheEventInTheSameTransaction(t *testing.T) {
	repo := porttest.NewInMemoryExampleRepository()
	e := seed(t, repo, "Demo", model.ExampleStatusPending)
	events := &porttest.RecordingExampleEventPublisher{}
	tx := &sharedporttest.TransactionManager{}

	res, err := usecase.NewChangeExampleStatusUseCase(repo, events, tx, fixedClock{t0}).
		Execute(ctx, dto.ChangeExampleStatusCommand{Actor: admin, UUID: e.UUID(), Action: dto.ActionActivate})

	require.NoError(t, err)
	require.True(t, res.IsSuccess())
	assert.Equal(t, 1, tx.Calls, "UPDATE y evento en una sola transacción")
	require.Len(t, events.Events, 1)
	assert.Equal(t, dto.ExampleStatusChanged{
		ExampleUUID: e.UUID(), Status: "active", Version: 2, OccurredAt: t0,
	}, events.Events[0], "el evento lleva el estado persistido (versión nueva)")
}

func TestChangeExampleStatusEventFailureIsTechnical(t *testing.T) {
	repo := porttest.NewInMemoryExampleRepository()
	e := seed(t, repo, "Demo", model.ExampleStatusPending)
	events := &porttest.RecordingExampleEventPublisher{Err: errBoom}

	res, err := usecase.NewChangeExampleStatusUseCase(repo, events, &sharedporttest.TransactionManager{}, fixedClock{t0}).
		Execute(ctx, dto.ChangeExampleStatusCommand{Actor: admin, UUID: e.UUID(), Action: dto.ActionActivate})

	require.ErrorIs(t, err, errBoom, "sin evento no hay cambio: la transacción se revierte (500)")
	assert.False(t, res.IsSuccess())
}

func TestChangeExampleStatusFailureEmitsNoEvent(t *testing.T) {
	repo := porttest.NewInMemoryExampleRepository()
	e := seed(t, repo, "Demo", model.ExampleStatusPending)
	events := &porttest.RecordingExampleEventPublisher{}

	res, err := usecase.NewChangeExampleStatusUseCase(repo, events, &sharedporttest.TransactionManager{}, fixedClock{t0}).
		Execute(ctx, dto.ChangeExampleStatusCommand{Actor: admin, UUID: e.UUID(), Action: dto.ActionDeactivate})

	assertFailure(t, res, err, apperr.ErrExampleInvalidTransition)
	assert.Empty(t, events.Events)
}

func TestChangeExampleStatusNotFound(t *testing.T) {
	res, err := usecase.NewChangeExampleStatusUseCase(porttest.NewInMemoryExampleRepository(), &porttest.RecordingExampleEventPublisher{}, &sharedporttest.TransactionManager{}, fixedClock{t0}).
		Execute(ctx, dto.ChangeExampleStatusCommand{Actor: admin, UUID: uuid.New(), Action: dto.ActionActivate})

	assertFailure(t, res, err, apperr.ErrExampleNotFound)
}

func TestChangeExampleStatusUnknownActionIsTechnical(t *testing.T) {
	repo := porttest.NewInMemoryExampleRepository()
	e := seed(t, repo, "Demo", model.ExampleStatusPending)

	_, err := usecase.NewChangeExampleStatusUseCase(repo, &porttest.RecordingExampleEventPublisher{}, &sharedporttest.TransactionManager{}, fixedClock{t0}).
		Execute(ctx, dto.ChangeExampleStatusCommand{Actor: admin, UUID: e.UUID(), Action: 99})

	assert.Error(t, err)
}

//--------------------------------------------------------------
// AUTORIZACIÓN POR RECURSO
//--------------------------------------------------------------

func TestWritesRequireOwnerOrAdmin(t *testing.T) {
	actions := map[string]func(*porttest.InMemoryExampleRepository, *model.Example, dto.ExampleActor) (bool, []result.Notification, error){
		"update": func(r *porttest.InMemoryExampleRepository, e *model.Example, a dto.ExampleActor) (bool, []result.Notification, error) {
			res, err := usecase.NewUpdateExampleUseCase(r, fixedClock{t0}).Execute(ctx, dto.UpdateExampleCommand{Actor: a, UUID: e.UUID(), Name: e.Name().String() + " nuevo", Version: e.Version()})
			return res.IsSuccess(), res.Notifications(), err
		},
		"change status": func(r *porttest.InMemoryExampleRepository, e *model.Example, a dto.ExampleActor) (bool, []result.Notification, error) {
			res, err := usecase.NewChangeExampleStatusUseCase(r, &porttest.RecordingExampleEventPublisher{}, &sharedporttest.TransactionManager{}, fixedClock{t0}).
				Execute(ctx, dto.ChangeExampleStatusCommand{Actor: a, UUID: e.UUID(), Action: dto.ActionActivate})
			return res.IsSuccess(), res.Notifications(), err
		},
		"delete": func(r *porttest.InMemoryExampleRepository, e *model.Example, a dto.ExampleActor) (bool, []result.Notification, error) {
			res, err := usecase.NewDeleteExampleUseCase(r).Execute(ctx, dto.DeleteExampleCommand{Actor: a, UUID: e.UUID()})
			return res.IsSuccess(), res.Notifications(), err
		},
	}
	for name, act := range actions {
		t.Run(name, func(t *testing.T) {
			repo := porttest.NewInMemoryExampleRepository()
			e := seed(t, repo, "Demo", model.ExampleStatusPending)

			ok, notes, err := act(repo, e, dto.ExampleActor{UserUUID: uuid.New()})
			require.NoError(t, err)
			require.False(t, ok, "un tercero no puede")
			assert.Equal(t, apperr.ErrExampleForbidden.Notification(), notes[0])
			assert.Zero(t, repo.Calls["Update"]+repo.Calls["Delete"], "no se escribe nada")

			ok, _, err = act(repo, e, dto.ExampleActor{UserUUID: e.CreatedBy()})
			require.NoError(t, err)
			assert.True(t, ok, "el creador sí")

			other := seed(t, repo, "Otro", model.ExampleStatusPending)
			ok, _, err = act(repo, other, dto.ExampleActor{UserUUID: uuid.New(), IsAdmin: true})
			require.NoError(t, err)
			assert.True(t, ok, "un administrador sí")
		})
	}
}

//--------------------------------------------------------------
// DELETE
//--------------------------------------------------------------

func TestDeleteExample(t *testing.T) {
	tests := map[string]struct {
		status model.ExampleStatus
		fail   *result.Failure
	}{
		"pending se elimina":  {model.ExampleStatusPending, nil},
		"inactive se elimina": {model.ExampleStatusInactive, nil},
		"active no":           {model.ExampleStatusActive, apperr.ErrExampleDeleteActive},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			repo := porttest.NewInMemoryExampleRepository()
			e := seed(t, repo, "Demo", tt.status)

			res, err := usecase.NewDeleteExampleUseCase(repo).Execute(ctx, dto.DeleteExampleCommand{Actor: admin, UUID: e.UUID()})

			if tt.fail != nil {
				assertFailure(t, res, err, tt.fail)
				assert.Zero(t, repo.Calls["Delete"], "la regla se valida antes de escribir")
				return
			}
			require.NoError(t, err)
			assert.True(t, res.IsSuccess())
			_, found, _ := repo.FindByUUID(ctx, e.UUID())
			assert.False(t, found)
		})
	}
}

func TestDeleteExampleNotFound(t *testing.T) {
	res, err := usecase.NewDeleteExampleUseCase(porttest.NewInMemoryExampleRepository()).
		Execute(ctx, dto.DeleteExampleCommand{Actor: admin, UUID: uuid.New()})

	assertFailure(t, res, err, apperr.ErrExampleNotFound)
}

func TestListExamplesUsesDefaultSortAndTrimsSearch(t *testing.T) {
	repo := porttest.NewInMemoryExampleRepository()
	seed(t, repo, "Alfa", model.ExampleStatusPending)
	seed(t, repo, "Beta", model.ExampleStatusPending)

	res, err := usecase.NewListExamplesUseCase(repo).
		Execute(ctx, dto.ListExamplesQuery{Search: "  alf  ", Page: shareddto.NewPageQuery(1, 10)})

	require.NoError(t, err)
	require.Len(t, res.Data().Items, 1)
	assert.Equal(t, "Alfa", res.Data().Items[0].Name)
}

//--------------------------------------------------------------
// LOS ERRORES TÉCNICOS SE PROPAGAN, NUNCA SE DISFRAZAN DE NEGOCIO
//--------------------------------------------------------------

func TestTechnicalErrorsPropagate(t *testing.T) {
	id := uuid.New()
	run := map[string]func(repo *porttest.InMemoryExampleRepository) (bool, error){
		"create": func(r *porttest.InMemoryExampleRepository) (bool, error) {
			res, err := usecase.NewCreateExampleUseCase(r, fixedClock{t0}).Execute(ctx, dto.CreateExampleCommand{Name: "Demo", CreatedBy: id})
			return res.IsSuccess() || len(res.Notifications()) > 0, err
		},
		"get": func(r *porttest.InMemoryExampleRepository) (bool, error) {
			res, err := usecase.NewGetExampleUseCase(r).Execute(ctx, dto.GetExampleQuery{UUID: id})
			return res.IsSuccess() || len(res.Notifications()) > 0, err
		},
		"list": func(r *porttest.InMemoryExampleRepository) (bool, error) {
			res, err := usecase.NewListExamplesUseCase(r).Execute(ctx, dto.ListExamplesQuery{Page: shareddto.NewPageQuery(1, 10)})
			return res.IsSuccess() || len(res.Notifications()) > 0, err
		},
		"update": func(r *porttest.InMemoryExampleRepository) (bool, error) {
			res, err := usecase.NewUpdateExampleUseCase(r, fixedClock{t0}).Execute(ctx, dto.UpdateExampleCommand{Actor: admin, UUID: id, Name: "Demo", Version: 1})
			return res.IsSuccess() || len(res.Notifications()) > 0, err
		},
		"delete": func(r *porttest.InMemoryExampleRepository) (bool, error) {
			res, err := usecase.NewDeleteExampleUseCase(r).Execute(ctx, dto.DeleteExampleCommand{Actor: admin, UUID: id})
			return res.IsSuccess() || len(res.Notifications()) > 0, err
		},
		"change status": func(r *porttest.InMemoryExampleRepository) (bool, error) {
			res, err := usecase.NewChangeExampleStatusUseCase(r, &porttest.RecordingExampleEventPublisher{}, &sharedporttest.TransactionManager{}, fixedClock{t0}).Execute(ctx, dto.ChangeExampleStatusCommand{Actor: admin, UUID: id, Action: dto.ActionActivate})
			return res.IsSuccess() || len(res.Notifications()) > 0, err
		},
	}
	for name, fn := range run {
		t.Run(name, func(t *testing.T) {
			repo := porttest.NewInMemoryExampleRepository()
			repo.Err = errBoom

			hasResult, err := fn(repo)

			require.ErrorIs(t, err, errBoom, "el error técnico se propaga envuelto")
			_, isFailure := result.AsFailure(err)
			assert.False(t, isFailure)
			assert.False(t, hasResult, "no hay resultado de negocio junto a un error técnico")
		})
	}
}
