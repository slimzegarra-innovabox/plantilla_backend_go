package s3

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
)

// newTestClient firma con credenciales estáticas: presignar no hace llamadas de red.
func newTestClient(api objectAPI) *Client {
	c := New(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret-example", ""),
	}, config.S3Config{Bucket: "my-bucket", UploadMaxBytes: 1 << 20, PresignTTL: 5 * time.Minute})
	if api != nil {
		c.api = api
	}
	return c
}

func TestNewKey(t *testing.T) {
	id := uuid.MustParse("0192f0a4-1b2c-7d3e-8f40-5a6b7c8d9e0f")

	key, err := NewKey("examples/attachments", "png", id)
	require.NoError(t, err)
	assert.Equal(t, "examples/attachments/0192f0a4-1b2c-7d3e-8f40-5a6b7c8d9e0f.png", key)

	tests := map[string]struct {
		prefix, ext string
		ids         []uuid.UUID
	}{
		"prefijo con ..":        {"../etc", "", []uuid.UUID{id}},
		"prefijo absoluto":      {"/examples", "", []uuid.UUID{id}},
		"prefijo en mayúsculas": {"Examples", "", []uuid.UUID{id}},
		"sin uuid":              {"examples", "", nil},
		"uuid vacío":            {"examples", "", []uuid.UUID{uuid.Nil}},
		"extensión con punto":   {"examples", ".png", []uuid.UUID{id}},
		"extensión con barra":   {"examples", "a/b", []uuid.UUID{id}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := NewKey(tt.prefix, tt.ext, tt.ids...)
			assert.ErrorIs(t, err, ErrInvalidRequest)
		})
	}
}

func TestPresignUploadSignsTypeSizeAndEncryption(t *testing.T) {
	c := newTestClient(nil)

	req, err := c.PresignUpload(context.Background(), Upload{Key: "examples/a.png", ContentType: "image/png", Size: 2048})

	require.NoError(t, err)
	assert.Equal(t, "PUT", req.Method)
	u, err := url.Parse(req.URL)
	require.NoError(t, err)
	signed := strings.Split(u.Query().Get("X-Amz-SignedHeaders"), ";")
	// Firmados: el cliente final no puede cambiar tipo, tamaño ni cifrado sin invalidar la firma.
	assert.Subset(t, signed, []string{"content-length", "content-type", "host", "x-amz-server-side-encryption"})
	assert.Equal(t, "300", u.Query().Get("X-Amz-Expires"))
	assert.Equal(t, map[string]string{
		"Content-Type":                 "image/png",
		"Content-Length":               "2048",
		"X-Amz-Server-Side-Encryption": "AES256",
	}, req.Headers)
	assert.WithinDuration(t, time.Now().Add(5*time.Minute), req.ExpiresAt, 5*time.Second)
}

func TestPresignUploadRejectsInvalidRequests(t *testing.T) {
	c := newTestClient(nil)
	tests := map[string]Upload{
		"clave con ..":         {Key: "examples/../secret", ContentType: "image/png", Size: 1},
		"clave absoluta":       {Key: "/examples/a", ContentType: "image/png", Size: 1},
		"tipo con parámetros":  {Key: "examples/a", ContentType: "text/html; charset=utf-8", Size: 1},
		"tamaño cero":          {Key: "examples/a", ContentType: "image/png", Size: 0},
		"tamaño sobre límite":  {Key: "examples/a", ContentType: "image/png", Size: 1<<20 + 1},
		"clave con mayúsculas": {Key: "examples/A", ContentType: "image/png", Size: 1},
	}
	for name, up := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := c.PresignUpload(context.Background(), up)
			assert.ErrorIs(t, err, ErrInvalidRequest)
		})
	}
}

func TestPresignDownload(t *testing.T) {
	req, err := newTestClient(nil).PresignDownload(context.Background(), "examples/a.png")

	require.NoError(t, err)
	assert.Equal(t, "GET", req.Method)
	assert.Contains(t, req.URL, "/examples/a.png")
}

type fakeAPI struct{ getErr error }

func (f fakeAPI) GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	return nil, f.getErr
}

func (f fakeAPI) PutObject(context.Context, *awss3.PutObjectInput, ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	return &awss3.PutObjectOutput{}, nil
}

func (f fakeAPI) DeleteObject(context.Context, *awss3.DeleteObjectInput, ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error) {
	return &awss3.DeleteObjectOutput{}, nil
}

func TestGetTranslatesNoSuchKey(t *testing.T) {
	c := newTestClient(fakeAPI{getErr: &types.NoSuchKey{}})

	_, err := c.Get(context.Background(), "examples/a.png")

	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestGetPropagatesOtherErrorsAsTechnical(t *testing.T) {
	boom := errors.New("connection reset")
	c := newTestClient(fakeAPI{getErr: boom})

	_, err := c.Get(context.Background(), "examples/a.png")

	require.ErrorIs(t, err, boom)
	assert.NotErrorIs(t, err, ErrObjectNotFound)
}

func TestAWSCallsAreTraced(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>NoSuchKey</Code><Message>x</Message></Error>`))
	}))
	defer srv.Close()
	c := New(aws.Config{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(srv.URL),
		Credentials:  credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret-example", ""),
	}, config.S3Config{Bucket: "my-bucket", UploadMaxBytes: 1, PresignTTL: time.Minute})

	ctx, parent := otel.Tracer("test").Start(context.Background(), "petición")
	_, err := c.Get(ctx, "examples/a.png")
	parent.End()

	require.ErrorIs(t, err, ErrObjectNotFound)
	var names []string
	for _, s := range rec.Ended() {
		names = append(names, s.Name())
		if s.Name() != "petición" {
			assert.Equal(t, parent.SpanContext().TraceID(), s.SpanContext().TraceID())
		}
	}
	assert.Contains(t, names, "S3.GetObject")
}
