package sqs_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
	"myapp.v1.0.0/internal/shared/infrastructure/external/sqs"
)

// fakeQueue entrega cada lote una vez y después bloquea como el long polling.
type fakeQueue struct {
	mu         sync.Mutex
	batches    [][]types.Message
	receiveErr []error
	deleted    []string
}

func (q *fakeQueue) ReceiveMessage(ctx context.Context, _ *awssqs.ReceiveMessageInput, _ ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	q.mu.Lock()
	if len(q.receiveErr) > 0 {
		err := q.receiveErr[0]
		q.receiveErr = q.receiveErr[1:]
		q.mu.Unlock()
		return nil, err
	}
	if len(q.batches) > 0 {
		b := q.batches[0]
		q.batches = q.batches[1:]
		q.mu.Unlock()
		return &awssqs.ReceiveMessageOutput{Messages: b}, nil
	}
	q.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (q *fakeQueue) DeleteMessage(ctx context.Context, in *awssqs.DeleteMessageInput, _ ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err // como el SDK real, con el plazo vencido no llega a enviarlo
	}
	q.deleted = append(q.deleted, aws.ToString(in.ReceiptHandle))
	return &awssqs.DeleteMessageOutput{}, nil
}

func (q *fakeQueue) Deleted() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.deleted...)
}

func msg(id, body string) types.Message {
	return types.Message{MessageId: aws.String(id), Body: aws.String(body), ReceiptHandle: aws.String("rh-" + id)}
}

var cfg = config.SQSConfig{QueueURL: "https://sqs.local/q", WaitTime: time.Second, VisibilityTimeout: 5 * time.Second, MaxMessages: 10}

func runUntil(t *testing.T, c *sqs.Consumer, done func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- c.Run(ctx) }()
	require.Eventually(t, done, 2*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("el consumidor no se detuvo al cancelar el context")
	}
}

func TestDeletesOnlyProcessedMessagesAndNeverLogsBodies(t *testing.T) {
	q := &fakeQueue{batches: [][]types.Message{{msg("1", "ok"), msg("2", "fail secret@example.com"), msg("3", "panic")}}}
	var logs bytes.Buffer
	var calls sync.WaitGroup
	calls.Add(3)
	handler := func(_ context.Context, body string) error {
		defer calls.Done()
		switch body {
		case "ok":
			return nil
		case "panic":
			panic("boom")
		}
		return errors.New("fallo de negocio")
	}
	c := sqs.NewConsumer(q, cfg, handler, slog.New(slog.NewJSONHandler(&logs, nil)))

	runUntil(t, c, func() bool { return len(q.Deleted()) == 1 })
	calls.Wait()

	assert.Equal(t, []string{"rh-1"}, q.Deleted())
	assert.Contains(t, logs.String(), `"message_id":"2"`)
	assert.Contains(t, logs.String(), "panic en el handler: boom")
	assert.NotContains(t, logs.String(), "secret@example.com")
}

func TestRecoversFromReceiveErrors(t *testing.T) {
	q := &fakeQueue{receiveErr: []error{errors.New("throttled")}, batches: [][]types.Message{{msg("1", "ok")}}}
	c := sqs.NewConsumer(q, cfg, func(context.Context, string) error { return nil }, slog.New(slog.DiscardHandler))

	runUntil(t, c, func() bool { return len(q.Deleted()) == 1 })
}

func TestStopsPromptlyWhileWaitingForMessages(t *testing.T) {
	c := sqs.NewConsumer(&fakeQueue{}, cfg, func(context.Context, string) error { return nil }, slog.New(slog.DiscardHandler))

	runUntil(t, c, func() bool { return true })
}

func TestProcessingContinuesTheProducerTrace(t *testing.T) {
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

	m := msg("1", "ok")
	m.MessageAttributes = map[string]types.MessageAttributeValue{
		"traceparent": {DataType: aws.String("String"), StringValue: aws.String("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")},
	}
	q := &fakeQueue{batches: [][]types.Message{{m}}}
	var got trace.TraceID
	var mu sync.Mutex
	handler := func(ctx context.Context, _ string) error {
		mu.Lock()
		defer mu.Unlock()
		got = trace.SpanContextFromContext(ctx).TraceID()
		return nil
	}
	c := sqs.NewConsumer(q, cfg, handler, slog.New(slog.DiscardHandler))

	runUntil(t, c, func() bool { return len(q.Deleted()) == 1 })

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "0af7651916cd43dd8448eb211c80319c", got.String())
}

// Un mensaje procesado se borra aunque el handler haya terminado al filo del plazo: el
// margen existe para eso. Si no, se reentregaría un mensaje ya procesado.
func TestDeletesMessageFinishedAtTheDeadline(t *testing.T) {
	q := &fakeQueue{batches: [][]types.Message{{msg("1", "a")}}}
	short := cfg
	short.VisibilityTimeout = 2 * time.Second // sin margen: el plazo vence a los 2 s
	handler := func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return nil
	}
	c := sqs.NewConsumer(q, short, handler, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- c.Run(ctx) }()

	require.Eventually(t, func() bool { return len(q.Deleted()) == 1 }, 4*time.Second, 10*time.Millisecond)
	cancel()
	require.NoError(t, <-stopped)
}

// El long polling no consume el plazo: la visibilidad empieza al entregarse el mensaje.
func TestDeadlineIsNotReducedByLongPolling(t *testing.T) {
	q := &slowReceive{fakeQueue: fakeQueue{batches: [][]types.Message{{msg("1", "a")}}}, wait: 300 * time.Millisecond}
	got := make(chan time.Time, 1)
	var received time.Time
	handler := func(ctx context.Context, _ string) error {
		received = time.Now()
		d, _ := ctx.Deadline()
		got <- d
		return nil
	}
	c := sqs.NewConsumer(q, cfg, handler, slog.New(slog.DiscardHandler))

	runUntil(t, c, func() bool { return len(q.Deleted()) == 1 })

	// Visibilidad 5 s, margen 1 s: quedan unos 4 s, sin los 300 ms de espera.
	assert.Greater(t, (<-got).Sub(received), 3900*time.Millisecond)
}

// slowReceive tarda en entregar el lote, como un long polling en una cola tranquila.
type slowReceive struct {
	fakeQueue
	wait time.Duration
}

func (q *slowReceive) ReceiveMessage(ctx context.Context, in *awssqs.ReceiveMessageInput, o ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	time.Sleep(q.wait)
	return q.fakeQueue.ReceiveMessage(ctx, in, o...)
}

func TestBatchIsProcessedInParallelWithDeadlineFromReceive(t *testing.T) {
	q := &fakeQueue{batches: [][]types.Message{{msg("1", "a"), msg("2", "b"), msg("3", "c")}}}
	var arrived sync.WaitGroup
	arrived.Add(3)
	start := time.Now()
	var mu sync.Mutex
	var deadlines []time.Time
	handler := func(ctx context.Context, _ string) error {
		arrived.Done()
		arrived.Wait() // en secuencia, el primer handler esperaría para siempre
		d, ok := ctx.Deadline()
		require.True(t, ok)
		mu.Lock()
		deadlines = append(deadlines, d)
		mu.Unlock()
		return nil
	}
	c := sqs.NewConsumer(q, cfg, handler, slog.New(slog.DiscardHandler))

	runUntil(t, c, func() bool { return len(q.Deleted()) == 3 })

	mu.Lock()
	defer mu.Unlock()
	for _, d := range deadlines {
		// visibilidad 5s, margen 1s: el plazo vence ~4s después de recibir, no después de empezar.
		assert.WithinDuration(t, start.Add(4*time.Second), d, 500*time.Millisecond)
	}
}
