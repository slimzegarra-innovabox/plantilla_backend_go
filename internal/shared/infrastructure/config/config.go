// Package config carga y valida la configuración del proceso desde variables de entorno.
//
// Reglas:
//   - Se valida TODO al arrancar; si algo falla, Load devuelve un error con la lista
//     completa de problemas y el proceso no arranca.
//   - No hay sufijos por entorno (_D/_T/_Q/_P): cada entorno define sus variables.
//   - Los valores sensibles usan el tipo Secret, que no se imprime en logs.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Entornos válidos de APP_ENV.
const (
	EnvDevelop    = "develop"
	EnvTest       = "test"
	EnvQA         = "qa"
	EnvProduction = "production"
)

// Config es la configuración completa del servidor HTTP (cmd/api).
type Config struct {
	Env  string
	Log  LogConfig
	HTTP HTTPConfig
	DB   DBConfig
	JWT  JWTConfig

	// Opcionales: con Enabled == false el resto de sus campos está vacío.
	OTel  OTelConfig
	Redis RedisConfig
	AWS   AWSConfig
	S3    S3Config
	Brevo BrevoConfig
}

// WorkerConfig es la configuración del proceso en segundo plano (cmd/worker).
// No exige HTTP ni JWT; procesa tareas de Asynq (Redis), mensajes de SQS o ambos.
type WorkerConfig struct {
	Env   string
	Log   LogConfig
	DB    DBConfig
	Queue QueueConfig
	OTel  OTelConfig
	Redis RedisConfig
	AWS   AWSConfig
	S3    S3Config
	Brevo BrevoConfig
	SQS   SQSConfig
}

// IsDevelop indica si el worker corre en desarrollo local.
func (c WorkerConfig) IsDevelop() bool { return c.Env == EnvDevelop }

// LogConfig configura slog.
type LogConfig struct {
	Level slog.Level
}

// HTTPConfig configura el servidor y sus protecciones.
type HTTPConfig struct {
	Addr            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	// DrainDelay es la espera entre SIGTERM y el cierre del listener: /health/ready ya
	// responde 503 y el balanceador deja de enviar tráfico antes de que se cierre el puerto.
	DrainDelay time.Duration
	// RequestTimeout es el plazo del context de cada petición; debe ser menor que
	// WriteTimeout para que el cliente reciba el 503 en lugar de un corte de conexión.
	RequestTimeout time.Duration
	BodyLimitBytes int64
	TrustedProxies []string
	CORSOrigins    []string
	// RateLimitRPS == 0 desactiva el rate limit.
	RateLimitRPS   float64
	RateLimitBurst int
}

// DBConfig configura PostgreSQL.
type DBConfig struct {
	Host            string
	Port            int
	User            string
	Password        Secret
	Name            string
	SSLMode         string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration

	// Plazos que aplica el propio PostgreSQL a cada sesión (0 = sin límite). Protegen el
	// pool aunque un context no se cancele (worker, consultas sin plazo).
	StatementTimeout time.Duration // statement_timeout: una sentencia que tarda más se aborta
	LockTimeout      time.Duration // lock_timeout: no se espera un lock indefinidamente
	IdleInTxTimeout  time.Duration // idle_in_transaction_session_timeout: transacciones abandonadas
}

// DSN devuelve la cadena de conexión de la aplicación, con los plazos de sesión.
// NUNCA se registra en logs.
func (c DBConfig) DSN() string { return c.dsn(c.StatementTimeout) }

// MigrationDSN es la conexión de cmd/migrate: sin statement_timeout, porque una
// migración (crear un índice, reescribir una tabla) puede tardar legítimamente, pero
// con lock_timeout, para que no bloquee el tráfico mientras espera un lock.
func (c DBConfig) MigrationDSN() string { return c.dsn(0) }

func (c DBConfig) dsn(statementTimeout time.Duration) string {
	q := url.Values{"sslmode": {c.SSLMode}}
	// pgx envía los parámetros desconocidos como parámetros de sesión de PostgreSQL.
	for key, d := range map[string]time.Duration{
		"statement_timeout":                   statementTimeout,
		"lock_timeout":                        c.LockTimeout,
		"idle_in_transaction_session_timeout": c.IdleInTxTimeout,
	} {
		if d > 0 {
			q.Set(key, strconv.FormatInt(d.Milliseconds(), 10))
		}
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.User, c.Password.Reveal()),
		Host:     fmt.Sprintf("%s:%d", c.Host, c.Port),
		Path:     "/" + c.Name,
		RawQuery: q.Encode(),
	}
	return u.String()
}

// JWTConfig configura la verificación de tokens.
type JWTConfig struct {
	Secret   Secret
	Issuer   string
	Audience string
	Leeway   time.Duration
}

// IsDevelop indica si el proceso corre en desarrollo local.
func (c Config) IsDevelop() bool { return c.Env == EnvDevelop }

// LoadDotEnv carga .env solo en desarrollo (APP_ENV vacío o "develop").
// En QA/producción las variables vienen del entorno, nunca de un archivo.
func LoadDotEnv() error {
	env := strings.TrimSpace(os.Getenv("APP_ENV"))
	if env != "" && env != EnvDevelop {
		return nil
	}
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("leer .env: %w", err)
	}
	return nil
}

// Load lee la configuración desde el entorno del proceso.
func Load() (Config, error) { return LoadFrom(os.LookupEnv) }

// LookupFunc abstrae os.LookupEnv para poder probar la carga.
type LookupFunc func(key string) (string, bool)

// LoadFrom lee y valida la configuración con la función de búsqueda indicada.
func LoadFrom(lookup LookupFunc) (Config, error) {
	r := reader{lookup: lookup}

	env := readEnv(&r)
	cfg := Config{
		Env: env,
		Log: LogConfig{Level: r.logLevel("LOG_LEVEL", slog.LevelInfo)},
		HTTP: HTTPConfig{
			Addr:            r.str("HTTP_ADDR", ":8080"),
			ReadTimeout:     r.duration("HTTP_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:    r.duration("HTTP_WRITE_TIMEOUT", 30*time.Second),
			IdleTimeout:     r.duration("HTTP_IDLE_TIMEOUT", 60*time.Second),
			ShutdownTimeout: r.duration("HTTP_SHUTDOWN_TIMEOUT", 30*time.Second),
			DrainDelay:      r.durationOrZero("HTTP_DRAIN_DELAY", defaultDrainDelay(env)),
			RequestTimeout:  r.duration("HTTP_REQUEST_TIMEOUT", 25*time.Second),
			BodyLimitBytes:  int64(r.intMin("HTTP_BODY_LIMIT_BYTES", 1<<20, 1)),
			TrustedProxies:  readTrustedProxies(&r, env),
			CORSOrigins:     r.list("HTTP_CORS_ALLOWED_ORIGINS"),
			RateLimitRPS:    r.floatMin("HTTP_RATE_LIMIT_RPS", 20, 0),
			RateLimitBurst:  r.intMin("HTTP_RATE_LIMIT_BURST", 40, 1),
		},
		DB: readDB(&r),
		JWT: JWTConfig{
			Secret:   Secret(r.required("JWT_SECRET")),
			Issuer:   r.required("JWT_ISSUER"),
			Audience: r.required("JWT_AUDIENCE"),
			Leeway:   r.duration("JWT_LEEWAY", 30*time.Second),
		},
	}

	cfg.OTel = readOTel(&r, "myapp-api")
	cfg.Redis = readRedis(&r, cfg.Env)
	cfg.S3 = readS3(&r)
	cfg.Brevo = readBrevo(&r, cfg.Env)
	cfg.AWS = readAWS(&r, cfg.Env, cfg.S3.Enabled)

	checkPlaceholder(&r, cfg.Env, "JWT_SECRET", cfg.JWT.Secret)
	if s := cfg.JWT.Secret.Reveal(); s != "" && len(s) < 32 {
		r.fail("JWT_SECRET", "debe tener al menos 32 bytes")
	}
	if cfg.HTTP.RequestTimeout >= cfg.HTTP.WriteTimeout {
		r.fail("HTTP_REQUEST_TIMEOUT", "debe ser menor que HTTP_WRITE_TIMEOUT")
	}
	if cfg.HTTP.ShutdownTimeout < cfg.HTTP.RequestTimeout {
		r.fail("HTTP_SHUTDOWN_TIMEOUT", "debe ser mayor o igual que HTTP_REQUEST_TIMEOUT: si no, el apagado corta peticiones en curso")
	}
	if cfg.HTTP.DrainDelay > time.Minute {
		r.fail("HTTP_DRAIN_DELAY", "no puede superar 1m")
	}
	if cfg.JWT.Leeway > time.Minute {
		r.fail("JWT_LEEWAY", "no puede superar 1m")
	}
	for _, o := range cfg.HTTP.CORSOrigins {
		if o == "*" {
			r.fail("HTTP_CORS_ALLOWED_ORIGINS", "no admite '*'; liste los orígenes explícitamente")
		} else if !validOrigin(o) {
			r.fail("HTTP_CORS_ALLOWED_ORIGINS", fmt.Sprintf("%q no es un origen: use scheme://host[:puerto], sin ruta ni '/' final", o))
		}
	}
	checkDB(&r, cfg.Env, cfg.DB)

	if err := r.err(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// LoadWorker lee la configuración de cmd/worker.
func LoadWorker() (WorkerConfig, error) { return LoadWorkerFrom(os.LookupEnv) }

// LoadWorkerFrom es LoadWorker con una función de búsqueda inyectable.
func LoadWorkerFrom(lookup LookupFunc) (WorkerConfig, error) {
	r := reader{lookup: lookup}
	cfg := WorkerConfig{
		Env: readEnv(&r),
		Log: LogConfig{Level: r.logLevel("LOG_LEVEL", slog.LevelInfo)},
		DB:  readDB(&r),
	}
	cfg.Queue = readQueue(&r)
	cfg.OTel = readOTel(&r, "myapp-worker")
	cfg.Redis = readRedis(&r, cfg.Env)
	cfg.S3 = readS3(&r)
	cfg.Brevo = readBrevo(&r, cfg.Env)
	cfg.SQS = readSQS(&r)
	cfg.AWS = readAWS(&r, cfg.Env, cfg.S3.Enabled || cfg.SQS.Enabled)

	checkDB(&r, cfg.Env, cfg.DB)
	if !cfg.Redis.Enabled && !cfg.SQS.Enabled {
		r.fail("REDIS_ENABLED / SQS_ENABLED", "el worker necesita al menos una fuente de trabajo")
	}
	if err := r.err(); err != nil {
		return WorkerConfig{}, err
	}
	return cfg, nil
}

// LoadDatabase lee solo APP_ENV y DB_*: lo que necesita cmd/migrate, que no
// debe exigir la configuración HTTP ni JWT del servidor.
func LoadDatabase() (env string, db DBConfig, err error) { return LoadDatabaseFrom(os.LookupEnv) }

// LoadDatabaseFrom es LoadDatabase con una función de búsqueda inyectable.
func LoadDatabaseFrom(lookup LookupFunc) (env string, db DBConfig, err error) {
	r := reader{lookup: lookup}
	env = readEnv(&r)
	db = readDB(&r)
	checkDB(&r, env, db)
	if err := r.err(); err != nil {
		return "", DBConfig{}, err
	}
	return env, db, nil
}

// placeholderPrefix marca los valores de .env.template que hay que reemplazar.
const placeholderPrefix = "change-me"

// checkPlaceholder impide que un marcador de .env.template llegue a QA o producción.
func checkPlaceholder(r *reader, env, key string, s Secret) {
	if env != EnvDevelop && env != EnvTest && strings.HasPrefix(s.Reveal(), placeholderPrefix) {
		r.fail(key, "contiene el marcador de .env.template; configure el valor real")
	}
}

func readEnv(r *reader) string {
	return r.oneOf("APP_ENV", "", EnvDevelop, EnvTest, EnvQA, EnvProduction)
}

func readDB(r *reader) DBConfig {
	return DBConfig{
		Host:            r.required("DB_HOST"),
		Port:            r.intMin("DB_PORT", 5432, 1),
		User:            r.required("DB_USER"),
		Password:        Secret(r.required("DB_PASSWORD")),
		Name:            r.required("DB_NAME"),
		SSLMode:         r.oneOf("DB_SSLMODE", "require", "disable", "require", "verify-ca", "verify-full"),
		MaxOpenConns:    r.intMin("DB_MAX_OPEN_CONNS", 20, 1),
		MaxIdleConns:    r.intMin("DB_MAX_IDLE_CONNS", 5, 0),
		ConnMaxLifetime: r.duration("DB_CONN_MAX_LIFETIME", 30*time.Minute),

		StatementTimeout: r.durationOrZero("DB_STATEMENT_TIMEOUT", 30*time.Second),
		LockTimeout:      r.durationOrZero("DB_LOCK_TIMEOUT", 10*time.Second),
		IdleInTxTimeout:  r.durationOrZero("DB_IDLE_IN_TRANSACTION_TIMEOUT", 60*time.Second),
	}
}

// readTrustedProxies lee las IPs/CIDR de los proxies cuyo X-Forwarded-For se acepta.
// Fuera de develop/test es obligatoria: vacía, detrás de un balanceador todos los
// clientes comparten su IP (rate limit y logs). Sin proxy delante se declara "none".
func readTrustedProxies(r *reader, env string) []string {
	raw, ok := r.raw("HTTP_TRUSTED_PROXIES")
	if !ok {
		if !isLocal(env) {
			r.fail("HTTP_TRUSTED_PROXIES", "es obligatoria fuera de develop/test: las IPs/CIDR del balanceador (p. ej. la subred del ALB), o 'none' si no hay proxy delante")
		}
		return nil
	}
	if raw == "none" {
		return nil
	}
	list := r.list("HTTP_TRUSTED_PROXIES")
	for _, p := range list {
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			addr, aerr := netip.ParseAddr(p)
			if aerr != nil {
				r.fail("HTTP_TRUSTED_PROXIES", fmt.Sprintf("%q no es una IP ni un CIDR", p))
				continue
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		if prefix.Bits() == 0 {
			r.fail("HTTP_TRUSTED_PROXIES", fmt.Sprintf("%q confía en cualquier IP: cualquiera podría falsear su X-Forwarded-For", p))
		}
	}
	return list
}

// validOrigin acepta scheme://host[:puerto]: es lo que el navegador envía en Origin.
func validOrigin(o string) bool {
	u, err := url.Parse(o)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
		u.Path == "" && u.RawQuery == "" && u.Fragment == "" && u.User == nil
}

// defaultDrainDelay: en local no hay balanceador y Ctrl+C debe ser inmediato.
func defaultDrainDelay(env string) time.Duration {
	if isLocal(env) {
		return 0
	}
	return 5 * time.Second
}

func checkDB(r *reader, env string, db DBConfig) {
	checkPlaceholder(r, env, "DB_PASSWORD", db.Password)
	// PostgreSQL recibe milisegundos: un valor positivo menor de 1ms se enviaría como 0,
	// que significa «sin límite».
	for key, d := range map[string]time.Duration{
		"DB_STATEMENT_TIMEOUT":           db.StatementTimeout,
		"DB_LOCK_TIMEOUT":                db.LockTimeout,
		"DB_IDLE_IN_TRANSACTION_TIMEOUT": db.IdleInTxTimeout,
	} {
		if d > 0 && d < time.Millisecond {
			r.fail(key, "debe ser 0 (sin límite) o al menos 1ms")
		}
	}
	if env == EnvProduction && db.SSLMode == "disable" {
		r.fail("DB_SSLMODE", "no puede ser 'disable' en production")
	}
}

//--------------------------------------------------------------
// LECTOR: acumula todos los errores en lugar de cortar en el primero
//--------------------------------------------------------------

type reader struct {
	lookup LookupFunc
	errs   []error
}

func (r *reader) err() error {
	if err := errors.Join(r.errs...); err != nil {
		return fmt.Errorf("configuración inválida:\n%w", err)
	}
	return nil
}

func (r *reader) fail(key, msg string) {
	r.errs = append(r.errs, fmt.Errorf("  - %s: %s", key, msg))
}

func (r *reader) raw(key string) (string, bool) {
	v, ok := r.lookup(key)
	v = strings.TrimSpace(v)
	return v, ok && v != ""
}

func (r *reader) str(key, def string) string {
	if v, ok := r.raw(key); ok {
		return v
	}
	return def
}

func (r *reader) required(key string) string {
	v, ok := r.raw(key)
	if !ok {
		r.fail(key, "es obligatoria")
	}
	return v
}

// oneOf valida que el valor esté entre los permitidos. def == "" lo hace obligatorio.
func (r *reader) oneOf(key, def string, allowed ...string) string {
	v, ok := r.raw(key)
	if !ok {
		if def == "" {
			r.fail(key, "es obligatoria ("+strings.Join(allowed, " | ")+")")
		}
		return def
	}
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	r.fail(key, fmt.Sprintf("valor %q no válido (%s)", v, strings.Join(allowed, " | ")))
	return def
}

// boolean lee un interruptor; vacío es false.
func (r *reader) boolean(key string) bool { return r.booleanDefault(key, false) }

func (r *reader) booleanDefault(key string, def bool) bool {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.fail(key, "debe ser true o false")
		return def
	}
	return b
}

func (r *reader) intMin(key string, def, minVal int) int {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < minVal {
		r.fail(key, fmt.Sprintf("debe ser un entero >= %d", minVal))
		return def
	}
	return n
}

func (r *reader) floatMin(key string, def, minVal float64) float64 {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < minVal {
		r.fail(key, fmt.Sprintf("debe ser un número >= %g", minVal))
		return def
	}
	return n
}

func (r *reader) duration(key string, def time.Duration) time.Duration {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		r.fail(key, "debe ser una duración positiva (p. ej. 30s, 5m)")
		return def
	}
	return d
}

// durationOrZero admite 0 (desactivado) además de duraciones positivas.
func (r *reader) durationOrZero(key string, def time.Duration) time.Duration {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		r.fail(key, "debe ser una duración >= 0 (p. ej. 0, 5s, 1m)")
		return def
	}
	return d
}

func (r *reader) list(key string) []string {
	v, ok := r.raw(key)
	if !ok {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (r *reader) logLevel(key string, def slog.Level) slog.Level {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	var l slog.Level
	if err := l.UnmarshalText([]byte(v)); err != nil {
		r.fail(key, "debe ser debug | info | warn | error")
		return def
	}
	return l
}
