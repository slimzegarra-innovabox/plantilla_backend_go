// Package persistence contiene la infraestructura común de base de datos:
// conexión, transacciones y clasificación de errores de PostgreSQL.
package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // driver database/sql "pgx" (HealthChecker)
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
)

// Open abre el pool de conexiones, lo configura y comprueba que responde.
// verboseSQL registra cada sentencia (solo en desarrollo). Los valores de los
// parámetros nunca se registran (ParameterizedQueries).
func Open(ctx context.Context, cfg config.DBConfig, log *slog.Logger, verboseSQL bool) (*gorm.DB, error) {
	level := gormlogger.Warn
	if verboseSQL {
		level = gormlogger.Info
	}

	db, err := gorm.Open(postgres.Open(cfg.DSN()), &gorm.Config{
		Logger: gormlogger.New(
			slog.NewLogLogger(log.Handler(), slog.LevelWarn),
			gormlogger.Config{
				SlowThreshold:             200 * time.Millisecond,
				LogLevel:                  level,
				IgnoreRecordNotFoundError: true,
				ParameterizedQueries:      true,
			},
		),
	})
	if err != nil {
		// El error de gorm/pgx no incluye la contraseña; aun así no se agrega el DSN.
		return nil, fmt.Errorf("abrir conexión a %s:%d/%s: %w", cfg.Host, cfg.Port, cfg.Name, err)
	}

	// OpenTelemetry: un span por sentencia con el SQL parametrizado (nunca los valores)
	// y métricas del pool. No-op si OTEL_ENABLED=false.
	if err := db.Use(otelPlugin{}); err != nil {
		return nil, fmt.Errorf("instrumentar gorm: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("obtener pool sql: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	if err := registerPoolMetrics(sqlDB, cfg.Name); err != nil {
		return nil, fmt.Errorf("métricas del pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping a %s:%d/%s: %w", cfg.Host, cfg.Port, cfg.Name, err)
	}
	return db, nil
}

// CountUpTo cuenta las filas de query, como máximo limit. Un listado la usa en lugar de
// Count, con shareddto.MaxCount: su costo queda acotado por limit, sin importar el tamaño
// de la tabla, mientras que un COUNT(*) la recorre entera en cada petición.
func CountUpTo(query *gorm.DB, limit int) (int64, error) {
	sub := query.Session(&gorm.Session{}).Select("1").Limit(limit)
	var total int64
	err := query.Session(&gorm.Session{NewDB: true}).Table("(?) AS capped", sub).Count(&total).Error
	return total, err
}

// Close cierra el pool de conexiones.
func Close(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Ping comprueba que la base de datos responde usando el pool de la aplicación.
// Para /health/ready se usa HealthChecker, que no compite por ese pool.
func Ping(db *gorm.DB) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.PingContext(ctx)
	}
}

// HealthChecker comprueba la base de datos con una conexión propia, fuera del pool de
// la aplicación. Responde «¿la base está viva?» y no «¿hay conexiones libres?»: con el
// pool lleno en un pico, un ping que espera una conexión fallaría en todas las réplicas
// a la vez, y el balanceador sacaría de rotación instancias sanas (caída en cascada).
type HealthChecker struct{ db *sql.DB }

// NewHealthChecker abre la conexión de las sondas (una sola, perezosa: no conecta hasta
// la primera sonda). Se cierra con Close al apagar.
func NewHealthChecker(cfg config.DBConfig) (*HealthChecker, error) {
	db, err := sql.Open("pgx", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("conexión de health check: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	return &HealthChecker{db: db}, nil
}

// Ping hace una ida y vuelta real a PostgreSQL.
func (h *HealthChecker) Ping(ctx context.Context) error { return h.db.PingContext(ctx) }

// Close cierra la conexión de las sondas.
func (h *HealthChecker) Close() error { return h.db.Close() }
