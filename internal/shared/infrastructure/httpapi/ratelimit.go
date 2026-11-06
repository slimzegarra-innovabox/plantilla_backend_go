package httpapi

import (
	"container/list"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"myapp.v1.0.0/internal/shared/application/result"
)

const (
	// limiterTTL: una IP sin peticiones durante este tiempo se olvida.
	limiterTTL = 10 * time.Minute
	// limiterMaxEntries acota la memoria (~200 B por entrada → ~20 MB). Al llenarse se
	// descarta la IP menos reciente: en el peor caso, esa IP recupera su burst.
	limiterMaxEntries = 100_000
)

// RateLimit limita las peticiones por cliente con un token bucket en memoria.
//
//   - Es por instancia: con varias réplicas el límite efectivo se multiplica. El
//     límite global se aplica en el balanceador/WAF o con un almacén compartido.
//   - La IP sale de c.ClientIP(), que solo confía en X-Forwarded-For de los proxies
//     configurados (HTTP_TRUSTED_PROXIES): sin ellos, detrás de un ALB todos los
//     usuarios comparten la IP del balanceador.
//   - IPv6 se agrupa por /64: un cliente suele controlar el /64 entero y, por
//     dirección, rotaría para saltarse el límite.
//   - /health/* no se limita: las sondas del balanceador salen de pocas IPs y un 429
//     sacaría de rotación una instancia sana.
func RateLimit(rps float64, burst int) gin.HandlerFunc {
	l := newIPLimiter(rate.Limit(rps), burst, limiterTTL, limiterMaxEntries)
	notif := result.Error(result.Validation, "TOO_MANY_REQUESTS",
		"Demasiadas solicitudes. Espere un momento e intente nuevamente.")
	// Tiempo hasta recuperar un token: con 0.1 rps son 10 s, no 1.
	retryAfter := strconv.Itoa(int(math.Max(1, math.Ceil(1/rps))))
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/health/") {
			c.Next()
			return
		}
		if !l.allow(clientKey(c.ClientIP()), time.Now()) {
			c.Header("Retry-After", retryAfter)
			writeNotification(c, http.StatusTooManyRequests, notif)
			return
		}
		c.Next()
	}
}

// clientKey es la clave del limitador: la IPv4 completa o el /64 de una IPv6.
func clientKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return ip
	}
	return prefix.String()
}

type visitor struct {
	key      string
	limiter  *rate.Limiter
	lastSeen time.Time
}

// ipLimiter es una LRU acotada: la lista va del más reciente (frente) al menos
// reciente (fondo), así que expirar y desalojar son O(1) por entrada, sin recorrer
// el mapa con el lock tomado.
type ipLimiter struct {
	mu       sync.Mutex
	entries  map[string]*list.Element
	order    *list.List
	rps      rate.Limit
	burst    int
	ttl      time.Duration
	capacity int
}

func newIPLimiter(rps rate.Limit, burst int, ttl time.Duration, capacity int) *ipLimiter {
	return &ipLimiter{entries: map[string]*list.Element{}, order: list.New(),
		rps: rps, burst: burst, ttl: ttl, capacity: capacity}
}

func (l *ipLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Expira desde el fondo mientras el menos reciente esté vencido.
	for back := l.order.Back(); back != nil; back = l.order.Back() {
		v := back.Value.(*visitor) //nolint:errcheck // la lista solo contiene *visitor
		if now.Sub(v.lastSeen) <= l.ttl {
			break
		}
		l.remove(back)
	}

	if el, ok := l.entries[key]; ok {
		v := el.Value.(*visitor) //nolint:errcheck // la lista solo contiene *visitor
		v.lastSeen = now
		l.order.MoveToFront(el)
		return v.limiter.AllowN(now, 1)
	}
	if l.order.Len() >= l.capacity {
		l.remove(l.order.Back())
	}
	v := &visitor{key: key, limiter: rate.NewLimiter(l.rps, l.burst), lastSeen: now}
	l.entries[key] = l.order.PushFront(v)
	return v.limiter.AllowN(now, 1)
}

func (l *ipLimiter) remove(el *list.Element) {
	v := el.Value.(*visitor) //nolint:errcheck // la lista solo contiene *visitor
	delete(l.entries, v.key)
	l.order.Remove(el)
}

func (l *ipLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.order.Len()
}
