//go:build integration

// Package e2e ejecuta los binarios reales (cmd/api y cmd/worker) contra PostgreSQL y
// Redis reales: comprueba lo que ninguna prueba de un solo paquete ve, el sistema
// armado por las raíces de composición, con su configuración y sus señales.
//
// Redis sale de TEST_REDIS_ADDR o de un contenedor desechable; sin ninguno de los dos,
// las pruebas que lo necesitan se omiten. Las que usan el módulo example se omiten si
// el proyecto ya lo quitó (make remove-example).
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
	"myapp.v1.0.0/internal/shared/infrastructure/persistence/pgtest"
	"myapp.v1.0.0/internal/shared/infrastructure/security"
)

const redisImage = "redis:7.4.12-alpine@sha256:8da19c7399dca900c6822a1cf30f9d2285585e2c4b457ff4095b0f4500054d3f"

var (
	binDir    string
	coverDir  string
	redisAddr string
	jwtCfg    = config.JWTConfig{Secret: "0123456789abcdef0123456789abcdef", Issuer: "e2e", Audience: "e2e", Leeway: time.Second}
)

func TestMain(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "e2e-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	binDir = dir
	// Con E2E_COVERDIR, los binarios miden su cobertura (make cover, CI).
	var coverFlags []string
	if coverDir = os.Getenv("E2E_COVERDIR"); coverDir != "" {
		coverFlags = []string{"-cover", "-covermode=atomic", "-coverpkg=../../cmd/...,../../internal/..."}
	}
	for _, name := range []string{"api", "worker", "migrate"} {
		args := append(append([]string{"build"}, coverFlags...), "-o", filepath.Join(dir, name), "../../cmd/"+name)
		out, err := exec.Command("go", args...).CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "compilar %s: %v\n%s", name, err, out)
			return 1
		}
	}
	stop := startRedis()
	defer stop()
	return pgtest.Main(m)
}

// startRedis usa TEST_REDIS_ADDR o levanta un contenedor. Sin ninguno, redisAddr queda vacío.
func startRedis() (stop func()) {
	if redisAddr = os.Getenv("TEST_REDIS_ADDR"); redisAddr != "" {
		return func() {}
	}
	out, err := exec.Command("docker", "run", "-d", "--rm", "-p", "127.0.0.1::6379", redisImage).Output()
	if err != nil {
		return func() {}
	}
	id := strings.TrimSpace(string(out))
	stop = func() { _ = exec.Command("docker", "rm", "-f", id).Run() }
	port, err := exec.Command("docker", "port", id, "6379/tcp").Output()
	if err != nil {
		return stop
	}
	addr := strings.TrimSpace(strings.Split(string(port), "\n")[0])
	for range 50 {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			_ = c.Close()
			redisAddr = addr
			return stop
		}
		time.Sleep(100 * time.Millisecond)
	}
	return stop
}

func needRedis(t *testing.T) {
	t.Helper()
	if redisAddr == "" {
		t.Skip("sin Redis: defina TEST_REDIS_ADDR o instale Docker")
	}
}

func needExampleModule(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("../../internal/example/module.go"); err != nil {
		t.Skip("el proyecto ya quitó el módulo example")
	}
}

//--------------------------------------------------------------
// PROCESOS
//--------------------------------------------------------------

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// logEntry busca en los logs JSON la primera línea con ese msg que cumpla match.
func (s *syncBuffer) logEntry(msg string, match func(map[string]any) bool) (map[string]any, bool) {
	for _, line := range strings.Split(s.String(), "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) == nil && e["msg"] == msg && (match == nil || match(e)) {
			return e, true
		}
	}
	return nil, false
}

type process struct {
	cmd  *exec.Cmd
	logs *syncBuffer
	done chan error
}

// baseEnv es un entorno limpio: el proceso no hereda nada del de la prueba.
func baseEnv(extra ...string) []string {
	db := pgtest.Config()
	return append([]string{
		"PATH=" + os.Getenv("PATH"),
		"GOCOVERDIR=" + coverDir,
		"APP_ENV=test",
		"LOG_LEVEL=info",
		"DB_HOST=" + db.Host,
		"DB_PORT=" + strconv.Itoa(db.Port),
		"DB_USER=" + db.User,
		"DB_PASSWORD=" + db.Password.Reveal(),
		"DB_NAME=" + db.Name,
		"DB_SSLMODE=" + db.SSLMode,
		"JWT_SECRET=" + jwtCfg.Secret.Reveal(),
		"JWT_ISSUER=" + jwtCfg.Issuer,
		"JWT_AUDIENCE=" + jwtCfg.Audience,
		"HTTP_RATE_LIMIT_RPS=0",
	}, extra...)
}

func redisEnv() []string {
	return []string{"REDIS_ENABLED=true", "REDIS_ADDR=" + redisAddr, "REDIS_DB=14"}
}

func start(t *testing.T, name string, env []string) *process {
	t.Helper()
	logs := &syncBuffer{}
	cmd := exec.Command(filepath.Join(binDir, name))
	cmd.Env, cmd.Stdout, cmd.Stderr = env, logs, logs
	require.NoError(t, cmd.Start())
	p := &process{cmd: cmd, logs: logs, done: make(chan error, 1)}
	go func() {
		p.done <- cmd.Wait()
		close(p.done) // un segundo lector (la limpieza) no se queda esperando
	}()
	t.Cleanup(func() {
		// SIGTERM y no Kill: el proceso termina limpio y vuelca su cobertura.
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
		if t.Failed() {
			t.Logf("logs de %s:\n%s", name, logs.String())
		}
	})
	return p
}

// stop envía SIGTERM y devuelve el código de salida.
func (p *process) stop(t *testing.T) int {
	t.Helper()
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
	select {
	case err := <-p.done:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		require.NoError(t, err)
		return 0
	case <-time.After(20 * time.Second):
		t.Fatal("el proceso no terminó tras SIGTERM")
		return -1
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().String()
}

// startAPI arranca la API y espera a que atienda.
func startAPI(t *testing.T, extra ...string) (*process, string) {
	t.Helper()
	addr := freeAddr(t)
	p := start(t, "api", baseEnv(append(extra, "HTTP_ADDR="+addr)...))
	base := "http://" + addr
	require.Eventually(t, func() bool { return status(base+"/health/live") == http.StatusOK }, 10*time.Second, 50*time.Millisecond, "la API no arrancó")
	return p, base
}

func status(url string) int {
	c := http.Client{Timeout: time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

//--------------------------------------------------------------
// HTTP
//--------------------------------------------------------------

func token(t *testing.T, user uuid.UUID) string {
	t.Helper()
	tok, err := security.SignForTesting(jwtCfg, user, []string{"example:read", "example:write"}, time.Hour)
	require.NoError(t, err)
	return tok
}

type reply struct {
	code   int
	header http.Header
	body   map[string]any
}

func call(t *testing.T, method, url, tok, body string, headers map[string]string) reply {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	r := reply{code: resp.StatusCode, header: resp.Header}
	if len(raw) > 0 {
		require.NoError(t, json.Unmarshal(raw, &r.body), string(raw))
	}
	return r
}

func createExample(t *testing.T, base, tok, name string) string {
	t.Helper()
	r := call(t, http.MethodPost, base+"/api/v1/examples", tok, `{"name":"`+name+`"}`, nil)
	require.Equal(t, http.StatusCreated, r.code, r.body)
	return r.body["data"].(map[string]any)["uuid"].(string)
}

//--------------------------------------------------------------
// PRUEBAS
//--------------------------------------------------------------

// Un cambio de estado viaja por HTTP → transacción → outbox → despachador → Asynq →
// handler del worker, como una sola traza.
func TestEventTravelsFromAPIToWorkerInOneTrace(t *testing.T) {
	needExampleModule(t)
	needRedis(t)
	pgtest.Truncate(t, "examples", "outbox")
	_, base := startAPI(t, redisEnv()...)
	worker := start(t, "worker", baseEnv(append(redisEnv(), "WORKER_HEALTH_ADDR="+freeAddr(t))...))
	tok := token(t, uuid.New())

	id := createExample(t, base, tok, "Viajero "+uuid.NewString()[:8])
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	r := call(t, http.MethodPost, base+"/api/v1/examples/"+id+"/activate", tok, "", map[string]string{
		"traceparent": "00-" + traceID + "-00f067aa0ba902b7-01",
	})
	require.Equal(t, http.StatusOK, r.code, r.body)

	var entry map[string]any
	require.Eventually(t, func() bool {
		var ok bool
		entry, ok = worker.logs.logEntry("example cambió de estado", func(e map[string]any) bool { return e["example_uuid"] == id })
		return ok
	}, 15*time.Second, 50*time.Millisecond, "el worker no procesó el evento")

	assert.Equal(t, traceID, entry["trace_id"], "el worker sigue la traza de la petición original")
	assert.Equal(t, "active", entry["status"])
	assert.EqualValues(t, 2, entry["version"], "el evento lleva la versión persistida")

	var row struct {
		UUID      string
		Published bool
		Attempts  int
	}
	require.NoError(t, pgtest.DB(t).Raw("SELECT uuid, published_at IS NOT NULL AS published, attempts FROM outbox").Scan(&row).Error)
	assert.Equal(t, row.UUID, entry["task_id"], "el TaskID de Asynq es el id del mensaje del outbox")
	require.Eventually(t, func() bool {
		require.NoError(t, pgtest.DB(t).Raw("SELECT uuid, published_at IS NOT NULL AS published, attempts FROM outbox").Scan(&row).Error)
		return row.Published
	}, 5*time.Second, 50*time.Millisecond, "el mensaje no quedó marcado como publicado")
	assert.Equal(t, 1, row.Attempts)
}

// Sin Redis no hay outbox: la operación funciona, la tabla no crece y se avisa.
func TestWithoutRedisEventsAreDiscardedAndTheOutboxStaysEmpty(t *testing.T) {
	needExampleModule(t)
	pgtest.Truncate(t, "examples", "outbox")
	api, base := startAPI(t)
	tok := token(t, uuid.New())

	id := createExample(t, base, tok, "Sin Redis "+uuid.NewString()[:8])
	r := call(t, http.MethodPost, base+"/api/v1/examples/"+id+"/activate", tok, "", nil)
	require.Equal(t, http.StatusOK, r.code, r.body)

	var n int64
	require.NoError(t, pgtest.DB(t).Raw("SELECT count(*) FROM outbox").Scan(&n).Error)
	assert.Zero(t, n)
	assert.Contains(t, api.logs.String(), "el outbox está desactivado")
}

// La cadena real de auth + regla del model protege los registros ajenos.
func TestAnotherUserCannotModifyARecord(t *testing.T) {
	needExampleModule(t)
	pgtest.Truncate(t, "examples")
	_, base := startAPI(t)
	id := createExample(t, base, token(t, uuid.New()), "Ajeno "+uuid.NewString()[:8])

	r := call(t, http.MethodDelete, base+"/api/v1/examples/"+id, token(t, uuid.New()), "", nil)

	require.Equal(t, http.StatusForbidden, r.code)
	assert.Contains(t, fmt.Sprint(r.body["notifications"]), "EXAMPLE_FORBIDDEN")
}

// De muchos POST simultáneos con la misma clave, solo uno ejecuta.
func TestConcurrentCreatesWithTheSameKeyCreateOneRow(t *testing.T) {
	needExampleModule(t)
	pgtest.Truncate(t, "examples", "idempotency_keys")
	_, base := startAPI(t)
	tok := token(t, uuid.New())
	body := `{"name":"Simultaneo ` + uuid.NewString()[:8] + `"}`

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		original int
		codes    = map[int]int{}
	)
	for range 20 {
		wg.Go(func() {
			r := call(t, http.MethodPost, base+"/api/v1/examples", tok, body, map[string]string{"Idempotency-Key": "e2e-1"})
			mu.Lock()
			defer mu.Unlock()
			codes[r.code]++
			if r.code == http.StatusCreated && r.header.Get("Idempotency-Replayed") == "" {
				original++
			}
		})
	}
	wg.Wait()

	var rows int64
	require.NoError(t, pgtest.DB(t).Raw("SELECT count(*) FROM examples").Scan(&rows).Error)
	assert.EqualValues(t, 1, rows, "se creó una sola fila: %v", codes)
	assert.Equal(t, 1, original, "una sola respuesta es la ejecución original: %v", codes)
	assert.Equal(t, 20, codes[http.StatusCreated]+codes[http.StatusConflict], "solo 201 o 409: %v", codes)
}

// Al recibir SIGTERM la API drena: readiness en 503 y liveness en 200 mientras sigue
// atendiendo, y después termina limpia.
func TestSIGTERMDrainsBeforeClosing(t *testing.T) {
	api, base := startAPI(t, "HTTP_DRAIN_DELAY=1500ms")
	require.Equal(t, http.StatusOK, status(base+"/health/ready"))

	require.NoError(t, api.cmd.Process.Signal(syscall.SIGTERM))

	require.Eventually(t, func() bool { return status(base+"/health/ready") == http.StatusServiceUnavailable },
		time.Second, 10*time.Millisecond, "readiness debe pasar a 503 de inmediato")
	assert.Equal(t, http.StatusOK, status(base+"/health/live"), "durante el drenaje el proceso sigue atendiendo")

	select {
	case err := <-api.done:
		require.NoError(t, err, "salida limpia")
	case <-time.After(10 * time.Second):
		t.Fatal("no terminó tras el drenaje")
	}
	assert.Zero(t, status(base+"/health/live"), "el puerto quedó cerrado")
	assert.Contains(t, api.logs.String(), "drenando")
}

// El worker responde su sonda, y se apaga limpio con SIGTERM.
func TestWorkerHealthcheckAndShutdown(t *testing.T) {
	needRedis(t)
	addr := freeAddr(t)
	env := baseEnv(append(redisEnv(), "WORKER_HEALTH_ADDR="+addr)...)
	worker := start(t, "worker", env)
	probe := func() error {
		cmd := exec.Command(filepath.Join(binDir, "worker"), "healthcheck")
		cmd.Env = env
		return cmd.Run()
	}

	require.Eventually(t, func() bool { return probe() == nil }, 10*time.Second, 100*time.Millisecond, "la sonda del worker no responde")
	assert.Zero(t, worker.stop(t), "salida limpia")
	assert.Error(t, probe(), "con el worker detenido, la sonda falla")
}

// Una configuración inválida no arranca: código 2 y la lista de lo que falta.
func TestInvalidConfigurationDoesNotStart(t *testing.T) {
	env := baseEnv("APP_ENV=production", "DB_SSLMODE=require")
	out := &syncBuffer{}
	cmd := exec.Command(filepath.Join(binDir, "api"))
	cmd.Env, cmd.Stdout, cmd.Stderr = env, out, out

	err := cmd.Run()

	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 2, exit.ExitCode())
	assert.Contains(t, out.String(), "HTTP_TRUSTED_PROXIES")
}

// runMigrate ejecuta el binario de migraciones, como en un despliegue.
func runMigrate(t *testing.T, args ...string) (string, int) {
	t.Helper()
	out := &syncBuffer{}
	cmd := exec.Command(filepath.Join(binDir, "migrate"), args...)
	cmd.Env, cmd.Stdout, cmd.Stderr = baseEnv(), out, out
	cmd.Dir = "../.." // busca scripts/db/migrations desde la raíz, como en la imagen
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.String(), exit.ExitCode()
	}
	require.NoError(t, err, out.String())
	return out.String(), 0
}

// El paso de migraciones de cada despliegue: aplicar sin cambios, revertir, reaplicar.
func TestMigrateCommand(t *testing.T) {
	log, code := runMigrate(t, "up")
	require.Zero(t, code, "con todo aplicado, up no es un error: %s", log)
	log, code = runMigrate(t, "version")
	require.Zero(t, code, log)
	assert.Contains(t, log, `"version"`)

	log, code = runMigrate(t, "down", "1")
	require.Zero(t, code, log)
	log, code = runMigrate(t, "up")
	require.Zero(t, code, log)

	_, code = runMigrate(t, "comando-que-no-existe")
	assert.NotZero(t, code)
}
