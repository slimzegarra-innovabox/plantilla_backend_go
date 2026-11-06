// Comando migrate gestiona las migraciones SQL de la base de datos (golang-migrate).
//
// Uso:
//
//	go run ./cmd/migrate up            aplica todas las migraciones pendientes
//	go run ./cmd/migrate down [N]      revierte las últimas N (por defecto 1)
//	go run ./cmd/migrate steps N       avanza N versiones (negativo: retrocede)
//	go run ./cmd/migrate version       muestra la versión actual
//	go run ./cmd/migrate force V       fuerza la versión V sin ejecutar SQL (solo emergencias)
//
// Usa la misma configuración que el servidor (APP_ENV y DB_*). Las migraciones se
// leen de scripts/db/migrations, o de MIGRATIONS_PATH.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/jackc/pgx/v5/stdlib"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
	"myapp.v1.0.0/internal/shared/infrastructure/logging"
)

var errUsage = errors.New("uso incorrecto")

func main() {
	err := run(os.Args[1:])
	switch {
	case err == nil:
	case errors.Is(err, errUsage):
		printUsage()
		os.Exit(2)
	default:
		slog.Error("migrate", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	if err := config.LoadDotEnv(); err != nil {
		return err
	}
	env, dbCfg, err := config.LoadDatabase()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return errors.New("configuración inválida")
	}
	log := logging.New(os.Stdout, slog.LevelInfo, env == config.EnvDevelop)
	slog.SetDefault(log)

	m, closeDB, err := open(dbCfg)
	if err != nil {
		return err
	}
	defer closeDB()

	command := strings.ToLower(args[0])
	switch command {
	case "up":
		err = ignoreNoChange(m.Up())
	case "down":
		n, perr := intArg(args, 1)
		if perr != nil {
			return perr
		}
		if n <= 0 {
			return fmt.Errorf("down: N debe ser positivo: %w", errUsage)
		}
		err = ignoreNoChange(m.Steps(-n))
	case "steps":
		n, perr := requiredIntArg(args)
		if perr != nil {
			return perr
		}
		err = ignoreNoChange(m.Steps(n))
	case "version":
	case "force":
		v, perr := requiredIntArg(args)
		if perr != nil {
			return perr
		}
		if err = m.Force(v); err == nil {
			log.Warn("versión forzada: verificar el estado del esquema manualmente", "version", v)
		}
	default:
		return fmt.Errorf("comando desconocido %q: %w", command, errUsage)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", command, err)
	}
	return printVersion(m, log)
}

// open conecta y prepara golang-migrate. La conexión se cierra con closeDB.
func open(cfg config.DBConfig) (m *migrate.Migrate, closeDB func(), err error) {
	db, err := sql.Open("pgx", cfg.MigrationDSN())
	if err != nil {
		return nil, nil, fmt.Errorf("abrir conexión: %w", err)
	}
	closeDB = func() {
		if cerr := db.Close(); cerr != nil {
			slog.Error("cerrar la conexión", "error", cerr)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		closeDB()
		return nil, nil, fmt.Errorf("conectar a %s:%d/%s: %w", cfg.Host, cfg.Port, cfg.Name, err)
	}

	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		closeDB()
		return nil, nil, fmt.Errorf("crear driver postgres: %w", err)
	}
	path := envOr("MIGRATIONS_PATH", "scripts/db/migrations")
	m, err = migrate.NewWithDatabaseInstance("file://"+path, "postgres", driver)
	if err != nil {
		closeDB()
		return nil, nil, fmt.Errorf("inicializar migraciones desde %s: %w", path, err)
	}
	m.Log = migrateLogger{}
	return m, closeDB, nil
}

func printVersion(m *migrate.Migrate, log *slog.Logger) error {
	v, dirty, err := m.Version()
	switch {
	case errors.Is(err, migrate.ErrNilVersion):
		log.Info("no hay migraciones aplicadas")
		return nil
	case err != nil:
		return fmt.Errorf("leer versión: %w", err)
	case dirty:
		log.Warn("el esquema está en estado DIRTY: una migración falló a medias", "version", v)
	default:
		log.Info("versión actual del esquema", "version", v)
	}
	return nil
}

func ignoreNoChange(err error) error {
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}

// intArg lee args[i] como entero; si no existe devuelve 1.
func intArg(args []string, i int) (int, error) {
	if len(args) <= i {
		return 1, nil
	}
	n, err := strconv.Atoi(args[i])
	if err != nil {
		return 0, fmt.Errorf("argumento %q no es un entero: %w", args[i], errUsage)
	}
	return n, nil
}

func requiredIntArg(args []string) (int, error) {
	if len(args) < 2 {
		return 0, fmt.Errorf("%s requiere un número: %w", args[0], errUsage)
	}
	return intArg(args, 1)
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func printUsage() {
	fmt.Fprint(os.Stderr, `Uso: go run ./cmd/migrate <comando> [args]

Comandos:
  up              aplica todas las migraciones pendientes
  down [N]        revierte las últimas N (por defecto 1)
  steps N         avanza N versiones (negativo: retrocede)
  version         muestra la versión actual del esquema
  force V         fuerza la versión V sin ejecutar SQL (solo emergencias)

Variables de entorno (las mismas que el servidor):
  APP_ENV                       develop | test | qa | production
  DB_HOST, DB_USER, DB_PASSWORD, DB_NAME
  DB_PORT                       por defecto 5432
  DB_SSLMODE                    disable | require | verify-ca | verify-full (por defecto require)
  MIGRATIONS_PATH               por defecto scripts/db/migrations
`)
}

// migrateLogger adapta slog a la interfaz migrate.Logger.
type migrateLogger struct{}

func (migrateLogger) Printf(format string, v ...any) {
	slog.Info(strings.TrimSpace(fmt.Sprintf(format, v...)))
}

func (migrateLogger) Verbose() bool { return false }
