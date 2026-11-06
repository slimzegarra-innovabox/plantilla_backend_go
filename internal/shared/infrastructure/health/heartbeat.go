package health

import (
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"
)

// Heartbeats es la liveness de un proceso sin HTTP propio (cmd/worker). Cada bucle de
// trabajo registra un latido mientras AVANZA, aunque su dependencia falle: lo que se
// detecta es un bucle colgado (deadlock, goroutine bloqueada), no un Redis caído.
// Reiniciar el proceso arregla lo primero y no lo segundo, así que una dependencia
// caída no debe provocar reinicios en bucle (misma regla que /health/live).
type Heartbeats struct {
	mu     sync.Mutex
	now    func() time.Time
	maxAge map[string]time.Duration
	last   map[string]time.Time
}

// NewHeartbeats crea el registro.
func NewHeartbeats() *Heartbeats {
	return &Heartbeats{now: time.Now, maxAge: map[string]time.Duration{}, last: map[string]time.Time{}}
}

// Register declara un componente y cuánto puede pasar sin latido antes de darlo por
// colgado. Cuenta como latido inicial, para no fallar mientras arranca.
// Devuelve la función que el componente llama en cada vuelta.
func (h *Heartbeats) Register(name string, maxAge time.Duration) func() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.maxAge[name] = maxAge
	h.last[name] = h.now()
	return func() { h.beat(name) }
}

func (h *Heartbeats) beat(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last[name] = h.now()
}

// Stale devuelve los componentes cuyo último latido es más viejo que su máximo.
func (h *Heartbeats) Stale() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	var out []string
	for name, last := range h.last {
		if age := now.Sub(last); age > h.maxAge[name] {
			out = append(out, fmt.Sprintf("%s (sin latido hace %s)", name, age.Round(time.Second)))
		}
	}
	slices.Sort(out)
	return out
}

// Handler responde GET /health/live: 200 si todos los componentes laten, 503 si no.
// Sin detalles en la respuesta; el motivo va al log del proceso (lo registra quien
// arma el servidor, ver cmd/worker).
func (h *Heartbeats) Handler(onStale func(stale []string)) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if stale := h.Stale(); len(stale) > 0 {
			if onStale != nil {
				onStale(stale)
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"DOWN"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"UP"}`))
	})
	return mux
}
