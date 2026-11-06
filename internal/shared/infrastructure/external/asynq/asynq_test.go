package asynq_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
	sharedasynq "myapp.v1.0.0/internal/shared/infrastructure/external/asynq"
)

func TestDecodePayloadMarksInvalidPayloadAsFinal(t *testing.T) {
	var dst struct {
		ID string `json:"id"`
	}

	err := sharedasynq.DecodePayload(asynq.NewTask("example:x", []byte("{no-json")), &dst)

	require.Error(t, err)
	assert.True(t, errors.Is(err, asynq.SkipRetry), "un payload inválido no se reintenta")
	require.NoError(t, sharedasynq.DecodePayload(asynq.NewTask("example:x", []byte(`{"id":"a"}`)), &dst))
	assert.Equal(t, "a", dst.ID)
}

func TestRedisOptUsesTLSWhenConfigured(t *testing.T) {
	opt := sharedasynq.RedisOpt(config.RedisConfig{Addr: "redis:6379", Password: "pw", DB: 2, TLS: true})

	assert.Equal(t, "redis:6379", opt.Addr)
	assert.Equal(t, "pw", opt.Password)
	assert.Equal(t, 2, opt.DB)
	require.NotNil(t, opt.TLSConfig)
	assert.Nil(t, sharedasynq.RedisOpt(config.RedisConfig{Addr: "redis:6379"}).TLSConfig)
}

func TestPingRespectsContextDeadline(t *testing.T) {
	// 10.255.255.1 no responde: sin el plazo del context, Ping esperaría el DialTimeout.
	p := sharedasynq.NewPublisher(config.RedisConfig{Addr: "10.255.255.1:6379"})
	t.Cleanup(func() { _ = p.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := p.Ping(ctx)

	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second)
}
