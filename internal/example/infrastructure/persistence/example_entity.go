// Package persistence implementa los puertos de persistencia del módulo example con GORM.
package persistence

import (
	"time"

	"github.com/google/uuid"
)

// ExampleEntity mapea la tabla examples. El módulo example es su dueño de escritura.
//
// Sin default: de negocio en los tags (la única fuente es la migración y el model),
// y sin timestamps automáticos de GORM: el tiempo lo decide el caso de uso con su Clock.
type ExampleEntity struct {
	ID        int64     `gorm:"column:id;primaryKey"`
	UUID      uuid.UUID `gorm:"column:uuid;type:uuid"`
	Name      string    `gorm:"column:name"`
	Status    string    `gorm:"column:status"`
	Version   int       `gorm:"column:version"`
	CreatedBy uuid.UUID `gorm:"column:created_by;type:uuid"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime:false"`
}

// TableName implementa gorm.Tabler.
func (ExampleEntity) TableName() string { return "examples" }
