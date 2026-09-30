//go:build integration

package growth

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
)

// Integration coverage of the Growth Engine against a real database: the SQL
// filter of annual FY facts (D2, the part a unit test cannot cover) plus the
// persistence contract (as_of, available_at, inputs_snapshot, idempotencia).

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		os.Exit(0) // sin BD: suite de integración se omite (sin error)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var err error
	testPool, err = storage.Connect(ctx, dsn)
	if err != nil {
		panic("growth integration: Connect falló: " + err.Error())
	}
	defer testPool.Close()
	if err := storage.EnsureTestDatabase(ctx, testPool); err != nil {
		panic("growth integration: guard de BD de test falló (no se debe tocar producción): " + err.Error())
	}
	if err := storage.RunMigrations(ctx, testPool, "../../migrations"); err != nil {
		panic("growth integration: migraciones fallaron: " + err.Error())
	}
	os.Exit(m.Run())
}

func requireDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testPool == nil {
		t.Skip("DATABASE_URL no configurado; saltando tests de integración de growth")
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
	if _, err := pool.Exec(ctx, `TRUNCATE growth_metrics, wacc_metrics, derived_metrics, daily_prices, fundamentals, securities RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
}

// seedSecurityAndPrices crea la security y UNA barra de precio, que fija el
// as_of de todo el cálculo.
func seedSecurityAndPrice(t *testing.T, pool *pgxpool.Pool, ticker string, close float64, date time.Time) *storage.Security {
	t.Helper()
	ctx := context.Background()
	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
		Ticker: ticker, CIK: "0000000000", Name: ticker + " Test",
		Type: "stock", Currency: "USD", Status: "active",
	})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}
	open, high, low := close, close, close
	vol := int64(1_000_000)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := storage.UpsertDailyPrices(ctx, tx, []storage.DailyPrice{{
		SecurityID: sec.ID, Date: date, Open: &open, High: &high, Low: &low,
		Close: close, AdjustedClose: close, Volume: &vol, Source: "yahoo",
	}}); err != nil {
		t.Fatalf("UpsertDailyPrices: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return sec
}

// seedFY siembra 5 años anuales (revenues, eps_diluted, free_cash_flow) con
// growth del 10% y filing_date 2 meses después del cierre fiscal.
func seedFY(t *testing.T, pool *pgxpool.Pool, sec *storage.Security, startYear, years int, concepts map[string]float64) map[string]time.Time {
	t.Helper()
	ctx := context.Background()
	rows := []storage.Fundamental{}
	lastEnd := map[string]time.Time{}
	for i := 0; i < years; i++ {
		end := time.Date(startYear+i, 9, 30, 0, 0, 0, 0, time.UTC)
		filing := end.AddDate(0, 2, 0)
		for concept, base := range concepts {
			value := base * pow(1.10, float64(i))
			start := end.AddDate(0, 0, -365)
			rows = append(rows, storage.Fundamental{
				SecurityID: sec.ID, Concept: concept, Value: &value, Unit: ptrOf("USD"),
				PeriodType: "duration", PeriodStart: &start, PeriodEnd: end,
				FiscalYear: ptrOf(int16(end.Year())), FiscalPeriod: ptrOf("FY"),
				FilingDate: &filing, Source: "sec_edgar",
			})
			lastEnd[concept] = end
		}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := storage.UpsertFundamentals(ctx, tx, rows); err != nil {
		t.Fatalf("UpsertFundamentals: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return lastEnd
}

func pow(base, exp float64) float64 {
	out := 1.0
	for i := 0; i < int(exp); i++ {
		out *= base
	}
	return out
}

// TestGrowthEngineSobreSerieAnualReal (CA-M6a-1/2): 5 años sembrados →
// normalized 10%, confidence high, source eps_fcf_3y, as_of = fecha del último
// precio, available_at = max(filing_date) y el snapshot con los puntos.
func TestGrowthEngineSobreSerieAnualReal(t *testing.T) {
	pool := requireDB(t)
	reset(t, pool)
	ctx := context.Background()

	asOf := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	sec := seedSecurityAndPrice(t, pool, "M6GROW", 230.0, asOf)
	// 6 puntos anuales (FY2020..FY2025): 5y CAGR necesita 6 observaciones.
	lastEnd := seedFY(t, pool, sec, 2020, 6, map[string]float64{
		"revenues": 300_000, "eps_diluted": 5.0, "free_cash_flow": 80_000,
	})

	series, err := storage.GetFYAnnualSeries(ctx, pool, sec.ID,
		[]string{"revenues", "eps_diluted", "free_cash_flow"}, asOf, 330, 400)
	if err != nil {
		t.Fatalf("GetFYAnnualSeries: %v", err)
	}
	in := Inputs{Ticker: sec.Ticker, AsOf: asOf, Series: Series{
		Revenue: toPoints(series["revenues"]),
		EPS:     toPoints(series["eps_diluted"]),
		FCF:     toPoints(series["free_cash_flow"]),
	}}
	cfg := DefaultConfig()
	res := Calculate(in, cfg)

	if res.NormalizedGrowthRate == nil {
		t.Fatal("con 6 puntos anuales debe haber tasa")
	}
	if got := *res.NormalizedGrowthRate; got < 9.9 || got > 10.1 {
		t.Fatalf("tasa esperada ~10%%, got %v", got)
	}
	if res.Confidence != ConfidenceHigh || res.Source != SourceEPSFCF3y {
		t.Fatalf("taxonomía: %q/%q", res.Confidence, res.Source)
	}
	if res.RevenueCAGR3y == nil || res.EPSCAGR3y == nil || res.FCFCAGR3y == nil ||
		res.RevenueCAGR5y == nil || res.EPSCAGR5y == nil || res.FCFCAGR5y == nil {
		t.Fatalf("los seis CAGR deben calcularse con 6 puntos: %+v", res)
	}
	if res.Clamped || res.RevenueDiscrepancy {
		t.Fatalf("sin límites ni discrepancia esperados: %+v", res)
	}

	snap, err := res.Snapshot(in, cfg)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var decoded struct {
		Series map[string][]map[string]any `json:"series"`
	}
	if err := json.Unmarshal(snap, &decoded); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(decoded.Series["eps"]) != 6 {
		t.Fatalf("el snapshot debe llevar los 6 puntos de EPS: %d", len(decoded.Series["eps"]))
	}

	// available_at = max(filing_date) de los hechos usados.
	var maxFiling time.Time
	for _, concept := range []string{"revenues", "eps_diluted", "free_cash_flow"} {
		for _, p := range series[concept] {
			if p.AvailableAt.After(maxFiling) {
				maxFiling = p.AvailableAt
			}
		}
	}
	if maxFiling.IsZero() {
		t.Fatal("los puntos deben traer filing_date")
	}
	var lastPeriodEnd time.Time
	for _, concept := range []string{"revenues", "eps_diluted", "free_cash_flow"} {
		if e := lastEnd[concept]; e.After(lastPeriodEnd) {
			lastPeriodEnd = e
		}
	}

	// Persistencia: dos pasadas → 1 fila (idempotencia del UNIQUE).
	for i := 0; i < 2; i++ {
		row := &storage.GrowthMetric{
			SecurityID: sec.ID, AsOf: asOf, AvailableAt: &maxFiling, FundamentalsAsOf: &lastPeriodEnd,
			RevenueCAGR3y: res.RevenueCAGR3y, RevenueCAGR5y: res.RevenueCAGR5y,
			EPSCAGR3y: res.EPSCAGR3y, EPSCAGR5y: res.EPSCAGR5y,
			FCFCAGR3y: res.FCFCAGR3y, FCFCAGR5y: res.FCFCAGR5y,
			NormalizedGrowthRate: res.NormalizedGrowthRate,
			Confidence:           res.Confidence, Source: res.Source,
			Clamped: res.Clamped, RevenueDiscrepancy: res.RevenueDiscrepancy,
			InputsSnapshot: snap, ModelVersion: ModelVersion,
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if err := storage.UpsertGrowthMetric(ctx, tx, row); err != nil {
			t.Fatalf("UpsertGrowthMetric: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Commit: %v", err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM growth_metrics WHERE security_id=$1`, sec.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("la segunda pasada debe ser idempotente, hay %d filas", n)
	}
	got, err := storage.GetLatestGrowthMetric(ctx, pool, sec.ID)
	if err != nil {
		t.Fatalf("GetLatestGrowthMetric: %v", err)
	}
	if got.AsOf.Format("2006-01-02") != asOf.Format("2006-01-02") {
		t.Fatalf("as_of debe ser la fecha del último precio: %s", got.AsOf.Format("2006-01-02"))
	}
	if got.AvailableAt == nil || !got.AvailableAt.Equal(maxFiling) {
		t.Fatalf("available_at: %v (esperado %s)", got.AvailableAt, maxFiling)
	}
	if got.FundamentalsAsOf == nil || !got.FundamentalsAsOf.Equal(lastPeriodEnd) {
		t.Fatalf("fundamentals_as_of: %v", got.FundamentalsAsOf)
	}
	if got.ModelVersion != ModelVersion {
		t.Fatalf("model_version: %q", got.ModelVersion)
	}
}

// TestGrowthSinSerieAnualNoInventaTasa (CA-M6a-1, riesgo R2): sin serie anual
// suficiente la fila sale con tasa NULL, confidence low e
// insufficient_data — nunca el 7% de relleno.
func TestGrowthSinSerieAnualNoInventaTasa(t *testing.T) {
	pool := requireDB(t)
	reset(t, pool)
	ctx := context.Background()

	asOf := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	sec := seedSecurityAndPrice(t, pool, "M6NOGROW", 100.0, asOf)

	series, err := storage.GetFYAnnualSeries(ctx, pool, sec.ID,
		[]string{"revenues", "eps_diluted", "free_cash_flow"}, asOf, 330, 400)
	if err != nil {
		t.Fatalf("GetFYAnnualSeries: %v", err)
	}
	res := Calculate(Inputs{Ticker: sec.Ticker, AsOf: asOf, Series: Series{
		Revenue: toPoints(series["revenues"]), EPS: toPoints(series["eps_diluted"]), FCF: toPoints(series["free_cash_flow"]),
	}}, DefaultConfig())

	if res.NormalizedGrowthRate != nil {
		t.Fatalf("se esperaba nil, got %v", *res.NormalizedGrowthRate)
	}
	if res.Confidence != ConfidenceLow || res.Source != SourceInsufficientData {
		t.Fatalf("taxonomía: %q/%q", res.Confidence, res.Source)
	}
	cfg := DefaultConfig()
	if res.EffectiveRate(cfg) != 7 {
		t.Fatalf("el fallback legacy debe seguir siendo 7 en M6a, got %v", res.EffectiveRate(cfg))
	}
}

// TestGrowthTrimestresNoCuentan (D2): sembrar quarters con fiscal_period='FY'
// NO debe producir CAGR: el filtro es de duración en SQL, no en el motor.
func TestGrowthTrimestresNoCuentan(t *testing.T) {
	pool := requireDB(t)
	reset(t, pool)
	ctx := context.Background()

	asOf := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	sec := seedSecurityAndPrice(t, pool, "M6QTRS", 50.0, asOf)

	rows := []storage.Fundamental{}
	for i := 0; i < 8; i++ {
		end := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC).AddDate(0, 3, i)
		start := end.AddDate(0, 0, -91)
		value := 10.0 * pow(1.05, float64(i))
		filing := end.AddDate(0, 0, 30)
		rows = append(rows, storage.Fundamental{
			SecurityID: sec.ID, Concept: "eps_diluted", Value: &value, Unit: ptrOf("USD/shares"),
			PeriodType: "duration", PeriodStart: &start, PeriodEnd: end,
			FiscalYear: ptrOf(int16(end.Year())), FiscalPeriod: ptrOf("FY"),
			FilingDate: &filing, Source: "sec_edgar",
		})
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := storage.UpsertFundamentals(ctx, tx, rows); err != nil {
		t.Fatalf("UpsertFundamentals: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	series, err := storage.GetFYAnnualSeries(ctx, pool, sec.ID, []string{"eps_diluted"}, asOf, 330, 400)
	if err != nil {
		t.Fatalf("GetFYAnnualSeries: %v", err)
	}
	if len(series["eps_diluted"]) != 0 {
		t.Fatalf("los trimestres no pueden entrar en la serie anual: %d", len(series["eps_diluted"]))
	}
	res := Calculate(Inputs{Ticker: sec.Ticker, AsOf: asOf, Series: Series{EPS: toPoints(series["eps_diluted"])}}, DefaultConfig())
	if res.NormalizedGrowthRate != nil || res.Source != SourceInsufficientData {
		t.Fatalf("con solo trimestres: %+v", res)
	}
}

func ptrOf[T any](v T) *T { return &v }

func toPoints(rows []storage.FYPoint) []Point {
	if len(rows) == 0 {
		return nil
	}
	out := make([]Point, 0, len(rows))
	for _, r := range rows {
		out = append(out, Point{PeriodEnd: r.PeriodEnd, Value: r.Value, AvailableAt: r.AvailableAt})
	}
	return out
}
