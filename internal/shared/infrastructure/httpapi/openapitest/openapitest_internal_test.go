package openapitest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Contrato propio: la prueba no depende de ningún módulo de negocio.
const spec = `
openapi: 3.0.3
info: { title: t, version: "1" }
servers: [{ url: /api/v1 }]
paths:
  /things/{id}:
    parameters:
      - { name: id, in: path, required: true, schema: { type: string } }
    get:
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema:
                type: object
                additionalProperties: false
                required: [name]
                properties:
                  name: { type: string }
    delete:
      responses:
        '204': { description: sin cuerpo }
`

func router(t *testing.T) routers.Router {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData([]byte(spec))
	require.NoError(t, err)
	require.NoError(t, doc.Validate(context.Background()))
	r, err := legacy.NewRouter(doc)
	require.NoError(t, err)
	return r
}

// recordingTB registra si la validación falló, sin hacer fallar la prueba real.
type recordingTB struct {
	testing.TB
	failed bool
	msg    string
}

func (r *recordingTB) Helper() {}
func (r *recordingTB) Errorf(format string, args ...any) {
	r.failed, r.msg = true, fmt.Sprintf(format, args...)
}

func TestValidate(t *testing.T) {
	tests := map[string]struct {
		method, path string
		status       int
		body         string
		wantFail     bool
	}{
		"respuesta correcta":       {http.MethodGet, "/api/v1/things/1", 200, `{"name":"a"}`, false},
		"204 sin cuerpo":           {http.MethodDelete, "/api/v1/things/1", 204, "", false},
		"campo sin documentar":     {http.MethodGet, "/api/v1/things/1", 200, `{"name":"a","secret":"x"}`, true},
		"falta un campo requerido": {http.MethodGet, "/api/v1/things/1", 200, `{}`, true},
		"tipo incorrecto":          {http.MethodGet, "/api/v1/things/1", 200, `{"name":1}`, true},
		"estado sin documentar":    {http.MethodGet, "/api/v1/things/1", 500, `{"name":"a"}`, true},
		"ruta fuera del contrato":  {http.MethodGet, "/api/v1/otra", 404, `{}`, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			if tt.body != "" {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
			}
			w.WriteHeader(tt.status)
			_, _ = w.WriteString(tt.body)
			rec := &recordingTB{TB: t}

			validate(rec, router(t), httptest.NewRequest(tt.method, tt.path, nil), w)

			assert.Equal(t, tt.wantFail, rec.failed, rec.msg)
		})
	}
}
