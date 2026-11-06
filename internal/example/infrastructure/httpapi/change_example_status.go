package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"myapp.v1.0.0/internal/example/application/dto"
	sharedhttp "myapp.v1.0.0/internal/shared/infrastructure/httpapi"
)

// Activate atiende POST /api/v1/examples/{uuid}/activate.
func (h *ExampleHandler) Activate(c *gin.Context) { h.applyStatus(c, dto.ActionActivate) }

// Deactivate atiende POST /api/v1/examples/{uuid}/deactivate.
func (h *ExampleHandler) Deactivate(c *gin.Context) { h.applyStatus(c, dto.ActionDeactivate) }

// applyStatus traduce una intención de negocio (sub-recurso POST) al caso de uso.
func (h *ExampleHandler) applyStatus(c *gin.Context, action dto.ExampleStatusAction) {
	id, ok := sharedhttp.UUIDParam(c, "uuid")
	if !ok {
		return
	}

	res, err := h.changeStatus.Execute(c.Request.Context(), dto.ChangeExampleStatusCommand{Actor: actor(c), UUID: id, Action: action})
	if !sharedhttp.Handle(c, res, err) {
		return
	}
	sharedhttp.WriteData(c, http.StatusOK, toExampleResponse(res.Data()), res.Notifications())
}
