package security_test

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
	"myapp.v1.0.0/internal/shared/infrastructure/security"
)

var cfg = config.JWTConfig{
	Secret:   "0123456789abcdef0123456789abcdef",
	Issuer:   "https://auth.example.com",
	Audience: "myapp-api",
	Leeway:   30 * time.Second,
}

func sign(t *testing.T, method jwt.SigningMethod, key any, claims security.Claims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, claims).SignedString(key)
	require.NoError(t, err)
	return s
}

func validClaims(sub uuid.UUID) security.Claims {
	now := time.Now()
	return security.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   sub.String(),
			Issuer:    cfg.Issuer,
			Audience:  jwt.ClaimStrings{cfg.Audience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
		TenantUUID: uuid.NewString(),
		Scope:      "example:read example:write",
	}
}

func TestVerifyValidToken(t *testing.T) {
	sub := uuid.New()
	token, err := security.SignForTesting(cfg, sub, []string{"example:read"}, time.Hour)
	require.NoError(t, err)

	p, err := security.NewJWTVerifier(cfg).Verify(token)

	require.NoError(t, err)
	assert.Equal(t, sub, p.UserUUID)
	assert.Equal(t, uuid.Nil, p.TenantUUID)
	assert.Equal(t, []string{"example:read"}, p.Scopes)
}

func TestVerifyParsesTenantAndScopes(t *testing.T) {
	c := validClaims(uuid.New())
	token := sign(t, jwt.SigningMethodHS256, []byte(cfg.Secret.Reveal()), c)

	p, err := security.NewJWTVerifier(cfg).Verify(token)

	require.NoError(t, err)
	assert.Equal(t, c.TenantUUID, p.TenantUUID.String())
	assert.Equal(t, []string{"example:read", "example:write"}, p.Scopes)
}

func TestVerifyRejectsInvalidTokens(t *testing.T) {
	key := []byte(cfg.Secret.Reveal())
	sub := uuid.New()

	mutate := func(f func(*security.Claims)) security.Claims {
		c := validClaims(sub)
		f(&c)
		return c
	}

	tests := map[string]string{
		"firma con otra clave": sign(t, jwt.SigningMethodHS256, []byte("otra-clave-otra-clave-otra-clave"), validClaims(sub)),
		"algoritmo none":       sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, validClaims(sub)),
		"algoritmo HS512":      sign(t, jwt.SigningMethodHS512, key, validClaims(sub)),
		"vencido": sign(t, jwt.SigningMethodHS256, key, mutate(func(c *security.Claims) {
			c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
		})),
		"sin exp":     sign(t, jwt.SigningMethodHS256, key, mutate(func(c *security.Claims) { c.ExpiresAt = nil })),
		"otro emisor": sign(t, jwt.SigningMethodHS256, key, mutate(func(c *security.Claims) { c.Issuer = "https://evil.example.com" })),
		"otra audiencia": sign(t, jwt.SigningMethodHS256, key, mutate(func(c *security.Claims) {
			c.Audience = jwt.ClaimStrings{"other-api"}
		})),
		"sub no uuid":    sign(t, jwt.SigningMethodHS256, key, mutate(func(c *security.Claims) { c.Subject = "admin" })),
		"sub vacío":      sign(t, jwt.SigningMethodHS256, key, mutate(func(c *security.Claims) { c.Subject = "" })),
		"tenant no uuid": sign(t, jwt.SigningMethodHS256, key, mutate(func(c *security.Claims) { c.TenantUUID = "acme" })),
		"basura":         "no.es.un-token",
		"cadena vacía":   "",
	}
	v := security.NewJWTVerifier(cfg)
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			p, err := v.Verify(token)
			require.ErrorIs(t, err, security.ErrInvalidToken)
			assert.Equal(t, uuid.Nil, p.UserUUID, "nunca devuelve un Principal parcial")
		})
	}
}

func TestVerifyAcceptsClockSkewWithinLeeway(t *testing.T) {
	c := validClaims(uuid.New())
	c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-10 * time.Second))
	token := sign(t, jwt.SigningMethodHS256, []byte(cfg.Secret.Reveal()), c)

	_, err := security.NewJWTVerifier(cfg).Verify(token)

	assert.NoError(t, err)
}
