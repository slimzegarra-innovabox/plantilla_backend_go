package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"golang.org/x/time/rate"
)

func TestClientKeyGroupsIPv6By64(t *testing.T) {
	assert.Equal(t, "203.0.113.7", clientKey("203.0.113.7"))
	assert.Equal(t, "203.0.113.7", clientKey("::ffff:203.0.113.7"), "IPv4 mapeada en IPv6")
	assert.Equal(t, "2001:db8:1:2::/64", clientKey("2001:db8:1:2:aaaa::1"))
	assert.Equal(t, clientKey("2001:db8:1:2:aaaa::1"), clientKey("2001:db8:1:2:ffff::9"), "mismo /64, mismo bucket")
	assert.NotEqual(t, clientKey("2001:db8:1:2::1"), clientKey("2001:db8:1:3::1"))
	assert.Equal(t, "no-es-ip", clientKey("no-es-ip"))
}

func TestLimiterIsBoundedAndExpires(t *testing.T) {
	now := time.Now()
	l := newIPLimiter(rate.Limit(1), 1, time.Minute, 3)

	for _, ip := range []string{"a", "b", "c", "d"} {
		assert.True(t, l.allow(ip, now))
	}
	assert.Equal(t, 3, l.size(), "al llenarse se desaloja la IP menos reciente")
	assert.False(t, l.allow("d", now), "d sigue en memoria y ya gastó su burst")
	assert.True(t, l.allow("a", now), "a fue desalojada: vuelve con un bucket nuevo")

	assert.True(t, l.allow("e", now.Add(2*time.Minute)))
	assert.Equal(t, 1, l.size(), "las entradas vencidas se expiran")
}

func TestRateLimitSkipsHealthAndComputesRetryAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(RateLimit(0.1, 1)) // 1 cada 10 s
	e.GET("/health/ready", func(c *gin.Context) { c.Status(http.StatusOK) })
	e.GET("/api/v1/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	do := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr = "203.0.113.7:1234"
		e.ServeHTTP(w, r)
		return w
	}

	assert.Equal(t, http.StatusOK, do("/api/v1/x").Code)
	w := do("/api/v1/x")
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "10", w.Header().Get("Retry-After"))
	for range 5 {
		assert.Equal(t, http.StatusOK, do("/health/ready").Code, "las sondas nunca reciben 429")
	}
}
