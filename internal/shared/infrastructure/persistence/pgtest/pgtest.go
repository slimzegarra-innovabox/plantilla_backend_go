//go:build integration

// Package pgtest prepara una base PostgreSQL real para las pruebas de integración.
//
//   - Si TEST_DATABASE_URL está definida (p. ej. el service container del CI), la usa.
//   - Si no, levanta un contenedor postgres:16-alpine desechable con el CLI de Docker
//     y lo elimina al terminar.
//
// En ambos casos aplica las migraciones reales de scripts/db/migrations.
//
// Uso, una vez por paquete de pruebas:
//
//	func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }
//
// y en cada prueba: db := pgtest.DB(t) (las tablas se vacían con pgtest.Truncate).
//
// No usa testcontainers-go: su grafo de dependencias exige Go 1.25 junto con
// golang-migrate, y este helper cubre lo mismo sin dependencias nuevas.
package pgtest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file" // fuente file:// de las migraciones
	_ "github.com/jackc/pgx/v5/stdlib"                   // driver database/sql "pgx"
	"gorm.io/gorm"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
	"myapp.v1.0.0/internal/shared/infrastructure/persistence"
)

const image = "postgres:16-alpine"

var (
	shared   *gorm.DB
	dbConfig config.DBConfig
)

// Main levanta la base, aplica las migraciones, ejecuta las pruebas y limpia.
func Main(m *testing.M) int {
	cfg, stop, err := start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pgtest:", err)
		return 1
	}
	defer stop()

	dbConfig = cfg
	if err := MigrateUp(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "pgtest: migraciones:", err)
		return 1
	}
	db, err := persistence.Open(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pgtest:", err)
		return 1
	}
	shared = db
	defer func() { _ = persistence.Close(db) }()

	return m.Run()
}

// DB devuelve la conexión compartida del paquete de pruebas.
func DB(t *testing.T) *gorm.DB {
	t.Helper()
	if shared == nil {
		t.Fatal("pgtest.DB: falta TestMain con pgtest.Main(m)")
	}
	return shared
}

// Config devuelve la configuración de la base de prueba.
func Config() config.DBConfig { return dbConfig }

// Truncate vacía las tablas indicadas y reinicia sus secuencias.
func Truncate(t *testing.T, tables ...string) {
	t.Helper()
	for _, tbl := range tables {
		if !isIdentifier(tbl) {
			t.Fatalf("pgtest.Truncate: nombre de tabla no válido %q", tbl)
		}
	}
	sqlStmt := "TRUNCATE " + strings.Join(tables, ", ") + " RESTART IDENTITY CASCADE"
	if err := DB(t).Exec(sqlStmt).Error; err != nil {
		t.Fatalf("pgtest.Truncate: %v", err)
	}
}

// MigrationsPath es la ruta absoluta de scripts/db/migrations.
func MigrationsPath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..", "scripts", "db", "migrations")
}

// NewMigrate crea un golang-migrate contra la base indicada. El llamador lo cierra.
func NewMigrate(cfg config.DBConfig) (*migrate.Migrate, error) {
	sqlDB, err := sql.Open("pgx", cfg.MigrationDSN())
	if err != nil {
		return nil, err
	}
	driver, err := migratepg.WithInstance(sqlDB, &migratepg.Config{})
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return migrate.NewWithDatabaseInstance("file://"+MigrationsPath(), "postgres", driver)
}

// MigrateUp aplica todas las migraciones pendientes.
func MigrateUp(cfg config.DBConfig) error {
	m, err := NewMigrate(cfg)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

//--------------------------------------------------------------
// ARRANQUE DE LA BASE
//--------------------------------------------------------------

func start() (config.DBConfig, func(), error) {
	if raw := os.Getenv("TEST_DATABASE_URL"); raw != "" {
		cfg, err := parseURL(raw)
		return cfg, func() {}, err
	}
	return startContainer()
}

func startContainer() (config.DBConfig, func(), error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return config.DBConfig{}, nil, errors.New("docker no está disponible: instálalo o define TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // incluye descargar la imagen
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--rm",
		"-e", "POSTGRES_USER=test", "-e", "POSTGRES_PASSWORD=test", "-e", "POSTGRES_DB=test",
		"-p", "127.0.0.1::5432", image).Output()
	if err != nil {
		return config.DBConfig{}, nil, fmt.Errorf("docker run %s: %w", image, exitDetail(err))
	}
	id := strings.TrimSpace(string(out))
	// El id lo devuelve docker run; no es una entrada externa.
	stop := func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		_ = exec.CommandContext(stopCtx, "docker", "rm", "-f", id).Run() //nolint:gosec // id de docker run
	}

	portOut, err := exec.CommandContext(ctx, "docker", "port", id, "5432/tcp").Output() //nolint:gosec // id de docker run
	if err != nil {
		stop()
		return config.DBConfig{}, nil, fmt.Errorf("docker port: %w", exitDetail(err))
	}
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(strings.Split(string(portOut), "\n")[0]))
	if err != nil {
		stop()
		return config.DBConfig{}, nil, fmt.Errorf("puerto del contenedor: %w", err)
	}
	port, _ := strconv.Atoi(portStr)

	cfg := config.DBConfig{
		Host: host, Port: port, User: "test", Password: "test", Name: "test", SSLMode: "disable",
		MaxOpenConns: 10, MaxIdleConns: 2, ConnMaxLifetime: time.Minute,
	}
	if err := waitReady(cfg, 60*time.Second); err != nil {
		stop()
		return config.DBConfig{}, nil, err
	}
	return cfg, stop, nil
}

// waitReady espera a que el servidor acepte conexiones TCP. La imagen oficial
// arranca un servidor temporal (solo socket Unix) para inicializar y luego reinicia.
func waitReady(cfg config.DBConfig, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		db, err := sql.Open("pgx", cfg.DSN())
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err = db.PingContext(ctx)
			cancel()
			_ = db.Close()
			if err == nil {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("postgres no respondió en %s: %w", timeout, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func parseURL(raw string) (config.DBConfig, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return config.DBConfig{}, fmt.Errorf("TEST_DATABASE_URL inválida: %w", err)
	}
	port := 5432
	if p := u.Port(); p != "" {
		if port, err = strconv.Atoi(p); err != nil {
			return config.DBConfig{}, fmt.Errorf("TEST_DATABASE_URL: puerto inválido: %w", err)
		}
	}
	pass, _ := u.User.Password()
	ssl := u.Query().Get("sslmode")
	if ssl == "" {
		ssl = "disable"
	}
	return config.DBConfig{
		Host: u.Hostname(), Port: port, User: u.User.Username(), Password: config.Secret(pass),
		Name: strings.TrimPrefix(u.Path, "/"), SSLMode: ssl,
		MaxOpenConns: 10, MaxIdleConns: 2, ConnMaxLifetime: time.Minute,
	}, nil
}

func exitDetail(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}
