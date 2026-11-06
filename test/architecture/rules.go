// Package architecture verifica las reglas de dependencia del estándar (CLAUDE.md §4).
// Las reglas se evalúan sobre los imports directos de producción (sin _test.go) de
// cada paquete bajo internal/ y cmd/.
package architecture

import (
	"fmt"
	"slices"
	"strings"
)

// Package es lo que la prueba necesita saber de un paquete Go.
type Package struct {
	ImportPath string
	Imports    []string
}

// allowedExternalInApplication es la lista cerrada de paquetes no estándar que
// application puede importar (D1/D2). Agregar uno requiere un ADR.
var allowedExternalInApplication = []string{
	"github.com/google/uuid",
}

// Violations devuelve una descripción por cada import que rompe una regla.
func Violations(modulePath string, pkgs []Package) []string {
	var out []string
	for _, p := range pkgs {
		from, ok := locate(modulePath, p.ImportPath)
		if !ok {
			continue
		}
		for _, imp := range p.Imports {
			if msg := check(modulePath, from, imp); msg != "" {
				out = append(out, fmt.Sprintf("%s → %s: %s", p.ImportPath, imp, msg))
			}
		}
	}
	slices.Sort(out)
	return out
}

// location describe dónde está un paquete del módulo.
type location struct {
	module string // módulo de negocio ("shared" incluido); "" si no está en internal/
	layer  string // application | infrastructure | "" (raíz del módulo)
	folder string // carpeta dentro de la capa: usecase, httpapi, persistence…
}

func locate(modulePath, importPath string) (location, bool) {
	if importPath != modulePath && !strings.HasPrefix(importPath, modulePath+"/") {
		return location{}, false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(importPath, modulePath), "/")
	var loc location
	parts := strings.Split(rel, "/")
	if len(parts) >= 2 && parts[0] == "internal" {
		loc.module = parts[1]
		if len(parts) >= 3 {
			loc.layer = parts[2]
		}
		if len(parts) >= 4 {
			loc.folder = parts[3]
		}
	}
	return loc, true
}

func isStdlib(imp string) bool {
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".")
}

func check(modulePath string, from location, imp string) string {
	to, internalImp := locate(modulePath, imp)

	if from.module == "" {
		return "" // cmd/ y otros: raíz de composición, puede importar todo
	}

	// D1/D2: application es pura.
	if from.layer == "application" {
		// Los imports del propio módulo Go se evalúan primero: su ruta puede no tener
		// punto (p. ej. "mod/...") y se confundiría con la biblioteca estándar.
		switch {
		case internalImp:
			if to.layer != "application" || (to.module != from.module && to.module != "shared") {
				return "D1/D2: application solo puede importar application del mismo módulo o de shared"
			}
		case isStdlib(imp), slices.Contains(allowedExternalInApplication, imp):
		default:
			return "D1/D2: application solo puede importar la biblioteca estándar y " +
				strings.Join(allowedExternalInApplication, ", ")
		}
	}

	if !internalImp || to.module == "" {
		return ""
	}

	// D4: shared no conoce los módulos de negocio.
	if from.module == "shared" && to.module != "shared" {
		return "D4: shared no puede importar un módulo de negocio"
	}

	// D3: los módulos de negocio no se conocen entre sí.
	if from.module != "shared" && to.module != from.module && to.module != "shared" {
		return "D3: un módulo de negocio no puede importar otro (usar shared o replantear)"
	}

	sameModule := to.module == from.module
	// D5: el adaptador HTTP solo conoce casos de uso y DTOs.
	if from.layer == "infrastructure" && from.folder == "httpapi" && sameModule && from.module != "shared" {
		if to.layer == "infrastructure" && slices.Contains([]string{"persistence", "external", "file"}, to.folder) {
			return "D5: httpapi no puede importar adapters (" + to.folder + ")"
		}
		if to.layer == "application" && to.folder == "port" {
			return "D5: httpapi no puede importar los puertos; solo conoce casos de uso"
		}
	}

	// D6: la persistencia no conoce HTTP.
	if from.layer == "infrastructure" && from.folder == "persistence" && to.folder == "httpapi" {
		return "D6: persistence no puede importar httpapi"
	}
	return ""
}
