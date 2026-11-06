package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// runHealthcheck es la sonda del contenedor: `api healthcheck`.
//
// La imagen distroless no tiene shell, wget ni curl, así que el propio binario
// consulta /health/live. Es liveness a propósito: un orquestador reinicia el
// contenedor si falla, y no debe hacerlo solo porque la base de datos cayó
// (eso lo informa /health/ready al balanceador).
//
// Solo lee HTTP_ADDR: no carga ni valida la configuración completa.
func runHealthcheck() int {
	addr := strings.TrimSpace(os.Getenv("HTTP_ADDR"))
	if addr == "" {
		addr = ":8080"
	}
	_, portStr, err := net.SplitHostPort(addr)
	port, convErr := strconv.Atoi(portStr)
	if err != nil || convErr != nil || port < 1 || port > 65535 {
		fmt.Fprintln(os.Stderr, "healthcheck: HTTP_ADDR inválido:", addr)
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
