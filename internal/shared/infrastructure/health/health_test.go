package health_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"myapp.v1.0.0/internal/shared/infrastructure/health"
)

func serve(checks ...health.Check) func(path string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	health.Register(e, nil, 50*time.Millisecond, checks...)
	return func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
}

func ok(context.Context) error { return nil }

func TestLiveNeverChecksDependencies(t *testing.T) {
	get := serve(health.Check{Name: "db", Fn: func(context.Context) error { return errors.New("caída") }})

	w := get("/health/live")

	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"status":"UP"}`, w.Body.String())
}

func TestHeadIsSupported(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	health.Register(e, nil, 50*time.Millisecond, health.Check{Name: "db", Fn: ok})
	for _, path := range []string{"/health/live", "/health/ready"} {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(http.MethodHead, path, nil))
		assert.Equal(t, http.StatusOK, w.Code, path)
	}
}

func TestReadyUp(t *testing.T) {
	w := serve(health.Check{Name: "db", Fn: ok}, health.Check{Name: "redis", Fn: ok})("/health/ready")

	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"status":"UP"}`, w.Body.String())
}

func TestReadyDownHidesDetails(t *testing.T) {
	fail := func(context.Context) error { return errors.New("password authentication failed for user app") }

	w := serve(health.Check{Name: "db", Fn: ok}, health.Check{Name: "redis", Fn: fail})("/health/ready")

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.JSONEq(t, `{"status":"DOWN"}`, w.Body.String())
}

func TestReadyTimesOutSlowChecks(t *testing.T) {
	slow := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}

	start := time.Now()
	w := serve(health.Check{Name: "db", Fn: slow})("/health/ready")

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Less(t, time.Since(start), time.Second)
}

func TestDrainingTurnsReadyDownButKeepsLive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	gate := &health.Gate{}
	called := false
	health.Register(e, gate, 50*time.Millisecond, health.Check{Name: "db", Fn: func(context.Context) error {
		called = true
		return nil
	}})
	get := func(path string) int {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w.Code
	}
	assert.Equal(t, http.StatusOK, get("/health/ready"))

	called = false
	gate.Drain()

	assert.Equal(t, http.StatusServiceUnavailable, get("/health/ready"))
	assert.False(t, called, "al drenar no se consultan las dependencias")
	assert.Equal(t, http.StatusOK, get("/health/live"), "live sigue en 200: el proceso no debe reiniciarse")
}
