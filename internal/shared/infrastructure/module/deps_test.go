package module_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"myapp.v1.0.0/internal/shared/infrastructure/module"
)

func TestMustHaveStopsStartupWhenADependencyIsMissing(t *testing.T) {
	assert.NotPanics(t, func() { module.MustHave("orders", "S3_ENABLED", true) })
	assert.PanicsWithValue(t, "módulo orders: requiere S3_ENABLED=true", func() { module.MustHave("orders", "S3_ENABLED", false) })
}
