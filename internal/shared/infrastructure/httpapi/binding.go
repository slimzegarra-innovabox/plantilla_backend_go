package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"

	shareddto "myapp.v1.0.0/internal/shared/application/dto"
	"myapp.v1.0.0/internal/shared/application/result"
)

const (
	codeInvalidBody      = "INVALID_REQUEST_BODY"
	codeFieldInvalid     = "REQUEST_FIELD_INVALID"
	codeInvalidParameter = "INVALID_REQUEST_PARAMETER"
	codePayloadTooLarge  = "PAYLOAD_TOO_LARGE"
)

const (
	searchMinLength = 3
	searchMaxLength = 100
)

const invalidTextMessage = "El texto contiene caracteres no válidos."

// validText dice si PostgreSQL puede guardar el texto: UTF-8 válido y sin el carácter
// NUL. Si no, responde 22021 y el cliente recibiría un 500 con cualquier texto a medida.
func validText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// hasInvalidText recorre lo que se decodificó de la petición en busca de un texto no
// válido: las reglas de binding no lo comprueban, y el error llegaría hasta la base.
func hasInvalidText(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return !validText(v.String())
	case reflect.Pointer, reflect.Interface:
		return !v.IsNil() && hasInvalidText(v.Elem())
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() && hasInvalidText(v.Field(i)) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 { // bytes y uuid.UUID: no son texto
			return false
		}
		for i := range v.Len() {
			if hasInvalidText(v.Index(i)) {
				return true
			}
		}
	case reflect.Map:
		for iter := v.MapRange(); iter.Next(); {
			if hasInvalidText(iter.Key()) || hasInvalidText(iter.Value()) {
				return true
			}
		}
	}
	return false
}

// BindJSON decodifica y valida el cuerpo en dst. Si falla, ya respondió (400 o 413)
// y devuelve false: el handler solo hace return.
func BindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		writeBindError(c, err, codeInvalidBody, "El cuerpo de la petición no es un JSON válido.")
		return false
	}
	if hasInvalidText(reflect.ValueOf(dst)) {
		writeNotification(c, http.StatusBadRequest, result.Error(result.Validation, codeInvalidBody, invalidTextMessage))
		return false
	}
	return true
}

// BindQuery decodifica y valida los query params en dst (tags form:"…" en camelCase).
func BindQuery(c *gin.Context, dst any) bool {
	if err := c.ShouldBindQuery(dst); err != nil {
		writeBindError(c, err, codeInvalidParameter, "Los parámetros de la consulta no son válidos.")
		return false
	}
	if hasInvalidText(reflect.ValueOf(dst)) {
		writeNotification(c, http.StatusBadRequest, result.Error(result.Validation, codeInvalidParameter, invalidTextMessage))
		return false
	}
	return true
}

// UUIDParam lee un path param que debe ser un uuid distinto de cero.
func UUIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil || id == uuid.Nil {
		writeNotification(c, http.StatusBadRequest, result.Error(result.Validation, codeInvalidParameter,
			"El identificador no es un UUID válido.").WithField(name))
		return uuid.Nil, false
	}
	return id, true
}

// PageQuery lee page (desde 1) y size (1-100) de la query string.
// Un page ausente o < 1 se toma como 1; un size fuera de rango, o un page que supera
// el desplazamiento máximo (MaxOffset), es un error de validación.
func PageQuery(c *gin.Context) (shareddto.PageQuery, bool) {
	page, okPage := intQuery(c, "page", 1)
	size, okSize := intQuery(c, "size", shareddto.DefaultPageSize)
	if !okPage || !okSize {
		return shareddto.PageQuery{}, false
	}
	if size < 1 || size > shareddto.MaxPageSize {
		writeNotification(c, http.StatusBadRequest, result.Error(result.Validation, codeInvalidParameter,
			fmt.Sprintf("size debe estar entre 1 y %d.", shareddto.MaxPageSize)).WithField("size"))
		return shareddto.PageQuery{}, false
	}
	if page > shareddto.MaxPage(size) {
		writeNotification(c, http.StatusBadRequest, result.Error(result.Validation, codeInvalidParameter,
			fmt.Sprintf("page no puede superar %d con size=%d; para recorrer más resultados, use filtros.",
				shareddto.MaxPage(size), size)).WithField("page"))
		return shareddto.PageQuery{}, false
	}
	return shareddto.NewPageQuery(page, size), true
}

// SortQuery lee el query param sort ("name,-createdAt") validando cada campo contra
// allowed (los campos lógicos que el módulo permite ordenar). Ausente devuelve def.
// Un campo no permitido es 400 INVALID_REQUEST_PARAMETER con field "sort".
func SortQuery(c *gin.Context, allowed []string, def shareddto.Sort) (shareddto.Sort, bool) {
	sort, err := shareddto.ParseSort(c.Query("sort"), allowed, def)
	if err != nil {
		writeNotification(c, http.StatusBadRequest, result.Error(result.Validation, codeInvalidParameter,
			fmt.Sprintf("sort inválido. Campos permitidos: %s (prefijo - para descendente).", strings.Join(allowed, ", "))).
			WithField("sort"))
		return nil, false
	}
	return sort, true
}

// SearchQuery lee el query param q de una búsqueda de texto: sin espacios en los extremos
// y de 3 a 100 caracteres. Ausente devuelve "". El mínimo se valida después de recortar:
// con menos de 3 caracteres, el índice de trigramas no sirve y se recorrería la tabla.
func SearchQuery(c *gin.Context) (string, bool) {
	raw := c.Query("q")
	if raw == "" {
		return "", true
	}
	q := strings.TrimSpace(raw)
	if !validText(q) {
		writeNotification(c, http.StatusBadRequest, result.Error(result.Validation, codeInvalidParameter, invalidTextMessage).WithField("q"))
		return "", false
	}
	if n := utf8.RuneCountInString(q); n < searchMinLength || n > searchMaxLength {
		writeNotification(c, http.StatusBadRequest, result.Error(result.Validation, codeFieldInvalid,
			fmt.Sprintf("Debe tener entre %d y %d caracteres.", searchMinLength, searchMaxLength)).WithField("q"))
		return "", false
	}
	return q, true
}

func intQuery(c *gin.Context, name string, def int) (int, bool) {
	raw, ok := c.GetQuery(name)
	if !ok || raw == "" {
		return def, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		writeNotification(c, http.StatusBadRequest, result.Error(result.Validation, codeInvalidParameter,
			name+" debe ser un número entero.").WithField(name))
		return 0, false
	}
	return n, true
}

func writeBindError(c *gin.Context, err error, malformedCode, malformedMsg string) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeNotification(c, http.StatusRequestEntityTooLarge, result.Error(result.Validation, codePayloadTooLarge,
			"El cuerpo de la petición supera el tamaño permitido."))
		return
	}

	var verrs validator.ValidationErrors
	if errors.As(err, &verrs) {
		notes := make([]result.Notification, 0, len(verrs))
		for _, fe := range verrs {
			notes = append(notes, result.Error(result.Validation, codeFieldInvalid, fieldMessage(fe)).WithField(fe.Field()))
		}
		WriteFailure(c, notes)
		return
	}

	// JSON mal formado, tipos incorrectos, cuerpo vacío: sin eco del contenido.
	writeNotification(c, http.StatusBadRequest, result.Error(result.Validation, malformedCode, malformedMsg))
}

// fieldMessage traduce las reglas de validación más comunes a un mensaje en español.
func fieldMessage(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "Es obligatorio."
	case "max":
		return "Debe tener como máximo " + fe.Param() + " caracteres."
	case "min":
		return "Debe tener al menos " + fe.Param() + " caracteres."
	case "len":
		return "Debe tener exactamente " + fe.Param() + " caracteres."
	case "oneof":
		return "Debe ser uno de: " + strings.ReplaceAll(fe.Param(), " ", ", ") + "."
	case "email":
		return "Debe ser un correo electrónico válido."
	case "uuid", "nonzerouuid":
		return "Debe ser un UUID válido."
	case "gte":
		return "Debe ser mayor o igual que " + fe.Param() + "."
	case "lte":
		return "Debe ser menor o igual que " + fe.Param() + "."
	default:
		return "El valor no es válido."
	}
}

var registerValidatorsOnce sync.Once

// registerValidators hace que los errores de validación usen el nombre público del
// campo (tag json o form) y agrega el validador nonzerouuid.
func registerValidators() {
	registerValidatorsOnce.Do(func() {
		v, ok := binding.Validator.Engine().(*validator.Validate)
		if !ok {
			return
		}
		v.RegisterTagNameFunc(func(f reflect.StructField) string {
			for _, tag := range []string{"json", "form", "uri"} {
				name := strings.SplitN(f.Tag.Get(tag), ",", 2)[0]
				if name != "" && name != "-" {
					return name
				}
			}
			return f.Name
		})
		// uuid.UUID acepta el uuid cero al decodificar JSON; esta regla lo rechaza.
		_ = v.RegisterValidation("nonzerouuid", func(fl validator.FieldLevel) bool {
			id, ok := fl.Field().Interface().(uuid.UUID)
			return ok && id != uuid.Nil
		})
	})
}
