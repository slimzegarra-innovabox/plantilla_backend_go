package brevo_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
	"myapp.v1.0.0/internal/shared/infrastructure/external/brevo"
)

func newClient(url string) *brevo.Client {
	return brevo.New(config.BrevoConfig{
		APIKey:      "test-api-key",
		BaseURL:     url,
		SenderEmail: "no-reply@example.com",
		SenderName:  "MyApp",
		Timeout:     2 * time.Second,
	})
}

var email = brevo.TemplateEmail{
	To:         []brevo.Recipient{{Email: "user@example.com", Name: "Ana"}},
	TemplateID: 7,
	Params:     map[string]any{"code": "1234"},
}

func TestSendTemplateSendsBrevoContract(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/smtp/email", r.URL.Path)
		assert.Equal(t, "test-api-key", r.Header.Get("Api-Key"))
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &got))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"messageId":"<abc@smtp-relay>"}`))
	}))
	defer srv.Close()

	id, err := newClient(srv.URL).SendTemplate(context.Background(), email)

	require.NoError(t, err)
	assert.Equal(t, "<abc@smtp-relay>", id)
	assert.Equal(t, map[string]any{"email": "no-reply@example.com", "name": "MyApp"}, got["sender"])
	assert.Equal(t, []any{map[string]any{"email": "user@example.com", "name": "Ana"}}, got["to"])
	assert.InDelta(t, 7, got["templateId"], 0)
	assert.Equal(t, map[string]any{"code": "1234"}, got["params"])
}

func TestSendTemplateErrorNeverContainsBodies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"invalid_parameter","message":"email user@example.com is not valid"}`))
	}))
	defer srv.Close()

	_, err := newClient(srv.URL).SendTemplate(context.Background(), email)

	var apiErr *brevo.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, "invalid_parameter", apiErr.Code)
	assert.True(t, apiErr.Rejected())
	assert.NotContains(t, err.Error(), "user@example.com")
	assert.NotContains(t, err.Error(), "test-api-key")
}

func TestAPIErrorRejected(t *testing.T) {
	for status, rejected := range map[int]bool{400: true, 404: true, 401: false, 403: false, 429: false, 500: false, 503: false} {
		assert.Equal(t, rejected, (&brevo.APIError{StatusCode: status}).Rejected(), "status %d", status)
	}
}

func TestSendTemplateRejectsInvalidEmailBeforeCalling(t *testing.T) {
	c := newClient("http://127.0.0.1:1") // no debe llamarse
	for name, e := range map[string]brevo.TemplateEmail{
		"sin destinatarios":  {TemplateID: 1},
		"sin plantilla":      {To: []brevo.Recipient{{Email: "a@b.c"}}},
		"destinatario vacío": {To: []brevo.Recipient{{Name: "x"}}, TemplateID: 1},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.SendTemplate(context.Background(), e)
			assert.ErrorIs(t, err, brevo.ErrInvalidRequest)
		})
	}
}

func TestSendTemplateRespectsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// Con el cuerpo leído, el servidor detecta la desconexión y cancela r.Context().
		_, _ = io.ReadAll(r.Body)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := newClient(srv.URL).SendTemplate(ctx, email)

	assert.True(t, errors.Is(err, context.DeadlineExceeded), "got %v", err)
}
