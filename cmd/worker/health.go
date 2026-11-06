package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"myapp.v1.0.0/internal/shared/infrastructure/health"
)

const defaultHealthAddr = "127.0.0.1:8081"

// serveHealth sirve /health/live con los latidos de los bucles. Devuelve la función
// que lo detiene. Por defecto escucha solo en loopback: es para el HEALTHCHECK del
// contenedor, no para el balanceador (el worker no recibe tráfico).
func serveHealth(ctx context.Context, addr string, beats *health.Heartbeats, log *slog.Logger) (func(), error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("escuchar la sonda de salud en %s: %w", addr, err)
	}
	srv := &http.Server{
		Handler: beats.Handler(func(stale []string) {
			// Único registro del motivo: la respuesta no lleva detalles.
			log.Error("liveness: bucle sin latido", "stale", stale)
		}),
		ReadHeaderTimeout: 2 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("sonda de salud", "error", err)
		}
	}()
	return func() {
		// Se llama al apagar, con ctx ya cancelado: el plazo propio sale de uno que no lo está.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}, nil
}

// runHealthcheck es la sonda del contenedor: `worker healthcheck`. La imagen
// distroless no tiene shell ni curl, así que el binario consulta su propia sonda.
// Solo lee WORKER_HEALTH_ADDR: no carga la configuración completa.
func runHealthcheck() int {
	addr := strings.TrimSpace(os.Getenv("WORKER_HEALTH_ADDR"))
	if addr == "" {
		addr = defaultHealthAddr
	}
	_, portStr, err := net.SplitHostPort(addr)
	port, convErr := strconv.Atoi(portStr)
	if err != nil || convErr != nil || port < 1 || port > 65535 {
		fmt.Fprintln(os.Stderr, "healthcheck: WORKER_HEALTH_ADDR inválido:", addr)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Host fijo (loopback) y puerto validado como entero: no hay destino controlable.
	url := fmt.Sprintf("http://127.0.0.1:%d/health/live", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody) //nolint:gosec // G704: host loopback fijo, puerto entero validado
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: misma URL de loopback
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: estado", resp.StatusCode)
		return 1
	}
	return 0
}
