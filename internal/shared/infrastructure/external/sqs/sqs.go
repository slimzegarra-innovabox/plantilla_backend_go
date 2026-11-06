// Package sqs es el consumidor de Amazon SQS de cmd/worker.
//
// Reglas:
//   - Corre solo en el worker y vive lo mismo que el proceso: no hay endpoints HTTP
//     para arrancarlo o detenerlo. Se detiene cancelando el context (SIGTERM).
//   - Los mensajes de un lote se procesan en paralelo (hasta SQS_MAX_MESSAGES), cada
//     uno con un plazo contado desde la recepción: la visibilidad empieza a correr en
//     ReceiveMessage, y pasado ese plazo SQS ya entregó el mensaje a otro consumidor.
//   - Al apagar deja de recibir y espera el lote en curso, como máximo un
//     SQS_VISIBILITY_TIMEOUT: el orquestador debe esperar más que eso.
//   - Entrega al menos una vez: el Handler debe ser idempotente. Un mensaje se borra
//     solo si el Handler devuelve nil; si no, SQS lo reentrega al vencer la visibilidad
//     (y lo manda a la DLQ según la redrive policy de la cola).
//   - Nunca se registra el cuerpo de un mensaje (puede tener datos personales): solo su id.
//   - Trazas (ADR 0005): si el productor envía los atributos de mensaje W3C (traceparent,
//     tracestate), el procesamiento es hijo de su traza; si no, empieza una traza nueva.
package sqs

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
	"myapp.v1.0.0/internal/shared/infrastructure/logging"
)

// Handler procesa el cuerpo de un mensaje. Debe ser idempotente.
type Handler func(ctx context.Context, body string) error

// API es el subconjunto del SDK que usa el consumidor (permite probarlo sin AWS).
type API interface {
	ReceiveMessage(ctx context.Context, in *awssqs.ReceiveMessageInput, opts ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *awssqs.DeleteMessageInput, opts ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error)
}

// Consumer recibe mensajes de una cola y los entrega a un Handler.
type Consumer struct {
	api        API
	queueURL   string
	handler    Handler
	log        *slog.Logger
	wait       time.Duration
	visibility time.Duration
	max        int
	// backoff tras un error al recibir; crece hasta maxBackoff.
	backoff, maxBackoff time.Duration
	// queueName es el último segmento de la URL: nombre de los spans.
	queueName string
	heartbeat func()
}

// WithHeartbeat hace que el consumidor llame a beat en cada vuelta del bucle
// (liveness de cmd/worker). Devuelve el mismo consumidor.
func (c *Consumer) WithHeartbeat(beat func()) *Consumer {
	c.heartbeat = beat
	return c
}

// MaxLoopGap es lo más que puede tardar una vuelta del bucle sin estar colgado: el
// long polling más el procesamiento de un lote (una visibilidad), con margen.
func MaxLoopGap(cfg config.SQSConfig) time.Duration {
	return cfg.WaitTime + cfg.VisibilityTimeout + time.Minute
}

// NewConsumer crea el consumidor. api suele ser sqs.NewFromConfig(awsCfg).
func NewConsumer(api API, cfg config.SQSConfig, handler Handler, log *slog.Logger) *Consumer {
	return &Consumer{
		api:        api,
		queueURL:   cfg.QueueURL,
		handler:    handler,
		log:        log.With("component", "sqs"),
		wait:       cfg.WaitTime,
		visibility: cfg.VisibilityTimeout,
		max:        cfg.MaxMessages,
		backoff:    time.Second,
		maxBackoff: 30 * time.Second,
		queueName:  cfg.QueueURL[strings.LastIndex(cfg.QueueURL, "/")+1:],
		heartbeat:  func() {},
	}
}

// Run consume hasta que ctx se cancela. Devuelve nil al detenerse de forma ordenada.
func (c *Consumer) Run(ctx context.Context) error {
	c.log.Info("consumidor iniciado")
	delay := c.backoff
	for {
		if ctx.Err() != nil {
			c.log.Info("consumidor detenido")
			return nil
		}
		c.heartbeat()
		out, err := c.api.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:            aws.String(c.queueURL),
			MaxNumberOfMessages: int32(c.max),
			WaitTimeSeconds:     int32(c.wait / time.Second),
			VisibilityTimeout:   int32(c.visibility / time.Second),
			// Solo los atributos de traza: el resto no se necesita para procesar.
			MessageAttributeNames: []string{"traceparent", "tracestate", "baggage"},
		})
		if err != nil {
			if ctx.Err() != nil {
				continue // apagado durante el long polling
			}
			c.log.Error("recibir mensajes de SQS", "error", err, "retry_in", delay.String())
			if !sleep(ctx, delay) {
				continue
			}
			delay = min(delay*2, c.maxBackoff)
			continue
		}
		delay = c.backoff
		// La visibilidad corre desde que SQS entregó el lote, no desde que empezó el long
		// polling: contar desde antes restaría al mensaje hasta WaitTime. El margen cubre
		// la latencia de la respuesta y el DeleteMessage.
		deadline := time.Now().Add(c.visibility - processingMargin(c.visibility))
		var wg sync.WaitGroup
		for _, m := range out.Messages {
			wg.Go(func() { c.process(ctx, m, deadline) })
		}
		wg.Wait()
	}
}

// processingMargin es el tiempo reservado para borrar el mensaje antes de que
// venza su visibilidad: el 10%, entre 1s y 30s (0 si la visibilidad es mínima).
func processingMargin(visibility time.Duration) time.Duration {
	if visibility <= 2*time.Second {
		return 0
	}
	return min(max(visibility/10, time.Second), 30*time.Second)
}

// process maneja un mensaje hasta deadline. No se corta al apagar: termina lo empezado.
func (c *Consumer) process(parent context.Context, m types.Message, deadline time.Time) {
	id := aws.ToString(m.MessageId)
	log := c.log.With("message_id", id)
	if m.Body == nil || m.ReceiptHandle == nil {
		log.Warn("mensaje sin cuerpo o sin receipt handle; se ignora")
		return
	}

	ctx, cancel := context.WithDeadline(context.WithoutCancel(parent), deadline)
	defer cancel()
	ctx = otel.GetTextMapPropagator().Extract(ctx, attributeCarrier(m.MessageAttributes))
	ctx, span := otel.Tracer(scopeName).Start(ctx, "process "+c.queueName, trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attribute.String("messaging.system", "aws_sqs"),
			attribute.String("messaging.destination.name", c.queueName), attribute.String("messaging.message.id", id)))
	defer span.End()
	ctx = logging.WithLogger(ctx, log)

	start := time.Now()
	if err := c.safeHandle(ctx, *m.Body); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "procesar mensaje")
		// Único registro del fallo; el mensaje no se borra y SQS lo reentrega.
		log.ErrorContext(ctx, "procesar mensaje (se reintentará)", "error", err)
		return
	}
	// Con su propio plazo: el del proceso pudo vencer, y el margen existe para este borrado.
	// Si no se borrara, un mensaje ya procesado se reentregaría.
	deleteCtx, cancelDelete := context.WithTimeout(context.WithoutCancel(ctx), max(processingMargin(c.visibility), time.Second))
	defer cancelDelete()
	if _, err := c.api.DeleteMessage(deleteCtx, &awssqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.queueURL),
		ReceiptHandle: m.ReceiptHandle,
	}); err != nil {
		// Se reentregará: la idempotencia del Handler lo cubre.
		span.RecordError(err)
		log.ErrorContext(ctx, "borrar mensaje procesado", "error", err)
		return
	}
	log.InfoContext(ctx, "mensaje procesado", "duration_ms", time.Since(start).Milliseconds())
}

const scopeName = "myapp.v1.0.0/internal/shared/infrastructure/external/sqs"

// attributeCarrier lee el contexto de traza de los atributos de mensaje de SQS.
type attributeCarrier map[string]types.MessageAttributeValue

func (a attributeCarrier) Get(key string) string {
	if v, ok := a[key]; ok {
		return aws.ToString(v.StringValue)
	}
	return ""
}

func (a attributeCarrier) Set(string, string) {} // el consumidor solo lee

func (a attributeCarrier) Keys() []string {
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	return keys
}

// safeHandle convierte un panic del Handler en error (con stack trace), para no tumbar el worker.
func (c *Consumer) safeHandle(ctx context.Context, body string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic en el handler: %v\n%s", r, debug.Stack())
		}
	}()
	return c.handler(ctx, body)
}

// sleep espera d o hasta que ctx se cancele. Devuelve false si se canceló.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
