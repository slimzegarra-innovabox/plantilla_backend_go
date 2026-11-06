// Package security verifica los tokens de acceso. La plantilla no emite tokens:
// los emite un servicio de identidad externo (o el módulo de login del proyecto).
package security

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	shareddto "myapp.v1.0.0/internal/shared/application/dto"
	"myapp.v1.0.0/internal/shared/infrastructure/config"
)

// ErrInvalidToken es el único error que devuelve Verify. El motivo concreto se
// envuelve para el log, pero nunca se expone al cliente.
var ErrInvalidToken = errors.New("token inválido")

// Claims es el contenido esperado del token.
//
//   - sub:         uuid del usuario (obligatorio)
//   - tenant_uuid: uuid de la empresa (opcional)
//   - scope:       scopes separados por espacios, como en OAuth 2.0 (RFC 8693 §4.2)
type Claims struct {
	jwt.RegisteredClaims
	TenantUUID string `json:"tenant_uuid,omitempty"`
	Scope      string `json:"scope,omitempty"`
}

// JWTVerifier verifica tokens HS256 de forma estricta: algoritmo fijado, exp
// obligatorio, iss y aud verificados y desfase de reloj acotado.
type JWTVerifier struct {
	secret []byte
	parser *jwt.Parser
}

// NewJWTVerifier crea el verificador a partir de la configuración ya validada.
func NewJWTVerifier(cfg config.JWTConfig) *JWTVerifier {
	return &JWTVerifier{
		secret: []byte(cfg.Secret.Reveal()),
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
			jwt.WithIssuer(cfg.Issuer),
			jwt.WithAudience(cfg.Audience),
			jwt.WithLeeway(cfg.Leeway),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
		),
	}
}

// Verify valida el token y devuelve la identidad. Nunca devuelve un Principal
// parcial: o el token es válido por completo, o hay error.
func (v *JWTVerifier) Verify(token string) (shareddto.Principal, error) {
	var claims Claims
	parsed, err := v.parser.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) {
		return v.secret, nil
	})
	if err != nil {
		return shareddto.Principal{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if !parsed.Valid {
		return shareddto.Principal{}, ErrInvalidToken
	}

	userUUID, err := uuid.Parse(claims.Subject)
	if err != nil || userUUID == uuid.Nil {
		return shareddto.Principal{}, fmt.Errorf("%w: sub no es un uuid válido", ErrInvalidToken)
	}

	tenantUUID := uuid.Nil
	if claims.TenantUUID != "" {
		if tenantUUID, err = uuid.Parse(claims.TenantUUID); err != nil {
			return shareddto.Principal{}, fmt.Errorf("%w: tenant_uuid no es un uuid válido", ErrInvalidToken)
		}
	}

	return shareddto.Principal{
		UserUUID:   userUUID,
		TenantUUID: tenantUUID,
		Scopes:     strings.Fields(claims.Scope),
	}, nil
}

// SignForTesting firma un token con las mismas reglas que espera Verify.
// Solo para pruebas y para generar tokens en desarrollo local; la plantilla no
// expone ningún endpoint que emita tokens.
func SignForTesting(cfg config.JWTConfig, sub uuid.UUID, scopes []string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   sub.String(),
			Issuer:    cfg.Issuer,
			Audience:  jwt.ClaimStrings{cfg.Audience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
		Scope: strings.Join(scopes, " "),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(cfg.Secret.Reveal()))
}
