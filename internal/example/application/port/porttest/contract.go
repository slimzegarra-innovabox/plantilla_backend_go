package porttest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"myapp.v1.0.0/internal/example/application/apperr"
	"myapp.v1.0.0/internal/example/application/model"
	"myapp.v1.0.0/internal/example/application/port"
	shareddto "myapp.v1.0.0/internal/shared/application/dto"
)

// RunExampleRepositoryContract verifica que un ExampleRepository cumple el contrato
// del puerto. newRepo debe devolver un repositorio VACÍO en cada llamada.
func RunExampleRepositoryContract(t *testing.T, newRepo func(t *testing.T) port.ExampleRepository) {
	t.Helper()
	// Microsegundos: la precisión de TIMESTAMPTZ en PostgreSQL.
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	t.Run("Create y FindByUUID conservan todos los campos", func(t *testing.T) {
		repo := newRepo(t)
		e := NewExample(t, "Alpha", base)

		created, err := repo.Create(ctx, e)
		mustNoErr(t, err)
		if created.ID() <= 0 || created.Version() != 1 {
			t.Fatalf("Create: id=%d version=%d, se esperaba id > 0 y versión 1", created.ID(), created.Version())
		}

		got, found, err := repo.FindByUUID(ctx, e.UUID())
		mustNoErr(t, err)
		if !found {
			t.Fatal("FindByUUID: no encontró el example recién creado")
		}
		assertSame(t, created, got)
		if got.Name().String() != "Alpha" || got.Status() != model.ExampleStatusPending ||
			got.CreatedBy() != e.CreatedBy() || !got.CreatedAt().Equal(base) {
			t.Fatalf("FindByUUID: campos distintos de los creados: %+v", describe(got))
		}
	})

	t.Run("FindByUUID de un uuid inexistente devuelve found=false sin error", func(t *testing.T) {
		got, found, err := newRepo(t).FindByUUID(ctx, uuid.New())
		mustNoErr(t, err)
		if found || got != nil {
			t.Fatalf("FindByUUID: found=%v example=%v, se esperaba false y nil", found, got)
		}
	})

	t.Run("Create con un nombre repetido (sin distinguir mayúsculas) es ErrExampleNameTaken", func(t *testing.T) {
		repo := newRepo(t)
		_, err := repo.Create(ctx, NewExample(t, "Alpha", base))
		mustNoErr(t, err)

		_, err = repo.Create(ctx, NewExample(t, "ALPHA", base))
		mustBe(t, err, apperr.ErrExampleNameTaken)
	})

	t.Run("Update persiste los cambios e incrementa la versión", func(t *testing.T) {
		repo := newRepo(t)
		created, err := repo.Create(ctx, NewExample(t, "Alpha", base))
		mustNoErr(t, err)

		later := base.Add(time.Hour)
		created.Rename(MustName(t, "Beta"), later)
		mustNoErr(t, created.Activate(later))
		updated, err := repo.Update(ctx, created)
		mustNoErr(t, err)

		if updated.Version() != 2 || updated.Name().String() != "Beta" || updated.Status() != model.ExampleStatusActive ||
			!updated.UpdatedAt().Equal(later) || !updated.CreatedAt().Equal(base) || updated.ID() != created.ID() {
			t.Fatalf("Update: resultado inesperado %+v", describe(updated))
		}
		got, _, err := repo.FindByUUID(ctx, created.UUID())
		mustNoErr(t, err)
		assertSame(t, updated, got)
	})

	t.Run("Update con una versión vieja es ErrExampleVersionConflict y no escribe", func(t *testing.T) {
		repo := newRepo(t)
		created, err := repo.Create(ctx, NewExample(t, "Alpha", base))
		mustNoErr(t, err)
		stale, _, err := repo.FindByUUID(ctx, created.UUID())
		mustNoErr(t, err)

		created.Rename(MustName(t, "Primero"), base)
		_, err = repo.Update(ctx, created)
		mustNoErr(t, err)

		stale.Rename(MustName(t, "Segundo"), base)
		_, err = repo.Update(ctx, stale)
		mustBe(t, err, apperr.ErrExampleVersionConflict)

		got, _, err := repo.FindByUUID(ctx, created.UUID())
		mustNoErr(t, err)
		if got.Name().String() != "Primero" || got.Version() != 2 {
			t.Fatalf("la escritura con versión vieja modificó el registro: %+v", describe(got))
		}
	})

	t.Run("Update de un example inexistente es ErrExampleNotFound", func(t *testing.T) {
		_, err := newRepo(t).Update(ctx, NewExample(t, "Fantasma", base))
		mustBe(t, err, apperr.ErrExampleNotFound)
	})

	t.Run("Update a un nombre usado por otro es ErrExampleNameTaken", func(t *testing.T) {
		repo := newRepo(t)
		_, err := repo.Create(ctx, NewExample(t, "Alpha", base))
		mustNoErr(t, err)
		other, err := repo.Create(ctx, NewExample(t, "Beta", base))
		mustNoErr(t, err)

		other.Rename(MustName(t, "alpha"), base)
		_, err = repo.Update(ctx, other)
		mustBe(t, err, apperr.ErrExampleNameTaken)
	})

	t.Run("Update conservando su propio nombre no es un conflicto", func(t *testing.T) {
		repo := newRepo(t)
		created, err := repo.Create(ctx, NewExample(t, "Alpha", base))
		mustNoErr(t, err)

		created.Rename(MustName(t, "ALPHA"), base)
		_, err = repo.Update(ctx, created)
		mustNoErr(t, err)
	})

	t.Run("List filtra, ordena por creación descendente y pagina", func(t *testing.T) {
		repo := newRepo(t)
		var names []string
		for i := range 5 {
			e := NewExample(t, fmt.Sprintf("Item %d", i), base.Add(time.Duration(i)*time.Minute))
			created, err := repo.Create(ctx, e)
			mustNoErr(t, err)
			if i%2 == 0 {
				mustNoErr(t, created.Activate(base))
				_, err = repo.Update(ctx, created)
				mustNoErr(t, err)
			}
			names = append(names, e.Name().String())
		}

		page, err := repo.List(ctx, port.ExampleFilter{}, nil, shareddto.NewPageQuery(1, 2))
		mustNoErr(t, err)
		if page.TotalElements != 5 || page.TotalPages != 3 || len(page.Items) != 2 {
			t.Fatalf("List sin filtro: total=%d páginas=%d items=%d", page.TotalElements, page.TotalPages, len(page.Items))
		}
		if page.Items[0].Name().String() != names[4] || page.Items[1].Name().String() != names[3] {
			t.Fatalf("List: orden inesperado %s, %s", page.Items[0].Name(), page.Items[1].Name())
		}

		last, err := repo.List(ctx, port.ExampleFilter{}, nil, shareddto.NewPageQuery(3, 2))
		mustNoErr(t, err)
		if len(last.Items) != 1 || last.Items[0].Name().String() != names[0] || last.Page != 3 {
			t.Fatalf("List última página: %d items, página %d", len(last.Items), last.Page)
		}

		active, err := repo.List(ctx, port.ExampleFilter{Status: model.ExampleStatusActive}, nil, shareddto.NewPageQuery(1, 10))
		mustNoErr(t, err)
		if active.TotalElements != 3 {
			t.Fatalf("List filtrado por active: total=%d, se esperaban 3", active.TotalElements)
		}
		for _, e := range active.Items {
			if e.Status() != model.ExampleStatusActive {
				t.Fatalf("List filtrado devolvió un example %s", e.Status())
			}
		}

		beyond, err := repo.List(ctx, port.ExampleFilter{}, nil, shareddto.NewPageQuery(9, 10))
		mustNoErr(t, err)
		if beyond.Items == nil || len(beyond.Items) != 0 || beyond.TotalElements != 5 {
			t.Fatalf("List fuera de rango: items=%v total=%d", beyond.Items, beyond.TotalElements)
		}
	})

	t.Run("List busca por nombre sin distinguir mayúsculas, con comodines literales", func(t *testing.T) {
		repo := newRepo(t)
		for i, name := range []string{"Alpha Uno", "alpha dos", "Beta", "100% real", "a_b", "axb"} {
			_, err := repo.Create(ctx, NewExample(t, name, base.Add(time.Duration(i)*time.Minute)))
			mustNoErr(t, err)
		}
		for search, want := range map[string][]string{
			"ALPHA": {"alpha dos", "Alpha Uno"}, // creación descendente
			"%":     {"100% real"},              // % es literal, no comodín
			"_":     {"a_b"},                    // _ es literal: no coincide con "axb"
			"zzz":   {},
		} {
			page, err := repo.List(ctx, port.ExampleFilter{Search: search}, nil, shareddto.NewPageQuery(1, 10))
			mustNoErr(t, err)
			got := []string{}
			for _, e := range page.Items {
				got = append(got, e.Name().String())
			}
			if fmt.Sprint(got) != fmt.Sprint(want) || page.TotalElements != int64(len(want)) {
				t.Fatalf("List(Search=%q) = %v (total %d), se esperaba %v", search, got, page.TotalElements, want)
			}
		}
	})

	t.Run("List ordena por nombre sin distinguir mayúsculas, ascendente y descendente", func(t *testing.T) {
		repo := newRepo(t)
		for i, name := range []string{"bravo", "Alfa", "charlie"} {
			_, err := repo.Create(ctx, NewExample(t, name, base.Add(time.Duration(i)*time.Minute)))
			mustNoErr(t, err)
		}
		for _, tc := range []struct {
			sort shareddto.Sort
			want string
		}{
			{shareddto.Sort{{Field: "name"}}, "[Alfa bravo charlie]"},
			{shareddto.Sort{{Field: "name", Desc: true}}, "[charlie bravo Alfa]"},
			{shareddto.Sort{{Field: "createdAt"}}, "[bravo Alfa charlie]"},
		} {
			page, err := repo.List(ctx, port.ExampleFilter{}, tc.sort, shareddto.NewPageQuery(1, 10))
			mustNoErr(t, err)
			var got []string
			for _, e := range page.Items {
				got = append(got, e.Name().String())
			}
			if fmt.Sprint(got) != tc.want {
				t.Fatalf("List(sort=%v) = %v, se esperaba %s", tc.sort, got, tc.want)
			}
		}
	})

	t.Run("Delete elimina solo con la versión vigente", func(t *testing.T) {
		repo := newRepo(t)
		created, err := repo.Create(ctx, NewExample(t, "Borrable", base))
		mustNoErr(t, err)
		stale := *created
		created.Rename(MustName(t, "Borrable 2"), base)
		_, err = repo.Update(ctx, created)
		mustNoErr(t, err)

		if err := repo.Delete(ctx, &stale); !errors.Is(err, apperr.ErrExampleVersionConflict) {
			t.Fatalf("Delete con versión vieja: %v, se esperaba ErrExampleVersionConflict", err)
		}
		current, _, err := repo.FindByUUID(ctx, created.UUID())
		mustNoErr(t, err)
		mustNoErr(t, repo.Delete(ctx, current))
		if _, found, err := repo.FindByUUID(ctx, created.UUID()); err != nil || found {
			t.Fatalf("después de Delete: found=%v err=%v", found, err)
		}
		if err := repo.Delete(ctx, current); !errors.Is(err, apperr.ErrExampleNotFound) {
			t.Fatalf("Delete de un example inexistente: %v, se esperaba ErrExampleNotFound", err)
		}
	})
}

//--------------------------------------------------------------
// HELPERS (también los usan las pruebas de los casos de uso)
//--------------------------------------------------------------

// MustName construye un ExampleName válido o detiene la prueba.
func MustName(t *testing.T, raw string) model.ExampleName {
	t.Helper()
	n, err := model.NewExampleName(raw)
	mustNoErr(t, err)
	return n
}

// NewExample construye un example pending válido o detiene la prueba.
func NewExample(t *testing.T, name string, now time.Time) *model.Example {
	t.Helper()
	e, err := model.NewExample(MustName(t, name), uuid.New(), now)
	mustNoErr(t, err)
	return e
}

func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
}

func mustBe(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, se esperaba %v", err, want)
	}
}

func assertSame(t *testing.T, want, got *model.Example) {
	t.Helper()
	if describe(want) != describe(got) {
		t.Fatalf("example distinto:\n  esperado %s\n  obtenido %s", describe(want), describe(got))
	}
}

func describe(e *model.Example) string {
	return fmt.Sprintf("{id:%d uuid:%s name:%q status:%s version:%d createdBy:%s createdAt:%s updatedAt:%s}",
		e.ID(), e.UUID(), e.Name(), e.Status(), e.Version(), e.CreatedBy(),
		e.CreatedAt().UTC().Format(time.RFC3339Nano), e.UpdatedAt().UTC().Format(time.RFC3339Nano))
}
