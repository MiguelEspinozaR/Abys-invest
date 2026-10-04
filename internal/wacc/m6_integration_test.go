//go:build integration

package wacc

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
	"github.com/miky/abys-invest/internal/testsupport"
)

// Integration coverage of the CAPM WACC against a real database: the
// persistence contract of wacc_metrics and, sobre todo, la COHERENCIA de la
// taxonomía con la beta realmente observada (D17 / CA-M6a-3).

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		os.Exit(0) // sin BD: suite de integración se omite (sin error)
	}
	// Validate and redact DSN before connecting (ADR D30)
	// Validate the DSN targets a test DB; only its redacted form is ever logged (ADR D30).
	testsupport.EnsureTestDSN(dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var err error
	testPool, err = storage.Connect(ctx, dsn)
	if err != nil {
		panic("wacc integration: Connect falló: " + err.Error())
	}
	defer testPool.Close()
	if err := storage.EnsureTestDatabase(ctx, testPool); err != nil {
		panic("wacc integration: guard de BD de test falló (no se debe tocar producción): " + err.Error())
	}
	// Serialise the database phase of the integration suites (shared TRUNCATEs),
	// which is what lets them run WITHOUT `-p 1`.
	lockCtx, lockCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	releaseLock, err := testsupport.LockIntegrationDB(lockCtx, testPool)
	lockCancel()
	if err != nil {
		panic("wacc integration: no se pudo tomar el advisory lock: " + err.Error())
	}
	defer releaseLock()
	if err := storage.RunMigrations(ctx, testPool, "../../migrations"); err != nil {
		panic("wacc integration: migraciones fallaron: " + err.Error())
	}
	os.Exit(m.Run())
}

func requireDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testPool == nil {
		t.Skip("DATABASE_URL no configurado; saltando tests de integración de wacc")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := storage.RunMigrations(ctx, testPool, "../../migrations"); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	return testPool
}

func reset(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := storage.EnsureTestDatabase(ctx, pool); err != nil {
		t.Fatalf("guard de BD de test: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE wacc_metrics, growth_metrics, derived_metrics, daily_prices, fundamentals, securities RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
}

func ptrOf[T any](v T) *T { return &v }

// setup siembra security (con beta opcional), precio y los conceptos FY de la
// estructura de capital, y devuelve (security, precio de cierre).
func setup(t *testing.T, pool *pgxpool.Pool, ticker string, beta *float64, shares, totalDebt *float64, close float64) (*storage.Security, float64) {
	t.Helper()
	ctx := context.Background()
	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
		Ticker: ticker, CIK: "0000000000", Name: ticker + " WACC",
		Type: "stock", Currency: "USD", Status: "active",
	})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}
	asOf := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	open, high, low := close, close, close
	vol := int64(1_000_000)
	fyEnd := time.Date(2025, 9, 30, 0, 0, 0, 0, time.UTC)
	fyStart := fyEnd.AddDate(0, 0, -365)
	filing := fyEnd.AddDate(0, 2, 0)

	rows := []storage.Fundamental{}
	if shares != nil {
		rows = append(rows, storage.Fundamental{SecurityID: sec.ID, Concept: "shares_outstanding", Value: shares,
			Unit: ptrOf("shares"), PeriodType: "instant", PeriodEnd: fyEnd, FiscalYear: ptrOf(int16(2025)),
			FiscalPeriod: ptrOf("FY"), FilingDate: &filing, Source: "sec_edgar"})
	}
	if totalDebt != nil {
		rows = append(rows, storage.Fundamental{SecurityID: sec.ID, Concept: "total_debt", Value: totalDebt,
			Unit: ptrOf("USD"), PeriodType: "instant", PeriodEnd: fyEnd, FiscalYear: ptrOf(int16(2025)),
			FiscalPeriod: ptrOf("FY"), FilingDate: &filing, Source: "sec_edgar"})
	}
	if len(rows) > 0 {
		_ = fyStart
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if err := storage.UpsertFundamentals(ctx, tx, rows); err != nil {
			t.Fatalf("UpsertFundamentals: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Commit: %v", err)
		}
	}
	// La beta observada vive en beta_history (ADR D29): securities.beta es solo la
	// caché. Se siembra la OBSERVACIÓN fechada en `asOf` (no en time.Now), porque
	// GetBetaAsOf rechaza tanto una fila posterior a la fecha de valoración como
	// una de más de BETA_MAX_AGE_DAYS.
	if beta != nil {
		if err := storage.UpdateSecurityReference(ctx, pool, ticker, nil, nil, beta); err != nil {
			t.Fatalf("UpdateSecurityReference: %v", err)
		}
		testsupport.SeedBetaAsOf(t, pool, sec.ID, asOf, *beta)
		testsupport.AssertBetaCacheMatchesHistory(t, pool, sec.ID)
		sec, err = storage.GetSecurityByTicker(ctx, pool, ticker)
		if err != nil {
			t.Fatalf("GetSecurityByTicker tras la beta: %v", err)
		}
		if sec.Beta == nil || *sec.Beta != *beta {
			t.Fatalf("la beta no quedó en el catálogo: %v", sec.Beta)
		}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := storage.UpsertDailyPrices(ctx, tx, []storage.DailyPrice{{
		SecurityID: sec.ID, Date: asOf, Open: &open, High: &high, Low: &low,
		Close: close, AdjustedClose: close, Volume: &vol, Source: "yahoo",
	}}); err != nil {
		t.Fatalf("UpsertDailyPrices: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return sec, close
}

// persistir escribe la fila de wacc_metrics en su propia transacción.
func persistir(t *testing.T, pool *pgxpool.Pool, row *storage.WaccMetric) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := storage.UpsertWaccMetric(ctx, tx, row); err != nil {
		t.Fatalf("UpsertWaccMetric: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func rowFrom(t *testing.T, res Result, sec *storage.Security, asOf time.Time, equity, debt *float64) *storage.WaccMetric {
	t.Helper()
	return &storage.WaccMetric{
		SecurityID: sec.ID, AsOf: asOf, AvailableAt: &asOf,
		EquityValue: equity, DebtValue: debt,
		Beta: res.Beta, BetaObserved: res.BetaObserved, BetaUpdatedAt: sec.BetaUpdatedAt,
		CostOfEquity: res.Ke, CostOfDebtPreTax: res.CostOfDebt, CostOfDebtAfterTax: res.KdAfterTax,
		TaxRate: res.TaxRate, RiskFreeRate: res.RiskFreeRate, EquityRiskPremium: res.EquityRiskPremium,
		Wacc: res.WACC, WeightEquity: res.WeightEquity, WeightDebt: res.WeightDebt,
		Source: res.Source, Confidence: res.Confidence, ModelVersion: ModelVersion,
	}
}

// TestWACCCAPMHybridConBetaObservadaPersistida (CA-M6a-3): E = precio x shares
// (el precio de cierre = valuation_price, §22), D = total_debt, beta observada
// de Yahoo → capm_hybrid/medium y beta_observed = true.
func TestWACCCAPMHybridConBetaObservadaPersistida(t *testing.T) {
	pool := requireDB(t)
	reset(t, pool)
	ctx := context.Background()
	asOf := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)

	shares, debt, close := 1_000.0, 400.0, 250.0
	sec, price := setup(t, pool, "M6WACC", ptrOf(1.2), &shares, &debt, close)

	// ADR D14: los hechos se leen con corte temporal; sin él el test podría
	// usar un 10-K publicado después de la fecha de valoración.
	funds, _, err := storage.GetLatestFYFundamentalsAsOf(ctx, pool, sec.ID, []string{"shares_outstanding", "total_debt"}, asOf)
	if err != nil {
		t.Fatalf("GetLatestFYFundamentalsAsOf: %v", err)
	}
	equity := *funds["shares_outstanding"] * price
	res := Calculate(Inputs{Ticker: sec.Ticker, AsOf: asOf, EquityValue: &equity, DebtValue: funds["total_debt"], Beta: sec.Beta}, DefaultConfig())
	if res.Source != SourceCAPMHybrid || res.Confidence != ConfidenceMedium || !res.BetaObserved {
		t.Fatalf("taxonomía: %q/%q beta_observed=%v", res.Source, res.Confidence, res.BetaObserved)
	}

	persistir(t, pool, rowFrom(t, res, sec, asOf, &equity, funds["total_debt"]))
	got, err := storage.GetLatestWaccMetric(ctx, pool, sec.ID)
	if err != nil {
		t.Fatalf("GetLatestWaccMetric: %v", err)
	}
	if got.EquityValue == nil || math.Abs(*got.EquityValue-equity) > 1e-2 {
		t.Fatalf("equity_value: %v (esperado %v)", got.EquityValue, equity)
	}
	if got.DebtValue == nil || math.Abs(*got.DebtValue-400) > 1e-2 {
		t.Fatalf("debt_value: %v", got.DebtValue)
	}
	// NUMERIC(8,4) en la tabla: se compara con la tolerancia del redondeo.
	if got.Wacc == nil || math.Abs(*got.Wacc-*res.WACC) > 1e-4 {
		t.Fatalf("wacc: %v vs %v", got.Wacc, res.WACC)
	}
	if got.Source != SourceCAPMHybrid || got.Confidence != ConfidenceMedium {
		t.Fatalf("fila degradada: %q/%q", got.Source, got.Confidence)
	}
	if !got.BetaObserved || got.Beta == nil || math.Abs(*got.Beta-1.2) > 1e-6 {
		t.Fatalf("beta: %v observada=%v", got.Beta, got.BetaObserved)
	}
	if got.BetaUpdatedAt == nil {
		t.Fatal("beta_updated_at debe llegar desde securities.beta")
	}
	if got.CostOfEquity == nil || got.CostOfDebtAfterTax == nil {
		t.Fatalf("Ke/Kd_at obligatorios: %+v", got)
	}
}

// TestWACCAllEquityNoDegrada: D = 0 → WACC = Ke manteniendo capm_hybrid.
func TestWACCAllEquityNoDegrada(t *testing.T) {
	pool := requireDB(t)
	reset(t, pool)
	ctx := context.Background()
	asOf := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)

	shares, zero, close := 500.0, 0.0, 100.0
	sec, price := setup(t, pool, "M6ALLEQ", ptrOf(0.9), &shares, &zero, close)
	equity := shares * price
	res := Calculate(Inputs{Ticker: sec.Ticker, AsOf: asOf, EquityValue: &equity, DebtValue: &zero, Beta: sec.Beta}, DefaultConfig())
	if res.Source != SourceCAPMHybrid {
		t.Fatalf("all-equity observado no degrada: %q", res.Source)
	}
	if res.WACC == nil || res.Ke == nil || *res.WACC != *res.Ke {
		t.Fatalf("WACC debe ser Ke: %v vs %v", res.WACC, res.Ke)
	}
	persistir(t, pool, rowFrom(t, res, sec, asOf, &equity, &zero))
	got, err := storage.GetLatestWaccMetric(ctx, pool, sec.ID)
	if err != nil {
		t.Fatalf("GetLatestWaccMetric: %v", err)
	}
	if got.WeightDebt == nil || *got.WeightDebt != 0 {
		t.Fatalf("weight_debt: %v", got.WeightDebt)
	}
}

// TestWACCSinDeudaUsaFallback: sin total_debt la fila sale
// configured_fallback/low con WACC_FALLBACK.
func TestWACCSinDeudaUsaFallback(t *testing.T) {
	pool := requireDB(t)
	reset(t, pool)
	ctx := context.Background()
	asOf := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)

	shares, close := 1_000.0, 200.0
	sec, _ := setup(t, pool, "M6NODEBT", nil, &shares, nil, close)
	// ADR D14: los hechos se leen con corte temporal; sin él el test podría
	// usar un 10-K publicado después de la fecha de valoración.
	funds, _, err := storage.GetLatestFYFundamentalsAsOf(ctx, pool, sec.ID, []string{"shares_outstanding", "total_debt"}, asOf)
	if err != nil {
		t.Fatalf("GetLatestFYFundamentalsAsOf: %v", err)
	}
	equity := *funds["shares_outstanding"] * close
	res := Calculate(Inputs{Ticker: sec.Ticker, AsOf: asOf, EquityValue: &equity, DebtValue: funds["total_debt"], Beta: sec.Beta}, DefaultConfig())

	cfg := DefaultConfig()
	if res.Source != SourceConfiguredFallback || res.Confidence != ConfidenceLow {
		t.Fatalf("taxonomía: %q/%q", res.Source, res.Confidence)
	}
	if res.WACC == nil || *res.WACC != cfg.Fallback {
		t.Fatalf("wacc: %v (esperado %v)", res.WACC, cfg.Fallback)
	}
	if res.BetaObserved {
		t.Fatal("sin beta observada, beta_observed debe ser false")
	}
	persistir(t, pool, rowFrom(t, res, sec, asOf, &equity, nil))
	got, err := storage.GetLatestWaccMetric(ctx, pool, sec.ID)
	if err != nil {
		t.Fatalf("GetLatestWaccMetric: %v", err)
	}
	if got.Source != SourceConfiguredFallback || got.Confidence != ConfidenceLow {
		t.Fatalf("fila: %q/%q", got.Source, got.Confidence)
	}
	if got.BetaObserved {
		t.Fatal("beta_observed debe quedar false en la fila persistida")
	}
}

// TestWACCBetaNulaEnCatalogoDegrada (D17 + CHECK 013): securities.beta NULL →
// configured_fallback/low, y la BD impide que esa fila se etiquete capm_hybrid.
func TestWACCBetaNulaEnCatalogoDegrada(t *testing.T) {
	pool := requireDB(t)
	reset(t, pool)
	ctx := context.Background()
	asOf := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)

	shares, debt, close := 1_000.0, 500.0, 150.0
	sec, _ := setup(t, pool, "M6NOBETA", nil, &shares, &debt, close)
	if sec.Beta != nil {
		t.Fatalf("la beta debe estar NULL en el catálogo: %v", *sec.Beta)
	}
	equity := shares * close
	res := Calculate(Inputs{Ticker: sec.Ticker, AsOf: asOf, EquityValue: &equity, DebtValue: &debt, Beta: sec.Beta}, DefaultConfig())
	if res.Source != SourceConfiguredFallback || res.Confidence != ConfidenceLow {
		t.Fatalf("sin beta observada debe degradar: %q/%q", res.Source, res.Confidence)
	}
	if *res.Beta != DefaultConfig().BetaAssumed {
		t.Fatalf("la beta asumida debe quedar como evidencia: %v", *res.Beta)
	}

	// ck_wacc_source_beta impide declarar un CAPM (capm_hybrid/capm_individual)
	// sin beta observada: es exactamente lo que significa degradar.
	for _, src := range []string{SourceCAPMHybrid, SourceCAPMIndividual} {
		liar := rowFrom(t, res, sec, asOf, &equity, &debt)
		liar.Source = src
		liar.Confidence = ConfidenceMedium
		liar.BetaObserved = false
		if err := tryUpsertWacc(t, pool, liar); err == nil {
			t.Fatalf("una fila %s con beta_observed=false debe violar ck_wacc_source_beta", src)
		}
	}
	// Y una fuente fuera de la taxonomía tampoco entra.
	raro := rowFrom(t, res, sec, asOf, &equity, &debt)
	raro.Source = "yahoo_beta"
	if err := tryUpsertWacc(t, pool, raro); err == nil {
		t.Fatal("una fuente fuera de la taxonomía debe violar el CHECK de wacc_source")
	}

	persistir(t, pool, rowFrom(t, res, sec, asOf, &equity, &debt))
	got, err := storage.GetLatestWaccMetric(ctx, pool, sec.ID)
	if err != nil {
		t.Fatalf("GetLatestWaccMetric: %v", err)
	}
	if got.Source != SourceConfiguredFallback || got.BetaObserved {
		t.Fatalf("fila persistida incoherente: %q beta_observed=%v", got.Source, got.BetaObserved)
	}
	if got.WeightEquity != nil {
		t.Fatal("sin CAPM no se persisten pesos inventados")
	}
}

func tryUpsertWacc(t *testing.T, pool *pgxpool.Pool, row *storage.WaccMetric) error {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := storage.UpsertWaccMetric(ctx, tx, row); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
