package health

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHeartbeatsDetectAStuckComponent(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	h := NewHeartbeats()
	h.now = func() time.Time { return now }
	beatOutbox := h.Register("outbox", time.Minute)
	h.Register("asynq", time.Minute)
	var logged []string
	srv := h.Handler(func(s []string) { logged = s })
	get := func() int {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health/live", nil))
		return w.Code
	}

	assert.Equal(t, http.StatusOK, get(), "recién registrados cuentan como vivos")

	now = now.Add(50 * time.Second)
	beatOutbox()
	now = now.Add(20 * time.Second)
	assert.Equal(t, http.StatusServiceUnavailable, get(), "asynq lleva 70s sin latido")
	assert.Equal(t, []string{"asynq (sin latido hace 1m10s)"}, logged)
	assert.Empty(t, h.Stale()[1:], "outbox latió hace 20s: sigue vivo")
}
