package persistence

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"myapp.v1.0.0/internal/example/application/apperr"
	"myapp.v1.0.0/internal/example/application/model"
	"myapp.v1.0.0/internal/example/application/port"
	shareddto "myapp.v1.0.0/internal/shared/application/dto"
	sharedpersistence "myapp.v1.0.0/internal/shared/infrastructure/persistence"
)

// constraintUniqueName es el índice único de la migración 000001.
const constraintUniqueName = "uq_examples_name"

// ExampleRepositoryAdapter implementa port.ExampleRepository con GORM.
type ExampleRepositoryAdapter struct {
	db *gorm.DB
}

var _ port.ExampleRepository = (*ExampleRepositoryAdapter)(nil)

// NewExampleRepositoryAdapter crea el adapter.
func NewExampleRepositoryAdapter(db *gorm.DB) *ExampleRepositoryAdapter {
	return &ExampleRepositoryAdapter{db: db}
}

// Create implementa port.ExampleRepository.
func (a *ExampleRepositoryAdapter) Create(ctx context.Context, e *model.Example) (*model.Example, error) {
	en := toExampleEntity(e)
	if err := sharedpersistence.DB(ctx, a.db).Create(&en).Error; err != nil {
		if sharedpersistence.IsUniqueViolation(err, constraintUniqueName) {
			return nil, apperr.ErrExampleNameTaken
		}
		return nil, fmt.Errorf("insertar example: %w", err)
	}
	return toExampleModel(en)
}

// FindByUUID implementa port.ExampleRepository.
func (a *ExampleRepositoryAdapter) FindByUUID(ctx context.Context, id uuid.UUID) (*model.Example, bool, error) {
	var en ExampleEntity
	err := sharedpersistence.DB(ctx, a.db).Where("uuid = ?", id).Take(&en).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("buscar example: %w", err)
	}
	e, err := toExampleModel(en)
	if err != nil {
		return nil, false, err
	}
	return e, true, nil
}

// List implementa port.ExampleRepository.
// sortColumns traduce los campos lógicos del contrato (dto.ExampleSortFields) a
// expresiones SQL. Es una lista cerrada: nunca se interpola texto del cliente.
var sortColumns = map[string]string{
	"name":      "lower(name)",
	"createdAt": "created_at",
	"updatedAt": "updated_at",
}

// likeEscaper escapa los comodines de LIKE: lo que busca el usuario es literal.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func (a *ExampleRepositoryAdapter) List(ctx context.Context, f port.ExampleFilter, sort shareddto.Sort, q shareddto.PageQuery) (shareddto.PageResult[*model.Example], error) {
	order, err := orderBy(sort)
	if err != nil {
		return shareddto.PageResult[*model.Example]{}, err
	}
	filtered := func() *gorm.DB {
		db := sharedpersistence.DB(ctx, a.db).Model(&ExampleEntity{})
		if f.Status != "" {
			db = db.Where("status = ?", string(f.Status))
		}
		if f.Search != "" {
			// ILIKE usa el índice trigram idx_examples_name_trgm (migración 000003).
			db = db.Where(`name ILIKE ? ESCAPE '\'`, "%"+likeEscaper.Replace(f.Search)+"%")
		}
		return db
	}

	total, err := sharedpersistence.CountUpTo(filtered(), shareddto.MaxCount)
	if err != nil {
		return shareddto.PageResult[*model.Example]{}, fmt.Errorf("contar examples: %w", err)
	}

	var rows []ExampleEntity
	err = filtered().
		Order(order). // orden explícito y estable (§9.3)
		Offset(q.Offset()).
		Limit(q.Limit()).
		Find(&rows).Error
	if err != nil {
		return shareddto.PageResult[*model.Example]{}, fmt.Errorf("listar examples: %w", err)
	}

	items := make([]*model.Example, 0, len(rows))
	for _, en := range rows {
		e, err := toExampleModel(en)
		if err != nil {
			return shareddto.PageResult[*model.Example]{}, err
		}
		items = append(items, e)
	}
	return shareddto.NewPageResult(q, items, total), nil
}

// Update implementa port.ExampleRepository con concurrencia optimista: la fila
// solo se modifica si su versión sigue siendo la que se leyó.
func (a *ExampleRepositoryAdapter) Update(ctx context.Context, e *model.Example) (*model.Example, error) {
	var en ExampleEntity
	res := sharedpersistence.DB(ctx, a.db).
		Model(&en).
		Clauses(clause.Returning{}).
		Where("uuid = ? AND version = ?", e.UUID(), e.Version()).
		Updates(map[string]any{
			"name":       e.Name().String(),
			"status":     string(e.Status()),
			"updated_at": e.UpdatedAt().UTC(),
			"version":    gorm.Expr("version + 1"),
		})
	if res.Error != nil {
		if sharedpersistence.IsUniqueViolation(res.Error, constraintUniqueName) {
			return nil, apperr.ErrExampleNameTaken
		}
		return nil, fmt.Errorf("actualizar example: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, a.whyNotUpdated(ctx, e.UUID())
	}
	return toExampleModel(en)
}

// whyNotUpdated distingue «no existe» de «otro escritor cambió la versión».
// orderBy arma el ORDER BY desde los campos permitidos, con id DESC de desempate.
func orderBy(sort shareddto.Sort) (string, error) {
	if len(sort) == 0 {
		sort = shareddto.Sort{{Field: "createdAt", Desc: true}}
	}
	parts := make([]string, 0, len(sort)+1)
	for _, s := range sort {
		col, ok := sortColumns[s.Field]
		if !ok {
			// La validación del borde HTTP ya lo impide: es un error de programación.
			return "", fmt.Errorf("campo de orden no soportado: %q", s.Field)
		}
		dir := "ASC"
		if s.Desc {
			dir = "DESC"
		}
		parts = append(parts, col+" "+dir)
	}
	return strings.Join(append(parts, "id DESC"), ", "), nil
}

// Delete implementa port.ExampleRepository: borra solo si la versión no cambió.
func (a *ExampleRepositoryAdapter) Delete(ctx context.Context, e *model.Example) error {
	res := sharedpersistence.DB(ctx, a.db).
		Where("uuid = ? AND version = ?", e.UUID(), e.Version()).
		Delete(&ExampleEntity{})
	if res.Error != nil {
		return fmt.Errorf("eliminar example: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return a.whyNotUpdated(ctx, e.UUID()) // ErrExampleNotFound o ErrExampleVersionConflict
	}
	return nil
}

func (a *ExampleRepositoryAdapter) whyNotUpdated(ctx context.Context, id uuid.UUID) error {
	var count int64
	if err := sharedpersistence.DB(ctx, a.db).Model(&ExampleEntity{}).Where("uuid = ?", id).Count(&count).Error; err != nil {
		return fmt.Errorf("verificar example: %w", err)
	}
	if count == 0 {
		return apperr.ErrExampleNotFound
	}
	return apperr.ErrExampleVersionConflict
}
