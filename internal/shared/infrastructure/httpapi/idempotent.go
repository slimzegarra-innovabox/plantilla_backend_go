package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"myapp.v1.0.0/internal/shared/application/result"
	"myapp.v1.0.0/internal/shared/infrastructure/idempotency"
	"myapp.v1.0.0/internal/shared/infrastructure/logging"
)

const (
	// HeaderIdempotencyKey es la clave que envía el cliente en un POST.
	HeaderIdempotencyKey = "Idempotency-Key"
	// HeaderIdempotencyReplayed marca una respuesta repetida, no ejecutada.
	HeaderIdempotencyReplayed = "Idempotency-Replayed"

	// maxStoredBody acota lo que se guarda: una respuesta mayor no se puede repetir.
	maxStoredBody = 1 << 20
	// idempotencyStoreTimeout acota guardar o liberar la clave al terminar, aunque el
	// context de la petición ya se haya cancelado.
	idempotencyStoreTimeout = 5 * time.Second
)

var validIdempotencyKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,255}$`)

// Idempotent hace seguro reintentar un POST (ADR 0007). Es opcional para el cliente: sin
// cabecera Idempotency-Key, la petición sigue su curso normal. Con ella:
//
//   - la primera petición se ejecuta y su respuesta se guarda (salvo un 5xx: un
//     reintento debe poder ejecutarse de verdad);
//   - un reintento con el mismo cuerpo recibe la misma respuesta, sin ejecutar nada,
//     con la cabecera Idempotency-Replayed: true;
//   - con otro cuerpo: 422 IDEMPOTENCY_KEY_REUSED;
//   - mientras la primera sigue en curso: 409 IDEMPOTENCY_IN_PROGRESS con Retry-After.
//
// El alcance es usuario + método + ruta, así que va DESPUÉS de auth en la ruta.
func Idempotent(store idempotency.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.GetHeader(HeaderIdempotencyKey)
		if key == "" {
			c.Next()
			return
		}
		if !validIdempotencyKey.MatchString(key) {
			writeNotification(c, http.StatusBadRequest, result.Error(result.Validation, codeInvalidParameter,
				"Idempotency-Key debe tener de 1 a 255 caracteres: letras, dígitos y . _ : -").WithField(HeaderIdempotencyKey))
			return
		}
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			writeBindError(c, err, codeInvalidBody, "No se pudo leer el cuerpo de la petición.")
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		sum := sha256.Sum256(body)
		fingerprint := hex.EncodeToString(sum[:])
		scope := MustPrincipal(c).UserUUID.String() + ":" + c.Request.Method + ":" + c.Request.URL.Path

		owner := uuid.New()
		outcome, rec, err := store.Begin(c.Request.Context(), scope, key, fingerprint, owner)
		if err != nil {
			_ = c.Error(err)
			c.Abort()
			return
		}
		switch outcome {
		case idempotency.Replay:
			replay(c, rec)
		case idempotency.Mismatch:
			writeNotification(c, http.StatusUnprocessableEntity, result.Error(result.BusinessRule, "IDEMPOTENCY_KEY_REUSED",
				"Esta clave de idempotencia ya se usó con otros datos. Use una clave nueva.").WithField(HeaderIdempotencyKey))
		case idempotency.InProgress:
			c.Header("Retry-After", "1")
			writeNotification(c, http.StatusConflict, result.Error(result.Conflict, "IDEMPOTENCY_IN_PROGRESS",
				"Una petición con esta clave sigue en curso. Reintente en un momento."))
		default:
			execute(c, store, scope, key, owner)
		}
	}
}

// execute corre el handler capturando su respuesta y decide si se guarda.
func execute(c *gin.Context, store idempotency.Store, scope, key string, owner uuid.UUID) {
	capture := &captureWriter{ResponseWriter: c.Writer}
	c.Writer = capture

	settle := func() (context.Context, context.CancelFunc) {
		// El context de la petición puede estar vencido, y justo entonces hay que liberar.
		return context.WithTimeout(context.WithoutCancel(c.Request.Context()), idempotencyStoreTimeout)
	}
	log := logging.FromContext(c.Request.Context())
	abandon := func() {
		ctx, cancel := settle()
		defer cancel()
		if err := store.Abandon(ctx, scope, key, owner); err != nil {
			log.ErrorContext(ctx, "liberar clave de idempotencia", "error", err)
		}
	}

	defer func() {
		if rec := recover(); rec != nil {
			abandon()
			panic(rec) // lo registra y responde Recovery
		}
	}()
	c.Next()

	status := c.Writer.Status()
	switch {
	case len(c.Errors) > 0 && !c.Writer.Written(), status >= http.StatusInternalServerError:
		abandon() // sin resultado que repetir: el error técnico lo escribe ErrorHandler
	case capture.overflow:
		log.WarnContext(c.Request.Context(), "respuesta demasiado grande para repetirla por idempotencia", "bytes_limit", maxStoredBody)
		abandon()
	default:
		ctx, cancel := settle()
		defer cancel()
		err := store.Complete(ctx, scope, key, owner, idempotency.Record{
			Status: status,
			Headers: map[string]string{
				"Content-Type":  c.Writer.Header().Get("Content-Type"),
				"Location":      c.Writer.Header().Get("Location"),
				HeaderRequestID: c.Writer.Header().Get(HeaderRequestID),
			},
			Body: capture.body.Bytes(),
		})
		if errors.Is(err, idempotency.ErrReservationLost) {
			// Tardó más que la reserva y otra petición la tomó: se conserva la de esa.
			log.WarnContext(ctx, "la petición superó su reserva de idempotencia; no se guarda su respuesta")
		} else if err != nil {
			// La respuesta ya salió bien; un reintento la ejecutaría de nuevo al vencer la reserva.
			log.ErrorContext(ctx, "guardar respuesta de idempotencia", "error", err)
		}
	}
}

// replay repite la respuesta guardada. El request_id del cuerpo se reemplaza por el de
// esta petición: el sobre siempre coincide con X-Request-ID y con los logs (§9.1).
func replay(c *gin.Context, rec idempotency.Record) {
	for _, name := range []string{"Content-Type", "Location"} {
		if v := rec.Headers[name]; v != "" {
			c.Header(name, v)
		}
	}
	c.Header(HeaderIdempotencyReplayed, "true")
	body := rec.Body
	if original := rec.Headers[HeaderRequestID]; original != "" {
		current := c.Writer.Header().Get(HeaderRequestID)
		body = []byte(strings.Replace(string(body), `"request_id":"`+original+`"`, `"request_id":"`+current+`"`, 1))
	}
	c.Status(rec.Status)
	if len(body) > 0 {
		_, _ = c.Writer.Write(body)
	} else {
		c.Writer.WriteHeaderNow()
	}
	c.Abort()
}

// captureWriter copia lo que se escribe, hasta maxStoredBody.
type captureWriter struct {
	gin.ResponseWriter
	body     bytes.Buffer
	overflow bool
}

func (w *captureWriter) capture(n int, write func()) {
	if w.body.Len()+n > maxStoredBody {
		w.overflow = true
		return
	}
	write()
}

func (w *captureWriter) Write(b []byte) (int, error) {
	w.capture(len(b), func() { w.body.Write(b) })
	return w.ResponseWriter.Write(b)
}

func (w *captureWriter) WriteString(s string) (int, error) {
	w.capture(len(s), func() { w.body.WriteString(s) })
	return w.ResponseWriter.WriteString(s)
}
