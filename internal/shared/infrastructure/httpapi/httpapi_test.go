package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	shareddto "myapp.v1.0.0/internal/shared/application/dto"
	"myapp.v1.0.0/internal/shared/application/result"
	sharedhttp "myapp.v1.0.0/internal/shared/infrastructure/httpapi"
	"myapp.v1.0.0/internal/shared/infrastructure/idempotency"
	"myapp.v1.0.0/internal/shared/infrastructure/idempotency/idempotencytest"
)

func init() { gin.SetMode(gin.TestMode) }

func defaultOpts() sharedhttp.EngineOptions {
	return sharedhttp.EngineOptions{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		HSTS:           true,
		CORSOrigins:    []string{"https://app.example.com"},
		BodyLimitBytes: 1 << 10,
		RequestTimeout: time.Second,
	}
}

func newEngine(t *testing.T, opts sharedhttp.EngineOptions, register func(e *gin.Engine)) *gin.Engine {
	t.Helper()
	e, err := sharedhttp.NewEngine(opts)
	require.NoError(t, err)
	register(e)
	return e
}

func do(e http.Handler, method, target, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) sharedhttp.Envelope {
	t.Helper()
	var env sharedhttp.Envelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), w.Body.String())
	return env
}

//--------------------------------------------------------------
// TABLA CATEGORÍA → HTTP
//--------------------------------------------------------------

func TestStatusForEveryCategory(t *testing.T) {
	want := map[result.Category]int{
		result.Validation:   400,
		result.Unauthorized: 401,
		result.Forbidden:    403,
		result.NotFound:     404,
		result.Conflict:     409,
		result.BusinessRule: 422,
		result.Technical:    500,
		"DESCONOCIDA":       500,
	}
	for cat, status := range want {
		assert.Equal(t, status, sharedhttp.StatusFor(cat), cat)
	}
}

func TestStatusForNotificationsUsesFirstError(t *testing.T) {
	notes := []result.Notification{
		result.Warning(result.Validation, "W", "aviso"),
		result.Error(result.NotFound, "A", "a"),
		result.Error(result.Validation, "B", "b"),
	}
	assert.Equal(t, 404, sharedhttp.StatusForNotifications(notes))
	assert.Equal(t, 500, sharedhttp.StatusForNotifications(nil))
}

//--------------------------------------------------------------
// SOBRE DE RESPUESTA
//--------------------------------------------------------------

func TestWriteDataEnvelope(t *testing.T) {
	e := newEngine(t, defaultOpts(), func(e *gin.Engine) {
		e.GET("/x", func(c *gin.Context) {
			sharedhttp.WriteData(c, http.StatusCreated, gin.H{"name": "demo"}, nil)
		})
	})

	w := do(e, http.MethodGet, "/x", "", sharedhttp.HeaderRequestID, "req-123")

	require.Equal(t, http.StatusCreated, w.Code)
	assert.JSONEq(t, `{"data":{"name":"demo"},"page":null,"notifications":[],"request_id":"req-123"}`, w.Body.String())
	assert.Equal(t, "req-123", w.Header().Get(sharedhttp.HeaderRequestID))
}

func TestWritePageEnvelope(t *testing.T) {
	e := newEngine(t, defaultOpts(), func(e *gin.Engine) {
		e.GET("/x", func(c *gin.Context) {
			q, ok := sharedhttp.PageQuery(c)
			if !ok {
				return
			}
			sharedhttp.WritePage(c, shareddto.NewPageResult(q, []string{"a"}, 41), nil)
		})
	})

	w := do(e, http.MethodGet, "/x?page=3&size=20", "")

	require.Equal(t, http.StatusOK, w.Code)
	env := decode(t, w)
	assert.Equal(t, []any{"a"}, env.Data)
	assert.Equal(t, &sharedhttp.PageMeta{Page: 3, Size: 20, TotalElements: 41, TotalPages: 3}, env.Page)
}

func TestWriteFailureUsesCategory(t *testing.T) {
	e := newEngine(t, defaultOpts(), func(e *gin.Engine) {
		e.GET("/x", func(c *gin.Context) {
			sharedhttp.WriteFailure(c, []result.Notification{
				result.Error(result.NotFound, "SAMPLE_NOT_FOUND", "No existe."),
			})
		})
	})

	w := do(e, http.MethodGet, "/x", "")

	require.Equal(t, http.StatusNotFound, w.Code)
	env := decode(t, w)
	assert.Nil(t, env.Data)
	require.Len(t, env.Notifications, 1)
	assert.Equal(t, sharedhttp.NotificationJSON{
		Category: "NOT_FOUND", Code: "SAMPLE_NOT_FOUND", Message: "No existe.", Severity: "ERROR",
	}, env.Notifications[0])
	assert.NotEmpty(t, env.RequestID)
}

//--------------------------------------------------------------
// ERRORES TÉCNICOS (§8.4)
//--------------------------------------------------------------

func TestErrorHandlerTechnicalErrors(t *testing.T) {
	tests := map[string]struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		"error genérico":        {errors.New("dial tcp: connection refused"), 500, "UNEXPECTED_ERROR"},
		"timeout":               {fmt.Errorf("consulta: %w", context.DeadlineExceeded), 503, "SERVICE_TIMEOUT"},
		"statement_timeout":     {fmt.Errorf("listar: %w", &pgconn.PgError{Code: "57014"}), 503, "SERVICE_TIMEOUT"},
		"lock_timeout":          {&pgconn.PgError{Code: "55P03"}, 503, "SERVICE_TIMEOUT"},
		"unicidad no traducida": {fmt.Errorf("insert: %w", &pgconn.PgError{Code: "23505", ConstraintName: "uq_secret"}), 409, "DATA_CONFLICT"},
		"FK no traducida":       {&pgconn.PgError{Code: "23503"}, 409, "DATA_CONFLICT"},
		"NOT NULL es un bug":    {&pgconn.PgError{Code: "23502"}, 500, "UNEXPECTED_ERROR"},
		"Failure por c.Error":   {result.NewFailure(result.Conflict, "SAMPLE_DUP", "dup"), 409, "SAMPLE_DUP"},
		"Failure envuelto":      {fmt.Errorf("x: %w", result.NewFailure(result.NotFound, "SAMPLE_NF", "nf")), 404, "SAMPLE_NF"},
		"cancelado por cliente": {context.Canceled, 499, ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			e := newEngine(t, defaultOpts(), func(e *gin.Engine) {
				e.GET("/x", func(c *gin.Context) { _ = c.Error(tt.err) })
			})

			w := do(e, http.MethodGet, "/x", "")

			require.Equal(t, tt.wantStatus, w.Code)
			if tt.wantCode == "" {
				assert.Empty(t, w.Body.String())
				return
			}
			env := decode(t, w)
			require.Len(t, env.Notifications, 1)
			assert.Equal(t, tt.wantCode, env.Notifications[0].Code)
			assert.NotContains(t, w.Body.String(), "connection refused", "no expone el error técnico")
			assert.NotContains(t, w.Body.String(), "uq_secret", "no expone el nombre del constraint")
		})
	}
}

func TestRecoveryConvertsPanicTo500(t *testing.T) {
	e := newEngine(t, defaultOpts(), func(e *gin.Engine) {
		e.GET("/x", func(*gin.Context) { panic("detalle interno") })
	})

	w := do(e, http.MethodGet, "/x", "")

	require.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "UNEXPECTED_ERROR", decode(t, w).Notifications[0].Code)
	assert.NotContains(t, w.Body.String(), "detalle interno")
}

func TestNoRouteAndNoMethod(t *testing.T) {
	e := newEngine(t, defaultOpts(), func(e *gin.Engine) {
		e.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	})

	w := do(e, http.MethodGet, "/no-existe", "")
	require.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "ROUTE_NOT_FOUND", decode(t, w).Notifications[0].Code)
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"), "los middlewares globales también aplican al 404")

	w = do(e, http.MethodDelete, "/x", "")
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.Equal(t, "METHOD_NOT_ALLOWED", decode(t, w).Notifications[0].Code)
}

func TestRequestTimeoutCancelsContext(t *testing.T) {
	opts := defaultOpts()
	opts.RequestTimeout = 20 * time.Millisecond
	e := newEngine(t, opts, func(e *gin.Engine) {
		e.GET("/slow", func(c *gin.Context) {
			<-c.Request.Context().Done() // simula una consulta que respeta el ctx
			_ = c.Error(c.Request.Context().Err())
		})
	})

	w := do(e, http.MethodGet, "/slow", "")

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "SERVICE_TIMEOUT", decode(t, w).Notifications[0].Code)
}

//--------------------------------------------------------------
// BINDING Y PARÁMETROS
//--------------------------------------------------------------

type sampleRequest struct {
	Name      string    `json:"name" binding:"required,max=5"`
	OwnerUUID uuid.UUID `json:"owner_uuid" binding:"nonzerouuid"`
}

func bindEngine(t *testing.T) *gin.Engine {
	return newEngine(t, defaultOpts(), func(e *gin.Engine) {
		e.POST("/x", func(c *gin.Context) {
			var req sampleRequest
			if !sharedhttp.BindJSON(c, &req) {
				return
			}
			sharedhttp.WriteData(c, http.StatusOK, req.Name, nil)
		})
		e.GET("/x/:uuid", func(c *gin.Context) {
			id, ok := sharedhttp.UUIDParam(c, "uuid")
			if !ok {
				return
			}
			sharedhttp.WriteData(c, http.StatusOK, id, nil)
		})
	})
}

func TestBindJSONValidationErrorsUsePublicFieldNames(t *testing.T) {
	w := do(bindEngine(t), http.MethodPost, "/x", `{"name":"demasiado largo"}`)

	require.Equal(t, http.StatusBadRequest, w.Code)
	env := decode(t, w)
	require.Len(t, env.Notifications, 2)
	assert.Equal(t, "REQUEST_FIELD_INVALID", env.Notifications[0].Code)
	assert.Equal(t, "name", env.Notifications[0].Field)
	assert.Equal(t, "Debe tener como máximo 5 caracteres.", env.Notifications[0].Message)
	assert.Equal(t, "owner_uuid", env.Notifications[1].Field, "rechaza el uuid cero")
}

func TestBindJSONMalformedBody(t *testing.T) {
	for name, body := range map[string]string{
		"sintaxis":     `{"name":`,
		"tipo erróneo": `{"name": 5}`,
		"vacío":        ``,
	} {
		t.Run(name, func(t *testing.T) {
			w := do(bindEngine(t), http.MethodPost, "/x", body, "Content-Type", "application/json")

			require.Equal(t, http.StatusBadRequest, w.Code)
			assert.Equal(t, "INVALID_REQUEST_BODY", decode(t, w).Notifications[0].Code)
		})
	}
}

func TestBindRejectsTextPostgresCannotStore(t *testing.T) {
	type nested struct {
		Tags []string          `json:"tags" form:"tags"`
		Meta map[string]string `json:"meta"`
		Any  any               `json:"any"`
	}
	type request struct {
		Name   string  `json:"name" form:"name"`
		Note   *string `json:"note"`
		Nested nested  `json:"nested"`
	}
	engine := gin.New()
	engine.POST("/", func(c *gin.Context) {
		var r request
		if sharedhttp.BindJSON(c, &r) {
			c.Status(http.StatusNoContent)
		}
	})
	engine.GET("/", func(c *gin.Context) {
		var r request
		if sharedhttp.BindQuery(c, &r) {
			c.Status(http.StatusNoContent)
		}
	})
	send := func(method, target, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(w, req)
		return w
	}

	for name, body := range map[string]string{
		"campo":   `{"name":"ab\u0000cd"}`,
		"puntero": `{"note":"\u0000"}`,
		"lista":   `{"nested":{"tags":["ok","\u0000"]}}`,
		"mapa":    `{"nested":{"meta":{"k":"\u0000"}}}`,
		"clave":   `{"nested":{"meta":{"\u0000":"v"}}}`,
		"any":     `{"nested":{"any":{"a":["\u0000"]}}}`,
	} {
		w := send(http.MethodPost, "/", body)
		assert.Equal(t, http.StatusBadRequest, w.Code, name)
		assert.Contains(t, w.Body.String(), "INVALID_REQUEST_BODY", name)
	}
	for _, q := range []string{"abc%00", "abc%FF"} {
		w := send(http.MethodGet, "/?name="+q, "")
		assert.Equal(t, http.StatusBadRequest, w.Code, q)
		assert.Contains(t, w.Body.String(), "INVALID_REQUEST_PARAMETER", q)
	}

	assert.Equal(t, http.StatusNoContent, send(http.MethodPost, "/", `{"name":"ñandú \\u0000 literal 🦜"}`).Code,
		"una barra seguida de u0000 es texto normal")
	assert.Equal(t, http.StatusNoContent, send(http.MethodGet, "/?name=%C3%B1and%C3%BA", "").Code)
}

func TestBodyLimit(t *testing.T) {
	big := `{"name":"` + strings.Repeat("a", 2<<10) + `"}`

	w := do(bindEngine(t), http.MethodPost, "/x", big)
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.Equal(t, "PAYLOAD_TOO_LARGE", decode(t, w).Notifications[0].Code)

	// Sin Content-Length (chunked): lo corta MaxBytesReader al leer.
	req := httptest.NewRequest(http.MethodPost, "/x", io.MultiReader(strings.NewReader(big)))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	bindEngine(t).ServeHTTP(w, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

func TestUUIDParam(t *testing.T) {
	e := bindEngine(t)

	w := do(e, http.MethodGet, "/x/no-uuid", "")
	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "uuid", decode(t, w).Notifications[0].Field)

	w = do(e, http.MethodGet, "/x/"+uuid.Nil.String(), "")
	require.Equal(t, http.StatusBadRequest, w.Code)

	id := uuid.New()
	w = do(e, http.MethodGet, "/x/"+id.String(), "")
	require.Equal(t, http.StatusOK, w.Code)
}

func TestPageQueryValidation(t *testing.T) {
	e := newEngine(t, defaultOpts(), func(e *gin.Engine) {
		e.GET("/x", func(c *gin.Context) {
			q, ok := sharedhttp.PageQuery(c)
			if !ok {
				return
			}
			sharedhttp.WriteData(c, http.StatusOK, []int{q.Page(), q.Size()}, nil)
		})
	})

	tests := map[string]struct {
		query      string
		wantStatus int
		wantData   []any
		wantField  string
	}{
		"por defecto":             {"", 200, []any{1.0, 20.0}, ""},
		"página 0 → 1":            {"?page=0&size=10", 200, []any{1.0, 10.0}, ""},
		"size 100":                {"?size=100", 200, []any{1.0, 100.0}, ""},
		"size 101":                {"?size=101", 400, nil, "size"},
		"size 0":                  {"?size=0", 400, nil, "size"},
		"page no entero":          {"?page=uno", 400, nil, "page"},
		"última página permitida": {"?page=1001&size=100", 200, []any{1001.0, 100.0}, ""},
		"page sobre el máximo":    {"?page=1002&size=100", 400, nil, "page"},
		"page que desbordaría":    {"?page=4611686018427387905&size=4", 400, nil, "page"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			w := do(e, http.MethodGet, "/x"+tt.query, "")
			require.Equal(t, tt.wantStatus, w.Code)
			env := decode(t, w)
			if tt.wantField != "" {
				assert.Equal(t, tt.wantField, env.Notifications[0].Field)
				return
			}
			assert.Equal(t, tt.wantData, env.Data)
		})
	}
}

//--------------------------------------------------------------
// AUTENTICACIÓN Y SCOPES
//--------------------------------------------------------------

type fakeVerifier struct{}

func (fakeVerifier) Verify(token string) (shareddto.Principal, error) {
	switch token {
	case "reader":
		return shareddto.Principal{UserUUID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Scopes: []string{"x:read"}}, nil
	case "writer":
		return shareddto.Principal{UserUUID: uuid.New(), Scopes: []string{"x:read", "x:write"}}, nil
	default:
		return shareddto.Principal{}, errors.New("firma inválida: detalle interno")
	}
}

func authEngine(t *testing.T) *gin.Engine {
	auth := sharedhttp.NewAuthenticator(fakeVerifier{})
	return newEngine(t, defaultOpts(), func(e *gin.Engine) {
		e.GET("/read", auth.Required(), func(c *gin.Context) {
			sharedhttp.WriteData(c, http.StatusOK, sharedhttp.MustPrincipal(c).UserUUID, nil)
		})
		e.POST("/write", auth.Required(), sharedhttp.RequireScopes("x:write"), func(c *gin.Context) {
			c.Status(http.StatusNoContent)
		})
		e.GET("/unprotected", func(c *gin.Context) { sharedhttp.MustPrincipal(c) })
	})
}

func TestAuthenticator(t *testing.T) {
	e := authEngine(t)

	tests := map[string]struct {
		method, path, header string
		wantStatus           int
		wantCode             string
	}{
		"sin cabecera":        {http.MethodGet, "/read", "", 401, "AUTH_TOKEN_INVALID"},
		"esquema incorrecto":  {http.MethodGet, "/read", "Basic abc", 401, "AUTH_TOKEN_INVALID"},
		"Bearer vacío":        {http.MethodGet, "/read", "Bearer ", 401, "AUTH_TOKEN_INVALID"},
		"token inválido":      {http.MethodGet, "/read", "Bearer otro", 401, "AUTH_TOKEN_INVALID"},
		"token válido":        {http.MethodGet, "/read", "Bearer reader", 200, ""},
		"bearer en minúscula": {http.MethodGet, "/read", "bearer reader", 200, ""},
		"falta scope":         {http.MethodPost, "/write", "Bearer reader", 403, "AUTH_FORBIDDEN"},
		"con scope":           {http.MethodPost, "/write", "Bearer writer", 204, ""},
		"ruta sin Required":   {http.MethodGet, "/unprotected", "Bearer writer", 500, "UNEXPECTED_ERROR"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			w := do(e, tt.method, tt.path, "", "Authorization", tt.header)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), "detalle interno")
			if tt.wantCode != "" {
				assert.Equal(t, tt.wantCode, decode(t, w).Notifications[0].Code)
			}
		})
	}
}

//--------------------------------------------------------------
// MIDDLEWARES GLOBALES
//--------------------------------------------------------------

func okEngine(t *testing.T, opts sharedhttp.EngineOptions) *gin.Engine {
	return newEngine(t, opts, func(e *gin.Engine) {
		e.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
		e.OPTIONS("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	})
}

func TestSecurityHeaders(t *testing.T) {
	w := do(okEngine(t, defaultOpts()), http.MethodGet, "/x", "")

	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Contains(t, w.Header().Get("Content-Security-Policy"), "default-src 'none'")
	assert.Contains(t, w.Header().Get("Strict-Transport-Security"), "max-age=")

	opts := defaultOpts()
	opts.HSTS = false
	w = do(okEngine(t, opts), http.MethodGet, "/x", "")
	assert.Empty(t, w.Header().Get("Strict-Transport-Security"))
}

func TestRequestIDRejectsUnsafeClientValues(t *testing.T) {
	e := okEngine(t, defaultOpts())

	w := do(e, http.MethodGet, "/x", "", sharedhttp.HeaderRequestID, "valid-id_1.2")
	assert.Equal(t, "valid-id_1.2", w.Header().Get(sharedhttp.HeaderRequestID))

	w = do(e, http.MethodGet, "/x", "", sharedhttp.HeaderRequestID, "malo\ninyectado")
	_, err := uuid.Parse(w.Header().Get(sharedhttp.HeaderRequestID))
	assert.NoError(t, err, "un valor inseguro se reemplaza por un uuid")
}

func TestCORS(t *testing.T) {
	e := okEngine(t, defaultOpts())

	w := do(e, http.MethodOptions, "/x", "", "Origin", "https://app.example.com", "Access-Control-Request-Method", "POST")
	require.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "https://app.example.com", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, w.Header().Get("Access-Control-Allow-Headers"), "Authorization")
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Credentials"))

	w = do(e, http.MethodGet, "/x", "", "Origin", "https://app.example.com")
	assert.Equal(t, "https://app.example.com", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, w.Header().Get("Access-Control-Expose-Headers"), "Location")

	w = do(e, http.MethodGet, "/x", "", "Origin", "https://evil.example.com")
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))

	w = do(e, http.MethodOptions, "/x", "", "Origin", "https://evil.example.com", "Access-Control-Request-Method", "POST")
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestRateLimit(t *testing.T) {
	opts := defaultOpts()
	opts.RateLimitRPS = 1
	opts.RateLimitBurst = 2
	e := okEngine(t, opts)

	assert.Equal(t, http.StatusOK, do(e, http.MethodGet, "/x", "").Code)
	assert.Equal(t, http.StatusOK, do(e, http.MethodGet, "/x", "").Code)
	w := do(e, http.MethodGet, "/x", "")
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "1", w.Header().Get("Retry-After"))
	assert.Equal(t, "TOO_MANY_REQUESTS", decode(t, w).Notifications[0].Code)
}

func TestNewEngineRejectsInvalidTrustedProxies(t *testing.T) {
	opts := defaultOpts()
	opts.TrustedProxies = []string{"no-es-una-ip"}

	_, err := sharedhttp.NewEngine(opts)

	assert.Error(t, err)
}

func TestAccessLogDemotesSuccessfulHealthProbes(t *testing.T) {
	var buf strings.Builder
	opts := defaultOpts()
	opts.Logger = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	e := newEngine(t, opts, func(e *gin.Engine) {
		e.GET("/health/live", func(c *gin.Context) { c.Status(http.StatusOK) })
		e.GET("/health/ready", func(c *gin.Context) { c.Status(http.StatusServiceUnavailable) })
		e.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	})

	do(e, http.MethodGet, "/health/live", "")
	assert.NotContains(t, buf.String(), "/health/live", "una sonda exitosa va a DEBUG")

	do(e, http.MethodGet, "/health/ready", "")
	assert.Contains(t, buf.String(), "/health/ready", "una sonda fallida sí se registra")

	do(e, http.MethodGet, "/x", "")
	assert.Contains(t, buf.String(), `"route":"/x"`)
}

//--------------------------------------------------------------
// IDEMPOTENCIA
//--------------------------------------------------------------

// stableVerifier da una identidad fija por token: el alcance de la clave es por usuario.
type stableVerifier struct{}

func (stableVerifier) Verify(token string) (shareddto.Principal, error) {
	ids := map[string]string{"writer": "22222222-2222-2222-2222-222222222222", "reader": "11111111-1111-1111-1111-111111111111"}
	return shareddto.Principal{UserUUID: uuid.MustParse(ids[token])}, nil
}

// idempotentEngine monta una ruta protegida e idempotente; exec cuenta las ejecuciones
// y handler decide qué responde.
func idempotentEngine(t *testing.T, store idempotency.Store, handler gin.HandlerFunc) (*gin.Engine, *atomic.Int32) {
	t.Helper()
	var exec atomic.Int32
	auth := sharedhttp.NewAuthenticator(stableVerifier{})
	e := newEngine(t, defaultOpts(), func(e *gin.Engine) {
		e.POST("/x", auth.Required(), sharedhttp.Idempotent(store), func(c *gin.Context) {
			exec.Add(1)
			handler(c)
		})
	})
	return e, &exec
}

func postIdem(e *gin.Engine, token, key, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set(sharedhttp.HeaderIdempotencyKey, key)
	}
	e.ServeHTTP(w, r)
	return w
}

func created(c *gin.Context) {
	c.Header("Location", "/x/1")
	sharedhttp.WriteData(c, http.StatusCreated, map[string]string{"id": "1"}, nil)
}

func TestIdempotentRepeatsTheResponseWithoutExecuting(t *testing.T) {
	e, exec := idempotentEngine(t, idempotencytest.NewMemory(), created)

	first := postIdem(e, "writer", "k1", `{"a":1}`)
	second := postIdem(e, "writer", "k1", `{"a":1}`)

	assert.EqualValues(t, 1, exec.Load(), "el reintento no ejecuta")
	require.Equal(t, http.StatusCreated, second.Code)
	assert.Empty(t, first.Header().Get(sharedhttp.HeaderIdempotencyReplayed))
	assert.Equal(t, "true", second.Header().Get(sharedhttp.HeaderIdempotencyReplayed))
	assert.Equal(t, "/x/1", second.Header().Get("Location"))
	assert.Equal(t, "application/json; charset=utf-8", second.Header().Get("Content-Type"))

	env := decode(t, second)
	assert.Equal(t, map[string]any{"id": "1"}, env.Data)
	assert.Equal(t, second.Header().Get(sharedhttp.HeaderRequestID), env.RequestID, "el sobre lleva el request_id de ESTA petición")
	assert.NotEqual(t, decode(t, first).RequestID, env.RequestID)
}

func TestIdempotentWithoutKeyAlwaysExecutes(t *testing.T) {
	e, exec := idempotentEngine(t, idempotencytest.NewMemory(), created)

	postIdem(e, "writer", "", `{}`)
	postIdem(e, "writer", "", `{}`)

	assert.EqualValues(t, 2, exec.Load())
}

func TestIdempotentRejections(t *testing.T) {
	store := idempotencytest.NewMemory()
	e, exec := idempotentEngine(t, store, created)
	postIdem(e, "writer", "k1", `{"a":1}`)

	w := postIdem(e, "writer", "k1", `{"a":2}`)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Equal(t, "IDEMPOTENCY_KEY_REUSED", decode(t, w).Notifications[0].Code)

	w = postIdem(e, "writer", "clave con espacios", `{}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, sharedhttp.HeaderIdempotencyKey, decode(t, w).Notifications[0].Field)
	assert.EqualValues(t, 1, exec.Load())
}

func TestIdempotentInProgressIsConflictWithRetryAfter(t *testing.T) {
	store := idempotencytest.NewMemory()
	started, release := make(chan struct{}), make(chan struct{})
	e, _ := idempotentEngine(t, store, func(c *gin.Context) {
		close(started)
		<-release
		created(c)
	})
	done := make(chan struct{})
	go func() { postIdem(e, "writer", "k1", `{}`); close(done) }()
	<-started

	w := postIdem(e, "writer", "k1", `{}`)

	require.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "IDEMPOTENCY_IN_PROGRESS", decode(t, w).Notifications[0].Code)
	assert.Equal(t, "1", w.Header().Get("Retry-After"))
	close(release)
	<-done
}

func TestIdempotentReleasesTheKeyWithoutARepeatableResult(t *testing.T) {
	tests := map[string]struct {
		handler gin.HandlerFunc
		status  int
	}{
		"error técnico": {func(c *gin.Context) { _ = c.Error(errors.New("db caída")) }, 500},
		"panic":         {func(*gin.Context) { panic("bug") }, 500},
		"5xx escrito":   {func(c *gin.Context) { sharedhttp.WriteData(c, 503, nil, nil) }, 503},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var fail atomic.Bool
			fail.Store(true)
			e, exec := idempotentEngine(t, idempotencytest.NewMemory(), func(c *gin.Context) {
				if fail.Load() {
					tt.handler(c)
					return
				}
				created(c)
			})

			require.Equal(t, tt.status, postIdem(e, "writer", "k1", `{}`).Code)

			fail.Store(false)
			w := postIdem(e, "writer", "k1", `{}`)
			require.Equal(t, http.StatusCreated, w.Code, "el reintento se ejecuta de verdad")
			assert.EqualValues(t, 2, exec.Load())
			assert.Empty(t, w.Header().Get(sharedhttp.HeaderIdempotencyReplayed))
		})
	}
}

func TestIdempotentRepeatsBusinessFailures(t *testing.T) {
	e, exec := idempotentEngine(t, idempotencytest.NewMemory(), func(c *gin.Context) {
		sharedhttp.WriteFailure(c, []result.Notification{result.Error(result.Conflict, "NAME_TAKEN", "Ya existe.")})
	})

	postIdem(e, "writer", "k1", `{}`)
	w := postIdem(e, "writer", "k1", `{}`)

	require.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "NAME_TAKEN", decode(t, w).Notifications[0].Code, "un fallo de negocio es un resultado: se repite")
	assert.EqualValues(t, 1, exec.Load())
}

func TestIdempotentKeysAreScopedByUser(t *testing.T) {
	e, exec := idempotentEngine(t, idempotencytest.NewMemory(), created)

	postIdem(e, "writer", "k1", `{}`)
	w := postIdem(e, "reader", "k1", `{}`)

	assert.EqualValues(t, 2, exec.Load(), "dos usuarios no comparten claves")
	assert.Empty(t, w.Header().Get(sharedhttp.HeaderIdempotencyReplayed))
}

type failingStore struct{ idempotency.Store }

func (failingStore) Begin(context.Context, string, string, string, uuid.UUID) (idempotency.Outcome, idempotency.Record, error) {
	return 0, idempotency.Record{}, errors.New("db caída")
}

// lostStore simula una petición que superó su reserva: otra la tomó mientras tanto.
type lostStore struct{ idempotency.Store }

func (lostStore) Complete(context.Context, string, string, uuid.UUID, idempotency.Record) error {
	return idempotency.ErrReservationLost
}

// El cliente recibe su respuesta: lo único que se descarta es guardarla para repetirla.
func TestIdempotentResponseStillServedWhenReservationWasLost(t *testing.T) {
	e, exec := idempotentEngine(t, lostStore{idempotencytest.NewMemory()}, created)

	w := postIdem(e, "writer", "k1", `{}`)

	require.Equal(t, http.StatusCreated, w.Code)
	assert.EqualValues(t, 1, exec.Load())
}

func TestIdempotentStoreFailureIsTechnical(t *testing.T) {
	e, exec := idempotentEngine(t, failingStore{}, created)

	w := postIdem(e, "writer", "k1", `{}`)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "UNEXPECTED_ERROR", decode(t, w).Notifications[0].Code)
	assert.Zero(t, exec.Load(), "sin reservar la clave no se ejecuta")
}
