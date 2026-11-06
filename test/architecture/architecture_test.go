package architecture_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/test/architecture"
)

// TestDependencyRules aplica las reglas al código real del repositorio.
func TestDependencyRules(t *testing.T) {
	modulePath := goList(t, "list", "-m")
	pkgs := listPackages(t, "./internal/...", "./cmd/...")
	require.NotEmpty(t, pkgs)

	violations := architecture.Violations(strings.TrimSpace(modulePath), pkgs)

	assert.Empty(t, violations, "violaciones de las reglas de dependencia (CLAUDE.md §4):\n%s",
		strings.Join(violations, "\n"))
}

// TestRulesDetectViolations prueba que cada regla detecta lo que debe. Sin esto,
// una prueba de arquitectura que siempre pasa no demostraría nada.
func TestRulesDetectViolations(t *testing.T) {
	const m = "mod"
	tests := map[string]struct {
		from, imp string
		wantRule  string // vacío: el import está permitido
	}{
		// D1/D2
		"application → stdlib":                 {"mod/internal/example/application/usecase", "context", ""},
		"application → uuid":                   {"mod/internal/example/application/model", "github.com/google/uuid", ""},
		"application → gorm":                   {"mod/internal/example/application/usecase", "gorm.io/gorm", "D1/D2"},
		"application → gin":                    {"mod/internal/example/application/dto", "github.com/gin-gonic/gin", "D1/D2"},
		"application → su model":               {"mod/internal/example/application/usecase", "mod/internal/example/application/model", ""},
		"application → shared application":     {"mod/internal/example/application/usecase", "mod/internal/shared/application/result", ""},
		"application → su infraestructura":     {"mod/internal/example/application/usecase", "mod/internal/example/infrastructure/persistence", "D1/D2"},
		"application → shared infraestructura": {"mod/internal/example/application/usecase", "mod/internal/shared/infrastructure/logging", "D1/D2"},
		"shared application → gorm":            {"mod/internal/shared/application/port", "gorm.io/gorm", "D1/D2"},
		"shared application → shared infra":    {"mod/internal/shared/application/port", "mod/internal/shared/infrastructure/config", "D1/D2"},
		// D3
		"módulo → otro módulo (infra)":  {"mod/internal/billing/infrastructure/persistence", "mod/internal/users/application/model", "D3"},
		"módulo → otro módulo (app)":    {"mod/internal/billing/application/usecase", "mod/internal/users/application/port", "D1/D2"},
		"módulo → shared":               {"mod/internal/billing/infrastructure/httpapi", "mod/internal/shared/infrastructure/httpapi", ""},
		"raíz del módulo → sus capas":   {"mod/internal/billing", "mod/internal/billing/infrastructure/persistence", ""},
		"raíz del módulo → otro módulo": {"mod/internal/billing", "mod/internal/users", "D3"},
		// D4
		"shared → módulo": {"mod/internal/shared/infrastructure/module", "mod/internal/example/application/model", "D4"},
		// D5
		"httpapi → usecase":     {"mod/internal/example/infrastructure/httpapi", "mod/internal/example/application/usecase", ""},
		"httpapi → dto":         {"mod/internal/example/infrastructure/httpapi", "mod/internal/example/application/dto", ""},
		"httpapi → persistence": {"mod/internal/example/infrastructure/httpapi", "mod/internal/example/infrastructure/persistence", "D5"},
		"httpapi → external":    {"mod/internal/example/infrastructure/httpapi", "mod/internal/example/infrastructure/external", "D5"},
		"httpapi → port":        {"mod/internal/example/infrastructure/httpapi", "mod/internal/example/application/port", "D5"},
		// D6
		"persistence → httpapi":        {"mod/internal/example/infrastructure/persistence", "mod/internal/example/infrastructure/httpapi", "D6"},
		"persistence → shared httpapi": {"mod/internal/example/infrastructure/persistence", "mod/internal/shared/infrastructure/httpapi", "D6"},
		"persistence → gorm":           {"mod/internal/example/infrastructure/persistence", "gorm.io/gorm", ""},
		// D7
		"cmd → todo": {"mod/cmd/api", "mod/internal/example/infrastructure/persistence", ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := architecture.Violations(m, []architecture.Package{{ImportPath: tt.from, Imports: []string{tt.imp}}})
			if tt.wantRule == "" {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			assert.Contains(t, got[0], tt.wantRule)
		})
	}
}

// goList ejecuta el comando go desde la raíz del módulo (la prueba corre en test/architecture).
func goList(t *testing.T, args ...string) string {
	t.Helper()
	gomod, err := exec.Command("go", "env", "GOMOD").Output()
	require.NoError(t, err)

	var stderr bytes.Buffer
	cmd := exec.Command("go", args...)
	cmd.Dir = filepath.Dir(strings.TrimSpace(string(gomod)))
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	return string(out)
}

func listPackages(t *testing.T, patterns ...string) []architecture.Package {
	t.Helper()
	out := goList(t, append([]string{"list", "-json=ImportPath,Imports"}, patterns...)...)

	var pkgs []architecture.Package
	dec := json.NewDecoder(strings.NewReader(out))
	for {
		var p architecture.Package
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		pkgs = append(pkgs, p)
	}
	return pkgs
}
