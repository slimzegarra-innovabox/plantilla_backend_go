// Comando modulegen crea un módulo de negocio (CLAUDE.md §16):
//
//	go run ./tools/modulegen -name orders -entity order [-plural orders] [-label pedido] [-full]
//	go run ./tools/modulegen -remove-example
//
// Por defecto genera el módulo MÍNIMO, desde una plantilla embebida: un CRUD con
// autorización por recurso, concurrencia optimista e idempotencia. Con -full copia el
// módulo de referencia internal/example, que además trae eventos por el outbox, máquina
// de estados, ordenamiento y búsqueda.
//
// En ambos casos crea el módulo, sus migraciones con los siguientes números, su registro
// en cmd/api/modules.go (y en cmd/worker/tasks.go con -full), sus bloques en
// docs/openapi.yaml y su fila en la tabla de dueños de CLAUDE.md (§5.3).
//
// -remove-example quita el módulo de referencia. Después se pueden seguir generando
// módulos mínimos, pero no con -full.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const (
	refModule     = "example"
	refPlural     = "examples"
	migrationsDir = "scripts/db/migrations"
	modulesFile   = "cmd/api/modules.go"
	tasksFile     = "cmd/worker/tasks.go"
	openAPIFile   = "docs/openapi.yaml"
	standardFile  = "CLAUDE.md"
)

func main() {
	var (
		name          = flag.String("name", "", "nombre del módulo en snake_case, p. ej. orders o purchase_orders")
		entity        = flag.String("entity", "", "entidad principal en singular y snake_case, p. ej. order o purchase_order")
		plural        = flag.String("plural", "", "plural de la entidad (tabla y ruta); por defecto <entity>s")
		label         = flag.String("label", "", "nombre en español para los mensajes, p. ej. pedido; por defecto <entity>")
		full          = flag.Bool("full", false, "copiar el módulo de referencia completo (eventos, máquina de estados, búsqueda)")
		removeExample = flag.Bool("remove-example", false, "quitar el módulo de referencia example")
	)
	flag.Parse()

	var err error
	if *removeExample {
		err = removeReference(".")
	} else {
		var n Names
		if n, err = NewNames(*name, *entity, *plural, *label); err == nil {
			err = generate(".", n, *full)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "modulegen:", err)
		os.Exit(1)
	}
}

//--------------------------------------------------------------
// NOMBRES
//--------------------------------------------------------------

// Names son las formas de los nombres del módulo nuevo.
type Names struct {
	Module, Entity, Plural, Label string
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// reserved son nombres que chocan con paquetes del proyecto o con identificadores de
// cmd/api (el módulo se importa allí con su nombre de paquete).
var reserved = []string{
	"shared", refModule, "module", "model", "usecase", "port", "porttest", "dto", "mapper", "apperr",
	"persistence", "httpapi", "external", "file", "result", "config", "logging", "health", "clock",
	"security", "gin", "http", "context", "errors", "fmt", "os", "signal", "syscall", "time", "slog",
	"main", "version", "test", "internal", "cmd", "api", "worker", "migrate", "s3", "sqs", "asynq", "brevo",
	"awsconfig", "uuid", "gorm", "run",
}

// reservedEntities son nombres que no sirven como entidad: su forma camelCase es una
// variable del módulo generado y chocaría con un paquete o con otra variable de las
// plantillas. La lista salió de generar y compilar cada candidato, en modo mínimo y
// completo. Otros nombres parecidos (strings, port, time) compilan, así que no se
// reservan. El plural no aparece como variable: solo se le exige no ser palabra clave.
var reservedEntities = []string{
	"result", "cmd", "err", "uc", "name", "now", "ctx", "context", "dto", "model", "mapper", "apperr", "fmt",
}

// validEntity dice si un nombre puede ser una variable del módulo generado.
func validEntity(name string) bool {
	return !token.IsKeyword(name) && types.Universe.Lookup(name) == nil && !slices.Contains(reservedEntities, name)
}

// NewNames valida los nombres, en snake_case. Un nombre compuesto toma la forma de cada
// contexto: purchase_orders → paquete purchaseorders, tipo PurchaseOrder, variable
// purchaseOrder, tabla purchase_orders, ruta /purchase-orders, código PURCHASE_ORDERS_.
func NewNames(module, entity, plural, label string) (Names, error) {
	if plural == "" {
		plural = entity + "s"
	}
	if label == "" {
		label = entity
	}
	n := Names{Module: module, Entity: entity, Plural: plural, Label: label}
	var errs []error
	for flagName, v := range map[string]string{"-name": module, "-entity": entity, "-plural": plural} {
		if len(v) < 2 || len(v) > 40 || !namePattern.MatchString(v) {
			errs = append(errs, fmt.Errorf("%s=%q: snake_case en minúsculas (a-z, 0-9), de 2 a 40 caracteres", flagName, v))
		}
	}
	if slices.Contains(reserved, n.Package()) || token.IsKeyword(n.Package()) {
		errs = append(errs, fmt.Errorf("-name=%q: nombre reservado", module))
	}
	if namePattern.MatchString(entity) && !validEntity(camel(entity)) {
		errs = append(errs, fmt.Errorf("-entity=%q: nombre reservado, chocaría con el código generado", entity))
	}
	if namePattern.MatchString(plural) && token.IsKeyword(camel(plural)) {
		errs = append(errs, fmt.Errorf("-plural=%q: es una palabra clave de Go", plural))
	}
	if entity == refModule || plural == refPlural {
		errs = append(errs, fmt.Errorf("-entity/-plural: %q es el módulo de referencia", refModule))
	}
	if entity == plural {
		errs = append(errs, errors.New("-plural debe ser distinto de -entity"))
	}
	if strings.ContainsAny(label, "\n\"`") {
		errs = append(errs, errors.New("-label no puede tener saltos de línea ni comillas"))
	}
	return n, errors.Join(errs...)
}

// pascal: purchase_order → PurchaseOrder.
func pascal(s string) string {
	var b strings.Builder
	for _, w := range strings.Split(s, "_") {
		b.WriteString(strings.ToUpper(w[:1]) + w[1:])
	}
	return b.String()
}

// camel: purchase_order → purchaseOrder.
func camel(s string) string {
	p := pascal(s)
	return strings.ToLower(p[:1]) + p[1:]
}

// kebab: purchase_orders → purchase-orders.
func kebab(s string) string { return strings.ReplaceAll(s, "_", "-") }

// Package es el nombre de la carpeta y del paquete Go: una palabra en minúsculas.
func (n Names) Package() string { return strings.ReplaceAll(n.Module, "_", "") }

// replacer traduce el texto del módulo de referencia al módulo nuevo. El orden importa:
// primero lo específico (ruta de import, paquete, scopes, códigos), después las formas
// de la entidad, plural antes que singular.
func (n Names) replacer(modPath string) *strings.Replacer {
	ref := modPath + "/internal/" + refModule
	pkg := n.Package()
	return strings.NewReplacer(
		ref+"/", modPath+"/internal/"+pkg+"/",
		ref+"\"", modPath+"/internal/"+pkg+"\"",
		"package "+refModule+"\n", "package "+pkg+"\n",
		"// Package "+refModule+" ", "// Package "+pkg+" ",
		// Referencias al paquete raíz del módulo, que se llama como el módulo.
		refModule+".Register", pkg+".Register",
		refModule+".Ports", pkg+".Ports",
		// Scopes y topics de mensajes: <modulo>:<accion|evento>.
		"\""+refModule+":", "\""+n.Module+":",
		refModule+":read", n.Module+":read",
		refModule+":write", n.Module+":write",
		refModule+":admin", n.Module+":admin",
		strings.ToUpper(refModule)+"_", strings.ToUpper(n.Module)+"_",
		// Rutas en kebab-case; tablas, columnas y JSON en snake_case; variables en camelCase.
		"/"+refPlural, "/"+kebab(n.Plural),
		"Examples", pascal(n.Plural),
		"examples", n.Plural,
		"Example", pascal(n.Entity),
		"example_", n.Entity+"_",
		"_example", "_"+n.Entity,
		"example", camel(n.Entity),
		"ejemplos", n.Label+"s",
		"ejemplo", n.Label,
	)
}

//--------------------------------------------------------------
// GENERAR
//--------------------------------------------------------------

func generate(root string, n Names, full bool) error {
	modPath, err := modulePath(root)
	if err != nil {
		return err
	}
	dst := filepath.Join(root, "internal", n.Package())
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%s ya existe", dst)
	}
	rep := n.replacer(modPath)

	// Se calcula todo antes de escribir: si algo falla, el árbol queda como estaba.
	var (
		files          map[string]string
		migrations     map[string]string
		firstMigration string
		blocks         []string
		regs           = registrations[:1] // el módulo mínimo no tiene tareas de worker
	)
	if full {
		regs = registrations
		if files, err = copyReferenceModule(root, n, rep); err != nil {
			return err
		}
		if migrations, firstMigration, err = plannedMigrations(root, n, rep); err != nil {
			return err
		}
	} else {
		if files, err = renderModule(n, rep); err != nil {
			return err
		}
		if migrations, firstMigration, err = templateMigrations(root, n, rep); err != nil {
			return err
		}
		if blocks, err = templateOpenAPIBlocks(rep); err != nil {
			return err
		}
	}

	edits := map[string][]byte{}
	for _, r := range regs {
		src, err := os.ReadFile(filepath.Join(root, r.file))
		if err != nil {
			return err
		}
		if edits[filepath.Join(root, r.file)], err = addRegistration(r, src, modPath, n.Package()); err != nil {
			return err
		}
	}
	openapi, err := os.ReadFile(filepath.Join(root, openAPIFile))
	if err != nil {
		return err
	}
	var newOpenAPI string
	if full {
		newOpenAPI, err = addOpenAPIBlocks(string(openapi), n.Package(), rep)
	} else {
		newOpenAPI, err = insertOpenAPIBlocks(string(openapi), n.Package(), blocks)
	}
	if err != nil {
		return err
	}
	standard, err := os.ReadFile(filepath.Join(root, standardFile))
	if err != nil {
		return err
	}
	newStandard, err := addOwner(string(standard), n.Plural, n.Package())
	if err != nil {
		return err
	}

	for rel, text := range files {
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(out, []byte(text), 0o644); err != nil {
			return err
		}
	}
	for path, body := range migrations {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	edits[filepath.Join(root, openAPIFile)] = []byte(newOpenAPI)
	edits[filepath.Join(root, standardFile)] = []byte(newStandard)
	for path, body := range edits {
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return err
		}
	}

	kind := "mínimo (CRUD)"
	if full {
		kind = "completo (copia de internal/example)"
	}
	fmt.Printf("Módulo %s %s: %d archivos; migraciones: %d, desde %s.\n\n",
		n.Package(), kind, len(files), len(migrations)/2, firstMigration)
	fmt.Printf(`Siguientes pasos:
  1. Revisar los mensajes en español de internal/%[1]s/application/apperr (género y redacción).
  2. Adaptar el model, la migración y docs/openapi.yaml a la entidad real.
  3. make check
`, n.Package())
	return nil
}

// renamePath cambia los nombres de archivo: list_examples.go → list_orders.go.
func renamePath(rel string, n Names) string {
	return strings.NewReplacer(refPlural, n.Plural, refModule, n.Entity).Replace(rel)
}

func modulePath(root string) (string, error) {
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(gomod), "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(p), nil
		}
	}
	return "", errors.New("go.mod sin directiva module")
}

// yamlExampleKey detecta la clave `example:` de OpenAPI (en bloque o en línea).
var yamlExampleKey = regexp.MustCompile(`(?m)(^\s*-?\s*|[{,]\s*)example:\s`)

// isRefMigration indica si una migración es del módulo de referencia: su descripción
// nombra la tabla (000001_create_examples, 000003_add_examples_name_search…).
func isRefMigration(name string) bool {
	m := migrationNumber.FindStringSubmatch(name)
	return m != nil && strings.Contains(name[7:], refPlural)
}

// plannedMigrations copia TODAS las migraciones del módulo de referencia, en orden,
// con números nuevos a partir del último existente. Devuelve ruta → contenido y el
// nombre de la primera (para el mensaje final).
func plannedMigrations(root string, n Names, rep *strings.Replacer) (map[string]string, string, error) {
	dir := filepath.Join(root, migrationsDir)
	last, err := lastMigration(dir, n)
	if err != nil {
		return nil, "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", err
	}
	var refs []string // up y down de la referencia, en orden de número
	for _, e := range entries {
		if isRefMigration(e.Name()) {
			refs = append(refs, e.Name())
		}
	}
	if len(refs) == 0 {
		return nil, "", fmt.Errorf("no se encontraron migraciones de %s en %s", refPlural, dir)
	}
	sort.Strings(refs)

	// Número viejo → nuevo, en el orden original.
	renumber := map[string]string{}
	next := last
	for _, name := range refs {
		if _, ok := renumber[name[:6]]; !ok {
			next++
			renumber[name[:6]] = fmt.Sprintf("%06d", next)
		}
	}
	out := map[string]string{}
	first := ""
	for _, name := range refs {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, "", err
		}
		text := rep.Replace(string(body))
		newName := renumber[name[:6]] + rep.Replace(name[6:])
		for old, nw := range renumber {
			text = strings.ReplaceAll(text, old, nw)
		}
		out[filepath.Join(dir, newName)] = text
		if strings.HasSuffix(newName, ".up.sql") && (first == "" || newName < first) {
			first = newName
		}
	}
	return out, first, nil
}

//--------------------------------------------------------------
// EDICIONES DE ARCHIVOS COMPARTIDOS
//--------------------------------------------------------------

// registration describe dónde se registra un módulo: el archivo, la función y la llamada.
type registration struct {
	file, fn, call string
}

// registrations: el servidor HTTP registra las rutas; el worker, los handlers de tareas.
var registrations = []registration{
	{modulesFile, "registerModules", "%s.Register(api, deps)"},
	{tasksFile, "registerTasks", "%s.RegisterTasks(mux, deps)"},
}

// addRegistration agrega el import del módulo y su llamada al final de r.fn.
func addRegistration(r registration, src []byte, modPath, module string) ([]byte, error) {
	s := string(src)
	imp := "\t\"" + modPath + "/internal/" + module + "\"\n"
	call := "\t" + fmt.Sprintf(r.call, module) + "\n"
	if strings.Contains(s, imp) {
		return nil, fmt.Errorf("%s ya registra el módulo %s", r.file, module)
	}
	anchor := "\t\"" + modPath + "/internal/shared/infrastructure/module\"\n"
	if !strings.Contains(s, anchor) {
		return nil, fmt.Errorf("%s: no se encontró el import de shared/infrastructure/module", r.file)
	}
	s = strings.Replace(s, anchor, imp+anchor, 1) // gofmt lo deja en orden
	fn := strings.Index(s, "func "+r.fn+"(")
	end := strings.Index(s[max(fn, 0):], "\n}")
	if fn < 0 || end < 0 {
		return nil, fmt.Errorf("%s: no se encontró func %s", r.file, r.fn)
	}
	at := fn + end + 1
	s = s[:at] + call + s[at:]
	out, err := format.Source([]byte(s))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.file, err)
	}
	return out, nil
}

// removeRegistration quita el import y la llamada del módulo.
func removeRegistration(r registration, src []byte, modPath, module string) ([]byte, error) {
	s := string(src)
	s = strings.Replace(s, "\t\""+modPath+"/internal/"+module+"\"\n", "", 1)
	s = strings.Replace(s, "\t"+fmt.Sprintf(r.call, module)+"\n", "", 1)
	return format.Source([]byte(s))
}

func blockMarkers(module string) (begin, end string) {
	return "# >>> module:" + module, "# <<< module:" + module
}

// openAPISections son los marcadores donde el generador inserta cada bloque.
var openAPISections = []string{"tags", "paths", "schemas"}

// addOpenAPIBlocks copia cada bloque del módulo de referencia, traducido, delante del
// marcador @generator de su sección.
func addOpenAPIBlocks(doc, module string, rep *strings.Replacer) (string, error) {
	blocks, err := findBlocks(doc, refModule)
	if err != nil {
		return "", err
	}
	for _, b := range blocks {
		if yamlExampleKey.MatchString(b.body) {
			return "", fmt.Errorf("%s: un bloque del módulo %s usa la clave `example:` de OpenAPI, que la traducción "+
				"de nombres rompería: ponga el ejemplo en la descripción", openAPIFile, refModule)
		}
	}
	bodies := make([]string, 0, len(blocks))
	for _, b := range blocks {
		bodies = append(bodies, rep.Replace(b.body))
	}
	return insertOpenAPIBlocks(doc, module, bodies)
}

// insertOpenAPIBlocks agrega cada bloque, entre sus marcadores, delante del marcador
// @generator de su sección.
func insertOpenAPIBlocks(doc, module string, bodies []string) (string, error) {
	doc = strings.Replace(doc, emptyPaths, "\npaths:\n", 1) // ya va a tener rutas
	if b, _ := findBlocks(doc, module); len(b) > 0 {
		return "", fmt.Errorf("%s ya tiene bloques del módulo %s", openAPIFile, module)
	}
	for i, section := range openAPISections {
		marker := "# @generator:" + section
		at := strings.Index(doc, marker)
		if at < 0 {
			return "", fmt.Errorf("%s: falta el marcador %q", openAPIFile, marker)
		}
		lineStart := strings.LastIndex(doc[:at], "\n") + 1
		indent := doc[lineStart:at]
		begin, end := blockMarkers(module)
		doc = doc[:lineStart] + indent + begin + "\n" + bodies[i] + indent + end + "\n" + doc[lineStart:]
	}
	return doc, nil
}

type block struct {
	start, end int
	body       string
}

// findBlocks devuelve los bloques del módulo en el orden de openAPISections.
// start/end abarcan las líneas de los marcadores; body es lo que hay entre ellos.
func findBlocks(doc, module string) ([]block, error) {
	begin, end := blockMarkers(module)
	var out []block
	rest := 0
	for {
		b := strings.Index(doc[rest:], begin)
		if b < 0 {
			break
		}
		b += rest
		lineStart := strings.LastIndex(doc[:b], "\n") + 1
		bodyStart := b + len(begin) + 1
		e := strings.Index(doc[bodyStart:], end)
		if e < 0 {
			return nil, fmt.Errorf("%s: el bloque %q no está cerrado", openAPIFile, begin)
		}
		e += bodyStart
		bodyEnd := strings.LastIndex(doc[:e], "\n") + 1
		lineEnd := e + len(end) + 1
		out = append(out, block{start: lineStart, end: lineEnd, body: doc[bodyStart:bodyEnd]})
		rest = lineEnd
	}
	if len(out) > 0 && len(out) != len(openAPISections) {
		return nil, fmt.Errorf("%s: el módulo %s tiene %d bloques; se esperan %d (%s)",
			openAPIFile, module, len(out), len(openAPISections), strings.Join(openAPISections, ", "))
	}
	if module == refModule && len(out) == 0 {
		return nil, fmt.Errorf("%s: no se encontraron los bloques del módulo %s", openAPIFile, refModule)
	}
	return out, nil
}

func removeOpenAPIBlocks(doc, module string) (string, error) {
	blocks, err := findBlocks(doc, module)
	if err != nil {
		return "", err
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].start > blocks[j].start })
	for _, b := range blocks {
		doc = doc[:b.start] + doc[b.end:]
	}
	return markEmptyPaths(doc), nil
}

// emptyPaths es la forma válida de una sección sin rutas: `paths:` a secas es nulo, y
// OpenAPI exige un objeto.
const emptyPaths = "\npaths: {}\n"

// markEmptyPaths usa emptyPaths si, tras quitar un módulo, no queda ninguna ruta.
func markEmptyPaths(doc string) string {
	const header = "\npaths:\n"
	at := strings.Index(doc, header)
	if at < 0 {
		return doc
	}
	rest := doc[at+len(header):]
	for rest != "" {
		line, tail, _ := strings.Cut(rest, "\n")
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			if strings.HasPrefix(line, " ") {
				return doc // hay al menos una ruta
			}
			break // la siguiente sección: paths quedó vacía
		}
		rest = tail
	}
	return doc[:at] + emptyPaths + doc[at+len(header):]
}

const ownersHeader = "| Tabla | Dueño de escritura | Módulos que solo leen |\n|---|---|---|\n"

func ownerRow(table, module string) string {
	return "| `" + table + "` | `" + module + "` | — |\n"
}

// addOwner agrega la tabla nueva al final de la tabla de dueños (§5.3).
func addOwner(doc, table, module string) (string, error) {
	at := strings.Index(doc, ownersHeader)
	if at < 0 {
		return "", fmt.Errorf("%s: no se encontró la tabla de dueños (§5.3)", standardFile)
	}
	rowsStart := at + len(ownersHeader)
	end := rowsStart
	for end < len(doc) && doc[end] == '|' {
		end += strings.Index(doc[end:], "\n") + 1
	}
	if strings.Contains(doc[rowsStart:end], "| `"+table+"` |") {
		return "", fmt.Errorf("%s: la tabla %s ya tiene dueño", standardFile, table)
	}
	return doc[:end] + ownerRow(table, module) + doc[end:], nil
}

//--------------------------------------------------------------
// QUITAR EL MÓDULO DE REFERENCIA
//--------------------------------------------------------------

func removeReference(root string) error {
	modPath, err := modulePath(root)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "internal", refModule)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("%s no existe: el módulo de referencia ya se quitó", dir)
	}

	edits := map[string][]byte{}
	for _, r := range registrations {
		src, err := os.ReadFile(filepath.Join(root, r.file))
		if err != nil {
			return err
		}
		if edits[filepath.Join(root, r.file)], err = removeRegistration(r, src, modPath, refModule); err != nil {
			return err
		}
	}
	openapi, err := os.ReadFile(filepath.Join(root, openAPIFile))
	if err != nil {
		return err
	}
	newOpenAPI, err := removeOpenAPIBlocks(string(openapi), refModule)
	if err != nil {
		return err
	}
	standard, err := os.ReadFile(filepath.Join(root, standardFile))
	if err != nil {
		return err
	}
	newStandard := strings.Replace(string(standard), ownerRow(refPlural, refModule), "", 1)
	all, err := os.ReadDir(filepath.Join(root, migrationsDir))
	if err != nil {
		return err
	}
	var migrations []string
	for _, e := range all {
		if isRefMigration(e.Name()) {
			migrations = append(migrations, filepath.Join(root, migrationsDir, e.Name()))
		}
	}

	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	for _, m := range migrations {
		if err := os.Remove(m); err != nil {
			return err
		}
	}
	edits[filepath.Join(root, openAPIFile)] = []byte(newOpenAPI)
	edits[filepath.Join(root, standardFile)] = []byte(newStandard)
	for path, body := range edits {
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("Módulo de referencia quitado. Migraciones borradas: %v.\n", migrations)
	fmt.Println("ATENCIÓN: si la migración ya se aplicó en un entorno compartido, no la borre:")
	fmt.Println("restáurela (git checkout) y cree una migración nueva que elimine la tabla (CLAUDE.md §12.2).")
	if !bytes.Contains(edits[filepath.Join(root, modulesFile)], []byte(".Register(api, deps)")) {
		fmt.Println("cmd/api/modules.go ya no registra ningún módulo.")
	}
	return nil
}
