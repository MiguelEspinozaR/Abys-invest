//go:build integration

package api_test

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"testing"

	"github.com/miky/abys-invest/internal/api"
)

// Tests E2E de la ampliación M5.2 de GET /health (plan §A6). Reusan el pool y el
// helper `do()` del TestMain de m3_integration_test.go (tolerantes a los tipos
// nuevos del body), de modo que no se tautologiza el tipo de producción.
//
// Ejecución: DATABASE_URL=... go test ./internal/api -p 1 -tags=integration -count=1

// TestHealthAmpliado: con BD conectada, /health responde 200 con los 3 campos
// base del contrato M1 y las 4 métricas nuevas con el tipo y la presencia
// correctos (CA-M5.2-1).
func TestHealthAmpliado(t *testing.T) {
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	router := api.NewRouter(pool)

	rec, body := do(t, router, http.MethodGet, "/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("/health esperado 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if body["status"] != "ok" || body["database"] != "connected" {
		t.Fatalf("body inesperado: %s", rec.Body.String())
	}
	if v, _ := body["version"].(string); v == "" {
		t.Fatalf("version no vacío obligatorio: %s", rec.Body.String())
	}

	// latency_ms: número >= 0 (0.00 es legal; el tipo de producción lo apunta
	// justamente para que `omitempty` no lo borre).
	latency, ok := body["latency_ms"].(float64)
	if !ok {
		t.Fatalf("latency_ms debe ser número, got %T: %s", body["latency_ms"], rec.Body.String())
	}
	if latency < 0 {
		t.Fatalf("latency_ms no puede ser negativo: %v", latency)
	}

	pgVersion, _ := body["postgres_version"].(string)
	if !regexp.MustCompile(`^\d+\.\d+$`).MatchString(pgVersion) {
		t.Fatalf("postgres_version inesperado: %q", pgVersion)
	}
	if size, _ := body["db_size"].(string); size == "" {
		t.Fatalf("db_size no vacío obligatorio: %s", rec.Body.String())
	}

	tables, ok := body["tables"].([]any)
	if !ok {
		t.Fatalf("tables debe ser array, got %T: %s", body["tables"], rec.Body.String())
	}
	// 12 desde M6b (las 9 de M5.2 + growth_metrics + wacc_metrics +
	// valuation_results). La lista esperada se declara aquí a propósito: si se
	// reutilizara la constante interna de producción el test sería tautológico.
	wantTables := []string{
		"securities", "daily_prices", "fundamentals", "derived_metrics", "scores",
		"growth_metrics", "wacc_metrics", "valuation_results", "watchlist",
		"macro_series", "edgar_staging", "xbrl_concept_map",
	}
	if len(tables) != len(wantTables) {
		t.Fatalf("se esperaban %d tablas, got %d", len(wantTables), len(tables))
	}
	rows := map[string]int64{}
	var sum int64
	for _, raw := range tables {
		row, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("fila de tables inesperada: %T", raw)
		}
		name, _ := row["name"].(string)
		n, ok := row["rows"].(float64)
		if name == "" || !ok {
			t.Fatalf("fila inválida (name string, rows número): %v", row)
		}
		if n < 0 {
			t.Fatalf("rows no puede ser negativo: %v", row)
		}
		rows[name] = int64(n)
		sum += int64(n)
	}
	for _, want := range wantTables {
		if _, ok := rows[want]; !ok {
			t.Errorf("tabla %q ausente en /health", want)
		}
	}
	if rows["securities"] <= 0 {
		t.Fatalf("securities.rows debe ser > 0, got %d", rows["securities"])
	}
	if sum <= 0 {
		t.Fatalf("la suma de filas debe ser > 0, got %d", sum)
	}
}

// TestHealthTablesCoincidenConEsquema (M5.2, riesgo R2): test ANTI-DERIVA. La
// lista de tablas de /health es una constante del código (healthTableNames); si
// una migración futura añade o renombra una tabla del esquema público, este test
// falla y obliga a actualizar la constante (y el endpoint).
func TestHealthTablesCoincidenConEsquema(t *testing.T) {
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	router := api.NewRouter(pool)

	rec, body := do(t, router, http.MethodGet, "/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("/health esperado 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	tables, ok := body["tables"].([]any)
	if !ok || len(tables) == 0 {
		t.Fatalf("tables ausente: %s", rec.Body.String())
	}
	var reported []string
	for _, raw := range tables {
		row, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("fila de tables inesperada: %T", raw)
		}
		name, _ := row["name"].(string)
		reported = append(reported, name)
	}

	ctx := context.Background()
	rows, err := pool.Query(ctx,
		`SELECT table_name FROM information_schema.tables WHERE table_schema = 'public'`)
	if err != nil {
		t.Fatalf("information_schema: %v", err)
	}
	defer rows.Close()
	var real []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		real = append(real, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("information_schema: %v", err)
	}

	sort.Strings(reported)
	sort.Strings(real)
	if len(reported) != len(real) {
		t.Fatalf("/health reporta %v y el esquema público tiene %v: actualiza healthTableNames",
			reported, real)
	}
	for i := range real {
		if reported[i] != real[i] {
			t.Fatalf("/health reporta %v y el esquema público tiene %v: actualiza healthTableNames",
				reported, real)
		}
	}
}
