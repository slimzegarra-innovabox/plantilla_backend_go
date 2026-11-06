// Package health expone las sondas del proceso. No se crean otros endpoints de salud.
//
//   - GET /health/live:  el proceso atiende. No comprueba dependencias, para que el
//     orquestador no reinicie en bucle una instancia sana mientras la base de datos cae.
//   - GET /health/ready: las dependencias responden. El balanceador apunta aquí.
//     Durante el apagado (Gate.Drain) responde 503 sin consultarlas, para que el
//     balanceador retire la instancia antes de que se cierre el puerto.
//
// Las respuestas nunca incluyen detalles internos; el motivo de un DOWN va al log.
package health

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"myapp.v1.0.0/internal/shared/infrastructure/logging"
)

// Check comprueba una dependencia.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// Gate indica si la instancia acepta tráfico nuevo. El valor cero está abierto.
type Gate struct{ draining atomic.Bool }

// Drain cierra la compuerta: desde ahora /health/ready responde 503. Es irreversible.
func (g *Gate) Drain() { g.draining.Store(true) }

// Draining indica si la instancia se está apagando.
func (g *Gate) Draining() bool { return g.draining.Load() }

// Register monta las sondas en el router. Son las únicas rutas públicas por diseño.
// gate puede ser nil si el proceso no drena (p. ej. en pruebas).
func Register(r gin.IRouter, gate *Gate, timeout time.Duration, checks ...Check) {
	live := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "UP"})
	}
	ready := func(c *gin.Context) {
		if gate != nil && gate.Draining() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "DOWN"})
			return
		}
		if failed := run(c.Request.Context(), timeout, checks); failed {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "DOWN"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "UP"})
	}
	// HEAD también: algunos balanceadores sondean sin cuerpo.
	r.GET("/health/live", live)
	r.HEAD("/health/live", live)
	r.GET("/health/ready", ready)
	r.HEAD("/health/ready", ready)
}

// run ejecuta las comprobaciones en paralelo con un plazo común.
func run(ctx context.Context, timeout time.Duration, checks []Check) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed bool
	)
	for _, chk := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := chk.Fn(ctx); err != nil {
				logging.FromContext(ctx).WarnContext(ctx, "readiness: dependencia no disponible", "check", chk.Name, "error", err)
				mu.Lock()
				failed = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return failed
}
