//go:build integration

package edgar

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
)

// TestM6cT1ConceptMapMatchesGoDictionary ata el catálogo Go que decide el
// canonizado (`conceptMap`, el único que manda) con el diccionario PERSISTIDO
// (`xbrl_concept_map`), que es lo que se consulta y audita. Antes de W2 los dos
// podían divergir en silencio: nadie comprobaba que la tabla contuviera los
// mismos conceptos, las mismas unidades, ni que las `notes` documentaran los
// tags de Az1 que el código usa.
//
// NO ESCRIBE NADA. Sólo lee `xbrl_concept_map`; lo único que muta la BD de test
// son las migraciones (`RunMigrations`), que son idempotentes y son las que
// crean/pueblan la tabla. Corre contra `abys_test` vía `make integration`:
// `EnsureTestDatabase` aborta si el nombre de la BD no acaba en `_test`, así que
// no hay forma de que esto escriba en la BD de desarrollo.
//
// qué ata, exactamente:
//  1. el conjunto de canónicos de `xbrl_concept_map` == el conjunto de canónicos
//     de `CanonicalConcepts()` (los dos sentidos: ni sobra ni falta ninguno);
//  2. `unit_expected` == `Unit` del diccionario Go para cada canónico;
//  3. cada uno de los 10 tags de Az1 aparece LITERALMENTE en las `notes` de la
//     fila de su canónico (que es donde queda escrito por qué existen y qué se
//     excluyó a propósito).
func TestM6cT1ConceptMapMatchesGoDictionary(t *testing.T) {
	pool := normalizeRequirePool(t)
	conceptMapSetupReadOnly(t, pool)
	ctx := context.Background()

	type mapRow struct {
		canonical string
		unit      string
		notes     string
	}
	rows, err := pool.Query(ctx,
		`SELECT canonical_name, coalesce(unit_expected,''), coalesce(notes,'')
		   FROM xbrl_concept_map ORDER BY canonical_name`)
	if err != nil {
		t.Fatalf("consulta xbrl_concept_map: %v", err)
	}
	db := map[string]mapRow{}
	for rows.Next() {
		var r mapRow
		if err := rows.Scan(&r.canonical, &r.unit, &r.notes); err != nil {
			rows.Close()
			t.Fatalf("scan xbrl_concept_map: %v", err)
		}
		db[r.canonical] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iteración xbrl_concept_map: %v", err)
	}
	if len(db) == 0 {
		t.Fatal("xbrl_concept_map vacía: las migraciones no se aplicaron")
	}

	// CanonicalConcepts() es tag -> concepto; aquí se colapsa a
	// concepto -> unidad, que es la granularidad de la tabla.
	goDict := map[string]string{}
	for _, cc := range CanonicalConcepts() {
		if prev, dup := goDict[cc.Canonical]; dup && prev != cc.Unit {
			t.Fatalf("el diccionario Go declara dos unidades para %q: %q y %q", cc.Canonical, prev, cc.Unit)
		}
		goDict[cc.Canonical] = cc.Unit
	}

	// (1) Los conjuntos deben ser idénticos en los DOS sentidos. Un canónico que
	// sólo exista en Go es una fila que falta en la tabla (y `/health` contaría
	// mal); uno que sólo exista en la tabla es una promesa que el código no
	// cumple.
	for canonical := range goDict {
		if _, ok := db[canonical]; !ok {
			t.Errorf("xbrl_concept_map: falta la fila del canónico %q (está en conceptMap de Go)", canonical)
		}
	}
	for canonical := range db {
		if _, ok := goDict[canonical]; !ok {
			t.Errorf("xbrl_concept_map: la fila %q no existe en conceptMap de Go (diccionario documentado que nadie cumple)", canonical)
		}
	}
	if len(db) != len(goDict) {
		t.Errorf("número de canónicos: Go=%d, xbrl_concept_map=%d", len(goDict), len(db))
	}

	// (2) Misma unidad declarada en los dos sitios.
	for canonical, wantUnit := range goDict {
		r, ok := db[canonical]
		if !ok {
			continue
		}
		if r.unit != wantUnit {
			t.Errorf("xbrl_concept_map %s: unit_expected=%q, el diccionario Go declara %q", canonical, r.unit, wantUnit)
		}
	}

	// (3) Cada tag de Az1 tiene que aparecer literalmente en las `notes` de su
	// canónico: es lo que convierte la tabla en documentación auditable del
	// criterio multi-etiqueta (y de sus exclusiones).
	for _, tc := range interestTaxTags {
		r, ok := db[tc.canonical]
		if !ok {
			t.Errorf("xbrl_concept_map: no hay fila para %q, así que el tag %q no queda documentado", tc.canonical, tc.tag)
			continue
		}
		if !strings.Contains(r.notes, tc.tag) {
			t.Errorf("xbrl_concept_map %s: las notes no mencionan el tag %q (P%d): %q",
				tc.canonical, tc.tag, tc.priority, r.notes)
		}
	}
	// La lista de 019 tiene que seguir siendo la de Az1: sin esto, un catálogo
	// que añadiera un tag y olvidara documentarlo pasaría el punto (3) por
	// trivialidad (sólo se comprueban los tags que el test conoce).
	if len(interestTaxTags) != 10 {
		t.Fatalf("Az1 fija 10 tags en interest_expense/income_tax_expense/pretax_income, la lista tiene %d", len(interestTaxTags))
	}
}

// conceptMapSetupReadOnly prepara la BD de test sin truncar nada: sólo el guard
// de nombre (`*_test`) y las migraciones. No hay TRUNCATE a propósito — este
// test comparte la tabla `xbrl_concept_map` con el resto de la suite y no
// necesita tocar nada.
func conceptMapSetupReadOnly(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := storage.EnsureTestDatabase(ctx, pool); err != nil {
		t.Fatalf("guard de BD de test falló (este test no escribe, pero el DSN debe ser de test): %v", err)
	}
	if err := storage.RunMigrations(ctx, pool, "../../../migrations"); err != nil {
		t.Fatalf("migraciones: %v", err)
	}
}
