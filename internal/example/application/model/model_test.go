package model_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/example/application/apperr"
	"myapp.v1.0.0/internal/example/application/model"
)

var now = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestNewExampleName(t *testing.T) {
	tests := map[string]struct {
		raw     string
		want    string
		wantErr error
	}{
		"válido":                   {raw: "Demo", want: "Demo"},
		"recorta espacios":         {raw: "  Demo  ", want: "Demo"},
		"mínimo (2)":               {raw: "ab", want: "ab"},
		"máximo (150)":             {raw: strings.Repeat("a", 150), want: strings.Repeat("a", 150)},
		"cuenta runas, no bytes":   {raw: strings.Repeat("ñ", 150), want: strings.Repeat("ñ", 150)},
		"vacío":                    {raw: "", wantErr: apperr.ErrExampleNameRequired},
		"solo espacios":            {raw: "   ", wantErr: apperr.ErrExampleNameRequired},
		"1 carácter":               {raw: "a", wantErr: apperr.ErrExampleNameTooShort},
		"1 carácter tras recortar": {raw: " a ", wantErr: apperr.ErrExampleNameTooShort},
		"151 caracteres":           {raw: strings.Repeat("a", 151), wantErr: apperr.ErrExampleNameTooLong},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := model.NewExampleName(tt.raw)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.String())
		})
	}
}

func TestExampleNameEqualsIgnoresCase(t *testing.T) {
	a, _ := model.NewExampleName("Demo")
	b, _ := model.NewExampleName("DEMO")
	c, _ := model.NewExampleName("Otro")

	assert.True(t, a.Equals(b))
	assert.False(t, a.Equals(c))
}

func TestParseExampleStatusIsStrict(t *testing.T) {
	for _, code := range []string{"pending", "active", "inactive"} {
		s, err := model.ParseExampleStatus(code)
		require.NoError(t, err)
		assert.Equal(t, code, string(s))
	}
	for _, code := range []string{"", "ACTIVE", "deleted", " active"} {
		_, err := model.ParseExampleStatus(code)
		assert.Error(t, err, code)
	}
}

func TestExampleStatusTransitions(t *testing.T) {
	all := []model.ExampleStatus{model.ExampleStatusPending, model.ExampleStatusActive, model.ExampleStatusInactive}
	allowed := map[[2]model.ExampleStatus]bool{
		{model.ExampleStatusPending, model.ExampleStatusActive}:  true,
		{model.ExampleStatusActive, model.ExampleStatusInactive}: true,
		{model.ExampleStatusInactive, model.ExampleStatusActive}: true,
	}
	// Las 9 combinaciones: lo que no está en allowed está prohibido.
	for _, from := range all {
		for _, to := range all {
			assert.Equal(t, allowed[[2]model.ExampleStatus{from, to}], from.CanTransitionTo(to), "%s → %s", from, to)
		}
	}
}

func newExample(t *testing.T) *model.Example {
	t.Helper()
	name, err := model.NewExampleName("Demo")
	require.NoError(t, err)
	e, err := model.NewExample(name, uuid.New(), now)
	require.NoError(t, err)
	return e
}

func TestNewExampleStartsPending(t *testing.T) {
	by := uuid.New()
	name, _ := model.NewExampleName("Demo")

	e, err := model.NewExample(name, by, now)

	require.NoError(t, err)
	assert.Equal(t, model.ExampleStatusPending, e.Status())
	assert.Equal(t, 1, e.Version())
	assert.Equal(t, by, e.CreatedBy())
	assert.Equal(t, now, e.CreatedAt())
	assert.Equal(t, now, e.UpdatedAt())
	assert.Zero(t, e.ID(), "el id interno lo asigna la persistencia")
	assert.Equal(t, uuid.Version(7), e.UUID().Version())
}

func TestNewExampleRejectsProgrammingErrors(t *testing.T) {
	name, _ := model.NewExampleName("Demo")

	_, err := model.NewExample(name, uuid.Nil, now)
	assert.Error(t, err)

	_, err = model.NewExample(model.ExampleName{}, uuid.New(), now)
	assert.Error(t, err)
}

func TestExampleLifecycle(t *testing.T) {
	e := newExample(t)
	later := now.Add(time.Hour)

	require.ErrorIs(t, e.Deactivate(later), apperr.ErrExampleInvalidTransition, "pending → inactive no está permitido")
	assert.Equal(t, model.ExampleStatusPending, e.Status())
	assert.Equal(t, now, e.UpdatedAt(), "un cambio rechazado no toca updatedAt")

	require.NoError(t, e.Activate(later))
	assert.Equal(t, model.ExampleStatusActive, e.Status())
	assert.Equal(t, later, e.UpdatedAt())

	require.ErrorIs(t, e.Activate(later), apperr.ErrExampleInvalidTransition, "active → active no está permitido")
	require.NoError(t, e.Deactivate(later))
	require.NoError(t, e.Activate(later), "inactive → active (reactivar)")
}

func TestRename(t *testing.T) {
	e := newExample(t)
	later := now.Add(time.Minute)
	name, _ := model.NewExampleName("Nuevo")

	e.Rename(name, later)

	assert.Equal(t, "Nuevo", e.Name().String())
	assert.Equal(t, later, e.UpdatedAt())
	assert.Equal(t, 1, e.Version(), "la versión solo cambia al persistir")
}

func TestCanDelete(t *testing.T) {
	e := newExample(t)
	assert.NoError(t, e.CanDelete(), "pending se puede eliminar")

	require.NoError(t, e.Activate(now))
	assert.ErrorIs(t, e.CanDelete(), apperr.ErrExampleDeleteActive, "active no")

	require.NoError(t, e.Deactivate(now))
	assert.NoError(t, e.CanDelete(), "inactive sí")
}

func TestCanBeManagedBy(t *testing.T) {
	e := newExample(t)

	assert.NoError(t, e.CanBeManagedBy(e.CreatedBy(), false), "el creador")
	assert.NoError(t, e.CanBeManagedBy(uuid.New(), true), "un administrador")
	assert.ErrorIs(t, e.CanBeManagedBy(uuid.New(), false), apperr.ErrExampleForbidden, "un tercero")
}
