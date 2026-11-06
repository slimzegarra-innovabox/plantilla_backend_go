package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"myapp.v1.0.0/internal/example/application/dto"
	sharedhttp "myapp.v1.0.0/internal/shared/infrastructure/httpapi"
)

// UpdateExampleRequest es el cuerpo de PUT /api/v1/examples/{uuid}.
// version es la que el cliente leyó: si cambió, la respuesta es 409.
type UpdateExampleRequest struct {
	Name    string `json:"name" binding:"required,max=150"`
	Version int    `json:"version" binding:"required,gte=1"`
}

// Update atiende PUT /api/v1/examples/{uuid}.
func (h *ExampleHandler) Update(c *gin.Context) {
	id, ok := sharedhttp.UUIDParam(c, "uuid")
	if !ok {
		return
	}
	var req UpdateExampleRequest
	if !sharedhttp.BindJSON(c, &req) {
		return
	}

	res, err := h.update.Execute(c.Request.Context(), dto.UpdateExampleCommand{
		Actor: actor(c), UUID: id, Name: req.Name, Version: req.Version,
	})
	if !sharedhttp.Handle(c, res, err) {
		return
	}
	sharedhttp.WriteData(c, http.StatusOK, toExampleResponse(res.Data()), res.Notifications())
}
