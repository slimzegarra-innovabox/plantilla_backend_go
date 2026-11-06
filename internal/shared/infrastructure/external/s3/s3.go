// Package s3 es el cliente técnico de almacenamiento de objetos (Amazon S3).
//
// No implementa ningún puerto: el puerto (p. ej. ObjectStorageClient) lo define el
// módulo que lo consume, y su adapter en <modulo>/infrastructure/external delega en
// este cliente y traduce ErrObjectNotFound a un apperr del módulo (CLAUDE.md §7.3).
//
// Reglas de seguridad (CLAUDE.md §10):
//   - las claves se construyen con NewKey (prefijo fijo + uuid) y todo método las valida;
//   - una URL de subida firma el tipo de contenido, el tamaño exacto y el cifrado SSE;
//   - este paquete no registra nada: ni la URL firmada ni sus parámetros (X-Amz-Credential,
//     X-Amz-Signature) llegan nunca a un log. Los errores se devuelven envueltos con la
//     operación y la clave, y los registra el borde que los recibe.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
	"myapp.v1.0.0/internal/shared/infrastructure/external/awsconfig"
)

// ErrObjectNotFound indica que la clave no existe en el bucket.
var ErrObjectNotFound = errors.New("s3: el objeto no existe")

// ErrInvalidRequest indica una petición que el cliente rechaza antes de llamar a S3
// (clave, tipo de contenido o tamaño no válidos). Es un error de programación del
// adapter que lo llama: el caso de uso debe haber validado la entrada antes.
var ErrInvalidRequest = errors.New("s3: petición inválida")

const maxKeyLength = 512

var (
	// Segmentos en minúsculas sin «..», sin «/» inicial ni final, y extensión opcional.
	keyPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(/[a-z0-9][a-z0-9-]*)*(\.[a-z0-9]{1,10})?$`)
	prefixPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(/[a-z0-9][a-z0-9-]*)*$`)
	extPattern       = regexp.MustCompile(`^[a-z0-9]{1,10}$`)
	mediaTypePattern = regexp.MustCompile(`^[a-z]+/[a-z0-9][a-z0-9.+-]*$`)
)

// NewKey construye una clave «prefijo/uuid[/uuid…][.ext]» a partir de identificadores
// ya validados. El prefijo es fijo en el código del módulo (p. ej. "examples/attachments");
// nunca se arma con texto del usuario.
func NewKey(prefix, ext string, ids ...uuid.UUID) (string, error) {
	if !prefixPattern.MatchString(prefix) {
		return "", fmt.Errorf("%w: prefijo %q", ErrInvalidRequest, prefix)
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("%w: la clave necesita al menos un uuid", ErrInvalidRequest)
	}
	parts := make([]string, 0, len(ids)+1)
	parts = append(parts, prefix)
	for _, id := range ids {
		if id == uuid.Nil {
			return "", fmt.Errorf("%w: uuid vacío en la clave", ErrInvalidRequest)
		}
		parts = append(parts, id.String())
	}
	key := strings.Join(parts, "/")
	if ext != "" {
		if !extPattern.MatchString(ext) {
			return "", fmt.Errorf("%w: extensión %q", ErrInvalidRequest, ext)
		}
		key += "." + ext
	}
	return key, validateKey(key)
}

func validateKey(key string) error {
	if len(key) > maxKeyLength || !keyPattern.MatchString(key) {
		return fmt.Errorf("%w: clave con formato no permitido", ErrInvalidRequest)
	}
	return nil
}

// Upload describe la subida que se firma.
type Upload struct {
	Key         string
	ContentType string
	// Size es el tamaño exacto en bytes: S3 rechaza un cuerpo de otro tamaño.
	Size int64
}

// PresignedRequest es la petición firmada que el cliente final ejecuta tal cual.
// URL es una credencial temporal: se entrega al cliente y nunca se registra.
type PresignedRequest struct {
	Method    string
	URL       string
	Headers   map[string]string
	ExpiresAt time.Time
}

// Object es el contenido de un objeto. El llamador debe cerrar Body.
type Object struct {
	Body        io.ReadCloser
	ContentType string
	Size        int64
}

// objectAPI es el subconjunto del SDK que usa el cliente (permite probarlo sin AWS).
type objectAPI interface {
	GetObject(ctx context.Context, in *awss3.GetObjectInput, opts ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	PutObject(ctx context.Context, in *awss3.PutObjectInput, opts ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	DeleteObject(ctx context.Context, in *awss3.DeleteObjectInput, opts ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error)
}

// Client opera sobre un único bucket.
type Client struct {
	api            objectAPI
	presign        *awss3.PresignClient
	bucket         string
	uploadMaxBytes int64
	ttl            time.Duration
}

// New crea el cliente a partir de la configuración de AWS y de S3.
func New(awsCfg aws.Config, cfg config.S3Config) *Client {
	sdk := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		o.TracerProvider, o.MeterProvider = awsconfig.TracerProvider(), awsconfig.MeterProvider()
	})
	return &Client{
		api:            sdk,
		presign:        awss3.NewPresignClient(sdk),
		bucket:         cfg.Bucket,
		uploadMaxBytes: cfg.UploadMaxBytes,
		ttl:            cfg.PresignTTL,
	}
}

// PresignUpload firma un PUT con el tipo de contenido, el tamaño y el cifrado fijados.
// El cliente final debe enviar exactamente las cabeceras de PresignedRequest.Headers.
func (c *Client) PresignUpload(ctx context.Context, up Upload) (PresignedRequest, error) {
	if err := validateKey(up.Key); err != nil {
		return PresignedRequest{}, err
	}
	if !mediaTypePattern.MatchString(up.ContentType) {
		return PresignedRequest{}, fmt.Errorf("%w: tipo de contenido %q", ErrInvalidRequest, up.ContentType)
	}
	if up.Size < 1 || up.Size > c.uploadMaxBytes {
		return PresignedRequest{}, fmt.Errorf("%w: tamaño %d fuera de 1..%d", ErrInvalidRequest, up.Size, c.uploadMaxBytes)
	}

	expires := time.Now().Add(c.ttl)
	req, err := c.presign.PresignPutObject(ctx, &awss3.PutObjectInput{
		Bucket:               aws.String(c.bucket),
		Key:                  aws.String(up.Key),
		ContentType:          aws.String(up.ContentType),
		ContentLength:        aws.Int64(up.Size),
		ServerSideEncryption: types.ServerSideEncryptionAes256,
	}, awss3.WithPresignExpires(c.ttl))
	if err != nil {
		return PresignedRequest{}, fmt.Errorf("s3: firmar subida de %s: %w", up.Key, err)
	}
	return PresignedRequest{
		Method: http.MethodPut,
		URL:    req.URL,
		Headers: map[string]string{
			"Content-Type":                 up.ContentType,
			"Content-Length":               strconv.FormatInt(up.Size, 10),
			"X-Amz-Server-Side-Encryption": string(types.ServerSideEncryptionAes256),
		},
		ExpiresAt: expires,
	}, nil
}

// PresignDownload firma un GET del objeto. No comprueba que exista: si no existe,
// S3 responde 404 al cliente final.
func (c *Client) PresignDownload(ctx context.Context, key string) (PresignedRequest, error) {
	if err := validateKey(key); err != nil {
		return PresignedRequest{}, err
	}
	expires := time.Now().Add(c.ttl)
	req, err := c.presign.PresignGetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}, awss3.WithPresignExpires(c.ttl))
	if err != nil {
		return PresignedRequest{}, fmt.Errorf("s3: firmar descarga de %s: %w", key, err)
	}
	return PresignedRequest{Method: http.MethodGet, URL: req.URL, Headers: map[string]string{}, ExpiresAt: expires}, nil
}

// Get descarga un objeto. Devuelve ErrObjectNotFound si la clave no existe.
func (c *Client) Get(ctx context.Context, key string) (Object, error) {
	if err := validateKey(key); err != nil {
		return Object{}, err
	}
	out, err := c.api.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return Object{}, fmt.Errorf("%w: %s", ErrObjectNotFound, key)
		}
		return Object{}, fmt.Errorf("s3: descargar %s: %w", key, err)
	}
	return Object{
		Body:        out.Body,
		ContentType: aws.ToString(out.ContentType),
		Size:        aws.ToInt64(out.ContentLength),
	}, nil
}

// Put sube un objeto desde el servidor (p. ej. un archivo generado por el worker).
func (c *Client) Put(ctx context.Context, key, contentType string, body io.Reader, size int64) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if !mediaTypePattern.MatchString(contentType) {
		return fmt.Errorf("%w: tipo de contenido %q", ErrInvalidRequest, contentType)
	}
	_, err := c.api.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:               aws.String(c.bucket),
		Key:                  aws.String(key),
		ContentType:          aws.String(contentType),
		ContentLength:        aws.Int64(size),
		Body:                 body,
		ServerSideEncryption: types.ServerSideEncryptionAes256,
	})
	if err != nil {
		return fmt.Errorf("s3: subir %s: %w", key, err)
	}
	return nil
}

// Delete elimina un objeto. Borrar una clave que no existe no es un error (semántica de S3).
func (c *Client) Delete(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if _, err := c.api.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("s3: eliminar %s: %w", key, err)
	}
	return nil
}
