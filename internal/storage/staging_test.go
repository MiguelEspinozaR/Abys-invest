package storage

import (
	"strings"
	"testing"
)

// TestSortDedupCIKs: la lista del universo acaba en el argumento `-companies`
// del collector, así que la forma de la lista ES parte del contrato:
// determinista (ordenada), sin duplicados y sin basura. Sin este test, un
// cambio en el selector podría producir un `-companies` no determinista y la
// re-ingesta dejaría de ser reproducible (y el log, comparable entre pasadas).
func TestSortDedupCIKs(t *testing.T) {
	got, err := SortDedupCIKs([]string{"0001045810", "0000320193", "0000320193", " 0000320193 ", "", "  "})
	if err != nil {
		t.Fatalf("SortDedupCIKs: %v", err)
	}
	want := []string{"0000320193", "0001045810"}
	if len(got) != len(want) {
		t.Fatalf("SortDedupCIKs: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SortDedupCIKs: got %v, want %v (orden ascendente, sin duplicados)", got, want)
		}
	}

	// Entrada vacía = universo vacío: no es un error aquí (quien llama decide
	// qué hacer; el CLI lo convierte en mensaje y sale 1).
	empty, err := SortDedupCIKs(nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("SortDedupCIKs(nil): got %v, %v; se esperaba vacío sin error", empty, err)
	}
}

// TestSortDedupCIKsRejectsGarbage: una fila de staging con un CIK que no es un
// CIK (columna sin CHECK, y un SELECT puede filter lo que sea en el futuro)
// no debe acabar dentro de `-companies`: se rechaza en voz alta en lugar de
// dejar que el collector la resuelva como si fuera un ticker.
func TestSortDedupCIKsRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"AAPL", "000032019a", "320193 123", "00003201933"} {
		if _, err := SortDedupCIKs([]string{bad}); err == nil {
			t.Errorf("SortDedupCIKs: %q debe rechazarse (no es un CIK de 10 dígitos)", bad)
		}
	}
}

// TestStagedNormalizedCIKsSQLIsReadOnlyAndPinned fija las DOS propiedades del
// selector del universo por defecto que importan, y que sólo se ven en el SQL:
//
//  1. es de SOLO LECTURA. `make reingest-fundamentals` lo ejecuta ANTES de
//     tocar nada precisamente para poder mostrarle al operador el universo que
//     va a re-canonizar; si esta constante dullera un INSERT/UPDATE/DELETE, el
//     "listar" escribiría.
//  2. está anclado a `payload_type = 'company_facts'` + `normalized` y NO a
//     `securities`. El plan (Az6) acota la re-ingesta a las empresas ya
//     ingeridas; `securities` son las ~10.400 del catálogo SEC, de las que ~42
//     se han descargado alguna vez.
func TestStagedNormalizedCIKsSQLIsReadOnlyAndPinned(t *testing.T) {
	sql := strings.ToLower(StagedNormalizedCIKsSQL)
	if !strings.HasPrefix(strings.TrimSpace(sql), "select") {
		t.Fatalf("el selector del universo debe ser un SELECT, no: %q", StagedNormalizedCIKsSQL)
	}
	for _, forbidden := range []string{"insert", "update", "delete", "truncate", "drop", "alter"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("el selector del universo no puede contener %q (debe ser sólo lectura): %q", forbidden, StagedNormalizedCIKsSQL)
		}
	}
	for _, required := range []string{"edgar_staging", "company_facts", "normalized", "group by cik"} {
		if !strings.Contains(sql, required) {
			t.Errorf("el selector del universo debe anclarse a %q: %q", required, StagedNormalizedCIKsSQL)
		}
	}
	if strings.Contains(sql, "securities") || strings.Contains(sql, "fundamentals") {
		t.Errorf("el universo por defecto NO se deriva de securities/fundamentals (Az6): %q", StagedNormalizedCIKsSQL)
	}
}
