package main

import (
	"embed"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// templateFS es el módulo mínimo: un CRUD con autorización por recurso, concurrencia
// optimista e idempotencia, sin eventos, máquina de estados ni búsqueda. Está embebido,
// así que se puede generar aunque el proyecto ya haya quitado internal/example.
//
//go:embed all:template
var templateFS embed.FS

// renderModule devuelve archivo relativo al módulo → contenido, ya traducido.
func renderModule(n Names, rep *strings.Replacer) (map[string]string, error) {
	const dir = "template/module"
	out := map[string]string{}
	err := fs.WalkDir(templateFS, dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := templateFS.ReadFile(path)
		if err != nil {
			return err
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(path, dir+"/"), ".tmpl")
		text, err := renderGo(rel, rep.Replace(string(body)))
		if err != nil {
			return err
		}
		out[filepath.FromSlash(renamePath(rel, n))] = text
		return nil
	})
	return out, err
}

// renderGo da formato al código generado y falla si no es Go válido.
func renderGo(name, text string) (string, error) {
	if !strings.HasSuffix(name, ".go") {
		return text, nil
	}
	formatted, err := format.Source([]byte(text))
	if err != nil {
		return "", fmt.Errorf("%s: el código generado no es Go válido: %w", name, err)
	}
	return string(formatted), nil
}

// copyReferenceModule es la generación completa: lee internal/example.
func copyReferenceModule(root string, n Names, rep *strings.Replacer) (map[string]string, error) {
	src := filepath.Join(root, "internal", refModule)
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("no existe el módulo de referencia %s (¿se quitó con -remove-example?): "+
			"sin -full se genera el módulo mínimo", src)
	}
	out := map[string]string{}
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text, err := renderGo(rel, rep.Replace(string(body)))
		if err != nil {
			return err
		}
		out[renamePath(rel, n)] = text
		return nil
	})
	return out, err
}

var migrationNumber = regexp.MustCompile(`^(\d{6})_.+\.(up|down)\.sql$`)

// lastMigration devuelve el mayor número existente, y falla si la tabla ya tiene migración.
func lastMigration(dir string, n Names) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	last := 0
	for _, e := range entries {
		m := migrationNumber.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		num, _ := strconv.Atoi(m[1])
		last = max(last, num)
		// Exacto: «create_idempotency_keys» no es la tabla keys.
		if strings.TrimSuffix(strings.TrimSuffix(e.Name()[7:], ".up.sql"), ".down.sql") == "create_"+n.Plural {
			return 0, fmt.Errorf("ya existe una migración para la tabla %s: %s", n.Plural, e.Name())
		}
	}
	return last, nil
}

// templateMigrations es la migración del módulo mínimo, con el siguiente número.
func templateMigrations(root string, n Names, rep *strings.Replacer) (map[string]string, string, error) {
	dir := filepath.Join(root, migrationsDir)
	last, err := lastMigration(dir, n)
	if err != nil {
		return nil, "", err
	}
	base := fmt.Sprintf("%06d_create_%s", last+1, n.Plural)
	out := map[string]string{}
	for _, kind := range []string{"up", "down"} {
		body, err := templateFS.ReadFile("template/migration/create_" + refPlural + "." + kind + ".sql.tmpl")
		if err != nil {
			return nil, "", err
		}
		out[filepath.Join(dir, base+"."+kind+".sql")] = rep.Replace(string(body))
	}
	return out, base + ".up.sql", nil
}

// templateOpenAPIBlocks devuelve los bloques del módulo mínimo, en el orden de openAPISections.
func templateOpenAPIBlocks(rep *strings.Replacer) ([]string, error) {
	out := make([]string, 0, len(openAPISections))
	for _, section := range openAPISections {
		body, err := templateFS.ReadFile("template/openapi/" + section + ".yaml.tmpl")
		if err != nil {
			return nil, err
		}
		out = append(out, rep.Replace(string(body)))
	}
	return out, nil
}
