package httpapi_test

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/example"
	"myapp.v1.0.0/internal/example/application/model"
	"myapp.v1.0.0/internal/example/application/port/porttest"
	"myapp.v1.0.0/internal/example/infrastructure/httpapi"
	sharedporttest "myapp.v1.0.0/internal/shared/application/port/porttest"
	"myapp.v1.0.0/internal/shared/infrastructure/config"
	sharedhttp "myapp.v1.0.0/internal/shared/infrastructure/httpapi"
	"myapp.v1.0.0/internal/shared/infrastructure/httpapi/openapitest"
	"myapp.v1.0.0/internal/shared/infrastructure/idempotency/idempotencytest"
	"myapp.v1.0.0/internal/shared/infrastructure/module"
	"myapp.v1.0.0/internal/shared/infrastructure/security"
)

var (
	jwtCfg = config.JWTConfig{
		Secret: "0123456789abcdef0123456789abcdef", Issuer: "https://auth.example.com",
		Audience: "myapp-api", Leeway: 30 * time.Second,
	}
	t0 = time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return t0 }

// server arma el router real con el mismo cableado de producción (example.RegisterWith):
// casos de uso reales sobre dobles en memoria.
type server struct {
	engine *gin.Engine
	repo   *porttest.InMemoryExampleRepository
	user   uuid.UUID
	writer string // token con example:read y example:write
	reader string // token solo con example:read
}

func newServer(t *testing.T) *server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine, err := sharedhttp.NewEngine(sharedhttp.EngineOptions{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		BodyLimitBytes: 1 << 20,
	})
	require.NoError(t, err)

	repo := porttest.NewInMemoryExampleRepository()
	example.RegisterWith(engine.Group("/api/v1"), module.Deps{
		Tx:         &sharedporttest.TransactionManager{},
		Clock:      fixedClock{},
		Auth:       sharedhttp.NewAuthenticator(security.NewJWTVerifier(jwtCfg)),
		Idempotent: sharedhttp.Idempotent(idempotencytest.NewMemory()),
	}, example.Ports{Repo: repo, Events: &porttest.RecordingExampleEventPublisher{}})

	s := &server{engine: engine, repo: repo, user: uuid.New()}
	s.writer = s.token(t, httpapi.ScopeRead, httpapi.ScopeWrite)
	s.reader = s.token(t, httpapi.ScopeRead)
	return s
}

func (s *server) token(t *testing.T, scopes ...string) string {
	t.Helper()
	tok, err := security.SignForTesting(jwtCfg, s.user, scopes, time.Hour)
	require.NoError(t, err)
	return tok
}

type response struct {
	Code   int
	Header http.Header
	Data   map[string]any
	List   []map[string]any
	Page   map[string]any
	Notes  []sharedhttp.NotificationJSON
	Raw    string
}

func (s *server) do(t *testing.T, method, path, token, body string) response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, req)
	// Cada respuesta de cada prueba se valida contra el contrato (docs/openapi.yaml).
	openapitest.ValidateResponse(t, req, w)

	r := response{Code: w.Code, Header: w.Header(), Raw: w.Body.String()}
	var env struct {
		Data          json.RawMessage               `json:"data"`
		Page          map[string]any                `json:"page"`
		Notifications []sharedhttp.NotificationJSON `json:"notifications"`
		RequestID     string                        `json:"request_id"`
	}
	if w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), r.Raw)
		require.NotEmpty(t, env.RequestID, "toda respuesta lleva request_id")
		if env.Page != nil {
			require.NoError(t, json.Unmarshal(env.Data, &r.List))
		} else if string(env.Data) != "null" {
			require.NoError(t, json.Unmarshal(env.Data, &r.Data))
		}
		r.Page, r.Notes = env.Page, env.Notifications
	}
	return r
}

func (r response) code(t *testing.T) string {
	t.Helper()
	require.NotEmpty(t, r.Notes, r.Raw)
	return r.Notes[0].Code
}

func (s *server) create(t *testing.T, name string) map[string]any {
	t.Helper()
	r := s.do(t, http.MethodPost, "/api/v1/examples", s.writer, `{"name":"`+name+`"}`)
	require.Equal(t, http.StatusCreated, r.Code, r.Raw)
	return r.Data
}

//--------------------------------------------------------------
// POST /api/v1/examples
//--------------------------------------------------------------

func TestCreate(t *testing.T) {
	s := newServer(t)

	// created_by en el body se ignora: el autor sale del token.
	r := s.do(t, http.MethodPost, "/api/v1/examples", s.writer, `{"name":"Demo","created_by":"`+uuid.NewString()+`"}`)

	require.Equal(t, http.StatusCreated, r.Code, r.Raw)
	id := r.Data["uuid"].(string)
	assert.Equal(t, "/api/v1/examples/"+id, r.Header.Get("Location"))
	assert.Equal(t, "Demo", r.Data["name"])
	assert.Equal(t, "pending", r.Data["status"])
	assert.EqualValues(t, 1, r.Data["version"])
	assert.Equal(t, s.user.String(), r.Data["created_by"])
	assert.Equal(t, "2026-05-01T10:00:00Z", r.Data["created_at"])
	assert.Empty(t, r.Notes)
	assert.NotContains(t, r.Raw, `"id"`, "nunca se expone el id numérico")
}

// Sin esta validación, PostgreSQL responde 22021 y el cliente recibe un 500.
func TestTextPostgresCannotStoreIsRejected(t *testing.T) {
	s := newServer(t)
	r := s.do(t, http.MethodPost, "/api/v1/examples", s.writer, `{"name":"ab\u0000cd"}`)
	require.Equal(t, http.StatusBadRequest, r.Code)
	assert.Equal(t, "INVALID_REQUEST_BODY", r.Notes[0].Code)
}

func TestCreateFailures(t *testing.T) {
	tests := map[string]struct {
		body       string
		token      func(s *server) string
		wantStatus int
		wantCode   string
		wantField  string
	}{
		"sin token":        {`{"name":"Demo"}`, func(*server) string { return "" }, 401, "AUTH_TOKEN_INVALID", ""},
		"sin scope write":  {`{"name":"Demo"}`, func(s *server) string { return s.reader }, 403, "AUTH_FORBIDDEN", ""},
		"falta name":       {`{}`, nil, 400, "REQUEST_FIELD_INVALID", "name"},
		"name de 1 letra":  {`{"name":"a"}`, nil, 400, "EXAMPLE_NAME_TOO_SHORT", ""},
		"name de espacios": {`{"name":"   "}`, nil, 400, "EXAMPLE_NAME_REQUIRED", ""},
		"JSON mal formado": {`{"name":`, nil, 400, "INVALID_REQUEST_BODY", ""},
		"nombre duplicado": {`{"name":"EXISTENTE"}`, nil, 409, "EXAMPLE_NAME_TAKEN", ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newServer(t)
			s.create(t, "Existente")
			token := s.writer
			if tt.token != nil {
				token = tt.token(s)
			}

			r := s.do(t, http.MethodPost, "/api/v1/examples", token, tt.body)

			require.Equal(t, tt.wantStatus, r.Code, r.Raw)
			assert.Equal(t, tt.wantCode, r.code(t))
			if tt.wantField != "" {
				assert.Equal(t, tt.wantField, r.Notes[0].Field)
			}
			assert.Nil(t, r.Data)
		})
	}
}

func TestCreateWithIdempotencyKeyCreatesOnce(t *testing.T) {
	s := newServer(t)
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/examples", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+s.writer)
		req.Header.Set(sharedhttp.HeaderIdempotencyKey, "op-1")
		w := httptest.NewRecorder()
		s.engine.ServeHTTP(w, req)
		openapitest.ValidateResponse(t, req, w)
		return w
	}

	first := post(`{"name":"Demo"}`)
	retry := post(`{"name":"Demo"}`)

	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	require.Equal(t, http.StatusCreated, retry.Code, "un reintento no choca con EXAMPLE_NAME_TAKEN")
	assert.Equal(t, 1, s.repo.Calls["Create"], "se creó una sola vez")
	assert.Equal(t, first.Header().Get("Location"), retry.Header().Get("Location"))
	assert.Equal(t, "true", retry.Header().Get(sharedhttp.HeaderIdempotencyReplayed))

	reused := post(`{"name":"Otro"}`)
	require.Equal(t, http.StatusUnprocessableEntity, reused.Code)
	assert.Contains(t, reused.Body.String(), "IDEMPOTENCY_KEY_REUSED")
}

//--------------------------------------------------------------
// GET /api/v1/examples/{uuid}
//--------------------------------------------------------------

func TestGet(t *testing.T) {
	s := newServer(t)
	created := s.create(t, "Demo")

	r := s.do(t, http.MethodGet, "/api/v1/examples/"+created["uuid"].(string), s.reader, "")

	require.Equal(t, http.StatusOK, r.Code, r.Raw)
	assert.Equal(t, created, r.Data)
}

func TestGetFailures(t *testing.T) {
	s := newServer(t)

	r := s.do(t, http.MethodGet, "/api/v1/examples/"+uuid.NewString(), s.reader, "")
	require.Equal(t, http.StatusNotFound, r.Code)
	assert.Equal(t, "EXAMPLE_NOT_FOUND", r.code(t))
	assert.Equal(t, "NOT_FOUND", r.Notes[0].Category)

	r = s.do(t, http.MethodGet, "/api/v1/examples/no-es-uuid", s.reader, "")
	require.Equal(t, http.StatusBadRequest, r.Code)
	assert.Equal(t, "INVALID_REQUEST_PARAMETER", r.code(t))
}

//--------------------------------------------------------------
// GET /api/v1/examples
//--------------------------------------------------------------

func TestList(t *testing.T) {
	s := newServer(t)
	for _, n := range []string{"Uno", "Dos", "Tres"} {
		s.create(t, n)
	}
	id := s.create(t, "Cuatro")["uuid"].(string)
	require.Equal(t, http.StatusOK, s.do(t, http.MethodPost, "/api/v1/examples/"+id+"/activate", s.writer, "").Code)

	r := s.do(t, http.MethodGet, "/api/v1/examples?page=1&size=2", s.reader, "")
	require.Equal(t, http.StatusOK, r.Code, r.Raw)
	assert.Len(t, r.List, 2)
	assert.Equal(t, map[string]any{"page": 1.0, "size": 2.0, "total_elements": 4.0, "total_pages": 2.0}, r.Page)

	r = s.do(t, http.MethodGet, "/api/v1/examples?status=active", s.reader, "")
	require.Equal(t, http.StatusOK, r.Code, r.Raw)
	require.Len(t, r.List, 1)
	assert.Equal(t, id, r.List[0]["uuid"])

	r = s.do(t, http.MethodGet, "/api/v1/examples?page=5", s.reader, "")
	require.Equal(t, http.StatusOK, r.Code)
	assert.NotNil(t, r.List)
	assert.Empty(t, r.List, "una página fuera de rango es una lista vacía, no null")
}

func TestListFailures(t *testing.T) {
	s := newServer(t)

	r := s.do(t, http.MethodGet, "/api/v1/examples?status=deleted", s.reader, "")
	require.Equal(t, http.StatusBadRequest, r.Code)
	assert.Equal(t, "status", r.Notes[0].Field)

	r = s.do(t, http.MethodGet, "/api/v1/examples?size=101", s.reader, "")
	require.Equal(t, http.StatusBadRequest, r.Code)
	assert.Equal(t, "size", r.Notes[0].Field)

	r = s.do(t, http.MethodGet, "/api/v1/examples", "", "")
	require.Equal(t, http.StatusUnauthorized, r.Code)
}

//--------------------------------------------------------------
// PUT /api/v1/examples/{uuid}
//--------------------------------------------------------------

func TestUpdate(t *testing.T) {
	s := newServer(t)
	id := s.create(t, "Viejo")["uuid"].(string)

	r := s.do(t, http.MethodPut, "/api/v1/examples/"+id, s.writer, `{"name":"Nuevo","version":1}`)

	require.Equal(t, http.StatusOK, r.Code, r.Raw)
	assert.Equal(t, "Nuevo", r.Data["name"])
	assert.EqualValues(t, 2, r.Data["version"])
}

func TestUpdateFailures(t *testing.T) {
	tests := map[string]struct {
		path       func(id string) string
		body       string
		wantStatus int
		wantCode   string
	}{
		"versión vieja":   {func(id string) string { return id }, `{"name":"Otro nombre","version":7}`, 409, "EXAMPLE_VERSION_CONFLICT"},
		"falta versión":   {func(id string) string { return id }, `{"name":"Otro nombre"}`, 400, "REQUEST_FIELD_INVALID"},
		"nombre inválido": {func(id string) string { return id }, `{"name":"a","version":1}`, 400, "EXAMPLE_NAME_TOO_SHORT"},
		"nombre de otro":  {func(id string) string { return id }, `{"name":"segundo","version":1}`, 409, "EXAMPLE_NAME_TAKEN"},
		"no existe":       {func(string) string { return uuid.NewString() }, `{"name":"Otro nombre","version":1}`, 404, "EXAMPLE_NOT_FOUND"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newServer(t)
			id := s.create(t, "Primero")["uuid"].(string)
			s.create(t, "Segundo")

			r := s.do(t, http.MethodPut, "/api/v1/examples/"+tt.path(id), s.writer, tt.body)

			require.Equal(t, tt.wantStatus, r.Code, r.Raw)
			assert.Equal(t, tt.wantCode, r.code(t))
		})
	}
}

//--------------------------------------------------------------
// POST /api/v1/examples/{uuid}/activate | deactivate
//--------------------------------------------------------------

func TestStatusActions(t *testing.T) {
	s := newServer(t)
	id := s.create(t, "Demo")["uuid"].(string)
	base := "/api/v1/examples/" + id

	r := s.do(t, http.MethodPost, base+"/deactivate", s.writer, "")
	require.Equal(t, http.StatusUnprocessableEntity, r.Code, "pending → inactive no está permitido")
	assert.Equal(t, "EXAMPLE_INVALID_TRANSITION", r.code(t))
	assert.Equal(t, "BUSINESS_RULE", r.Notes[0].Category)

	r = s.do(t, http.MethodPost, base+"/activate", s.writer, "")
	require.Equal(t, http.StatusOK, r.Code, r.Raw)
	assert.Equal(t, "active", r.Data["status"])
	assert.EqualValues(t, 2, r.Data["version"])

	r = s.do(t, http.MethodPost, base+"/deactivate", s.writer, "")
	require.Equal(t, http.StatusOK, r.Code, r.Raw)
	assert.Equal(t, "inactive", r.Data["status"])

	r = s.do(t, http.MethodPost, base+"/activate", s.reader, "")
	require.Equal(t, http.StatusForbidden, r.Code)
}

func TestListSearchAndSort(t *testing.T) {
	s := newServer(t)
	for _, n := range []string{"bravo demo", "Alfa Demo", "charlie"} {
		s.create(t, n)
	}
	names := func(r response) []string {
		var out []string
		for _, e := range r.List {
			out = append(out, e["name"].(string))
		}
		return out
	}

	r := s.do(t, http.MethodGet, "/api/v1/examples?q=DEMO&sort=name", s.reader, "")
	require.Equal(t, http.StatusOK, r.Code, r.Raw)
	assert.Equal(t, []string{"Alfa Demo", "bravo demo"}, names(r))

	r = s.do(t, http.MethodGet, "/api/v1/examples?q=%20DEMO%20&sort=name", s.reader, "")
	require.Equal(t, http.StatusOK, r.Code, r.Raw)
	assert.Equal(t, []string{"Alfa Demo", "bravo demo"}, names(r), "se busca sin los espacios de los extremos")

	r = s.do(t, http.MethodGet, "/api/v1/examples?sort=-name", s.reader, "")
	require.Equal(t, http.StatusOK, r.Code, r.Raw)
	assert.Equal(t, []string{"charlie", "bravo demo", "Alfa Demo"}, names(r))

	for _, bad := range []string{"password", "name,name", "-"} {
		r = s.do(t, http.MethodGet, "/api/v1/examples?sort="+bad, s.reader, "")
		require.Equal(t, http.StatusBadRequest, r.Code, bad)
		assert.Equal(t, "INVALID_REQUEST_PARAMETER", r.code(t))
		assert.Equal(t, "sort", r.Notes[0].Field)
	}
	// Los espacios no cuentan: con menos de 3 caracteres se recorrería la tabla entera.
	for _, q := range []string{"ab", strings.Repeat("x", 101), "%20a%20", "%20%20%20", "abc%00", "abc%FF"} {
		r = s.do(t, http.MethodGet, "/api/v1/examples?q="+q, s.reader, "")
		require.Equal(t, http.StatusBadRequest, r.Code, "q=%q", q)
		assert.Equal(t, "q", r.Notes[0].Field)
	}
}

//--------------------------------------------------------------
// AUTORIZACIÓN POR RECURSO
//--------------------------------------------------------------

func TestOnlyOwnerOrAdminCanWrite(t *testing.T) {
	s := newServer(t)
	id := s.create(t, "Ajeno")["uuid"].(string)
	base := "/api/v1/examples/" + id
	sign := func(scopes ...string) string {
		tok, err := security.SignForTesting(jwtCfg, uuid.New(), scopes, time.Hour)
		require.NoError(t, err)
		return tok
	}
	stranger := sign(httpapi.ScopeRead, httpapi.ScopeWrite)
	admin := sign(httpapi.ScopeRead, httpapi.ScopeWrite, httpapi.ScopeAdmin)

	for name, r := range map[string]response{
		"update":     s.do(t, http.MethodPut, base, stranger, `{"name":"Robado","version":1}`),
		"activate":   s.do(t, http.MethodPost, base+"/activate", stranger, ""),
		"deactivate": s.do(t, http.MethodPost, base+"/deactivate", stranger, ""),
		"delete":     s.do(t, http.MethodDelete, base, stranger, ""),
	} {
		require.Equal(t, http.StatusForbidden, r.Code, name)
		assert.Equal(t, "EXAMPLE_FORBIDDEN", r.code(t), name)
		assert.Equal(t, "FORBIDDEN", r.Notes[0].Category, name)
	}
	got := s.do(t, http.MethodGet, base, stranger, "")
	require.Equal(t, http.StatusOK, got.Code, "leer sí se puede: es un catálogo compartido")
	assert.Equal(t, "Ajeno", got.Data["name"], "nada cambió")

	require.Equal(t, http.StatusOK, s.do(t, http.MethodPost, base+"/activate", admin, "").Code, "un administrador sí")
}

//--------------------------------------------------------------
// DELETE /api/v1/examples/{uuid}
//--------------------------------------------------------------

func TestDelete(t *testing.T) {
	s := newServer(t)
	id := s.create(t, "Borrable")["uuid"].(string)
	base := "/api/v1/examples/" + id

	require.Equal(t, http.StatusOK, s.do(t, http.MethodPost, base+"/activate", s.writer, "").Code)
	r := s.do(t, http.MethodDelete, base, s.writer, "")
	require.Equal(t, http.StatusUnprocessableEntity, r.Code, "un example activo no se elimina")
	assert.Equal(t, "EXAMPLE_DELETE_ACTIVE", r.code(t))
	assert.Equal(t, "BUSINESS_RULE", r.Notes[0].Category)

	require.Equal(t, http.StatusOK, s.do(t, http.MethodPost, base+"/deactivate", s.writer, "").Code)
	r = s.do(t, http.MethodDelete, base, s.reader, "")
	require.Equal(t, http.StatusForbidden, r.Code, "eliminar exige example:write")

	r = s.do(t, http.MethodDelete, base, s.writer, "")
	require.Equal(t, http.StatusNoContent, r.Code, r.Raw)
	assert.Empty(t, r.Raw, "204 sin cuerpo")

	r = s.do(t, http.MethodDelete, base, s.writer, "")
	require.Equal(t, http.StatusNotFound, r.Code)
	assert.Equal(t, "EXAMPLE_NOT_FOUND", r.code(t))
	assert.Equal(t, http.StatusNotFound, s.do(t, http.MethodGet, base, s.reader, "").Code)
}

//--------------------------------------------------------------
// ERRORES TÉCNICOS
//--------------------------------------------------------------

func TestTechnicalErrorIs500WithoutDetails(t *testing.T) {
	s := newServer(t)
	s.repo.Seed(model.RestoreExample(0, uuid.New(), porttest.MustName(t, "Demo"), model.ExampleStatusPending, 1, uuid.New(), t0, t0))
	s.repo.Err = errors.New("pq: password authentication failed for user secret_user")

	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/examples", ""},
		{http.MethodGet, "/api/v1/examples/" + uuid.NewString(), ""},
		{http.MethodPost, "/api/v1/examples", `{"name":"Demo"}`},
	} {
		r := s.do(t, req.method, req.path, s.writer, req.body)

		require.Equal(t, http.StatusInternalServerError, r.Code, req.path)
		assert.Equal(t, "UNEXPECTED_ERROR", r.code(t))
		assert.NotContains(t, r.Raw, "secret_user")
	}
}
