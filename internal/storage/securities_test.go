//go:build integration

package storage

import (
	"context"
	"testing"
)

// M5 integration coverage for SearchSecurities (plan M5 B3): ranking por
// coincidencia exacta de ticker → prefijo de ticker → coincidencia de nombre,
// case-insensitive, límite, orden estable y escape de comodines ILIKE.

func TestSearchSecuritiesRanking(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	// "M5SR" exacto y por prefijo; "M5SRX" solo prefijo; "XXM5" solo por
	// nombre (el nombre contiene el término, el ticker no). "AAAA" solo por
	// nombre y con ticker que ordenaría ANTES que M5SRX si el ranking por
	// prefijo de ticker no se aplicara.
	seedSecurity(t, "M5SRX", "M5 Solo Prefijo")
	seedSecurity(t, "M5SR", "M5 Exacto Corp")
	seedSecurity(t, "XXM5", "Grupo M5SR Holdings")
	seedSecurity(t, "AAAA", "Holding M5SRX Corp")
	seedSecurity(t, "ZZZZ", "Zeta Corp")

	t.Run("ticker-exacto-primero", func(t *testing.T) {
		got, err := SearchSecurities(ctx, pool, "M5SR", 10)
		if err != nil {
			t.Fatalf("SearchSecurities: %v", err)
		}
		if len(got) != 4 {
			t.Fatalf("se esperaban 4 resultados (exacto + prefijo + nombres), hay %d: %s", len(got), tickersOf(got))
		}
		want := []string{"M5SR", "M5SRX", "AAAA", "XXM5"} // AAAA < XXM5 (ticker ASC)
		for i, w := range want {
			if got[i].Ticker != w {
				t.Fatalf("ranking %d: se esperaba %s, hay %s (orden: %s)", i, w, got[i].Ticker, tickersOf(got))
			}
		}
	})

	t.Run("prefijo-gana-a-nombre-sin-importar-mayusculas", func(t *testing.T) {
		// "m5srx" en minúsculas: el prefijo de ticker (rango 1) debe ir antes
		// que la coincidencia de nombre de AAAA (rango 2), aunque AAAA ordene
		// antes por ticker.
		got, err := SearchSecurities(ctx, pool, "m5srx", 10)
		if err != nil {
			t.Fatalf("SearchSecurities: %v", err)
		}
		if len(got) != 2 || got[0].Ticker != "M5SRX" || got[1].Ticker != "AAAA" {
			t.Fatalf("prefijo de ticker debe rankear por delante del nombre: %s", tickersOf(got))
		}
	})

	t.Run("case-insensitive", func(t *testing.T) {
		got, err := SearchSecurities(ctx, pool, "m5sr", 10)
		if err != nil {
			t.Fatalf("SearchSecurities: %v", err)
		}
		if len(got) == 0 || got[0].Ticker != "M5SR" {
			t.Fatalf("búsqueda en minúsculas debe encontrar M5SR primero: %s", tickersOf(got))
		}
	})

	t.Run("por-nombre-subcadena", func(t *testing.T) {
		got, err := SearchSecurities(ctx, pool, "solo prefijo", 10)
		if err != nil {
			t.Fatalf("SearchSecurities: %v", err)
		}
		if len(got) != 1 || got[0].Ticker != "M5SRX" {
			t.Fatalf("se esperaba M5SRX por nombre, hay %s", tickersOf(got))
		}
	})

	t.Run("limite-y-orden-estable", func(t *testing.T) {
		got, err := SearchSecurities(ctx, pool, "M5", 2)
		if err != nil {
			t.Fatalf("SearchSecurities: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("Límite no respetado: %d resultados", len(got))
		}
		// Repetida: mismo resultado (orden estable, no aleatorio).
		again, err := SearchSecurities(ctx, pool, "M5", 2)
		if err != nil {
			t.Fatalf("SearchSecurities (2ª): %v", err)
		}
		if tickersOf(again) != tickersOf(got) {
			t.Fatalf("orden inestable: %s vs %s", tickersOf(got), tickersOf(again))
		}
	})

	t.Run("comodines-escapan", func(t *testing.T) {
		// '%' debe buscarse literal: no devuelve el catálogo entero.
		got, err := SearchSecurities(ctx, pool, "%", 10)
		if err != nil {
			t.Fatalf("SearchSecurities: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("'%%' no debe actuar como comodín: %s", tickersOf(got))
		}
	})

	t.Run("sin-resultados-devuelve-lista-vacia", func(t *testing.T) {
		got, err := SearchSecurities(ctx, pool, "qqqqqqqq", 10)
		if err != nil {
			t.Fatalf("SearchSecurities: %v", err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("se esperaba slice vacío (no nil), got %v", got)
		}
	})
}

func tickersOf(secs []Security) string {
	out := ""
	for _, s := range secs {
		if out != "" {
			out += ","
		}
		out += s.Ticker
	}
	return out
}
