package clock_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"myapp.v1.0.0/internal/shared/infrastructure/clock"
)

func TestSystemNowIsUTCWithMicrosecondPrecision(t *testing.T) {
	now := clock.System{}.Now()

	assert.Equal(t, time.UTC, now.Location())
	assert.Zero(t, now.Nanosecond()%1000, "sin nanosegundos: coincide con TIMESTAMPTZ")
	assert.WithinDuration(t, time.Now(), now, time.Second)
}
