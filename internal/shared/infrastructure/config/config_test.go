package config_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
)

const testSecret = "0123456789abcdef0123456789abcdef" // 32 bytes, solo para pruebas

func validEnv() map[string]string {
	return map[string]string{
		"APP_ENV":      "develop",
		"DB_HOST":      "localhost",
		"DB_USER":      "app",
		"DB_PASSWORD":  "db-password-xyz",
		"DB_NAME":      "app",
		"DB_SSLMODE":   "disable",
		"JWT_SECRET":   testSecret,
		"JWT_ISSUER":   "https://auth.example.com",
		"JWT_AUDIENCE": "myapp-api",
	}
}

func lookup(env map[string]string) config.LookupFunc {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

func TestLoadValidConfigWithDefaults(t *testing.T) {
	cfg, err := config.LoadFrom(lookup(validEnv()))

	require.NoError(t, err)
	assert.Equal(t, "develop", cfg.Env)
	assert.Equal(t, ":8080", cfg.HTTP.Addr)
	assert.Equal(t, int64(1<<20), cfg.HTTP.BodyLimitBytes)
	assert.Equal(t, 5432, cfg.DB.Port)
	assert.Equal(t, 30*time.Second, cfg.JWT.Leeway)
	assert.Equal(t, slog.LevelInfo, cfg.Log.Level)
}

func TestLoadReportsAllMissingVarsAtOnce(t *testing.T) {
	_, err := config.LoadFrom(lookup(map[string]string{}))

	require.Error(t, err)
	for _, key := range []string{"APP_ENV", "DB_HOST", "DB_USER", "DB_PASSWORD", "DB_NAME", "JWT_SECRET", "JWT_ISSUER", "JWT_AUDIENCE"} {
		assert.Contains(t, err.Error(), key)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := map[string]struct {
		key, value string
	}{
		"entorno desconocido":       {"APP_ENV", "quality"},
		"secreto corto":             {"JWT_SECRET", "short"},
		"puerto no numérico":        {"DB_PORT", "abc"},
		"duración inválida":         {"HTTP_READ_TIMEOUT", "10"},
		"sslmode desconocido":       {"DB_SSLMODE", "maybe"},
		"CORS comodín":              {"HTTP_CORS_ALLOWED_ORIGINS", "https://a.com,*"},
		"leeway excesivo":           {"JWT_LEEWAY", "5m"},
		"rate limit negativo":       {"HTTP_RATE_LIMIT_RPS", "-1"},
		"nivel de log desconocido":  {"LOG_LEVEL", "verbose"},
		"límite de cuerpo inválido": {"HTTP_BODY_LIMIT_BYTES", "0"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			env := validEnv()
			env[tt.key] = tt.value

			_, err := config.LoadFrom(lookup(env))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.key)
		})
	}
}

func TestLoadRejectsSSLDisabledInProduction(t *testing.T) {
	env := validEnv()
	env["APP_ENV"] = "production"

	_, err := config.LoadFrom(lookup(env))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB_SSLMODE")
}

func TestLoadParsesLists(t *testing.T) {
	env := validEnv()
	env["HTTP_CORS_ALLOWED_ORIGINS"] = " https://a.com , ,https://b.com "

	cfg, err := config.LoadFrom(lookup(env))

	require.NoError(t, err)
	assert.Equal(t, []string{"https://a.com", "https://b.com"}, cfg.HTTP.CORSOrigins)
}

func TestSecretIsNeverPrinted(t *testing.T) {
	cfg, err := config.LoadFrom(lookup(validEnv()))
	require.NoError(t, err)

	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("cfg", "jwt", cfg.JWT.Secret, "db", cfg.DB)
	printed := fmt.Sprintf("%v %+v %#v %s", cfg, cfg, cfg.JWT.Secret, cfg.DB.Password)

	assert.NotContains(t, buf.String(), testSecret)
	assert.NotContains(t, printed, testSecret)
	assert.NotContains(t, buf.String()+printed, "db-password-xyz")
	assert.Equal(t, testSecret, cfg.JWT.Secret.Reveal())
}

func TestDSNEscapesCredentials(t *testing.T) {
	db := config.DBConfig{Host: "h", Port: 5432, User: "u", Password: "p@ss/w:rd", Name: "d", SSLMode: "require"}

	dsn := db.DSN()

	assert.True(t, strings.HasPrefix(dsn, "postgres://u:p%40ss%2Fw%3Ard@h:5432/d?"), dsn)
	assert.Contains(t, dsn, "sslmode=require")
}

func TestLoadDatabaseDoesNotRequireServerConfig(t *testing.T) {
	env := validEnv()
	delete(env, "JWT_SECRET")
	delete(env, "JWT_ISSUER")
	delete(env, "JWT_AUDIENCE")

	appEnv, db, err := config.LoadDatabaseFrom(lookup(env))

	require.NoError(t, err)
	assert.Equal(t, "develop", appEnv)
	assert.Equal(t, "localhost", db.Host)
	assert.Equal(t, "disable", db.SSLMode)
}

func TestLoadDatabaseAppliesSameRules(t *testing.T) {
	_, _, err := config.LoadDatabaseFrom(lookup(map[string]string{"APP_ENV": "production", "DB_SSLMODE": "disable"}))

	require.Error(t, err)
	for _, key := range []string{"DB_HOST", "DB_USER", "DB_PASSWORD", "DB_NAME", "DB_SSLMODE"} {
		assert.Contains(t, err.Error(), key)
	}
}

func TestLoadRejectsTemplatePlaceholdersOutsideDevelop(t *testing.T) {
	env := validEnv()
	env["JWT_SECRET"] = "change-me-generate-with-openssl-rand-base64-48"
	env["DB_PASSWORD"] = "change-me"

	_, err := config.LoadFrom(lookup(env))
	require.NoError(t, err, "en develop el marcador se tolera")

	env["APP_ENV"] = "qa"
	_, err = config.LoadFrom(lookup(env))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JWT_SECRET")
	assert.Contains(t, err.Error(), "DB_PASSWORD")
}

func TestOptionalIntegrationsAreOffByDefaultAndIgnoreTheirVars(t *testing.T) {
	env := validEnv()
	env["S3_BUCKET"] = "" // desactivado: no se exige ni se valida
	env["BREVO_API_KEY"] = "change-me"

	cfg, err := config.LoadFrom(lookup(env))

	require.NoError(t, err)
	assert.False(t, cfg.Redis.Enabled)
	assert.False(t, cfg.S3.Enabled)
	assert.False(t, cfg.Brevo.Enabled)
}

func TestEnabledIntegrationsRequireTheirVars(t *testing.T) {
	env := validEnv()
	env["REDIS_ENABLED"] = "true"
	env["S3_ENABLED"] = "true"
	env["BREVO_ENABLED"] = "true"

	_, err := config.LoadFrom(lookup(env))

	require.Error(t, err)
	for _, key := range []string{"REDIS_ADDR", "S3_BUCKET", "AWS_REGION", "BREVO_API_KEY", "BREVO_SENDER_EMAIL"} {
		assert.Contains(t, err.Error(), key)
	}
}

func TestEnabledIntegrationsWithDefaults(t *testing.T) {
	env := validEnv()
	for k, v := range map[string]string{
		"REDIS_ENABLED": "true", "REDIS_ADDR": "localhost:6379",
		"S3_ENABLED": "true", "S3_BUCKET": "bucket", "AWS_REGION": "us-east-1", "AWS_PROFILE": "dev",
		"BREVO_ENABLED": "true", "BREVO_API_KEY": "key", "BREVO_SENDER_EMAIL": "no-reply@example.com",
	} {
		env[k] = v
	}

	cfg, err := config.LoadFrom(lookup(env))

	require.NoError(t, err)
	assert.False(t, cfg.Redis.TLS, "en develop Redis va sin TLS por defecto")
	assert.Equal(t, int64(10<<20), cfg.S3.UploadMaxBytes)
	assert.Equal(t, 5*time.Minute, cfg.S3.PresignTTL)
	assert.Equal(t, "https://api.brevo.com/v3", cfg.Brevo.BaseURL)
	assert.Equal(t, "dev", cfg.AWS.Profile)

	env["APP_ENV"], env["DB_SSLMODE"], env["AWS_PROFILE"], env["REDIS_PASSWORD"] = "qa", "require", "", "pw"
	env["HTTP_TRUSTED_PROXIES"] = "10.0.0.0/16"
	cfg, err = config.LoadFrom(lookup(env))
	require.NoError(t, err)
	assert.True(t, cfg.Redis.TLS, "fuera de develop/test Redis usa TLS salvo que se desactive")
}

func TestOptionalIntegrationsAreHardenedOutsideDevelop(t *testing.T) {
	env := validEnv()
	env["APP_ENV"] = "production"
	env["DB_SSLMODE"] = "require"
	for k, v := range map[string]string{
		"REDIS_ENABLED": "true", "REDIS_ADDR": "redis:6379",
		"S3_ENABLED": "true", "S3_BUCKET": "bucket", "AWS_REGION": "us-east-1", "AWS_PROFILE": "dev",
		"BREVO_ENABLED": "true", "BREVO_API_KEY": "change-me", "BREVO_SENDER_EMAIL": "no-reply@example.com",
		"BREVO_BASE_URL": "http://api.brevo.com/v3",
	} {
		env[k] = v
	}

	_, err := config.LoadFrom(lookup(env))

	require.Error(t, err)
	for _, want := range []string{"REDIS_PASSWORD", "AWS_PROFILE", "BREVO_API_KEY", "BREVO_BASE_URL"} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestLoadRejectsInvalidOptionalValues(t *testing.T) {
	base := map[string]string{
		"S3_ENABLED": "true", "S3_BUCKET": "b", "AWS_REGION": "us-east-1",
		"BREVO_ENABLED": "true", "BREVO_API_KEY": "k", "BREVO_SENDER_EMAIL": "no-reply@example.com",
	}
	tests := map[string]struct{ key, value string }{
		"interruptor no booleano": {"REDIS_ENABLED", "si"},
		"TTL excesivo":            {"S3_PRESIGN_TTL", "2h"},
		"remitente inválido":      {"BREVO_SENDER_EMAIL", "no-es-correo"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			env := validEnv()
			for k, v := range base {
				env[k] = v
			}
			env[tt.key] = tt.value
			_, err := config.LoadFrom(lookup(env))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.key)
		})
	}
}

func workerEnv() map[string]string {
	env := validEnv()
	delete(env, "JWT_SECRET")
	delete(env, "JWT_ISSUER")
	delete(env, "JWT_AUDIENCE")
	env["REDIS_ENABLED"] = "true"
	env["REDIS_ADDR"] = "localhost:6379"
	return env
}

func TestLoadWorkerDoesNotRequireServerConfig(t *testing.T) {
	cfg, err := config.LoadWorkerFrom(lookup(workerEnv()))

	require.NoError(t, err)
	assert.Equal(t, 10, cfg.Queue.Concurrency)
	assert.Equal(t, 30*time.Second, cfg.Queue.ShutdownTimeout)
}

func TestLoadWorkerNeedsAWorkSource(t *testing.T) {
	env := workerEnv()
	env["REDIS_ENABLED"] = "false"

	_, err := config.LoadWorkerFrom(lookup(env))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "al menos una fuente de trabajo")
}

func TestLoadWorkerValidatesSQS(t *testing.T) {
	env := workerEnv()
	env["SQS_ENABLED"] = "true"
	env["SQS_QUEUE_URL"] = "http://sqs.local/q"
	env["SQS_WAIT_TIME"] = "30s"
	env["SQS_MAX_MESSAGES"] = "50"

	_, err := config.LoadWorkerFrom(lookup(env))

	require.Error(t, err)
	for _, key := range []string{"SQS_QUEUE_URL", "SQS_WAIT_TIME", "SQS_MAX_MESSAGES", "AWS_REGION"} {
		assert.Contains(t, err.Error(), key)
	}
}

func TestDrainDelayDefaultsByEnvironment(t *testing.T) {
	cfg, err := config.LoadFrom(lookup(validEnv()))
	require.NoError(t, err)
	assert.Zero(t, cfg.HTTP.DrainDelay, "en develop Ctrl+C es inmediato")

	env := validEnv()
	env["APP_ENV"], env["DB_SSLMODE"], env["HTTP_TRUSTED_PROXIES"] = "qa", "require", "none"
	cfg, err = config.LoadFrom(lookup(env))
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, cfg.HTTP.DrainDelay)

	env["HTTP_DRAIN_DELAY"] = "2m"
	_, err = config.LoadFrom(lookup(env))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP_DRAIN_DELAY")
}

func TestDBSessionTimeouts(t *testing.T) {
	cfg, err := config.LoadFrom(lookup(validEnv()))
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, cfg.DB.StatementTimeout)
	assert.Equal(t, 10*time.Second, cfg.DB.LockTimeout)
	assert.Equal(t, 60*time.Second, cfg.DB.IdleInTxTimeout)

	app, mig := cfg.DB.DSN(), cfg.DB.MigrationDSN()
	assert.Contains(t, app, "statement_timeout=30000")
	assert.Contains(t, app, "lock_timeout=10000")
	assert.Contains(t, app, "idle_in_transaction_session_timeout=60000")
	assert.NotContains(t, mig, "statement_timeout=", "una migración larga no se corta")
	assert.Contains(t, mig, "lock_timeout=10000", "una migración no bloquea el tráfico esperando un lock")

	env := validEnv()
	env["DB_STATEMENT_TIMEOUT"], env["DB_LOCK_TIMEOUT"] = "0", "-1s"
	_, err = config.LoadFrom(lookup(env))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB_LOCK_TIMEOUT")
	assert.NotContains(t, err.Error(), "DB_STATEMENT_TIMEOUT", "0 desactiva el límite")
}

func TestOTelConfig(t *testing.T) {
	cfg, err := config.LoadFrom(lookup(validEnv()))
	require.NoError(t, err)
	assert.False(t, cfg.OTel.Enabled)
	assert.Equal(t, "myapp-api", cfg.OTel.ServiceName, "el nombre se lee aunque esté desactivado")

	env := validEnv()
	env["OTEL_ENABLED"] = "true"
	cfg, err = config.LoadFrom(lookup(env))
	require.NoError(t, err)
	assert.True(t, cfg.OTel.Metrics, "las métricas se exportan por defecto")

	env["OTEL_METRICS_EXPORTER"] = "none"
	env["OTEL_SERVICE_NAME"] = "orders-api"
	cfg, err = config.LoadFrom(lookup(env))
	require.NoError(t, err)
	assert.False(t, cfg.OTel.Metrics)
	assert.Equal(t, "orders-api", cfg.OTel.ServiceName)

	env["OTEL_EXPORTER_OTLP_ENDPOINT"] = "localhost:4318"
	_, err = config.LoadFrom(lookup(env))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OTEL_EXPORTER_OTLP_ENDPOINT")

	w, err := config.LoadWorkerFrom(lookup(workerEnv()))
	require.NoError(t, err)
	assert.Equal(t, "myapp-worker", w.OTel.ServiceName)
}

func TestTrustedProxies(t *testing.T) {
	env := validEnv()
	env["APP_ENV"], env["DB_SSLMODE"] = "production", "require"
	_, err := config.LoadFrom(lookup(env))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP_TRUSTED_PROXIES", "fuera de develop/test es obligatoria")

	for value, want := range map[string][]string{
		"none":                  nil,
		"10.0.0.0/16, 10.1.2.3": {"10.0.0.0/16", "10.1.2.3"},
		"2600:1f18::/32":        {"2600:1f18::/32"},
	} {
		env["HTTP_TRUSTED_PROXIES"] = value
		cfg, err := config.LoadFrom(lookup(env))
		require.NoError(t, err, value)
		assert.Equal(t, want, cfg.HTTP.TrustedProxies, value)
	}
	for _, bad := range []string{"0.0.0.0/0", "::/0", "10.0.0.0/16,abc"} {
		env["HTTP_TRUSTED_PROXIES"] = bad
		_, err := config.LoadFrom(lookup(env))
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "HTTP_TRUSTED_PROXIES", bad)
	}
}

func TestLoadRejectsValuesThatWouldBreakAtRuntime(t *testing.T) {
	tests := map[string]struct{ key, value string }{
		"rate limit NaN":             {"HTTP_RATE_LIMIT_RPS", "NaN"},
		"rate limit infinito":        {"HTTP_RATE_LIMIT_RPS", "Inf"},
		"statement_timeout < 1ms":    {"DB_STATEMENT_TIMEOUT", "500us"},
		"lock_timeout < 1ms":         {"DB_LOCK_TIMEOUT", "1ns"},
		"apagado menor que el plazo": {"HTTP_SHUTDOWN_TIMEOUT", "10s"},
		"origen con barra final":     {"HTTP_CORS_ALLOWED_ORIGINS", "https://app.example.com/"},
		"origen sin esquema":         {"HTTP_CORS_ALLOWED_ORIGINS", "app.example.com"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			env := validEnv()
			env[tt.key] = tt.value
			_, err := config.LoadFrom(lookup(env))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.key)
		})
	}
}

func TestLoadWorkerRejectsSQSDurationsSQSCannotHonor(t *testing.T) {
	for key, value := range map[string]string{
		"SQS_WAIT_TIME":          "500ms",
		"SQS_VISIBILITY_TIMEOUT": "13h",
	} {
		env := workerEnv()
		env["SQS_ENABLED"], env["SQS_QUEUE_URL"], env["AWS_REGION"] = "true", "https://sqs.us-east-1.amazonaws.com/1/q", "us-east-1"
		env[key] = value
		_, err := config.LoadWorkerFrom(lookup(env))
		require.Error(t, err, key)
		assert.Contains(t, err.Error(), key)
	}
	env := workerEnv()
	env["SQS_VISIBILITY_TIMEOUT"] = "1500ms"
	env["SQS_ENABLED"], env["SQS_QUEUE_URL"], env["AWS_REGION"] = "true", "https://sqs.us-east-1.amazonaws.com/1/q", "us-east-1"
	_, err := config.LoadWorkerFrom(lookup(env))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SQS_VISIBILITY_TIMEOUT", "SQS no admite fracciones de segundo")
}
