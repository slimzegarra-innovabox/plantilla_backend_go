package httpapi

import (
	"github.com/gin-gonic/gin"

	"myapp.v1.0.0/internal/example/application/dto"
	shareddto "myapp.v1.0.0/internal/shared/application/dto"
	sharedhttp "myapp.v1.0.0/internal/shared/infrastructure/httpapi"
)

// ListExamplesRequest son los filtros del listado (query params en camelCase).
// page, size, q y sort los leen sharedhttp.PageQuery, SearchQuery y SortQuery.
type ListExamplesRequest struct {
	Status string `form:"status" binding:"omitempty,oneof=pending active inactive"`
}

// List atiende GET /api/v1/examples?page=1&size=20&status=active&q=demo&sort=name,-createdAt.
func (h *ExampleHandler) List(c *gin.Context) {
	var req ListExamplesRequest
	if !sharedhttp.BindQuery(c, &req) {
		return
	}
	page, ok := sharedhttp.PageQuery(c)
	if !ok {
		return
	}
	// q busca en el nombre (contiene, sin distinguir mayúsculas).
	search, ok := sharedhttp.SearchQuery(c)
	if !ok {
		return
	}
	sort, ok := sharedhttp.SortQuery(c, dto.ExampleSortFields, dto.DefaultExampleSort)
	if !ok {
		return
	}

	res, err := h.list.Execute(c.Request.Context(), dto.ListExamplesQuery{Status: req.Status, Search: search, Sort: sort, Page: page})
	if !sharedhttp.Handle(c, res, err) {
		return
	}
	sharedhttp.WritePage(c, shareddto.MapPage(res.Data(), toExampleResponse), res.Notifications())
}
