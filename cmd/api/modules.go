package main

import (
	"github.com/gin-gonic/gin"

	"myapp.v1.0.0/internal/example"
	"myapp.v1.0.0/internal/shared/infrastructure/module"
)

// registerModules monta cada módulo de negocio bajo /api/v1.
// Un módulo nuevo se agrega con una línea: <modulo>.Register(api, deps).
func registerModules(api *gin.RouterGroup, deps module.Deps) {
	example.Register(api, deps)
}
