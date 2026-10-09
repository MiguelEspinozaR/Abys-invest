//go:build integration

package edgar

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
	"github.com/miky/abys-invest/internal/testsupport"
)

// Integration tests for the normalization pipeline (internal/collect/edgar).
// Require a reachable PostgreSQL (DATABASE_URL) with migrations applied.
//
// Run with: go test ./internal/collect/edgar/... -tags=integration -v -count=1

var normPool *pgxpool.Pool

// normRelease unlocks the shared integration advisory lock (see
// testsupport.LockIntegrationDB). Held for the whole suite because the suite
// TRUNCATEs tables shared with the other integration packages.
var normRelease func()

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		pool, err := storage.Connect(ctx, dsn)
		cancel()
		if err == nil {
			ctx2, cancel2 := context.WithTimeout(context.Background(), 15*time.Second)
			if err := storage.EnsureTestDatabase(ctx2, pool); err == nil {
				// Waiting for the shared lock can take as long as the other
				// suites ahead in the queue, so it gets its OWN generous
				// context: the setup timeout above is not a lock timeout.
				lockCtx, lockCancel := context.WithTimeout(context.Background(), 10*time.Minute)
				release, lerr := testsupport.LockIntegrationDB(lockCtx, pool)
				lockCancel()
				if lerr != nil {
					// LOUD, never a silent skip: a suite that cannot take the
					// lock would otherwise report "skipped" and the whole
					// package would look green while testing nothing.
					panic("edgar integration: no se pudo tomar el advisory lock: " + lerr.Error())
				}
				normRelease = release
				normPool = pool
			} else {
				pool.Close()
			}
			cancel2()
		}
	}
	code := m.Run()
	if normRelease != nil {
		normRelease()
	}
	if normPool != nil {
		normPool.Close()
	}
	os.Exit(code)
}

func normalizeRequirePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if normPool == nil {
		t.Skip("DATABASE_URL no configurado o BD no alcanzable; saltando tests de integración de normalización")
	}
	return normPool
}

func normalizeSetup(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := storage.EnsureTestDatabase(ctx, pool); err != nil {
		t.Fatalf("guard de BD de test falló (no se trunca producción): %v", err)
	}
	if err := storage.RunMigrations(ctx, pool, "../../../migrations"); err != nil {
		t.Fatalf("migraciones: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE fundamentals, edgar_staging, securities RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func aaplStagingRow() *storage.EdgarStaging {
	payload, err := os.ReadFile("testdata/aapl_companyfacts.json")
	if err != nil {
		panic(err)
	}
	ticker := "AAPL"
	return &storage.EdgarStaging{
		CIK: "0000320193", Ticker: &ticker,
		Accession: "0000320193-24-000123", FormType: "10-K",
		FilingDate: time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC),
		Payload:    payload, PayloadType: CompanyFactsPayloadType,
	}
}

func TestNormalizeStagingAAPL(t *testing.T) {
	pool := normalizeRequirePool(t)
	normalizeSetup(t, pool)
	ctx := context.Background()

	id, err := storage.InsertStaging(ctx, pool, aaplStagingRow())
	if err != nil {
		t.Fatalf("InsertStaging falló: %v", err)
	}
	if id == nil {
		t.Fatal("InsertStaging no devolvió id")
	}

	// Primera pasada.
	if err := NormalizeStaging(ctx, pool, *id); err != nil {
		t.Fatalf("NormalizeStaging (1) falló: %v", err)
	}

	// Security creada/actualizada con el entityName del payload.
	sec, err := storage.GetSecurityByTicker(ctx, pool, "AAPL")
	if err != nil {
		t.Fatalf("GetSecurityByTicker falló: %v", err)
	}
	if sec.CIK != "0000320193" {
		t.Fatalf("CIK inesperado: %q", sec.CIK)
	}
	if sec.Name != "Apple Inc." {
		t.Fatalf("entityName no usado: name=%q", sec.Name)
	}

	// Fundamentals: valores del FY2024 (10-K de AAPL filed 2024-11-01).
	wantEnd := time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC)
	want := map[string]float64{
		"revenues":       391035000000,
		"net_earnings":   93736000000,
		"ebitda":         134661000000,
		"free_cash_flow": 108807000000,
		"total_debt":     106629000000,
		"net_debt":       76686000000,
	}
	funds, err := storage.GetFundamentalsBySecurity(ctx, pool, sec.ID, storage.FundamentalFilter{Limit: 500})
	if err != nil {
		t.Fatalf("GetFundamentalsBySecurity falló: %v", err)
	}
	if len(funds) == 0 {
		t.Fatal("no hay fundamentals normalizados")
	}

	got := map[string]float64{}
	for _, f := range funds {
		if f.PeriodEnd.Equal(wantEnd) && f.FiscalYear != nil && *f.FiscalYear == 2024 &&
			f.FiscalPeriod != nil && *f.FiscalPeriod == "FY" && f.Value != nil {
			got[f.Concept] = *f.Value
		}
	}
	if len(got) == 0 {
		t.Fatal("ningún fundamental coincide con el periodo FY2024 (end=2024-09-28)")
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("fundamental %s FY2024: got %v want %v", k, got[k], v)
		}
	}

	// C001: EntityCommonStockSharesOutstanding vive en el namespace "dei".
	// Tras la normalización debe existir shares_outstanding en fundamentals
	// con los valores reales de los 10-K del payload (instant, end=10-K date).
	wantShares := map[int]float64{
		2024: 15115823000, // 10-K filed 2024-11-01 (end 2024-10-18)
		2025: 14776353000, // 10-K filed 2025-10-31 (end 2025-10-17)
	}
	for fy, wantVal := range wantShares {
		var shares *float64
		if err := pool.QueryRow(ctx,
			`SELECT value FROM fundamentals WHERE security_id=$1 AND concept='shares_outstanding' AND fiscal_year=$2 AND fiscal_period='FY'`,
			sec.ID, fy).Scan(&shares); err != nil {
			t.Fatalf("shares_outstanding FY%d debería estar en fundamentals: %v", fy, err)
		}
		if shares == nil || *shares != wantVal {
			t.Fatalf("shares_outstanding FY%d: got %v want %v", fy, *shares, wantVal)
		}
	}

	// Los derivados se persisten con trazabilidad (raw_value = fórmula).
	var rawValue *string
	if err := pool.QueryRow(ctx,
		`SELECT raw_value FROM fundamentals WHERE security_id=$1 AND concept='ebitda' AND period_end=$2 AND fiscal_year=2024`,
		sec.ID, wantEnd).Scan(&rawValue); err != nil {
		t.Fatalf("raw_value de ebitda consultable: %v", err)
	}
	if rawValue == nil || *rawValue == "" {
		t.Fatalf("se esperaba raw_value con fórmula para ebitda, got %q", *rawValue)
	}

	// Staging marcado como normalizado.
	staging, err := storage.GetStagingByID(ctx, pool, *id)
	if err != nil {
		t.Fatalf("GetStagingByID falló: %v", err)
	}
	if !staging.Normalized {
		t.Fatal("staging debería quedar normalized=true")
	}

	// Idempotencia: el conteo SQL no varía tras la segunda pasada (ni duplica ni
	// pierde filas, gracias al ON CONFLICT keyed).
	var countBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM fundamentals WHERE security_id=$1`, sec.ID).Scan(&countBefore); err != nil {
		t.Fatalf("count fundamentals (antes) falló: %v", err)
	}
	if err := NormalizeStaging(ctx, pool, *id); err != nil {
		t.Fatalf("NormalizeStaging (2) falló: %v", err)
	}
	var countAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM fundamentals WHERE security_id=$1`, sec.ID).Scan(&countAfter); err != nil {
		t.Fatalf("count fundamentals (después) falló: %v", err)
	}
	if countAfter != countBefore {
		t.Fatalf("la segunda pasada cambió el conteo: antes=%d despues=%d", countBefore, countAfter)
	}
	if countBefore == 0 {
		t.Fatal("sin fundamentals tras la primera pasada")
	}
}

// concurrentInterestTagsPayload builds a companyfacts payload where several
// eligible interest tags describe the SAME fiscal period (same start/end/fy/fp):
// the real-world case behind ADR D31 (companies report interest under different
// us-gaap tags, and only one row per (canonical, period) may survive).
func concurrentInterestTagsPayload() []byte {
	entry := func(val float64, fy int) map[string]any {
		return map[string]any{
			"start": "2023-01-01", "end": "2023-12-31", "val": val,
			"accn": "0000000000-24-000001", "fy": fy, "fp": "FY",
			"form": "10-K", "filed": "2024-02-15",
		}
	}
	concept := func(vals ...float64) map[string]any {
		entries := make([]map[string]any, 0, len(vals))
		for _, v := range vals {
			entries = append(entries, entry(v, 2023))
		}
		return map[string]any{"units": map[string]any{"USD": entries}}
	}
	payload := map[string]any{
		"cik": 1, "entityName": "Dedupe Test Inc",
		"facts": map[string]any{
			"us-gaap": map[string]any{
				// Tres variantes del MISMO gasto de interés en el MISMO periodo.
				"InterestExpense":             concept(100),
				"InterestExpenseNonoperating": concept(150),
				"InterestExpenseDebt":         concept(200),
				"OperatingIncomeLoss":         concept(1300),
				"IncomeTaxExpenseBenefit":     concept(210),
				"IncomeLossFromContinuingOperationsBeforeIncomeTaxesExtraordinaryItemsNoncontrollingInterest": concept(1000),
			},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return b
}

// TestNormalizeStagingDedupeConcurrentInterestTags cubre Az1 e2e: tres tags de
// interés elegibles en el MISMO periodo no pueden convivir como tres filas de
// `interest_expense`; sobrevive la variante de menor prioridad (InterestExpense
// = 100). Antes del dedupe el batch ON CONFLICT podía fallar o pisar por orden
// de inserción, y el valor (100 vs 200) delataría cuál ganó.
func TestNormalizeStagingDedupeConcurrentInterestTags(t *testing.T) {
	pool := normalizeRequirePool(t)
	normalizeSetup(t, pool)
	ctx := context.Background()

	ticker := "DEDUP"
	row := &storage.EdgarStaging{
		CIK: "0000000001", Ticker: &ticker,
		Accession: "0000000000-24-000001", FormType: "10-K",
		FilingDate: time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC),
		Payload:    concurrentInterestTagsPayload(), PayloadType: CompanyFactsPayloadType,
	}
	id, err := storage.InsertStaging(ctx, pool, row)
	if err != nil {
		t.Fatalf("InsertStaging: %v", err)
	}
	if id == nil {
		t.Fatal("InsertStaging no devolvió id")
	}
	if err := NormalizeStaging(ctx, pool, *id); err != nil {
		t.Fatalf("NormalizeStaging: %v", err)
	}

	sec, err := storage.GetSecurityByTicker(ctx, pool, ticker)
	if err != nil {
		t.Fatalf("GetSecurityByTicker: %v", err)
	}
	end := time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC)

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM fundamentals WHERE security_id=$1 AND concept='interest_expense' AND period_end=$2`,
		sec.ID, end).Scan(&n); err != nil {
		t.Fatalf("count interest_expense: %v", err)
	}
	if n != 1 {
		t.Fatalf("debe quedar UNA fila de interest_expense por periodo, hay %d", n)
	}

	var val *float64
	var sourceFactID *string
	if err := pool.QueryRow(ctx,
		`SELECT value, source_fact_id FROM fundamentals WHERE security_id=$1 AND concept='interest_expense' AND period_end=$2`,
		sec.ID, end).Scan(&val, &sourceFactID); err != nil {
		t.Fatalf("select interest_expense: %v", err)
	}
	if val == nil || *val != 100 {
		got := "nil"
		if val != nil {
			got = strconv.FormatFloat(*val, 'g', -1, 64)
		}
		t.Fatalf("debe sobrevivir InterestExpense (100), got %s", got)
	}
	if sourceFactID == nil || !strings.HasSuffix(*sourceFactID, "#InterestExpense") {
		t.Fatalf("source_fact_id debe apuntar al tag de prioridad 1: %v", sourceFactID)
	}

	// El trío fiscal del mismo periodo queda alineado y con sus valores.
	want := map[string]float64{
		"operating_income":   1300,
		"income_tax_expense": 210,
		"pretax_income":      1000,
	}
	for concept, wantVal := range want {
		var got *float64
		if err := pool.QueryRow(ctx,
			`SELECT value FROM fundamentals WHERE security_id=$1 AND concept=$2 AND period_end=$3`,
			sec.ID, concept, end).Scan(&got); err != nil {
			t.Fatalf("%s FY2023: %v", concept, err)
		}
		if got == nil || *got != wantVal {
			t.Fatalf("%s FY2023: got %v want %v", concept, got, wantVal)
		}
	}
}

func TestNormalizePendingAAPL(t *testing.T) {
	pool := normalizeRequirePool(t)
	normalizeSetup(t, pool)
	ctx := context.Background()

	// Segunda fila: el 10-K de FY2025 (acceso distinto -> no conflicto).
	row := aaplStagingRow()
	row.Accession = "0000320193-25-000079"
	row.FilingDate = time.Date(2025, 10, 31, 0, 0, 0, 0, time.UTC)
	if _, err := storage.InsertStaging(ctx, pool, row); err != nil {
		t.Fatalf("InsertStaging falló: %v", err)
	}

	done, err := NormalizePending(ctx, pool, 5)
	if err != nil {
		t.Fatalf("NormalizePending devolvió error: %v", err)
	}
	if done != 1 {
		t.Fatalf("NormalizePending: se esperaba 1 fila normalizada, hubo %d", done)
	}

	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM edgar_staging WHERE normalized = false`).Scan(&pending); err != nil {
		t.Fatalf("count pending falló: %v", err)
	}
	if pending != 0 {
		t.Fatalf("no deberían quedar pendientes, quedan %d", pending)
	}

	var secCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM securities WHERE ticker='AAPL'`).Scan(&secCount); err != nil {
		t.Fatalf("count securities falló: %v", err)
	}
	if secCount != 1 {
		t.Fatalf("securities: se esperaba 1 fila para AAPL, hay %d", secCount)
	}

	// De nuevo: sin pendientes -> 0 normalizados, sin error.
	done, err = NormalizePending(ctx, pool, 5)
	if err != nil {
		t.Fatalf("NormalizePending (vacío) falló: %v", err)
	}
	if done != 0 {
		t.Fatalf("sin pendientes should done=0, got %d", done)
	}
}
