package persistence

import (
	"fmt"

	"myapp.v1.0.0/internal/example/application/model"
)

func toExampleEntity(e *model.Example) ExampleEntity {
	return ExampleEntity{
		ID:        e.ID(),
		UUID:      e.UUID(),
		Name:      e.Name().String(),
		Status:    string(e.Status()),
		Version:   e.Version(),
		CreatedBy: e.CreatedBy(),
		CreatedAt: e.CreatedAt().UTC(),
		UpdatedAt: e.UpdatedAt().UTC(),
	}
}

// toExampleModel rehidrata el model. Es ESTRICTO: si un valor persistido no es
// válido devuelve un error técnico (dato corrupto → 500 en el log), nunca un
// model con valores vacíos o por defecto.
//
// Ojo: el error de NewExampleName es un *result.Failure. NO se envuelve: si
// viajara envuelto, result.FromError lo confundiría con un fallo de negocio y
// respondería 400 por un dato corrupto de la base de datos. Se crea un error
// técnico nuevo que nombra la regla incumplida.
func toExampleModel(en ExampleEntity) (*model.Example, error) {
	name, err := model.NewExampleName(en.Name)
	if err != nil || name.String() != en.Name {
		return nil, fmt.Errorf("examples.id=%d: el nombre persistido no cumple las reglas de ExampleName "+
			"(2-150 caracteres, sin espacios en los extremos)", en.ID)
	}
	status, err := model.ParseExampleStatus(en.Status)
	if err != nil {
		return nil, fmt.Errorf("examples.id=%d: %w", en.ID, err)
	}
	if en.Version < 1 {
		return nil, fmt.Errorf("examples.id=%d: versión persistida inválida (%d)", en.ID, en.Version)
	}
	return model.RestoreExample(
		en.ID, en.UUID, name, status, en.Version, en.CreatedBy,
		en.CreatedAt.UTC(), en.UpdatedAt.UTC(),
	), nil
}
