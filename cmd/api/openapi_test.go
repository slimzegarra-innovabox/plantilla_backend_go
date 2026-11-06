package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
	sharedhttp "myapp.v1.0.0/internal/shared/infrastructure/httpapi"
	"myapp.v1.0.0/internal/shared/infrastructure/idempotency/idempotencytest"
	"myapp.v1.0.0/internal/shared/infrastructure/module"
	"myapp.v1.0.0/internal/shared/infrastructure/security"
)

// apiPrefix es el server de docs/openapi.yaml: las rutas del documento son relativas a él.
const apiPrefix = "/api/v1"

// TestOpenAPIMatchesRegisteredRoutes mantiene el contrato y el código sincronizados:
// toda ruta de /api/v1 está documentada y todo lo documentado existe (CLAUDE.md §9).
func TestOpenAPIMatchesRegisteredRoutes(t *testing.T) {
	doc := loadOpenAPI(t)

	documented := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			documented[method+" "+apiPrefix+path] = true
		}
	}

	registered := map[string]bool{}
	for _, r := range newTestRouter(t).Routes() {
		if strings.HasPrefix(r.Path, apiPrefix+"/") {
			registered[r.Method+" "+ginToOpenAPIPath(r.Path)] = true
		}
	}

	assert.Empty(t, missing(registered, documented), "rutas registradas sin documentar en docs/openapi.yaml")
	assert.Empty(t, missing(documented, registered), "rutas documentadas en docs/openapi.yaml que no existen")
}

// TestOpenAPIOperationsAreComplete exige lo mínimo de cada operación del contrato.
func TestOpenAPIOperationsAreComplete(t *testing.T) {
	doc := loadOpenAPI(t)
	ids := map[string]string{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			name := method + " " + path
			assert.NotEmpty(t, op.OperationID, "%s: sin operationId", name)
			if prev, dup := ids[op.OperationID]; dup {
				t.Errorf("operationId %q repetido en %s y %s", op.OperationID, prev, name)
			}
			ids[op.OperationID] = name
			assert.NotNil(t, op.Responses.Value("401"), "%s: toda ruta de /api/v1 exige token y documenta el 401", name)
			// El borde HTTP puede responder estos en cualquier ruta (CLAUDE.md §8.4).
			for _, status := range []string{"429", "500", "503"} {
				assert.NotNil(t, op.Responses.Value(status), "%s: sin respuesta %s", name, status)
			}
		}
	}
}

func loadOpenAPI(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(filepath.Join("..", "..", "docs", "openapi.yaml"))
	require.NoError(t, err)
	require.NoError(t, doc.Validate(context.Background()), "docs/openapi.yaml no es OpenAPI válido")
	return doc
}

// newTestRouter registra los módulos reales con dependencias que no se usan:
// Register solo arma adapters, casos de uso y rutas, sin tocar la base de datos.
func newTestRouter(t *testing.T) interface{ Routes() gin.RoutesInfo } {
	t.Helper()
	engine, err := sharedhttp.NewEngine(sharedhttp.EngineOptions{
		Logger:         slog.New(slog.DiscardHandler),
		BodyLimitBytes: 1 << 20,
		RequestTimeout: time.Second,
	})
	require.NoError(t, err)
	deps := module.Deps{
		Auth: sharedhttp.NewAuthenticator(security.NewJWTVerifier(config.JWTConfig{
			Secret: "0123456789abcdef0123456789abcdef", Issuer: "iss", Audience: "aud",
		})),
		Idempotent: sharedhttp.Idempotent(idempotencytest.NewMemory()),
	}
	registerModules(engine.Group(apiPrefix), deps)
	return engine
}

var ginParam = regexp.MustCompile(`:([A-Za-z0-9_]+)`)

// ginToOpenAPIPath convierte /examples/:uuid en /examples/{uuid}.
func ginToOpenAPIPath(p string) string { return ginParam.ReplaceAllString(p, "{$1}") }

func missing(from, in map[string]bool) []string {
	var out []string
	for k := range from {
		if !in[k] {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}
