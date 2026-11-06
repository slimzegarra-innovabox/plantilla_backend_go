// Package brevo es el cliente técnico de correo transaccional con plantillas (Brevo).
//
// No implementa ningún puerto: el puerto (p. ej. NotificationSender) lo define el
// módulo que lo consume, y su adapter en <modulo>/infrastructure/external delega aquí.
//
// Seguridad (CLAUDE.md §10-11): la API key viaja solo en la cabecera; este paquete no
// registra nada, y los errores nunca incluyen el cuerpo de la petición ni el de la
// respuesta (pueden contener correos y datos personales): solo el estado HTTP y el
// código de error de Brevo.
package brevo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
)

// maxResponseBytes acota lo que se lee de una respuesta.
const maxResponseBytes = 64 << 10

// ErrInvalidRequest indica un correo que el cliente rechaza antes de llamar a Brevo.
var ErrInvalidRequest = errors.New("brevo: petición inválida")

// APIError es una respuesta no exitosa de Brevo.
type APIError struct {
	StatusCode int
	// Code es el código de error de Brevo (p. ej. "invalid_parameter"); puede venir vacío.
	Code string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("brevo: respuesta %d (%s)", e.StatusCode, e.Code)
}

// Rejected indica que Brevo rechazó el contenido (4xx salvo 401/403/429): reintentar
// el mismo correo no sirve. 401/403 (API key), 429 y 5xx son fallos de operación.
func (e *APIError) Rejected() bool {
	switch e.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return false
	}
	return e.StatusCode >= 400 && e.StatusCode < 500
}

// Recipient es un destinatario.
type Recipient struct {
	Email string
	Name  string
}

// TemplateEmail es un correo basado en una plantilla configurada en Brevo.
type TemplateEmail struct {
	To         []Recipient
	TemplateID int64
	Params     map[string]any
	Tags       []string
}

// Client envía correos con la API v3 de Brevo.
type Client struct {
	http    *http.Client
	baseURL string
	apiKey  config.Secret
	sender  Recipient
}

// New crea el cliente. El timeout de cfg acota cada envío completo.
func New(cfg config.BrevoConfig) *Client {
	return &Client{
		// otelhttp: span de cliente y propagación W3C. No registra cabeceras (la API key
		// viaja en una) ni cuerpos.
		http:    &http.Client{Timeout: cfg.Timeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
		baseURL: cfg.BaseURL,
		apiKey:  cfg.APIKey,
		sender:  Recipient{Email: cfg.SenderEmail, Name: cfg.SenderName},
	}
}

// Contrato JSON de Brevo (POST /smtp/email).
type (
	contact struct {
		Email string `json:"email"`
		Name  string `json:"name,omitempty"`
	}
	sendRequest struct {
		Sender     contact        `json:"sender"`
		To         []contact      `json:"to"`
		TemplateID int64          `json:"templateId"`
		Params     map[string]any `json:"params,omitempty"`
		Tags       []string       `json:"tags,omitempty"`
	}
	sendResponse struct {
		MessageID string `json:"messageId"`
	}
	errorResponse struct {
		Code string `json:"code"`
	}
)

// SendTemplate envía el correo y devuelve el messageId de Brevo.
// Devuelve *APIError si Brevo responde con error.
func (c *Client) SendTemplate(ctx context.Context, email TemplateEmail) (string, error) {
	if len(email.To) == 0 || email.TemplateID <= 0 {
		return "", fmt.Errorf("%w: faltan destinatarios o la plantilla", ErrInvalidRequest)
	}
	body := sendRequest{
		Sender:     contact(c.sender),
		TemplateID: email.TemplateID,
		Params:     email.Params,
		Tags:       email.Tags,
	}
	for _, r := range email.To {
		if r.Email == "" {
			return "", fmt.Errorf("%w: destinatario sin correo", ErrInvalidRequest)
		}
		body.To = append(body.To, contact(r))
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("brevo: serializar correo: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/smtp/email", bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("brevo: crear petición: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Api-Key", c.apiKey.Reveal())

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("brevo: enviar correo: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("brevo: leer respuesta: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e errorResponse
		_ = json.Unmarshal(respBody, &e) // el código es informativo; sin él, solo el estado
		return "", &APIError{StatusCode: resp.StatusCode, Code: e.Code}
	}
	var out sendResponse
	if err := json.Unmarshal(respBody, &out); err != nil || out.MessageID == "" {
		return "", fmt.Errorf("brevo: respuesta %d sin messageId", resp.StatusCode)
	}
	return out.MessageID, nil
}
