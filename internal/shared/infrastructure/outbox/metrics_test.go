package outbox_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"myapp.v1.0.0/internal/shared/infrastructure/outbox"
)

// Una base caída o ya cerrada no debe impedir que se exporten las demás métricas: si el
// callback devuelve error, el SDK descarta la exportación completa.
func TestBacklogMetricsFailureKeepsOtherMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	sqlDB, err := sql.Open("pgx", "postgres://127.0.0.1:1/x")
	require.NoError(t, err)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	require.NoError(t, outbox.RegisterBacklogMetrics(db, slog.New(slog.NewTextHandler(io.Discard, nil))))
	other, err := otel.Meter("test").Int64Counter("other.count")
	require.NoError(t, err)
	other.Add(context.Background(), 1)

	var got metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &got), "el fallo no debe llegar al SDK")

	names := map[string]bool{}
	for _, sm := range got.ScopeMetrics {
		for _, m := range sm.Metrics {
			names[m.Name] = true
		}
	}
	assert.True(t, names["other.count"], "las demás métricas siguen saliendo")
	assert.False(t, names["outbox.pending"], "sin lectura no se inventa un valor: la alarma lo ve como dato faltante")
}
