// Package openapitest valida en las pruebas HTTP que cada respuesta real cumple el
// contrato docs/openapi.yaml (CLAUDE.md §9.4): el estado está documentado para esa
// operación y el cuerpo cumple su esquema.
//
// Los esquemas son estrictos (additionalProperties: false): un campo nuevo sin
// documentar hace fallar la prueba, además de un campo que falta o de otro tipo.
//
// Como pgtest, solo lo importan las pruebas: no llega a los binarios.
package openapitest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
)

// load carga el contrato una sola vez por proceso de prueba.
var load = sync.OnceValues(func() (routers.Router, error) {
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..", "docs", "openapi.yaml")
	doc, err := openapi3.NewLoader().LoadFromFile(path)
	if err != nil {
		return nil, err
	}
	if err := doc.Validate(context.Background()); err != nil {
		return nil, err
	}
	return legacy.NewRouter(doc)
})

// ValidateResponse falla la prueba si la respuesta no cumple el contrato.
//
// Las rutas que el contrato no declara (p. ej. una prueba de ROUTE_NOT_FOUND) no se
// validan: que no haya rutas sin documentar lo comprueba cmd/api/openapi_test.go.
func ValidateResponse(t testing.TB, req *http.Request, w *httptest.ResponseRecorder) {
	t.Helper()
	router, err := load()
	if err != nil {
		t.Fatalf("openapitest: cargar docs/openapi.yaml: %v", err)
	}
	validate(t, router, req, w)
}

func validate(t testing.TB, router routers.Router, req *http.Request, w *httptest.ResponseRecorder) {
	t.Helper()
	route, params, err := router.FindRoute(req)
	if err != nil {
		var routeErr *routers.RouteError
		if errors.As(err, &routeErr) || errors.Is(err, routers.ErrPathNotFound) || errors.Is(err, routers.ErrMethodNotAllowed) {
			return
		}
		t.Fatalf("openapitest: buscar %s %s: %v", req.Method, req.URL.Path, err)
	}

	err = openapi3filter.ValidateResponse(req.Context(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
		Status:                 w.Code,
		Header:                 w.Header(),
		Body:                   io.NopCloser(bytes.NewReader(w.Body.Bytes())),
		Options:                &openapi3filter.Options{IncludeResponseStatus: true, MultiError: true},
	})
	if err != nil {
		t.Errorf("%s %s → %d no cumple docs/openapi.yaml:\n%v\ncuerpo: %s", req.Method, req.URL.Path, w.Code, err, w.Body.String())
	}
}
