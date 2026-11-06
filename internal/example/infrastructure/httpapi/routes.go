package httpapi

import (
	"github.com/gin-gonic/gin"

	sharedhttp "myapp.v1.0.0/internal/shared/infrastructure/httpapi"
)

// RegisterRoutes monta las rutas del módulo. Todas exigen token; las de escritura,
// además, el scope example:write. Este módulo no tiene rutas públicas.
func RegisterRoutes(api *gin.RouterGroup, h *ExampleHandler, auth *sharedhttp.Authenticator, idempotent gin.HandlerFunc) {
	read := sharedhttp.RequireScopes(ScopeRead)
	write := sharedhttp.RequireScopes(ScopeWrite)

	g := api.Group(basePath, auth.Required())
	g.GET("", read, h.List)
	g.GET("/:uuid", read, h.Get)
	g.POST("", write, idempotent, h.Create) // crear admite Idempotency-Key
	g.PUT("/:uuid", write, h.Update)
	g.POST("/:uuid/activate", write, h.Activate)
	g.POST("/:uuid/deactivate", write, h.Deactivate)
	g.DELETE("/:uuid", write, h.Delete)
}
