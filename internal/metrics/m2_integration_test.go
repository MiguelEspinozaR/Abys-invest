//go:build integration

package metrics

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
)

// E2E integration of the metrics engine against the real PostgreSQL store:
// fundamentals + price -> derived_metrics (CA-3, CA-4, CA-8).

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		pool, err := storage.Connect(ctx, dsn)
		cancel()
		if err == nil {
			testPool = pool
		}
	}
	code := m.Run()
	if testPool != nil {
		testPool.Close()
	}
	os.Exit(code)
}

func requireDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testPool == nil {
		t.Skip("DATABASE_URL no configurado; saltando tests de integración de métricas")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := storage.RunMigrations(ctx, testPool, "../../migrations"); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	return testPool
}

// seedTestCompany inserts a security + full FY fundamentals + one price row
// and returns the security ID (cleanup: delete created rows).
func seedTestCompany(t *testing.T, pool *pgxpool.Pool, ticker string) int64 {
	t.Helper()
	ctx := context.Background()

	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
		Ticker: ticker, CIK: "9900000002", Name: "M2 Metrics Test",
		Type: "stock", Currency: "USD", Status: "active",
	})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}
	_ = sec

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	end := time.Date(2025, 9, 27, 0, 0, 0, 0, time.UTC)
	fy := int16(2025)
	vals := map[string]float64{
		"net_earnings":        112_010_000_000, // AAPL FY2025 (real imp_USD)
		"shareholders_equity": 73_733_000_000,
		"total_liabilities":   302_980_000_000,
		"free_cash_flow":      98_767_000_000,
	}
	var funds []storage.Fundamental
	for concept, v := range vals {
		vv := v
		funds = append(funds, storage.Fundamental{
			SecurityID: sec.ID, Concept: concept, Value: &vv, Unit: ptr("USD"),
			PeriodType: "duration", PeriodEnd: end, FiscalYear: &fy,
			FiscalPeriod: ptr("FY"), Source: "sec_edgar",
		})
	}
	if err := storage.UpsertFundamentals(ctx, tx, funds); err != nil {
		t.Fatalf("UpsertFundamentals: %v", err)
	}
	// shares_outstanding derivado de eps_diluted FY2025 (7.46) para que el
	// market_cap sea calculable: net_earnings / eps.
	shares := 112_010_000_000.0 / 7.46
	funds2 := []storage.Fundamental{{
		SecurityID: sec.ID, Concept: "shares_outstanding", Value: &shares, Unit: ptr("shares"),
		PeriodType: "instant", PeriodEnd: end, FiscalYear: &fy,
		FiscalPeriod: ptr("FY"), Source: "sec_edgar",
	}}
	if err := storage.UpsertFundamentals(ctx, tx, funds2); err != nil {
		t.Fatalf("UpsertFundamentals (shares): %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Precio de cierre más reciente (338.98 = regularMarketPrice real del fixture).
	price := &storage.DailyPrice{
		SecurityID: sec.ID, Date: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC),
		Close: 338.98, AdjustedClose: 338.98, Source: "yahoo",
	}
	ptx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin (price): %v", err)
	}
	if err := storage.UpsertDailyPrices(ctx, ptx, []storage.DailyPrice{*price}); err != nil {
		t.Fatalf("UpsertDailyPrices: %v", err)
	}
	if err := ptx.Commit(ctx); err != nil {
		t.Fatalf("Commit (price): %v", err)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM derived_metrics WHERE security_id=$1; DELETE FROM daily_prices WHERE security_id=$1; DELETE FROM fundamentals WHERE security_id=$1; DELETE FROM securities WHERE id=$1`, sec.ID)
	})

	// Cargar el input exactamente como lo haría cmd/analytics.
	return sec.ID
}

func loadInput(t *testing.T, pool *pgxpool.Pool, securityID int64) MetricInput {
	t.Helper()
	ctx := context.Background()

	concepts := []string{"net_earnings", "shares_outstanding", "shareholders_equity", "total_liabilities", "free_cash_flow"}
	vals := map[string]*float64{}
	rows, err := pool.Query(ctx, `SELECT DISTINCT ON (concept) concept, value
		FROM fundamentals WHERE security_id=$1 AND concept=ANY($2) AND fiscal_period='FY'
		ORDER BY concept, period_end DESC`, securityID, concepts)
	if err != nil {
		t.Fatalf("query fundamentales: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		var v *float64
		if err := rows.Scan(&c, &v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		vals[c] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	latest, err := storage.GetLatestPrice(ctx, pool, securityID)
	if err != nil {
		t.Fatalf("GetLatestPrice: %v", err)
	}
	return MetricInput{
		SecurityID:         securityID,
		Ticker:             "M2MET",
		AsOf:               latest.Date,
		NetEarnings:        vals["net_earnings"],
		SharesOutstanding:  vals["shares_outstanding"],
		Price:              latest.Close,
		ShareholdersEquity: vals["shareholders_equity"],
		TotalLiabilities:   vals["total_liabilities"],
		FreeCashFlow:       vals["free_cash_flow"],
		GrowthRate:         7,
	}
}

// TestMetricsE2E computes the 8 metrics from real rows and persists them.
func TestMetricsE2E(t *testing.T) {
	pool := requireDB(t)
	id := seedTestCompany(t, pool, "M2MET")

	input := loadInput(t, pool, id)
	if input.NetEarnings == nil || input.SharesOutstanding == nil || input.Price <= 0 {
		t.Fatal("inputs de seed incompletos")
	}

	rows, err := BuildDerivedMetrics(input, DefaultModelVersion)
	if err != nil {
		t.Fatalf("BuildDerivedMetrics: %v", err)
	}
	if len(rows) != 8 {
		t.Fatalf("se esperaban 8 métricas, hay %d", len(rows))
	}

	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := storage.UpsertDerivedMetrics(context.Background(), tx, rows); err != nil {
		t.Fatalf("UpsertDerivedMetrics: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	persisted, err := storage.GetDerivedMetricsBySecurity(context.Background(), pool, id, input.AsOf)
	if err != nil {
		t.Fatalf("GetDerivedMetricsBySecurity: %v", err)
	}
	if len(persisted) != 8 {
		t.Fatalf("filas persistidas: se esperaban 8, hay %d", len(persisted))
	}

	byName := map[string]float64{}
	for _, m := range persisted {
		if m.Value == nil {
			t.Fatalf("métrica %s: value NULL con inputs completos", m.Metric)
		}
		byName[m.Metric] = *m.Value
		var snap map[string]any
		if err := json.Unmarshal(m.InputsSnapshot, &snap); err != nil {
			t.Fatalf("snapshot %s no es JSON: %v", m.Metric, err)
		}
		if _, ok := snap["market_cap"]; !ok {
			t.Fatalf("snapshot %s sin market_cap", m.Metric)
		}
	}

	// Valores esperados (formulas §13, cálculos manuales).
	// EPS = 112.01e9 / (112.01e9/7.46) = 7.46
	if eps, ok := byName["eps"]; !ok || approx(&eps, f64(7.46), 0.001) == false {
		t.Fatalf("eps esperado ~7.46, got %v", eps)
	}
	// market_cap = 338.98 * shares; PE = price/EPS
	shares := 112_010_000_000.0 / 7.46
	mcap := 338.98 * shares
	pe := 338.98 / 7.46
	if v, ok := byName["pe_ratio"]; !ok || approx(&v, &pe, 0.01) == false {
		t.Fatalf("pe_ratio esperado ~%.3f, got %v", pe, v)
	}
	if v, ok := byName["pb_ratio"]; !ok || approx(&v, f64(mcap/73_733_000_000.0), 0.01) == false {
		t.Fatalf("pb_ratio esperado ~%.3f, got %v", mcap/73_733_000_000.0, v)
	}
	if v, ok := byName["pcf_ratio"]; !ok || approx(&v, f64(mcap/98_767_000_000.0), 0.01) == false {
		t.Fatalf("pcf_ratio esperado ~%.3f, got %v", mcap/98_767_000_000.0, v)
	}
	if v, ok := byName["peg_ratio"]; !ok || approx(&v, f64(pe/7), 0.01) == false {
		t.Fatalf("peg_ratio esperado ~%.3f, got %v", pe/7, v)
	}
	if v, ok := byName["roe"]; !ok || approx(&v, f64(112_010_000_000.0/73_733_000_000.0), 0.001) == false {
		t.Fatalf("roe esperado ~%.3f, got %v", 112_010_000_000.0/73_733_000_000.0, v)
	}
	if v, ok := byName["fcf_yield"]; !ok || approx(&v, f64(98_767_000_000.0/mcap*100), 0.01) == false {
		t.Fatalf("fcf_yield esperado ~%.3f, got %v", 98_767_000_000.0/mcap*100, v)
	}
}

// TestMetricsDeterminism re-calculates after persistence: same values (CA-4).
func TestMetricsDeterminism(t *testing.T) {
	pool := requireDB(t)
	// Reutiliza el security del E2E (ya con datos cargados).
	target, err := storage.GetSecurityByTicker(context.Background(), pool, "M2MET")
	if err != nil {
		t.Skip("M2MET no existe; correr TestMetricsE2E primero")
	}

	input := loadInput(t, pool, target.ID)
	first := CalculateMetrics(input)
	second := CalculateMetrics(input)
	if len(first) != len(second) {
		t.Fatalf("cantidad de métricas cambió entre pasadas")
	}
	for i := range first {
		if !approx(first[i].Value, second[i].Value, 0) {
			t.Fatalf("métrica %s no determinista: %v vs %v", first[i].Metric, first[i].Value, second[i].Value)
		}
	}
}

// TestMissingInputs: security sin fundamentales -> métricas NULL sin panic.
func TestMissingInputs(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
		Ticker: "M2NODATA", CIK: "9900000003", Name: "No Data Corp",
		Type: "stock", Currency: "USD", Status: "active",
	})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM derived_metrics WHERE security_id=$1; DELETE FROM daily_prices WHERE security_id=$1; DELETE FROM fundamentals WHERE security_id=$1; DELETE FROM securities WHERE id=$1`, sec.ID)
	})

	// Solo precio, sin fundamentales.
	price := &storage.DailyPrice{
		SecurityID: sec.ID, Date: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC),
		Close: 100.0, AdjustedClose: 100.0, Source: "yahoo",
	}
	ptx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin (price): %v", err)
	}
	if err := storage.UpsertDailyPrices(ctx, ptx, []storage.DailyPrice{*price}); err != nil {
		t.Fatalf("UpsertDailyPrices: %v", err)
	}
	if err := ptx.Commit(ctx); err != nil {
		t.Fatalf("Commit (price): %v", err)
	}

	input := loadInput(t, pool, sec.ID)
	results := CalculateMetrics(input)
	if len(results) != 8 {
		t.Fatalf("se esperaban 8 filas de métricas (con NULLs), hay %d", len(results))
	}
	for _, r := range results {
		if r.Value != nil {
			t.Fatalf("métrica %s debe ser NULL sin fundamentales, got %v", r.Metric, *r.Value)
		}
		if len(r.InputsSnapshot) == 0 {
			t.Fatalf("métrica %s sin snapshot de diagnóstico", r.Metric)
		}
	}
}

// ptr returns a pointer to v (generic helper for tests).
func ptr[T any](v T) *T { return &v }
