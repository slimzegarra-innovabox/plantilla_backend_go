package config

import (
	"net"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Integraciones opcionales (CLAUDE.md §11): cada una se activa con <X>_ENABLED=true
// y, solo entonces, sus variables pasan a ser obligatorias. Desactivada, el resto de
// sus variables se ignora y no se valida.

// RedisConfig configura Redis (lo usa Asynq para encolar y procesar tareas).
type RedisConfig struct {
	Enabled  bool
	Addr     string
	Password Secret
	DB       int
	// TLS cifra la conexión. Por defecto activo fuera de develop/test; se desactiva
	// explícitamente (REDIS_TLS=false) solo para un Redis en una red privada (compose).
	TLS bool
}

// AWSConfig es la configuración común de los clientes de AWS (S3, SQS).
type AWSConfig struct {
	Region string
	// Profile es el perfil de ~/.aws. Solo en develop: en QA/producción se usa el IAM Role.
	Profile string
}

// S3Config configura el almacenamiento de objetos.
type S3Config struct {
	Enabled bool
	Bucket  string
	// UploadMaxBytes es el tamaño máximo que se firma en una URL de subida.
	UploadMaxBytes int64
	// PresignTTL es la vigencia de las URL firmadas (máximo 1h).
	PresignTTL time.Duration
}

// BrevoConfig configura el envío de correo transaccional.
type BrevoConfig struct {
	Enabled     bool
	APIKey      Secret
	BaseURL     string
	SenderEmail string
	SenderName  string
	Timeout     time.Duration
}

// SQSConfig configura el consumidor de SQS (solo cmd/worker).
type SQSConfig struct {
	Enabled  bool
	QueueURL string
	// WaitTime es el long polling de cada ReceiveMessage (máximo 20s, límite de SQS).
	WaitTime time.Duration
	// VisibilityTimeout es el plazo para procesar un mensaje antes de que SQS lo reentregue.
	VisibilityTimeout time.Duration
	MaxMessages       int
}

// OTelConfig activa OpenTelemetry. El destino y el muestreo usan las variables
// estándar del SDK (OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_TRACES_SAMPLER…), que no se
// duplican aquí.
type OTelConfig struct {
	Enabled     bool
	ServiceName string
	// Metrics es false con OTEL_METRICS_EXPORTER=none (p. ej. en develop con Jaeger,
	// que solo recibe trazas).
	Metrics bool
}

func readOTel(r *reader, defaultService string) OTelConfig {
	// ServiceName se lee siempre: aun desactivado, el borde HTTP propaga un traceparent
	// entrante a los logs (correlación con el servicio que llamó).
	c := OTelConfig{Enabled: r.boolean("OTEL_ENABLED"), ServiceName: r.str("OTEL_SERVICE_NAME", defaultService)}
	if !c.Enabled {
		return c
	}
	c.Metrics = r.oneOf("OTEL_METRICS_EXPORTER", "otlp", "otlp", "none") == "otlp"
	if v, ok := r.raw("OTEL_EXPORTER_OTLP_ENDPOINT"); ok {
		if u, err := url.Parse(v); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			r.fail("OTEL_EXPORTER_OTLP_ENDPOINT", "debe ser una URL http(s), p. ej. http://localhost:4318")
		}
	}
	return c
}

// QueueConfig configura el servidor de tareas de Asynq (solo cmd/worker).
type QueueConfig struct {
	Concurrency     int
	ShutdownTimeout time.Duration
	// HealthAddr es donde el worker sirve /health/live para su HEALTHCHECK. Por
	// defecto solo en loopback: el worker no expone HTTP fuera del contenedor.
	HealthAddr string
}

func readRedis(r *reader, env string) RedisConfig {
	c := RedisConfig{Enabled: r.boolean("REDIS_ENABLED")}
	if !c.Enabled {
		return c
	}
	c.Addr = r.required("REDIS_ADDR")
	c.Password = Secret(r.str("REDIS_PASSWORD", ""))
	c.DB = r.intMin("REDIS_DB", 0, 0)
	c.TLS = r.booleanDefault("REDIS_TLS", !isLocal(env))

	checkPlaceholder(r, env, "REDIS_PASSWORD", c.Password)
	if !isLocal(env) && c.Password.Reveal() == "" {
		r.fail("REDIS_PASSWORD", "es obligatoria fuera de develop/test")
	}
	return c
}

// readAWS solo exige la región si alguna integración de AWS está activa.
func readAWS(r *reader, env string, needed bool) AWSConfig {
	c := AWSConfig{Profile: r.str("AWS_PROFILE", "")}
	if needed {
		c.Region = r.required("AWS_REGION")
	}
	if c.Profile != "" && env != EnvDevelop {
		r.fail("AWS_PROFILE", "solo se admite en develop; en QA/producción se usa el IAM Role")
	}
	return c
}

func readS3(r *reader) S3Config {
	c := S3Config{Enabled: r.boolean("S3_ENABLED")}
	if !c.Enabled {
		return c
	}
	c.Bucket = r.required("S3_BUCKET")
	c.UploadMaxBytes = int64(r.intMin("S3_UPLOAD_MAX_BYTES", 10<<20, 1))
	c.PresignTTL = r.duration("S3_PRESIGN_TTL", 5*time.Minute)
	if c.PresignTTL > time.Hour {
		r.fail("S3_PRESIGN_TTL", "no puede superar 1h")
	}
	return c
}

func readBrevo(r *reader, env string) BrevoConfig {
	c := BrevoConfig{Enabled: r.boolean("BREVO_ENABLED")}
	if !c.Enabled {
		return c
	}
	c.APIKey = Secret(r.required("BREVO_API_KEY"))
	c.BaseURL = strings.TrimRight(r.str("BREVO_BASE_URL", "https://api.brevo.com/v3"), "/")
	c.SenderEmail = r.required("BREVO_SENDER_EMAIL")
	c.SenderName = r.str("BREVO_SENDER_NAME", "")
	c.Timeout = r.duration("BREVO_TIMEOUT", 10*time.Second)

	checkPlaceholder(r, env, "BREVO_API_KEY", c.APIKey)
	if c.SenderEmail != "" {
		if _, err := mail.ParseAddress(c.SenderEmail); err != nil {
			r.fail("BREVO_SENDER_EMAIL", "no es un correo válido")
		}
	}
	if u, err := url.Parse(c.BaseURL); err != nil || u.Host == "" {
		r.fail("BREVO_BASE_URL", "no es una URL válida")
	} else if u.Scheme != "https" && !isLocal(env) {
		r.fail("BREVO_BASE_URL", "debe usar https fuera de develop/test")
	}
	return c
}

func readSQS(r *reader) SQSConfig {
	c := SQSConfig{Enabled: r.boolean("SQS_ENABLED")}
	if !c.Enabled {
		return c
	}
	c.QueueURL = r.required("SQS_QUEUE_URL")
	c.WaitTime = r.duration("SQS_WAIT_TIME", 20*time.Second)
	c.VisibilityTimeout = r.duration("SQS_VISIBILITY_TIMEOUT", 60*time.Second)
	c.MaxMessages = r.intMin("SQS_MAX_MESSAGES", 10, 1)
	// SQS trabaja en segundos enteros: 500ms se truncaría a 0 (sondeo sin espera).
	if c.WaitTime < time.Second || c.WaitTime > 20*time.Second || c.WaitTime%time.Second != 0 {
		r.fail("SQS_WAIT_TIME", "debe ser un número entero de segundos entre 1s y 20s (límites de SQS)")
	}
	if c.VisibilityTimeout < time.Second || c.VisibilityTimeout > 12*time.Hour || c.VisibilityTimeout%time.Second != 0 {
		r.fail("SQS_VISIBILITY_TIMEOUT", "debe ser un número entero de segundos entre 1s y 12h (límites de SQS)")
	}
	if c.MaxMessages > 10 {
		r.fail("SQS_MAX_MESSAGES", "no puede superar 10 (límite de SQS)")
	}
	if c.QueueURL != "" {
		if u, err := url.Parse(c.QueueURL); err != nil || u.Scheme != "https" || u.Host == "" {
			r.fail("SQS_QUEUE_URL", "debe ser la URL https de la cola")
		}
	}
	return c
}

func readQueue(r *reader) QueueConfig {
	return QueueConfig{
		Concurrency:     r.intMin("WORKER_CONCURRENCY", 10, 1),
		ShutdownTimeout: r.duration("WORKER_SHUTDOWN_TIMEOUT", 30*time.Second),
		HealthAddr:      readHostPort(r, "WORKER_HEALTH_ADDR", "127.0.0.1:8081"),
	}
}

// readHostPort lee una dirección host:puerto con puerto válido.
func readHostPort(r *reader, key, def string) string {
	v := r.str(key, def)
	_, port, err := net.SplitHostPort(v)
	if n, convErr := strconv.Atoi(port); err != nil || convErr != nil || n < 1 || n > 65535 {
		r.fail(key, "debe ser host:puerto (p. ej. 127.0.0.1:8081)")
	}
	return v
}

// isLocal indica los entornos donde se relajan TLS y contraseñas de servicios locales.
func isLocal(env string) bool { return env == EnvDevelop || env == EnvTest }
