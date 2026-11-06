package persistence

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/example/application/model"
	"myapp.v1.0.0/internal/shared/application/result"
)

func validEntity() ExampleEntity {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("PET", -5*3600))
	return ExampleEntity{
		ID: 7, UUID: uuid.New(), Name: "Demo", Status: "active", Version: 3,
		CreatedBy: uuid.New(), CreatedAt: ts, UpdatedAt: ts,
	}
}

func TestToExampleModelRoundTrip(t *testing.T) {
	en := validEntity()

	e, err := toExampleModel(en)

	require.NoError(t, err)
	assert.Equal(t, en.ID, e.ID())
	assert.Equal(t, model.ExampleStatusActive, e.Status())
	assert.Equal(t, 3, e.Version())
	assert.Equal(t, time.UTC, e.CreatedAt().Location(), "los tiempos se normalizan a UTC")

	back := toExampleEntity(e)
	assert.Equal(t, en.UUID, back.UUID)
	assert.Equal(t, en.Name, back.Name)
	assert.True(t, en.CreatedAt.Equal(back.CreatedAt))
}

func TestToExampleModelRejectsCorruptData(t *testing.T) {
	tests := map[string]func(*ExampleEntity){
		"nombre corto":        func(en *ExampleEntity) { en.Name = "a" },
		"nombre vacío":        func(en *ExampleEntity) { en.Name = "" },
		"nombre con espacios": func(en *ExampleEntity) { en.Name = " Demo " },
		"estado desconocido":  func(en *ExampleEntity) { en.Status = "deleted" },
		"estado vacío":        func(en *ExampleEntity) { en.Status = "" },
		"versión cero":        func(en *ExampleEntity) { en.Version = 0 },
	}
	for name, corrupt := range tests {
		t.Run(name, func(t *testing.T) {
			en := validEntity()
			corrupt(&en)

			e, err := toExampleModel(en)

			require.Error(t, err)
			assert.Nil(t, e, "nunca un model a medias")
			_, isFailure := result.AsFailure(err)
			assert.False(t, isFailure, "un dato corrupto es un error técnico (500), no un fallo de negocio (400)")
			assert.Contains(t, err.Error(), "examples.id=7")
		})
	}
}
