package dto_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/shared/application/dto"
)

func TestParseSort(t *testing.T) {
	allowed := []string{"name", "createdAt"}
	def := dto.Sort{{Field: "createdAt", Desc: true}}

	got, err := dto.ParseSort("name,-createdAt", allowed, def)
	require.NoError(t, err)
	assert.Equal(t, dto.Sort{{Field: "name"}, {Field: "createdAt", Desc: true}}, got)

	got, err = dto.ParseSort("  ", allowed, def)
	require.NoError(t, err)
	assert.Equal(t, def, got)

	for name, raw := range map[string]string{
		"campo no permitido": "password",
		"repetido":           "name,-name",
		"vacío":              "name,,createdAt",
		"demasiados":         "name,createdAt,name,createdAt",
		"inyección":          "name;DROP TABLE x",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := dto.ParseSort(raw, allowed, def)
			assert.ErrorIs(t, err, dto.ErrInvalidSort)
		})
	}
}
