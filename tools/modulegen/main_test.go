package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewNamesValidates(t *testing.T) {
	n, err := NewNames("orders", "order", "", "")
	require.NoError(t, err)
	assert.Equal(t, Names{Module: "orders", Entity: "order", Plural: "orders", Label: "order"}, n)

	for name, args := range map[string][4]string{
		"mayúsculas":            {"Orders", "order", "", ""},
		"guion":                 {"purchase-orders", "purchase_order", "", ""},
		"guion bajo al final":   {"orders_", "order", "", ""},
		"reservado":             {"model", "order", "", ""},
		"choca con cmd/api":     {"config", "order", "", ""},
		"reservado compuesto":   {"use_case", "order", "", ""},
		"es la referencia":      {"items", "example", "", ""},
		"plural igual":          {"sheep", "sheep", "sheep", ""},
		"etiqueta con comilla":  {"orders", "order", "", `pe"dido`},
		"función de cmd/api":    {"run", "order", "", ""},
		"palabra clave":         {"type", "order", "", ""},
		"entidad = paquete":     {"orders", "result", "orders", ""},
		"entidad = variable":    {"orders", "cmd", "orders", ""},
		"entidad predeclarada":  {"orders", "error", "orders", ""},
		"entidad clave de Go":   {"orders", "func", "orders", ""},
		"entidad = paquete dto": {"orders", "dto", "orders", ""},
		"plural clave de Go":    {"orders", "order", "func", ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewNames(args[0], args[1], args[2], args[3])
			assert.Error(t, err)
		})
	}
}

// Un nombre compuesto no choca: su camelCase es otra palabra.
func TestCompoundEntityIsNotReserved(t *testing.T) {
	_, err := NewNames("results", "exam_result", "exam_results", "")
	assert.NoError(t, err)
	_, err = NewNames("orders", "order", "err", "")
	assert.NoError(t, err, "el plural no aparece como variable")
}

// El módulo se importa en cmd/api y cmd/worker con su nombre de paquete. Si ese nombre
// coincide con algo declarado en esos paquetes, no compilan: esta prueba obliga a
// reservar lo que se agregue allí.
func TestEveryCmdIdentifierIsReserved(t *testing.T) {
	for _, dir := range []string{"../../cmd/api", "../../cmd/worker"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		require.NoError(t, err)
		require.NotEmpty(t, files)
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			require.NoError(t, err)
			for _, decl := range file.Decls {
				for _, name := range declaredNames(decl) {
					if isPackageName(name) {
						assert.Contains(t, reserved, name, "%s declara %s: agréguelo a reserved", path, name)
					}
				}
			}
		}
	}
}

// isPackageName dice si el generador podría producir ese nombre de paquete.
func isPackageName(name string) bool { return name == strings.ToLower(name) && name != "_" }

func declaredNames(decl ast.Decl) []string {
	var names []string
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Recv == nil {
			names = append(names, d.Name.Name)
		}
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch sp := spec.(type) {
			case *ast.ValueSpec:
				for _, n := range sp.Names {
					names = append(names, n.Name)
				}
			case *ast.TypeSpec:
				names = append(names, sp.Name.Name)
			}
		}
	}
	return names
}

func TestLastMigrationMatchesTheExactTable(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"000001_create_orders.up.sql", "000002_create_idempotency_keys.up.sql", "000003_create_purchase_items.up.sql"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), nil, 0o600))
	}
	names := func(plural string) Names { return Names{Plural: plural} }

	last, err := lastMigration(dir, names("keys"))
	require.NoError(t, err, "keys no es idempotency_keys")
	assert.Equal(t, 3, last)
	_, err = lastMigration(dir, names("items"))
	assert.NoError(t, err, "items no es purchase_items")

	_, err = lastMigration(dir, names("orders"))
	assert.Error(t, err, "orders ya existe")
}

// Sin módulos, el documento debe seguir siendo OpenAPI válido; y al generar otro módulo,
// la sección vuelve a su forma normal.
func TestRemovingTheLastModuleLeavesValidPaths(t *testing.T) {
	doc := `paths:
  # >>> module:example
  /examples:
    get: {}
  # <<< module:example
  # @generator:paths

components:
  schemas:
    # >>> module:example
    Example: {}
    # <<< module:example
    # @generator:schemas
`
	doc = "tags:\n  # >>> module:example\n  - name: x\n  # <<< module:example\n  # @generator:tags\n\n" + doc
	removed, err := removeOpenAPIBlocks(doc, "example")
	require.NoError(t, err)
	assert.Contains(t, removed, "\npaths: {}\n")

	added, err := insertOpenAPIBlocks(removed, "orders", []string{"  - name: y\n", "  /orders:\n    get: {}\n", "    Order: {}\n"})
	require.NoError(t, err)
	assert.Contains(t, added, "\npaths:\n  # >>> module:orders\n  /orders:")
	assert.NotContains(t, added, "paths: {}")

	again, err := removeOpenAPIBlocks(added, "orders")
	require.NoError(t, err)
	assert.Contains(t, again, "\npaths: {}\n")
}

func TestReplacerTranslatesEveryForm(t *testing.T) {
	n, err := NewNames("billing", "invoice", "", "factura")
	require.NoError(t, err)
	rep := n.replacer("mod.v1")

	in := `package example

import "mod.v1/internal/example/application/model"
import "mod.v1/internal/example"

// Package example agrupa examples. Scope example:write. El ejemplo no existe.
var x = ListExamplesQuery{} // ExampleData, toExampleModel, "EXAMPLE_NOT_FOUND", "examples"
const topic = "example:status_changed"
var y = example.Ports{}; func f() { example.RegisterWith(nil, nil, y) }
`
	want := `package billing

import "mod.v1/internal/billing/application/model"
import "mod.v1/internal/billing"

// Package billing agrupa invoices. Scope billing:write. El factura no existe.
var x = ListInvoicesQuery{} // InvoiceData, toInvoiceModel, "BILLING_NOT_FOUND", "invoices"
const topic = "billing:status_changed"
var y = billing.Ports{}; func f() { billing.RegisterWith(nil, nil, y) }
`
	assert.Equal(t, want, rep.Replace(in))
	assert.Equal(t, "usecase/list_invoices.go", renamePath("usecase/list_examples.go", n))
	assert.Equal(t, "httpapi/invoice_handler.go", renamePath("httpapi/example_handler.go", n))
}

// Datos fijos con la forma de los archivos reales: las pruebas no dependen de que el
// proyecto conserve el módulo de referencia (make remove-example).
const modulesFixture = `package main

import (
	"github.com/gin-gonic/gin"

	"myapp.v1.0.0/internal/example"
	"myapp.v1.0.0/internal/shared/infrastructure/module"
)

func registerModules(api *gin.RouterGroup, deps module.Deps) {
	example.Register(api, deps)
}
`

const openAPIFixture = `openapi: 3.0.3
tags:
  # >>> module:example
  - name: examples
  # <<< module:example
  # @generator:tags
paths:
  # >>> module:example
  /examples/{uuid}/activate:
    post:
      operationId: activateExample
  # <<< module:example
  # @generator:paths
components:
  schemas:
    Notification:
      type: object
    # >>> module:example
    ExampleEnvelope:
      type: object
    # <<< module:example
    # @generator:schemas
`

const tasksFixture = `package main

import (
	"github.com/hibiken/asynq"

	"myapp.v1.0.0/internal/example"
	"myapp.v1.0.0/internal/shared/infrastructure/module"
)

func registerTasks(mux *asynq.ServeMux, deps module.Deps) {
	example.RegisterTasks(mux, deps)
}
`

func TestCompoundNamesTakeTheFormOfEachContext(t *testing.T) {
	n, err := NewNames("purchase_orders", "purchase_order", "", "orden de compra")
	require.NoError(t, err)
	assert.Equal(t, "purchaseorders", n.Package())
	rep := n.replacer("mod.v1")

	for in, want := range map[string]string{
		`import "mod.v1/internal/example/application/model"`: `import "mod.v1/internal/purchaseorders/application/model"`,
		"package example\n":                      "package purchaseorders\n",
		`example.RegisterWith(api, deps, ports)`: `purchaseorders.RegisterWith(api, deps, ports)`,
		`CreateExampleUseCase`:                   `CreatePurchaseOrderUseCase`,
		`ListExamplesQuery`:                      `ListPurchaseOrdersQuery`,
		`example, found, err := repo`:            `purchaseOrder, found, err := repo`,
		`func newExample(t)`:                     `func newPurchaseOrder(t)`,
		`const basePath = "/examples"`:           `const basePath = "/purchase-orders"`,
		`"/api/v1/examples/" + id`:               `"/api/v1/purchase-orders/" + id`,
		`func (ExampleEntity) TableName() string { return "examples" }`: `func (PurchaseOrderEntity) TableName() string { return "purchase_orders" }`,
		`IsUniqueViolation(err, "uq_examples_name")`:                    `IsUniqueViolation(err, "uq_purchase_orders_name")`,
		"`json:\"example_uuid\"`":                                       "`json:\"purchase_order_uuid\"`",
		`"example:write"`:                                               `"purchase_orders:write"`,
		`"example:status_changed"`:                                      `"purchase_orders:status_changed"`,
		`"EXAMPLE_NOT_FOUND"`:                                           `"PURCHASE_ORDERS_NOT_FOUND"`,
		`El ejemplo no existe.`:                                         `El orden de compra no existe.`,
	} {
		assert.Equal(t, want, rep.Replace(in))
	}
	assert.Equal(t, "usecase/list_purchase_orders.go", renamePath("usecase/list_examples.go", n))
	assert.Equal(t, "httpapi/purchase_order_handler.go", renamePath("httpapi/example_handler.go", n))
}

func TestAddAndRemoveRegistration(t *testing.T) {
	for _, tc := range []struct {
		r       registration
		fixture string
		call    string
	}{
		{registrations[0], modulesFixture, "\torders.Register(api, deps)\n}"},
		{registrations[1], tasksFixture, "\torders.RegisterTasks(mux, deps)\n}"},
	} {
		t.Run(tc.r.file, func(t *testing.T) {
			src := []byte(tc.fixture)

			out, err := addRegistration(tc.r, src, "myapp.v1.0.0", "orders")
			require.NoError(t, err)
			assert.Contains(t, string(out), "\t\"myapp.v1.0.0/internal/orders\"\n")
			assert.Contains(t, string(out), tc.call)

			_, err = addRegistration(tc.r, out, "myapp.v1.0.0", "orders")
			require.Error(t, err, "no registra dos veces")

			back, err := removeRegistration(tc.r, out, "myapp.v1.0.0", "orders")
			require.NoError(t, err)
			assert.Equal(t, string(src), string(back))
		})
	}
}

func TestOpenAPIBlocksRoundTrip(t *testing.T) {
	src := openAPIFixture
	n, err := NewNames("orders", "order", "", "pedido")
	require.NoError(t, err)

	out, err := addOpenAPIBlocks(src, n.Module, n.replacer("myapp.v1.0.0"))
	require.NoError(t, err)
	assert.Contains(t, out, "  /orders/{uuid}/activate:\n")
	assert.Contains(t, out, "    OrderEnvelope:\n")
	assert.Equal(t, 3, strings.Count(out, "# >>> module:orders"))
	assert.Less(t, strings.Index(out, "# <<< module:orders"), strings.Index(out, "# @generator:tags"))

	_, err = addOpenAPIBlocks(out, n.Module, n.replacer("myapp.v1.0.0"))
	require.Error(t, err, "no duplica bloques")

	back, err := removeOpenAPIBlocks(out, n.Module)
	require.NoError(t, err)
	assert.Equal(t, src, back)

	withoutRef, err := removeOpenAPIBlocks(src, refModule)
	require.NoError(t, err)
	assert.NotContains(t, withoutRef, "example")
	assert.Contains(t, withoutRef, "# @generator:paths")
}

// TestRealFilesHaveTheMarkers detecta que alguien borró un marcador del que depende
// el generador. Se omite si el proyecto ya quitó el módulo de referencia.
func TestRealFilesHaveTheMarkers(t *testing.T) {
	if _, err := os.Stat("../../internal/" + refModule); err != nil {
		t.Skip("el módulo de referencia ya se quitó")
	}
	doc, err := os.ReadFile("../../" + openAPIFile)
	require.NoError(t, err)
	_, err = addOpenAPIBlocks(string(doc), "orders", strings.NewReplacer())
	require.NoError(t, err)
	for _, r := range registrations {
		src, err := os.ReadFile("../../" + r.file)
		require.NoError(t, err)
		_, err = addRegistration(r, src, "myapp.v1.0.0", "orders")
		require.NoError(t, err, r.file)
	}
	std, err := os.ReadFile("../../" + standardFile)
	require.NoError(t, err)
	_, err = addOwner(string(std), "orders", "orders")
	require.NoError(t, err)
}

func TestAddOwner(t *testing.T) {
	doc := "x\n" + ownersHeader + ownerRow("examples", "example") + "\nresto"

	out, err := addOwner(doc, "orders", "orders")

	require.NoError(t, err)
	assert.Equal(t, "x\n"+ownersHeader+ownerRow("examples", "example")+ownerRow("orders", "orders")+"\nresto", out)
	_, err = addOwner(out, "orders", "orders")
	assert.Error(t, err)
}

func TestOpenAPIBlocksRejectTheExampleKey(t *testing.T) {
	bad := strings.Replace(openAPIFixture, "      operationId: activateExample\n",
		"      operationId: activateExample\n      parameters:\n        - schema: { type: string, example: 'x' }\n", 1)
	n, err := NewNames("orders", "order", "", "")
	require.NoError(t, err)

	_, err = addOpenAPIBlocks(bad, n.Module, n.replacer("myapp.v1.0.0"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "example:")
}

func TestTemplatesRenderValidCodeAndLeaveNoReference(t *testing.T) {
	for _, tc := range []struct{ name, entity, pkg string }{
		{"orders", "order", "orders"},
		{"purchase_orders", "purchase_order", "purchaseorders"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, err := NewNames(tc.name, tc.entity, "", "pedido")
			require.NoError(t, err)
			rep := n.replacer("myapp.v1.0.0")

			files, err := renderModule(n, rep) // falla si algún .go no es válido
			require.NoError(t, err)
			require.Contains(t, files, "module.go")
			blocks, err := templateOpenAPIBlocks(rep)
			require.NoError(t, err)
			for path, text := range files {
				assert.NotContains(t, strings.ToLower(strings.ReplaceAll(text, "auth.example.com", "")), "example", path)
				assert.NotContains(t, path, "example")
				assert.NotContains(t, strings.ToLower(text), "ejemplo", path)
			}
			assert.Contains(t, files["module.go"], "package "+tc.pkg+"\n")
			for _, b := range blocks {
				assert.NotContains(t, strings.ToLower(b), "example")
			}
		})
	}
}

func TestMinimalModuleHasNoOptionalPieces(t *testing.T) {
	n, err := NewNames("orders", "order", "", "")
	require.NoError(t, err)

	files, err := renderModule(n, n.replacer("myapp.v1.0.0"))

	require.NoError(t, err)
	for path, text := range files {
		for _, piece := range []string{"outbox", "asynq", "Activate", "ILIKE", "SortQuery"} {
			assert.NotContains(t, text, piece, path)
		}
	}
}
