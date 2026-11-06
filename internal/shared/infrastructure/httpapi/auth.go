package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	shareddto "myapp.v1.0.0/internal/shared/application/dto"
	"myapp.v1.0.0/internal/shared/application/result"
	"myapp.v1.0.0/internal/shared/infrastructure/logging"
)

// TokenVerifier valida un token de acceso. Lo implementa security.JWTVerifier.
type TokenVerifier interface {
	Verify(token string) (shareddto.Principal, error)
}

const principalKey = "shared.principal"

var (
	notifTokenInvalid = result.Error(result.Unauthorized, "AUTH_TOKEN_INVALID",
		"La sesión no es válida o expiró. Inicie sesión nuevamente.")
	notifForbidden = result.Error(result.Forbidden, "AUTH_FORBIDDEN",
		"No tiene permiso para realizar esta acción.")
)

// Authenticator protege rutas con un token Bearer.
type Authenticator struct {
	verifier TokenVerifier
}

// NewAuthenticator crea el autenticador.
func NewAuthenticator(v TokenVerifier) *Authenticator { return &Authenticator{verifier: v} }

// Required exige un token válido y deja el Principal en el contexto de gin.
// La respuesta no dice POR QUÉ falló el token; el motivo va al log en nivel DEBUG.
func (a *Authenticator) Required() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			writeNotification(c, http.StatusUnauthorized, notifTokenInvalid)
			return
		}
		p, err := a.verifier.Verify(token)
		if err != nil {
			ctx := c.Request.Context()
			logging.FromContext(ctx).DebugContext(ctx, "token rechazado", "error", err)
			writeNotification(c, http.StatusUnauthorized, notifTokenInvalid)
			return
		}
		c.Set(principalKey, p)
		c.Request = c.Request.WithContext(logging.WithAttrs(c.Request.Context(), "user_uuid", p.UserUUID.String()))
		c.Next()
	}
}

// RequireScopes exige que el Principal tenga TODOS los scopes indicados.
// Va después de Required en la cadena de la ruta.
func RequireScopes(scopes ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := PrincipalFrom(c)
		if !ok {
			writeNotification(c, http.StatusUnauthorized, notifTokenInvalid)
			return
		}
		for _, s := range scopes {
			if !p.HasScope(s) {
				writeNotification(c, http.StatusForbidden, notifForbidden)
				return
			}
		}
		c.Next()
	}
}

// PrincipalFrom devuelve la identidad autenticada, si la ruta pasó por Required.
func PrincipalFrom(c *gin.Context) (shareddto.Principal, bool) {
	v, ok := c.Get(principalKey)
	if !ok {
		return shareddto.Principal{}, false
	}
	p, ok := v.(shareddto.Principal)
	return p, ok
}

// MustPrincipal devuelve la identidad autenticada. Si la ruta no pasó por Required
// es un error de programación: hace panic, y Recovery responde 500.
func MustPrincipal(c *gin.Context) shareddto.Principal {
	p, ok := PrincipalFrom(c)
	if !ok {
		panic("httpapi.MustPrincipal: la ruta no está protegida con Authenticator.Required")
	}
	return p
}

func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
