package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"myapp.v1.0.0/internal/example/application/dto"
	sharedhttp "myapp.v1.0.0/internal/shared/infrastructure/httpapi"
)

// CreateExampleRequest es el cuerpo de POST /api/v1/examples.
// La validación de forma va aquí; las reglas del nombre las aplica el model.
type CreateExampleRequest struct {
	Name string `json:"name" binding:"required,max=150"`
}

// Create atiende POST /api/v1/examples → 201 + Location.
func (h *ExampleHandler) Create(c *gin.Context) {
	var req CreateExampleRequest
	if !sharedhttp.BindJSON(c, &req) {
		return
	}

	res, err := h.create.Execute(c.Request.Context(), dto.CreateExampleCommand{
		Name:      req.Name,
		CreatedBy: sharedhttp.MustPrincipal(c).UserUUID, // del token, nunca del body
	})
	if !sharedhttp.Handle(c, res, err) {
		return
	}

	data := res.Data()
	c.Header("Location", "/api/v1"+basePath+"/"+data.UUID.String())
	sharedhttp.WriteData(c, http.StatusCreated, toExampleResponse(data), res.Notifications())
}
