package config

import "log/slog"

// Secret es un valor sensible. Se imprime como [REDACTED] con fmt y slog,
// para que un log accidental de la configuración no filtre credenciales.
type Secret string

const redacted = "[REDACTED]"

// Reveal devuelve el valor real. Úsalo solo en el punto exacto donde se necesita.
func (s Secret) Reveal() string { return string(s) }

// String implementa fmt.Stringer.
func (s Secret) String() string { return redacted }

// GoString implementa fmt.GoStringer (%#v).
func (s Secret) GoString() string { return redacted }

// LogValue implementa slog.LogValuer.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText evita que el valor se serialice (JSON, YAML…).
func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
