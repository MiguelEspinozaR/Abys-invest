//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/miky/abys-invest/internal/api"
	"github.com/miky/abys-invest/internal/storage"
	"github.com/miky/abys-invest/internal/testsupport"
)

// Integración M6a: los bloques aditivos `growth` y `wacc` de
// GET /valuation/{ticker} (D4) exponen la última fila de
// growth_metrics / wacc_metrics sin alterar el resto de la respuesta ni las
// fórmulas de valoración (que en M6a siguen usando la configuración por defecto).

func m6Seed(t *testing.T, ticker string, withBeta bool) int64 {
	t.Helper()
	ctx := context.Background()
	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
		Ticker: ticker, CIK: "96" + ticker[len(ticker)-1:] + "00001", Name: "M6 Test Co " + ticker,
		Type: "stock", Currency: "USD", Status: "active", Sector: strPtr("Technology"),
	})
	if err != nil {
		t.Fatalf("upsert security: %v", err)
	}
	fy, unit, src := "FY", "USD", "test"
	rows := []storage.Fundamental{}
	for i := 0; i < 5; i++ {
		end := time.Date(2021+i, 9, 30, 0, 0, 0, 0, time.UTC)
		start := end.AddDate(0, 0, -365)
		filing := end.AddDate(0, 2, 0)
		fiscalYear := int16(end.Year())
		g := func(base float64) float64 {
			out := base
			for k := 0; k < i; k++ {
				out *= 1.10
			}
			return out
		}
		add := func(concept string, v float64, instant bool) {
			row := storage.Fundamental{
				SecurityID: sec.ID, Concept: concept, Value: &v, Unit: &unit,
				PeriodType: "duration", PeriodStart: &start, PeriodEnd: end,
				FiscalYear: &fiscalYear, FiscalPeriod: &fy, FilingDate: &filing, Source: src,
			}
			if instant {
				row.PeriodType = "instant"
				row.PeriodStart = nil // instant facts: period_start IS NULL (EDGAR real)
				row.PeriodEnd = end
			}
			rows = append(rows, row)
		}
		add("revenues", g(300_000), false)
		add("eps_diluted", g(5), false)
		add("free_cash_flow", g(80_000), false)
		add("shares_outstanding", 1_000, true)
		add("total_debt", 400_000, true)
	}
	prices := make([]storage.DailyPrice, 0, 20)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		c := 150 + float64(i)
		prices = append(prices, storage.DailyPrice{
			SecurityID: sec.ID, Date: start.AddDate(0, 0, i),
			Close: c, AdjustedClose: c, Open: &c, High: &c, Low: &c, Source: src,
		})
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := storage.UpsertFundamentals(ctx, tx, rows); err != nil {
		t.Fatalf("fundamentals: %v", err)
	}
	if err := storage.UpsertDailyPrices(ctx, tx, prices); err != nil {
		t.Fatalf("prices: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if withBeta {
		beta := 1.2
		if err := storage.UpdateSecurityReference(ctx, pool, ticker, nil, nil, &beta); err != nil {
			t.Fatalf("beta: %v", err)
		}
		// ADR D29: la observación canónica, fechada en la última barra de precio
		// del fixture (la fecha de valoración que usa el pipeline).
		testsupport.SeedBetaAsOf(t, pool, sec.ID, prices[len(prices)-1].Date, beta)
	}
	return sec.ID
}

func strPtr(s string) *string { return &s }

func m6Valuation(t *testing.T, router http.Handler, ticker string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/valuation/"+ticker, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET valuation %s: %d %s", ticker, rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v (%s)", err, rec.Body.String())
	}
	return body
}

// TestValuationExponeGrowthYWacc: con filas persistidas la respuesta incluye
// ambos bloques con la taxonomía del engine, omitempty para nils, as_of y
// model_version, y el resto de campos de M3 siguen intactos.
func TestValuationExponeGrowthYWacc(t *testing.T) {
	const ticker = "M6TST"
	secID := m6Seed(t, ticker, true)
	ctx := context.Background()

	asOf := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	rate, eps3, eps5 := 10.0, 10.0, 10.0
	gm := &storage.GrowthMetric{
		SecurityID: secID, AsOf: asOf, AvailableAt: &asOf,
		NormalizedGrowthRate: &rate, Source: "eps_fcf_3y", Confidence: "high",
		EPSCAGR3y: &eps3, EPSCAGR5y: &eps5, Clamped: false, ModelVersion: "1.0.0",
	}
	waccValue, ke, kd, rf, erp, beta := 9.4, 11.3, 4.8, 4.5, 5.5, 1.2
	wm := &storage.WaccMetric{
		SecurityID: secID, AsOf: asOf, AvailableAt: &asOf,
		Wacc: &waccValue, CostOfEquity: &ke, CostOfDebtAfterTax: &kd,
		RiskFreeRate: &rf, EquityRiskPremium: &erp, Beta: &beta, BetaObserved: true,
		Source: "capm_hybrid", Confidence: "medium", ModelVersion: "1.0.0",
	}
	for _, row := range []any{gm, wm} {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		switch r := row.(type) {
		case *storage.GrowthMetric:
			err = storage.UpsertGrowthMetric(ctx, tx, r)
		case *storage.WaccMetric:
			err = storage.UpsertWaccMetric(ctx, tx, r)
		}
		if err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}

	body := m6Valuation(t, api.NewRouter(pool), ticker)

	g, ok := body["growth"].(map[string]any)
	if !ok {
		t.Fatalf("falta el bloque growth: %v", body)
	}
	if g["normalized_growth_rate"] != 10.0 || g["source"] != "eps_fcf_3y" || g["confidence"] != "high" {
		t.Fatalf("growth inesperado: %v", g)
	}
	if g["eps_cagr_3y"] != 10.0 || g["eps_cagr_5y"] != 10.0 {
		t.Fatalf("cagr: %v", g)
	}
	if _, present := g["revenue_cagr_3y"]; present {
		t.Fatalf("los nils no deben aparecer (omitempty): %v", g)
	}
	if g["clamped"] != false || g["revenue_discrepancy"] != false {
		t.Fatalf("flags: %v", g)
	}
	if g["as_of"] != "2026-08-20" || g["model_version"] != "1.0.0" {
		t.Fatalf("trazabilidad: %v", g)
	}

	w, ok := body["wacc"].(map[string]any)
	if !ok {
		t.Fatalf("falta el bloque wacc: %v", body)
	}
	if w["wacc"] != 9.4 || w["cost_of_equity"] != 11.3 || w["cost_of_debt_after_tax"] != 4.8 {
		t.Fatalf("wacc inesperado: %v", w)
	}
	if w["beta"] != 1.2 || w["beta_observed"] != true {
		t.Fatalf("beta: %v", w)
	}
	if w["source"] != "capm_hybrid" || w["confidence"] != "medium" {
		t.Fatalf("taxonomía: %v", w)
	}
	if w["as_of"] != "2026-08-20" || w["model_version"] != "1.0.0" {
		t.Fatalf("trazabilidad wacc: %v", w)
	}

	// Aditivo: los bloques previos siguen presentes y con la misma forma.
	if _, ok := body["value"]; !ok {
		t.Fatalf("el bloque value (Graham/DCF) desapareció: %v", body)
	}
	if body["price"] == nil || body["ticker"] != "M6TST" || body["currency"] != "USD" {
		t.Fatalf("el encabezado de la respuesta cambió: %v", body)
	}
	if body["as_of"] == nil {
		t.Fatalf("as_of raíz ausente: %v", body)
	}
}

// TestValuationOmiteGrowthYWaccSinFilas: sin growth_metrics/wacc_metrics los
// bloques NO aparecen (nil ≠ 0, contrato §1 conservador) y la respuesta sigue
// siendo válida.
func TestValuationOmiteGrowthYWaccSinFilas(t *testing.T) {
	const ticker = "M6TST2"
	secID := m6Seed(t, ticker, false)
	// This suite shares abys_test with the packages that DO run the real pipeline
	// (/refresh runs it over every seeded ticker), so a previous or later test can
	// leave growth/wacc rows for this very security. The contract under test is
	// "NO row ⇒ no block", so the precondition is created here instead of being
	// inherited from the order in which tests happened to run.
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM growth_metrics WHERE security_id = $1`,
		`DELETE FROM wacc_metrics WHERE security_id = $1`,
	} {
		if _, err := pool.Exec(ctx, q, secID); err != nil {
			t.Fatalf("limpiar growth/wacc del fixture: %v", err)
		}
	}
	body := m6Valuation(t, api.NewRouter(pool), ticker)
	if _, present := body["growth"]; present {
		t.Fatalf("growth no debe aparecer sin fila: %v", body["growth"])
	}
	if _, present := body["wacc"]; present {
		t.Fatalf("wacc no debe aparecer sin fila: %v", body["wacc"])
	}
	if _, ok := body["value"]; !ok {
		t.Fatalf("el resto de la respuesta debe seguir intacto: %v", body)
	}
}

// TestValuationGrowthFallbackVisible: la fila degradada se expone tal cual
// (normalized_growth_rate ausente, source = insufficient_data), nunca como 0.
func TestValuationGrowthFallbackVisible(t *testing.T) {
	const ticker = "M6TST3"
	secID := m6Seed(t, ticker, false)
	ctx := context.Background()
	asOf := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := storage.UpsertGrowthMetric(ctx, tx, &storage.GrowthMetric{
		SecurityID: secID, AsOf: asOf, AvailableAt: &asOf,
		Source: "insufficient_data", Confidence: "low", ModelVersion: "1.0.0",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	g, ok := m6Valuation(t, api.NewRouter(pool), ticker)["growth"].(map[string]any)
	if !ok {
		t.Fatal("falta el bloque growth degradado")
	}
	if _, present := g["normalized_growth_rate"]; present {
		t.Fatalf("insufficient_data no puede traer tasa: %v", g)
	}
	if g["source"] != "insufficient_data" || g["confidence"] != "low" {
		t.Fatalf("degradación visible: %v", g)
	}
}
