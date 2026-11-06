package messaging

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/assert"

	"myapp.v1.0.0/internal/example/application/dto"
)

func TestHandleStatusChanged(t *testing.T) {
	ok := asynq.NewTask(TopicStatusChanged, []byte(`{"example_uuid":"0192f0a4-1b2c-7d3e-8f40-5a6b7c8d9e0f","status":"active","version":2}`))
	assert.NoError(t, handleStatusChanged(context.Background(), ok))

	for name, payload := range map[string]string{
		"json inválido": `{no-json`,
		"sin uuid":      `{"status":"active"}`,
	} {
		t.Run(name, func(t *testing.T) {
			err := handleStatusChanged(context.Background(), asynq.NewTask(TopicStatusChanged, []byte(payload)))
			assert.True(t, errors.Is(err, asynq.SkipRetry), "un evento inválido no se reintenta: %v", err)
		})
	}
}

func TestWithoutOutboxEventsAreDiscarded(t *testing.T) {
	assert.NoError(t, NewExampleEventPublisher(nil).StatusChanged(context.Background(), dto.ExampleStatusChanged{}))
}
