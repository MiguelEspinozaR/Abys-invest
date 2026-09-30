//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// M6a integration coverage: growth_metrics/wacc_metrics (migrations 012/013),
// el filtro de serie anual FY (D2), la aislación por model_version (§26) y la
// columna securities.beta con su COALESCE (D17).

// fyAnnual siembra un hecho FY anual de duración days días con filing_date.
func fyAnnual(secID int64, concept string, end time.Time, value float64, days int, filing time.Time) Fundamental {
	start := end.AddDate(0, 0, -days)
	return Fundamental{
		SecurityID: secID, Concept: concept, Value: &value, Unit: ptr("USD"),
		PeriodType: "duration", PeriodStart: &start, PeriodEnd: end,
		FiscalYear: ptr(int16(end.Year())), FiscalPeriod: ptr("FY"),
		FilingDate: &filing, Source: "sec_edgar",
	}
}

func seedFundamentals(t *testing.T, pool *pgxpool.Pool, rows []Fundamental) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := UpsertFundamentals(ctx, tx, rows); err != nil {
		t.Fatalf("UpsertFundamentals: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

// TestGetFYAnnualSeriesFiltraTrimestresYDeduplica cubre el filtro D2: en EDGAR
// 'FY' también trae trimestres (AAPL net_earnings 2018-03-31), el orden es ASC
// y minDays/maxDays cortan las duraciones que no son años fiscales.
//
// NOTA de esquema (desvío consciente respecto al plan, a confirmar por el
// Reviewer): el UNIQUE de fundamentals es
// (security_id, concept, period_type, period_end, fiscal_year, fiscal_period), así
// que una reexpresión del MISMO periodo_end sobrescribe la anterior en el
// upsert y no coexisten 3 filas. El resultado observable que exige el plan —
// "gana la reexpresión más reciente" — sí se cumple, pero por el upsert
// (last-write-wins), no por el DISTINCT ON de la query, que queda como
// defensa ante datos venidos de otras vías. Sufficientness (cuántos puntos
// hacen falta para un CAGR) NO es de esta capa: aquí solo se filtran
// duraciones y look-ahead; el insufficient_data lo decide el engine (cubierto
// en internal/growth/m6_integration_test.go).
func TestGetFYAnnualSeriesFiltraTrimestresYDeduplica(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	sec, err := UpsertSecurity(ctx, pool, &Security{Ticker: "AAPL", CIK: "0000320193", Name: "Apple Inc", Type: "stock", Currency: "USD", Status: "active"})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}

	asOf := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	filing := func(end time.Time) time.Time { return end.AddDate(0, 2, 0) }
	rows := []Fundamental{
		// 4 años anuales (365 días) → 5 puntos, CAGR medible.
		fyAnnual(sec.ID, "revenues", time.Date(2022, 9, 24, 0, 0, 0, 0, time.UTC), 394_328, 365, filing(time.Date(2022, 9, 24, 0, 0, 0, 0, time.UTC))),
		fyAnnual(sec.ID, "revenues", time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC), 383_285, 371, filing(time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC))),
		fyAnnual(sec.ID, "revenues", time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC), 391_035, 364, filing(time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC))),
		fyAnnual(sec.ID, "revenues", time.Date(2025, 9, 27, 0, 0, 0, 0, time.UTC), 416_161, 364, filing(time.Date(2025, 9, 27, 0, 0, 0, 0, time.UTC))),
		// Trimestre con fiscal_period='FY' (el caso real de EDGAR): 90 días.
		fyAnnual(sec.ID, "revenues", time.Date(2018, 3, 31, 0, 0, 0, 0, time.UTC), 73_917, 90, filing(time.Date(2018, 3, 31, 0, 0, 0, 0, time.UTC))),
		// Duplicado del mismo period_end con OTRO valor (reexpresión posterior):
		// gana la más reciente (fiscal_year DESC, filing_date DESC, id DESC).
		fyAnnual(sec.ID, "revenues", time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC), 999_999, 364, filing(time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC)).AddDate(0, 0, 1)),
		// Concepto sin serie anual (3 puntos) → no debe aparecer.
		fyAnnual(sec.ID, "free_cash_flow", time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC), 99_715, 371, filing(time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC))),
	}
	seedFundamentals(t, pool, rows)

	series, err := GetFYAnnualSeries(ctx, pool, sec.ID, []string{"revenues", "free_cash_flow"}, asOf, 330, 400)
	if err != nil {
		t.Fatalf("GetFYAnnualSeries: %v", err)
	}
	rev := series["revenues"]
	if len(rev) != 4 {
		t.Fatalf("se esperaban 4 puntos anuales (el trimestre fuera), hay %d", len(rev))
	}
	// ASC por period_end.
	for i := 1; i < len(rev); i++ {
		if !rev[i].PeriodEnd.After(rev[i-1].PeriodEnd) {
			t.Fatalf("la serie debe ir ASC: %s antes que %s", rev[i-1].PeriodEnd, rev[i].PeriodEnd)
		}
	}
	if rev[0].PeriodEnd.Year() != 2022 {
		t.Fatalf("el trimestre 2018-03-31 no debe entrar: primer punto %s", rev[0].PeriodEnd.Format("2006-01-02"))
	}
	// La reexpresión posterior del 2024-09-28 es la que gana (999999).
	found := false
	for _, p := range rev {
		if p.PeriodEnd.Year() == 2024 {
			found = true
			if p.Value != 999_999 {
				t.Fatalf("esperaba la reexpresión más reciente (999999), got %v", p.Value)
			}
			if p.AvailableAt.IsZero() {
				t.Fatal("available_at (filing_date) debe venir en el punto")
			}
		}
	}
	if !found {
		t.Fatal("falta el punto de 2024")
	}
	// free_cash_flow tiene 1 solo punto anual: la capa de storage lo devuelve
	// (pasó los filtros) y es el engine el que lo degrada a insufficient_data.
	if fcf := series["free_cash_flow"]; len(fcf) != 1 {
		t.Fatalf("free_cash_flow debería traer su único punto anual, hay %d", len(fcf))
	}

	// minDays/maxDays más estrechos: fuera todo lo que no dure ~1 año.
	narrow, err := GetFYAnnualSeries(ctx, pool, sec.ID, []string{"revenues"}, asOf, 500, 600)
	if err != nil {
		t.Fatalf("GetFYAnnualSeries estrecho: %v", err)
	}
	if len(narrow["revenues"]) != 0 {
		t.Fatalf("con ventana 500-600 días no debe quedar nada, hay %d", len(narrow["revenues"]))
	}
}

// TestGetFYAnnualSeriesExcluyeLookAhead (CA-M6a-5, SPEC §4): un hecho con
// filing_date > as_of no puede haber informado un valor fechado as_of.
func TestGetFYAnnualSeriesExcluyeLookAhead(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	sec, err := UpsertSecurity(ctx, pool, &Security{Ticker: "MSFT", CIK: "0000789019", Name: "Microsoft Corp", Type: "stock", Currency: "USD", Status: "active"})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}
	asOf := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC)
	start := end.AddDate(0, 0, -365)

	// Un FY anterior depositado a tiempo: visible.
	oldEnd := time.Date(2024, 6, 30, 0, 0, 0, 0, time.UTC)
	oldStart := oldEnd.AddDate(0, 0, -365)
	rows := []Fundamental{
		{SecurityID: sec.ID, Concept: "eps_diluted", Value: ptr(10.0), Unit: ptr("USD/shares"),
			PeriodType: "duration", PeriodStart: &oldStart, PeriodEnd: oldEnd, FiscalYear: ptr(int16(2024)),
			FiscalPeriod: ptr("FY"), FilingDate: ptr(time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC)), Source: "sec_edgar"},
		{SecurityID: sec.ID, Concept: "eps_diluted", Value: ptr(12.5), Unit: ptr("USD/shares"),
			PeriodType: "duration", PeriodStart: &start, PeriodEnd: end, FiscalYear: ptr(int16(2025)),
			FiscalPeriod: ptr("FY"), FilingDate: ptr(time.Date(2025, 8, 1, 0, 0, 0, 0, time.UTC)), Source: "sec_edgar"},
		// Hecho cuyo period_end es ANTERIOR a as_of pero cuya 10-K se depositó
		// DESPUÉS de as_of: invisible. Es el caso real de look-ahead (una
		// reexpresión tardía no podía haber informado el valor de esa fecha).
		{SecurityID: sec.ID, Concept: "eps_diluted", Value: ptr(99.99), Unit: ptr("USD/shares"),
			PeriodType: "duration", PeriodStart: &start, PeriodEnd: end, FiscalYear: ptr(int16(2025)),
			FiscalPeriod: ptr("FY"), FilingDate: ptr(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)), Source: "sec_edgar"},
	}
	// Orden de sembrado: primero el hecho visible, después el futuro (que
	// reescribe la fila del mismo FY2025). Así el look-ahead se ejerce de verdad.
	seedFundamentals(t, pool, rows[:2])
	seedFundamentals(t, pool, rows[2:])

	series, err := GetFYAnnualSeries(ctx, pool, sec.ID, []string{"eps_diluted"}, asOf, 330, 400)
	if err != nil {
		t.Fatalf("GetFYAnnualSeries: %v", err)
	}
	eps := series["eps_diluted"]
	// El UNIQUE (…, period_end, fiscal_year, fiscal_period) colapsa el FY2025 en
	// una fila: al sembrar el hecho futuro DESPUÉS, la fila que queda es la de
	// filing_date 2026-05-01 y por tanto el look-ahead la elimina. Lo que se
	// comprueba aquí es el invariante que importa: ningún hecho con
	// filing_date > as_of puede aparecer en la serie.
	if len(eps) != 1 {
		t.Fatalf("solo el hecho ya presentado debe entrar, hay %d (%+v)", len(eps), eps)
	}
	if eps[0].Value != 10.0 || eps[0].PeriodEnd.Year() != 2024 {
		t.Fatalf("look-ahead: se coló el hecho futuro (value %v, period_end %s)",
			eps[0].Value, eps[0].PeriodEnd.Format("2006-01-02"))
	}
	if eps[0].AvailableAt.IsZero() || eps[0].AvailableAt.After(asOf) {
		t.Fatalf("available_at debe ser el filing_date ya presentado: %s", eps[0].AvailableAt)
	}
}

func TestGrowthAndWaccMetricsIdempotenciaYVersionado(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	sec, err := UpsertSecurity(ctx, pool, &Security{Ticker: "AAPL", CIK: "0000320193", Name: "Apple Inc", Type: "stock", Currency: "USD", Status: "active"})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}
	asOf := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	avail := asOf.AddDate(0, 0, -10)

	mkGrowth := func(rate float64, version string) *GrowthMetric {
		return &GrowthMetric{
			SecurityID: sec.ID, AsOf: asOf, AvailableAt: &avail, FundamentalsAsOf: &asOf,
			RevenueCAGR3y: ptr(8.1), RevenueCAGR5y: ptr(7.4), EPSCAGR3y: ptr(12.0), EPSCAGR5y: ptr(11.0),
			FCFCAGR3y: ptr(9.0), FCFCAGR5y: ptr(8.0), NormalizedGrowthRate: ptr(rate),
			Confidence: "high", Source: "eps_fcf_3y", InputsSnapshot: []byte(`{"series":{}}`), ModelVersion: version,
		}
	}
	mkWacc := func(value float64, version string) *WaccMetric {
		equity, debt := 3_000_000.0, 1_100_000.0
		return &WaccMetric{
			SecurityID: sec.ID, AsOf: asOf, AvailableAt: &avail, EquityValue: &equity, DebtValue: &debt,
			Beta: ptr(1.2), BetaObserved: true, CostOfEquity: ptr(11.1), CostOfDebtPreTax: ptr(6.0),
			CostOfDebtAfterTax: ptr(4.74), TaxRate: ptr(21.0), RiskFreeRate: ptr(4.5), EquityRiskPremium: ptr(5.5),
			Wacc: ptr(value), WeightEquity: ptr(equity / (equity + debt)), WeightDebt: ptr(debt / (equity + debt)),
			Source: "capm_hybrid", Confidence: "medium", InputsSnapshot: []byte(`{"inputs":{}}`), ModelVersion: version,
		}
	}

	apply := func(g *GrowthMetric, w *WaccMetric) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if err := UpsertGrowthMetric(ctx, tx, g); err != nil {
			t.Fatalf("UpsertGrowthMetric: %v", err)
		}
		if err := UpsertWaccMetric(ctx, tx, w); err != nil {
			t.Fatalf("UpsertWaccMetric: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Commit: %v", err)
		}
	}

	apply(mkGrowth(10.5, "1.0.0"), mkWacc(9.28, "1.0.0"))
	apply(mkGrowth(11.5, "1.0.0"), mkWacc(9.30, "1.0.0")) // idempotente: actualiza

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM growth_metrics WHERE security_id=$1`, sec.ID).Scan(&n); err != nil {
		t.Fatalf("count growth: %v", err)
	}
	if n != 1 {
		t.Fatalf("upsert de growth duplicó filas: %d", n)
	}
	got, err := GetLatestGrowthMetric(ctx, pool, sec.ID)
	if err != nil {
		t.Fatalf("GetLatestGrowthMetric: %v", err)
	}
	if got.NormalizedGrowthRate == nil || *got.NormalizedGrowthRate != 11.5 {
		t.Fatalf("growth no refrescado: %+v", got)
	}
	if got.EPSCAGR3y == nil || *got.EPSCAGR3y != 12.0 {
		t.Fatalf("los seis CAGR deben persistirse: %+v", got)
	}
	if got.AsOf.Format("2006-01-02") != "2026-09-26" {
		t.Fatalf("as_of: %s", got.AsOf)
	}

	w, err := GetLatestWaccMetric(ctx, pool, sec.ID)
	if err != nil {
		t.Fatalf("GetLatestWaccMetric: %v", err)
	}
	if w.Wacc == nil || *w.Wacc != 9.30 || w.Source != "capm_hybrid" || !w.BetaObserved {
		t.Fatalf("wacc no refrescado: %+v", w)
	}

	// Otra model_version: fila nueva, la anterior INTACTA (§26).
	apply(mkGrowth(12.5, "2.0.0"), mkWacc(9.40, "2.0.0"))
	hist, err := ListGrowthMetricsBySecurity(ctx, pool, sec.ID)
	if err != nil {
		t.Fatalf("ListGrowthMetricsBySecurity: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("se esperaban 2 filas (una por model_version), hay %d", len(hist))
	}
	var v11, v12 int
	for _, h := range hist {
		switch h.ModelVersion {
		case "1.0.0":
			v11++
			if *h.NormalizedGrowthRate != 11.5 {
				t.Fatalf("la fila 1.0.0 fue pisada: %v", *h.NormalizedGrowthRate)
			}
		case "2.0.0":
			v12++
		}
	}
	if v11 != 1 || v12 != 1 {
		t.Fatalf("historial por versión incorrecto: 1.0.0=%d 2.0.0=%d", v11, v12)
	}
	whist, err := ListWaccMetricsBySecurity(ctx, pool, sec.ID)
	if err != nil {
		t.Fatalf("ListWaccMetricsBySecurity: %v", err)
	}
	if len(whist) != 2 {
		t.Fatalf("historial wacc: %d", len(whist))
	}

	// available_at y inputs_snapshot sobreviven al viaje (trazabilidad §4/§26).
	if got.AvailableAt == nil || !got.AvailableAt.Equal(avail) {
		t.Fatalf("available_at: %v", got.AvailableAt)
	}
	var snap map[string]any
	if err := json.Unmarshal(got.InputsSnapshot, &snap); err != nil {
		t.Fatalf("inputs_snapshot no es JSON: %v", err)
	}
	if got.CalculationTimestamp.IsZero() {
		t.Fatal("calculation_timestamp debe rellenarse")
	}

	// Sin filas: ErrNoRows (la API omite el bloque en vez de fallar).
	if _, err := GetLatestGrowthMetric(ctx, pool, 999999); err != pgx.ErrNoRows {
		t.Fatalf("esperaba ErrNoRows, got %v", err)
	}
	if _, err := GetLatestWaccMetric(ctx, pool, 999999); err != pgx.ErrNoRows {
		t.Fatalf("esperaba ErrNoRows, got %v", err)
	}
}

// TestWaccMetricsCHECKCoherenciaTaxonomia: la BD impide declarar capm_hybrid
// sin beta observada (CHECK de migración 013).
func TestWaccMetricsCHECKCoherenciaTaxonomia(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	sec, err := UpsertSecurity(ctx, pool, &Security{Ticker: "BAC", CIK: "0000070858", Name: "Bank of America", Type: "stock", Currency: "USD", Status: "active"})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}
	asOf := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	row := &WaccMetric{
		SecurityID: sec.ID, AsOf: asOf, EquityValue: ptr(100.0), DebtValue: ptr(50.0),
		Beta: ptr(1.0), BetaObserved: false, Wacc: ptr(9.0),
		Source: "capm_hybrid", Confidence: "medium", ModelVersion: "1.0.0",
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := UpsertWaccMetric(ctx, tx, row); err == nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatal("el CHECK debe rechazar capm_hybrid sin beta observada")
	}
	tx.Rollback(ctx) //nolint:errcheck

	row.Source, row.Confidence = "configured_fallback", "low"
	if err := applyWacc(t, pool, row); err != nil {
		t.Fatalf("configured_fallback con beta_observed=false debe aceptarse: %v", err)
	}
	row.Source, row.Confidence = "configured_fallback", "low"
	row.BetaObserved, row.Beta = true, ptr(1.3)
	if err := applyWacc(t, pool, row); err != nil {
		t.Fatalf("beta observada con fallback debe aceptarse: %v", err)
	}
}

func applyWacc(t *testing.T, pool *pgxpool.Pool, w *WaccMetric) error {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := UpsertWaccMetric(ctx, tx, w); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// TestUpdateSecurityReferenceBeta (D17): la misma llamada quoteSummary persiste
// sector, industry y beta; un beta ausente NO borra el previo (COALESCE) y la
// columna llega en los lectores del catálogo.
func TestUpdateSecurityReferenceBeta(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	if _, err := UpsertSecurity(ctx, pool, &Security{Ticker: "AAPL", CIK: "0000320193", Name: "Apple Inc", Type: "stock", Currency: "USD", Status: "active"}); err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}

	if err := UpdateSecurityReference(ctx, pool, "AAPL", ptr("Technology"), ptr("Consumer Electronics"), ptr(1.15)); err != nil {
		t.Fatalf("UpdateSecurityReference: %v", err)
	}
	got, err := GetSecurityByTicker(ctx, pool, "AAPL")
	if err != nil {
		t.Fatalf("GetSecurityByTicker: %v", err)
	}
	if got.Beta == nil || *got.Beta != 1.15 {
		t.Fatalf("beta no persistida: %+v", got.Beta)
	}
	if got.BetaUpdatedAt == nil || got.BetaUpdatedAt.IsZero() {
		t.Fatal("beta_updated_at debe fijarse con la beta observada")
	}
	firstSeen := *got.BetaUpdatedAt

	// Segunda pasada SIN beta (módulo ausente): sector se refresca, la beta NO
	// se borra (si no, un hueco puntual de Yahoo degradaría a todo el universo).
	if err := UpdateSecurityReference(ctx, pool, "AAPL", ptr("Technology"), nil, nil); err != nil {
		t.Fatalf("UpdateSecurityReference sin beta: %v", err)
	}
	got, err = GetSecurityByTicker(ctx, pool, "AAPL")
	if err != nil {
		t.Fatalf("GetSecurityByTicker: %v", err)
	}
	if got.Beta == nil || *got.Beta != 1.15 {
		t.Fatalf("la beta previa no debe borrarse: %+v", got.Beta)
	}
	if !got.BetaUpdatedAt.Equal(firstSeen) {
		t.Fatal("beta_updated_at no debe moverse sin beta nueva")
	}

	// Beta fuera de rango (CHECK de migración 011): se guarda NULL → degradación
	// explícita, no un 99 que rompería la Ke.
	if err := UpdateSecurityReference(ctx, pool, "AAPL", nil, nil, ptr(99.0)); err != nil {
		t.Fatalf("UpdateSecurityReference con beta inválida: %v", err)
	}
	got, _ = GetSecurityByTicker(ctx, pool, "AAPL")
	if got.Beta == nil || *got.Beta != 1.15 {
		t.Fatalf("una beta inválida no debe pisar la válida: %+v", got.Beta)
	}

	// ListSecurities también la trae.
	list, err := ListSecurities(ctx, pool, 10, 0)
	if err != nil {
		t.Fatalf("ListSecurities: %v", err)
	}
	if len(list) != 1 || list[0].Beta == nil || *list[0].Beta != 1.15 {
		t.Fatalf("ListSecurities no expone la beta: %+v", list)
	}

	// Ticker no catalogado → ErrNoRows (no un UPDATE silencioso a 0 filas). El
	// error va envuelto con %w, así que la comparación correcta es errors.Is.
	if err := UpdateSecurityReference(ctx, pool, "NOPE", ptr("X"), nil, ptr(1.0)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("esperaba ErrNoRows, got %v", err)
	}
}

// TestSecuritiesBetaCHECKRechazaValores: el CHECK (0, 10] protege la columna
// aunque alguien escriba fuera del storage layer.
func TestSecuritiesBetaCHECKRechazaValores(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	if _, err := UpsertSecurity(ctx, pool, &Security{Ticker: "XOM", CIK: "0000034088", Name: "Exxon Mobil", Type: "stock", Currency: "USD", Status: "active"}); err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}
	for _, bad := range []float64{-1, 0, 99} {
		if _, err := pool.Exec(ctx, `UPDATE securities SET beta = $1 WHERE ticker='XOM'`, bad); err == nil {
			t.Fatalf("el CHECK debe rechazar beta = %v", bad)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE securities SET beta = 1.8 WHERE ticker='XOM'`); err != nil {
		t.Fatalf("una beta válida debe aceptarse: %v", err)
	}
}

// TestUpdateQuoteCloseNoSobrescribeAjustado: §22 — el quote solo actualiza
// `close`; adjusted_close/OHLC/volume de la serie histórica son intocables.
func TestUpdateQuoteCloseNoSobrescribeAjustado(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	sec, err := UpsertSecurity(ctx, pool, &Security{Ticker: "AAPL", CIK: "0000320193", Name: "Apple Inc", Type: "stock", Currency: "USD", Status: "active"})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}
	date := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	open, high, low, close, adj, vol := 290.0, 295.0, 288.0, 293.5, 292.1, int64(45_000_000)
	seed := []DailyPrice{{
		SecurityID: sec.ID, Date: date, Open: &open, High: &high, Low: &low,
		Close: close, AdjustedClose: adj, Volume: &vol, Source: "yahoo",
	}}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := UpsertDailyPrices(ctx, tx, seed); err != nil {
		t.Fatalf("UpsertDailyPrices: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// El quote del día con un precio distinto: solo cambia `close`.
	updated, err := UpdateQuoteClose(ctx, pool, sec.ID, date, 297.75)
	if err != nil {
		t.Fatalf("UpdateQuoteClose: %v", err)
	}
	if updated.Close != 297.75 {
		t.Fatalf("close no refrescado: %v", updated.Close)
	}
	if updated.AdjustedClose != adj {
		t.Fatalf("adjusted_close NO debe cambiar: %v", updated.AdjustedClose)
	}
	if updated.Open == nil || *updated.Open != open || updated.High == nil || *updated.High != high ||
		updated.Low == nil || *updated.Low != low {
		t.Fatalf("el OHLC no debe cambiar: %+v", updated)
	}
	if updated.Volume == nil || *updated.Volume != vol {
		t.Fatalf("el volumen no debe cambiar: %+v", updated.Volume)
	}
	if updated.Source != "yahoo" {
		t.Fatalf("la fila existente conserva su source: %q", updated.Source)
	}

	// Sin fila previa: inserta sintética con adjusted_close = close y
	// source = 'yahoo_quote' (fin de semana / símbolo sin serie).
	other := date.AddDate(0, 0, 2)
	ins, err := UpdateQuoteClose(ctx, pool, sec.ID, other, 299.0)
	if err != nil {
		t.Fatalf("UpdateQuoteClose (inserción): %v", err)
	}
	if ins.AdjustedClose != 299.0 {
		t.Fatalf("adjusted_close = close en la barra sintética: %v", ins.AdjustedClose)
	}
	if ins.Source != "yahoo_quote" {
		t.Fatalf("source de la barra sintética: %q", ins.Source)
	}
	if ins.Open != nil {
		t.Fatal("una barra de quote no puede traer OHLC inventado")
	}

	// Idempotente: repetirse no duplica la fila.
	if _, err := UpdateQuoteClose(ctx, pool, sec.ID, other, 299.5); err != nil {
		t.Fatalf("UpdateQuoteClose repetido: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM daily_prices WHERE security_id=$1 AND date=$2`, sec.ID, other).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("upsert de quote duplicó: %d", n)
	}
}
