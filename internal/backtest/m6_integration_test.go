//go:build integration

package backtest

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/growth"
	"github.com/miky/abys-invest/internal/modelcfg"
	"github.com/miky/abys-invest/internal/quality"
	"github.com/miky/abys-invest/internal/relative"
	"github.com/miky/abys-invest/internal/score"
	"github.com/miky/abys-invest/internal/storage"
	"github.com/miky/abys-invest/internal/testsupport"
	"github.com/miky/abys-invest/internal/valuation"
	"github.com/miky/abys-invest/internal/wacc"
)

// These tests are the fixture PROOF of B13: a persisted 2.1.0 (history) or
// 2.2.0 (current, M6c-T1 W6b) trace replays to the same score, and the forward
// returns come from the adjusted close with the §28 lag window. They live in
// the integration suite because the whole point is the round trip through the
// database.

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		os.Exit(0)
	}
	// Validate and redact DSN before connecting (ADR D30)
	// Validate the DSN targets a test DB; only its redacted form is ever logged (ADR D30).
	testsupport.EnsureTestDSN(dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := storage.Connect(ctx, dsn)
	if err != nil {
		panic("backtest integration: Connect: " + err.Error())
	}
	defer pool.Close()
	if err := storage.EnsureTestDatabase(ctx, pool); err != nil {
		panic("backtest integration: guard de BD de test: " + err.Error())
	}
	// Serialise the database phase of the integration suites (shared TRUNCATEs),
	// which is what lets them run WITHOUT `-p 1`.
	lockCtx, lockCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	releaseLock, err := testsupport.LockIntegrationDB(lockCtx, pool)
	lockCancel()
	if err != nil {
		panic("backtest integration: no se pudo tomar el advisory lock: " + err.Error())
	}
	defer releaseLock()
	if _, err := pool.Exec(ctx, `TRUNCATE daily_prices, scores, securities, parameter_sets RESTART IDENTITY CASCADE`); err != nil {
		panic("backtest integration: truncate: " + err.Error())
	}
	if err := storage.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		panic("backtest integration: migraciones: " + err.Error())
	}
	if code := m.Run(); code != 0 {
		os.Exit(code)
	}
	os.Exit(0)
}

func f64(v float64) *float64 { return &v }

// replaySecurity seeds one security with a score of the CURRENT revision
// (trace) and the daily bars needed for the three horizons.
func replaySecurity(t *testing.T, pool *pgxpool.Pool, ticker string, asOf time.Time, basePrice float64, withForward bool) int64 {
	t.Helper()
	return replaySecurityVersion(t, pool, ticker, asOf, basePrice, withForward, "")
}

// replaySecurityVersion is replaySecurity with an EXPLICIT revision to persist
// (M6c-T1 W6b): "" means "whatever the engine currently produces" (2.2.0); any
// other value seeds a HISTORY row whose trace is stamped with that same
// revision, exactly as it was written before the bump (§26: the old rows stay
// readable, they are not rewritten).
func replaySecurityVersion(t *testing.T, pool *pgxpool.Pool, ticker string, asOf time.Time, basePrice float64, withForward bool, modelVersion string) int64 {
	t.Helper()
	ctx := context.Background()
	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{Ticker: ticker, CIK: "9000" + ticker, Type: "stock", Currency: "USD", Status: "active"})
	if err != nil {
		t.Fatalf("upsert security: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	bars := []storage.DailyPrice{{SecurityID: sec.ID, Date: asOf, Close: basePrice, AdjustedClose: basePrice, Source: "test"}}
	if withForward {
		// Un +10% en cada horizonte, con las fechas dentro de la ventana de 5 días.
		for i, h := range ReplayHorizons {
			bars = append(bars, storage.DailyPrice{
				SecurityID: sec.ID, Date: asOf.AddDate(0, 0, h),
				Close: basePrice * (1 + 0.10), AdjustedClose: basePrice * (1 + 0.10), Source: "test",
			})
			_ = i
		}
	}
	if err := storage.UpsertDailyPrices(ctx, tx, bars); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert daily prices: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	mc := modelcfg.DefaultModelConfig()
	mc.ParameterSetName, mc.ParameterSetID = "base", 0
	// Use values where all 5 dimensions are valid but the weight shift between
	// base and conservative produces a different rounded score.
	// Base weights: graham 0.15, dcf 0.20, quality 0.35, relative 0.15, mc 0.05 (sum=0.90)
	// Conservative: graham 0.10, dcf 0.15, quality 0.40, relative 0.20, mc 0.05 (sum=0.90)
	// With graham=60, dcf=60, quality=90, relative=50, mc=77:
	//   base = (60*0.15 + 60*0.20 + 90*0.35 + 50*0.15 + 77*0.05) / 0.90 = 70.94 → 71
	//   conservative = (60*0.10 + 60*0.15 + 90*0.40 + 50*0.20 + 77*0.05) / 0.90 = 72.06 → 72
	in := score.ScoreInput21{
		Ticker: ticker, AsOf: asOf, Price: basePrice,
		GrahamBase: f64(basePrice * 1.5), DCFBase: f64(basePrice * 1.2),
		Quality:  score.QualityDetailFrom(&quality.Result{Score: f64(90), Coverage: 0.9, Confidence: quality.ConfidenceHigh}),
		Relative: score.RelativeDetailFrom(&relative.Result{Score: f64(50), Coverage: 0.8, Confidence: relative.ConfidenceMedium}),
		SMA50:    f64(basePrice * 1.1), SMA200: f64(basePrice), Momentum6m: f64(0.05), Momentum12m: f64(0.02),
		MarginOfSafety: mc.TargetMarginOfSafety,
	}
	res := score.CalculateScore21(in, mc)
	trace := score.BuildTrace21(in, res)
	if modelVersion == "" {
		modelVersion = res.ModelVersion
	} else {
		trace.ModelVersion = modelVersion
	}
	raw, err := trace.Marshal()
	if err != nil {
		t.Fatalf("marshal trace: %v", err)
	}
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin scores: %v", err)
	}
	if err := storage.UpsertScore(ctx, tx2, &storage.Score{
		SecurityID: sec.ID, AsOf: asOf, Score: res.Score, Signal: res.Signal,
		Justification: res.Justification, InputsSnapshot: raw, ModelVersion: modelVersion,
	}); err != nil {
		tx2.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert score: %v", err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("commit score: %v", err)
	}
	return sec.ID
}

// B13: el score persistido se reproduce desde su trace, sin tocar el motor.
func TestReplayReproduceElScorePersistido(t *testing.T) {
	pool := replPool(t)
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	asOf := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	replaySecurity(t, pool, "RPL1", asOf, 100, true)

	rows, err := ReplayScores(context.Background(), pool, ReplayOptions{TickersCSV: "RPL1"})
	if err != nil {
		t.Fatalf("ReplayScores: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("se esperaba 1 fila, got %d", len(rows))
	}
	r := rows[0]
	if r.Error != "" {
		t.Fatalf("error por security: %s", r.Error)
	}
	if !r.Reproduces {
		t.Fatalf("el score no se reproduce: %d vs %d (%s)", r.StoredScore, r.ReplayedScore, r.DivergenceReason)
	}
	if r.StoredScore != r.ReplayedScore {
		t.Fatalf("score almacenado %d != reproducido %d", r.StoredScore, r.ReplayedScore)
	}
	if r.ModelVersion != score.ModelVersion22 {
		t.Fatalf("model_version: esperado la revisión vigente %q, got %q", score.ModelVersion22, r.ModelVersion)
	}
	if len(r.Forward) != 3 {
		t.Fatalf("se esperaban 3 horizontes, got %d", len(r.Forward))
	}
	for i, fr := range r.Forward {
		if fr.HorizonDays != ReplayHorizons[i] {
			t.Fatalf("horizonte %d: esperado %d", i, ReplayHorizons[i])
		}
		if fr.ReturnPct == nil {
			t.Fatalf("horizonte %d sin retorno: %s", fr.HorizonDays, fr.Reason)
		}
		if d := *fr.ReturnPct - 10; d > 1e-9 || d < -1e-9 {
			t.Fatalf("horizonte %d: esperado +10%%, got %v", fr.HorizonDays, *fr.ReturnPct)
		}
	}
}

// M6c-T1 W6b (§26): el gate de replay acepta las DOS revisiones con trace —
// 2.2.0 (vigente) y 2.1.0 (historia) — porque comparten la misma forma de
// trace, y sigue omitiendo con motivo explícito las revisiones SIN trace
// (1.1.0/2.0.0). La historia no se reescribe: se reproduce.
func TestReplayAcepta220VigenteYConserva210Historica(t *testing.T) {
	pool := replPool(t)
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	ctx := context.Background()
	asOf := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)
	// 2.2.0: lo que escribe el motor HOY (replaySecurity sin revisión explícita).
	replaySecurity(t, pool, "W6B22", asOf, 100, true)
	// 2.1.0: fila histórica con SU revisión dentro del trace.
	replaySecurityVersion(t, pool, "W6B21", asOf, 120, true, score.ModelVersion21)
	// 1.1.0: revisión SIN trace (su inputs_snapshot es un ScoreInput viejo).
	seedLegacyScoreWithoutTrace(t, pool, "W6B11", asOf)

	rows, err := ReplayScores(ctx, pool, ReplayOptions{TickersCSV: "W6B22,W6B21,W6B11"})
	if err != nil {
		t.Fatalf("ReplayScores: %v", err)
	}
	byTicker := map[string]ReplayResult{}
	for _, r := range rows {
		byTicker[r.Ticker] = r
	}
	if len(byTicker) != 3 {
		t.Fatalf("se esperaban 3 filas (2.2.0 + 2.1.0 + 1.1.0), got %d (%+v)", len(byTicker), rows)
	}

	cur := byTicker["W6B22"]
	if cur.Error != "" {
		t.Fatalf("2.2.0 (vigente) NO debe marcarse skip: %s", cur.Error)
	}
	if cur.ModelVersion != score.ModelVersion22 {
		t.Fatalf("vigente: esperado %q, got %q", score.ModelVersion22, cur.ModelVersion)
	}
	if !cur.Reproduces || cur.StoredScore != cur.ReplayedScore {
		t.Fatalf("2.2.0 debe reproducirse: %d vs %d (%s)", cur.StoredScore, cur.ReplayedScore, cur.DivergenceReason)
	}

	hist := byTicker["W6B21"]
	if hist.Error != "" {
		t.Fatalf("2.1.0 (historia) debe seguir reproduciéndose, no omitirse: %s", hist.Error)
	}
	if hist.ModelVersion != score.ModelVersion21 {
		t.Fatalf("historia: esperado %q, got %q", score.ModelVersion21, hist.ModelVersion)
	}
	if !hist.Reproduces || hist.StoredScore != hist.ReplayedScore {
		t.Fatalf("2.1.0 debe reproducirse: %d vs %d (%s)", hist.StoredScore, hist.ReplayedScore, hist.DivergenceReason)
	}

	legacy := byTicker["W6B11"]
	if !strings.HasPrefix(legacy.Error, "skip:model_version=1.1.0") {
		t.Fatalf("1.1.0 (sin trace) debe OMITIRSE con su motivo, got %q", legacy.Error)
	}
	if !strings.Contains(legacy.Error, "2.1.0/2.2.0") {
		t.Fatalf("el motivo del skip debe nombrar las revisiones CON trace, got %q", legacy.Error)
	}
	if legacy.Reproduces {
		t.Fatalf("1.1.0 no es reproducible: no tiene trace que replayear")
	}
}

// seedLegacyScoreWithoutTrace persiste una fila como las previas a M6c: un
// inputs_snapshot que NO es un Trace21. Existe para probar el gate de replay.
func seedLegacyScoreWithoutTrace(t *testing.T, pool *pgxpool.Pool, ticker string, asOf time.Time) {
	t.Helper()
	ctx := context.Background()
	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
		Ticker: ticker, CIK: "9000" + ticker, Type: "stock", Currency: "USD", Status: "active",
	})
	if err != nil {
		t.Fatalf("upsert security: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := storage.UpsertScore(ctx, tx, &storage.Score{
		SecurityID: sec.ID, AsOf: asOf, Score: 45, Signal: "mantener",
		Justification: "fixture 1.1.0 sin trace", InputsSnapshot: []byte(`{"graham_intrinsic":150}`),
		ModelVersion: "1.1.0",
	}); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert score legacy: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit score legacy: %v", err)
	}
}

// (k) -all-revisions: el default replayea SOLO el max(as_of) de cada ticker;
// el flag audita TODAS las revisiones persistidas.
//
// El default no cambia porque el reporte histórico depende de él (una fila por
// ticker = "el modelo de HOY sobre lo que sé HOY"). Lo que el default NO puede
// responder es si el modelo reproduce su propio PASADO fila por fila, que es lo
// que este flag existe para auditar.
func TestReplayAllRevisionsAuditaElHistorico(t *testing.T) {
	pool := replPool(t)
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	// Cuatro as_of para el MISMO ticker: el default debe devolver solo la última.
	base := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	asOfs := []time.Time{
		base,
		base.AddDate(0, 0, 30),
		base.AddDate(0, 0, 60),
		base.AddDate(0, 0, 90),
	}
	for i, d := range asOfs {
		replaySecurity(t, pool, "REV1", d, 100+float64(i)*10, true)
	}

	// Default: una sola fila (la de max(as_of)).
	def, err := ReplayScores(context.Background(), pool, ReplayOptions{TickersCSV: "REV1"})
	if err != nil {
		t.Fatalf("ReplayScores default: %v", err)
	}
	if len(def) != 1 {
		t.Fatalf("el default debe devolver 1 fila (max as_of), got %d", len(def))
	}
	if !def[0].AsOf.Equal(asOfs[len(asOfs)-1]) {
		t.Errorf("el default debe quedarse con la última as_of %v, got %v",
			asOfs[len(asOfs)-1], def[0].AsOf)
	}

	// -all-revisions: todas las as_of, en orden descendente (estable).
	all, err := ReplayScores(context.Background(), pool, ReplayOptions{
		TickersCSV: "REV1", AllRevisions: true,
	})
	if err != nil {
		t.Fatalf("ReplayScores all-revisions: %v", err)
	}
	if len(all) != len(asOfs) {
		t.Fatalf("-all-revisions debe devolver %d filas, got %d", len(asOfs), len(all))
	}
	for i, r := range all {
		if r.Error != "" {
			t.Errorf("fila %d con error: %s", i, r.Error)
		}
		want := asOfs[len(asOfs)-1-i]
		if !r.AsOf.Equal(want) {
			t.Errorf("fila %d: esperado as_of %v, got %v", i, want, r.AsOf)
		}
		if !r.Reproduces {
			t.Errorf("fila %d (%v) no se reproduce: %d vs %d (%s)",
				i, r.AsOf, r.StoredScore, r.ReplayedScore, r.DivergenceReason)
		}
	}
}

// -all-revisions combinado con -as-of audita un histórico ACOTADO: hasta la
// fecha inclusiva, no todo el historial.
func TestReplayAllRevisionsRespetaAsOf(t *testing.T) {
	pool := replPool(t)
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	asOfs := []time.Time{
		base,
		base.AddDate(0, 0, 30),
		base.AddDate(0, 0, 60),
	}
	for i, d := range asOfs {
		replaySecurity(t, pool, "REV2", d, 200+float64(i)*10, true)
	}

	rows, err := ReplayScores(context.Background(), pool, ReplayOptions{
		TickersCSV: "REV2", AllRevisions: true, AsOf: asOfs[1],
	})
	if err != nil {
		t.Fatalf("ReplayScores: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("con -as-of %v deben salir 2 filas, got %d", asOfs[1], len(rows))
	}
	for _, r := range rows {
		if r.AsOf.After(asOfs[1]) {
			t.Errorf("fila %v posterior al corte %v", r.AsOf, asOfs[1])
		}
	}
}

// B13: sin barra en la ventana el retorno es n/d (no_forward_price), NO el bar
// más cercano: un retorno de otro periodo con esta etiqueta sería una mentira.
func TestReplaySinPrecioForwardDaND(t *testing.T) {
	pool := replPool(t)
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	asOf := time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)
	replaySecurity(t, pool, "RPL2", asOf, 50, false)

	rows, err := ReplayScores(context.Background(), pool, ReplayOptions{TickersCSV: "RPL2"})
	if err != nil {
		t.Fatalf("ReplayScores: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("se esperaba 1 fila, got %d", len(rows))
	}
	if !rows[0].Reproduces {
		t.Fatalf("el score debe reproducirse aunque no haya precio forward: %+v", rows[0])
	}
	for _, fr := range rows[0].Forward {
		if fr.ReturnPct != nil {
			t.Fatalf("horizonte %d: esperado n/d, got %v", fr.HorizonDays, *fr.ReturnPct)
		}
		if fr.Reason != ReasonNoForwardPrice {
			t.Fatalf("horizonte %d: esperado %q, got %q", fr.HorizonDays, ReasonNoForwardPrice, fr.Reason)
		}
	}
}

// B13: la tolerancia de 5 días absorbe un fin de semana sin inventar el retorno.
func TestReplayToleranciaDeCincoDias(t *testing.T) {
	pool := replPool(t)
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	asOf := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	secID := replaySecurity(t, pool, "RPL3", asOf, 20, false)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// asOf + 22 días: dentro de [20, 25].
	if err := storage.UpsertDailyPrices(ctx, tx, []storage.DailyPrice{{
		SecurityID: secID, Date: asOf.AddDate(0, 0, 22),
		Close: 22, AdjustedClose: 22, Source: "test",
	}}); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	rows, err := ReplayScores(ctx, pool, ReplayOptions{TickersCSV: "RPL3"})
	if err != nil {
		t.Fatalf("ReplayScores: %v", err)
	}
	var h20 *ForwardReturn
	for i := range rows[0].Forward {
		if rows[0].Forward[i].HorizonDays == 20 {
			h20 = &rows[0].Forward[i]
		}
	}
	if h20 == nil || h20.ReturnPct == nil {
		t.Fatalf("el horizonte 20 debe encontrar la barra del día 22: %+v", h20)
	}
	if d := *h20.ReturnPct - 10; d > 1e-9 || d < -1e-9 {
		t.Fatalf("retorno 20d: esperado +10%%, got %v", *h20.ReturnPct)
	}
	// Y un barra 10 días tarde NO cuenta.
	if rows[0].Forward[1].ReturnPct != nil {
		t.Fatalf("el horizonte 60 no debe encontrar precio: %+v", rows[0].Forward[1])
	}
}

// B13: con un parameter set DISTINTO al almacenado el score DEBE cambiar.
// El set `conservative` tiene target_margin_of_safety=40 (vs 30 base) y
// quality_sub_weights {profitability:0.20, growth:0.10, margins:0.10, stability:0.30, debt_solvency:0.30}
// (vs 0.20 cada uno en base). Estos cambios SÍ mueven el score.
func TestReplayConParameterSetDistinto(t *testing.T) {
	pool := replPool(t)
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	asOf := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	replaySecurity(t, pool, "RPL4", asOf, 80, true)

	same, err := ReplayScores(context.Background(), pool, ReplayOptions{TickersCSV: "RPL4"})
	if err != nil {
		t.Fatalf("ReplayScores: %v", err)
	}
	if !same[0].Reproduces {
		t.Fatalf("con el set por defecto debe reproducirse: %+v", same[0])
	}
	other, err := ReplayScores(context.Background(), pool, ReplayOptions{TickersCSV: "RPL4", ParameterSet: "conservative"})
	if err != nil {
		t.Fatalf("ReplayScores con set: %v", err)
	}
	if other[0].ParameterSet != "conservative" {
		t.Fatalf("esperado parameter_set conservative, got %q", other[0].ParameterSet)
	}
	// El set conservative DEBE producir un score distinto al base.
	// El mecanismo real es el shift de pesos top-level:
	// base: graham 0.15, dcf 0.20, quality 0.35, relative 0.15, mc 0.05 (sum=0.90)
	// conservative: graham 0.10, dcf 0.15, quality 0.40, relative 0.20, mc 0.05 (sum=0.90)
	// target_margin_of_safety: 40 vs 30 también cambia el MOS de Graham/DCF.
	// quality_sub_weights: stability/solvency 0.30 vs 0.20 altera la dimensión quality.
	if other[0].ReplayedScore == other[0].StoredScore {
		t.Fatalf("con parameter_set=conservative el score DEBE ser distinto al base (stored=%d, replayed=%d). Diferencia esperada por shift de pesos top-level, MOS 40 vs 30 y sub-pesos quality alterados",
			other[0].StoredScore, other[0].ReplayedScore)
	}
	if other[0].DivergenceReason != "parameter_set_override" {
		t.Fatalf("una diferencia debe declararse como override: %+v", other[0])
	}
	if other[0].Reproduces {
		t.Fatalf("con otro set el score no 'se reproduce': %+v", other[0])
	}
	// §18 estricto: con las cinco dimensiones válidas el peso USADO es la suma
	// configurada, 0.90 (no 1.00 — el 0.10 restante queda sin asignar a propósito).
	if math.Abs(other[0].WeightUsed-0.90) > 1e-9 {
		t.Fatalf("weight_used con las cinco dimensiones válidas: esperado 0.90 (§18), got %v", other[0].WeightUsed)
	}
}

// Un trace corrupto falla por security y no aborta el lote.
func TestReplayTraceIlegibleNoAbortaElLote(t *testing.T) {
	pool := replPool(t)
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	asOf := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)
	replaySecurity(t, pool, "RPL5", asOf, 10, true)
	ctx := context.Background()
	secID := replSecurityID(t, pool, "RPL5")
	if _, err := pool.Exec(ctx, `UPDATE scores SET inputs_snapshot = '{"trace_version":"99"}'::jsonb WHERE security_id = $1`, secID); err != nil {
		t.Fatalf("corromper snapshot: %v", err)
	}
	rows, err := ReplayScores(ctx, pool, ReplayOptions{TickersCSV: "RPL5"})
	if err != nil {
		t.Fatalf("ReplayScores no debe abortar: %v", err)
	}
	if len(rows) != 1 || rows[0].Error == "" {
		t.Fatalf("se esperaba un error por security: %+v", rows)
	}
	if rows[0].Reproduces {
		t.Fatalf("un trace ilegible no puede reproducirse")
	}
}

func replPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := storage.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func replSecurityID(t *testing.T, pool *pgxpool.Pool, ticker string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `SELECT id FROM securities WHERE ticker = $1`, ticker).Scan(&id); err != nil {
		t.Fatalf("id de %s: %v", ticker, err)
	}
	return id
}

// TestReplayFullChain verifies that FullChain replay exposes ChainSteps and
// ComputedFrom correctly, and that it handles nil market_context gracefully.
func TestReplayFullChain(t *testing.T) {
	pool := replPool(t)
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	asOf := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)

	// Seed a security with a complete score trace (current revision 2.2.0) AND
	// the underlying snapshots for growth, wacc, and valuation.
	seedFullChainFixtures(t, pool, "FC1", asOf, 150.0)

	rows, err := ReplayScores(context.Background(), pool, ReplayOptions{
		TickersCSV: "FC1",
		FullChain:  true,
	})
	if err != nil {
		t.Fatalf("ReplayScores FullChain: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("se esperaba 1 fila, got %d", len(rows))
	}
	r := rows[0]

	if r.Error != "" {
		t.Fatalf("error por security: %s", r.Error)
	}

	// Verify ChainSteps is populated
	if r.ChainSteps == nil {
		t.Fatal("ChainSteps debe estar poblado en FullChain")
	}
	if r.ChainSteps.Growth == nil {
		t.Fatal("ChainSteps.Growth no debe ser nil")
	}
	if r.ChainSteps.WACC == nil {
		t.Fatal("ChainSteps.WACC no debe ser nil")
	}
	if r.ChainSteps.Valuation == nil {
		t.Fatal("ChainSteps.Valuation no debe ser nil")
	}
	if r.ChainSteps.Quality == nil {
		t.Fatal("ChainSteps.Quality no debe ser nil")
	}
	if r.ChainSteps.Relative == nil {
		t.Fatal("ChainSteps.Relative no debe ser nil")
	}
	if r.ChainSteps.Score == nil {
		t.Fatal("ChainSteps.Score no debe ser nil")
	}

	// Verify ComputedFrom declares recomputed/traced correctly
	if r.ComputedFrom == nil {
		t.Fatal("ComputedFrom no debe ser nil")
	}
	expectedComputedFrom := map[string]string{
		"growth":         "recomputed",
		"wacc":           "recomputed",
		"valuation":      "recomputed",
		"quality":        "traced",
		"relative":       "traced",
		"market_context": "traced",
	}
	for k, v := range expectedComputedFrom {
		if r.ComputedFrom[k] != v {
			t.Errorf("ComputedFrom[%q]: got %q, want %q", k, r.ComputedFrom[k], v)
		}
	}

	// Verify the replayed score is reasonable (should reproduce or differ
	// only due to parameter set, but here we use the same config)
	if r.ReplayedScore < 0 || r.ReplayedScore > 100 {
		t.Errorf("ReplayedScore fuera de rango: %d", r.ReplayedScore)
	}
}

// TestReplayFullChainGrowthUsesChainConfig is the assertion (g) asks for: the
// growth step must be built from the SAME resolved ModelConfig as the wacc step,
// not from growth.ConfigFromEnv() read in isolation.
//
// The test asks for the `conservative` set ON PURPOSE. With no set, ReplayScores
// resolves mc from the env, which today makes ConfigFromModelConfig(mc) and
// ConfigFromEnv() interchangeable — a numeric assertion would then pass against
// either wiring and prove nothing. Asking for a set makes mc a config that is
// demonstrably NOT the environment, so "every step used the same mc" becomes an
// observable property:
//
//   - the WACC step reports the tax rate of that set (internal/wacc reads
//     mc.TaxRate), proving mc reached the chain;
//   - the growth step is still 'recomputed' — it did not fall back to a traced
//     value nor to an env-only reader — and it is recomputed through
//     growth.ConfigFromModelConfig, the anchor the replay now uses.
func TestReplayFullChainGrowthUsesChainConfig(t *testing.T) {
	pool := replPool(t)
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	asOf := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	seedFullChainFixtures(t, pool, "FC2", asOf, 160.0)

	// The mc the replay will build internally, resolved exactly the way
	// ReplayScores does it, so the expectations below are not guesses.
	_, want, err := modelcfg.Resolve(context.Background(), pool, "conservative")
	if err != nil {
		t.Fatalf("resolve conservative: %v", err)
	}

	rows, err := ReplayScores(context.Background(), pool, ReplayOptions{
		TickersCSV:   "FC2",
		FullChain:    true,
		ParameterSet: "conservative",
	})
	if err != nil {
		t.Fatalf("ReplayScores FullChain: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("se esperaba 1 fila, got %d", len(rows))
	}
	r := rows[0]
	if r.ChainSteps == nil || r.ChainSteps.Growth == nil || r.ChainSteps.WACC == nil {
		t.Fatalf("ChainSteps incompletos: %+v", r.ChainSteps)
	}

	// The chain's ModelConfig reached the steps: the WACC tax rate is the one of
	// the resolved set, not the env default.
	if r.ChainSteps.WACC.TaxRate == nil {
		t.Fatal("el step WACC debe reportar el tax rate")
	}
	if got := *r.ChainSteps.WACC.TaxRate; got != want.QualityTaxRate {
		t.Errorf("el step WACC debe usar el tax rate del chain (%v), got %v (env=%v)",
			want.QualityTaxRate, got, modelcfg.DefaultQualityTaxRate)
	}

	// The growth step came from the SAME chain, recomputed (not traced).
	if r.ComputedFrom["growth"] != "recomputed" {
		t.Errorf("el step growth debe recomputarse con el config del chain, got %q",
			r.ComputedFrom["growth"])
	}
	// And it is derived through the anchor, not an env-isolated reader: with no
	// growth knob in ModelConfig (§28 RESERVED) the two agree, which is the
	// equivalence pinned in internal/growth/config_modelcfg_test.go.
	if growth.ConfigFromModelConfig(want) != growth.ConfigFromEnv() {
		t.Error("growth.ConfigFromModelConfig debe derivar del mismo config del chain")
	}
}

// seedFullChainFixtures creates a security with all the snapshots needed for
// FullChain replay: growth_metrics, wacc_metrics, valuation_results, and a
// score trace of the CURRENT revision (2.2.0).
func seedFullChainFixtures(t *testing.T, pool *pgxpool.Pool, ticker string, asOf time.Time, basePrice float64) {
	t.Helper()
	ctx := context.Background()

	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
		Ticker: ticker, CIK: "9001" + ticker, Type: "stock", Currency: "USD", Status: "active",
	})
	if err != nil {
		t.Fatalf("upsert security: %v", err)
	}

	// 1. Daily price for as_of
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := storage.UpsertDailyPrices(ctx, tx, []storage.DailyPrice{{
		SecurityID: sec.ID, Date: asOf, Close: basePrice, AdjustedClose: basePrice, Source: "test",
	}}); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert daily prices: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// 2. Growth snapshot
	gIn := growth.Inputs{
		Ticker: ticker, AsOf: asOf,
		Series: growth.Series{
			EPS:     growthSerie(6, 1.0, 0.10),
			FCF:     growthSerie(6, 1.0, 0.12),
			Revenue: growthSerie(6, 100.0, 0.08),
		},
	}
	gCfg := growth.DefaultConfig()
	gRes := growth.Calculate(gIn, gCfg)
	gSnap, err := gRes.Snapshot(gIn, gCfg)
	if err != nil {
		t.Fatalf("growth snapshot: %v", err)
	}

	// 3. WACC snapshot
	wIn := wacc.Inputs{
		Ticker: ticker, AsOf: asOf,
		EquityValue: f64(basePrice * 1e9),
		DebtValue:   f64(40e9),
		Beta:        f64(1.2),
		BetaAsOf:    ptrTime(asOf.AddDate(0, -1, 0)),
		BetaSource:  wacc.BetaSourceHistory,
		Reasons:     []string{"beta_history"},
	}
	wCfg := wacc.ConfigFromEnv() // uses env or defaults
	wRes := wacc.Calculate(wIn, wCfg)
	wSnap, err := wRes.Snapshot(wIn, wCfg)
	if err != nil {
		t.Fatalf("wacc snapshot: %v", err)
	}

	// 4. Valuation snapshot (uses the recomputed growth and wacc results)
	vIn := valuation.Inputs{
		Ticker:               ticker,
		AsOf:                 asOf,
		Price:                f64v(basePrice),
		EPS:                  f64v(5.0),
		FreeCashFlow:         f64v(100e6),
		SharesOutstanding:    f64v(1e9),
		NetDebt:              f64v(0),
		NormalizedGrowthRate: gRes.NormalizedGrowthRate,
		GrowthConfidence:     valuation.Confidence(gRes.Confidence),
		GrowthSource:         gRes.Source,
		GrowthModelVersion:   growth.ModelVersion,
		WACC:                 wRes.WACC,
		CostOfEquity:         wRes.Ke,
		WACCSource:           wRes.Source,
		WACCConfidence:       valuation.Confidence(wRes.Confidence),
		WACCModelVersion:     wacc.ModelVersion,
		BetaObserved:         wRes.BetaObserved,
	}
	vCfg := valuation.DefaultConfig()
	vRes := valuation.Calculate(vIn, vCfg)
	vSnap, err := valuation.MarshalSnapshot(vIn, vCfg, vRes)
	if err != nil {
		t.Fatalf("valuation snapshot: %v", err)
	}

	// 5. Persist growth_metrics
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin growth: %v", err)
	}
	if err := storage.UpsertGrowthMetric(ctx, tx, &storage.GrowthMetric{
		SecurityID: sec.ID, AsOf: asOf,
		RevenueCAGR3y: gRes.RevenueCAGR3y, RevenueCAGR5y: gRes.RevenueCAGR5y,
		EPSCAGR3y: gRes.EPSCAGR3y, EPSCAGR5y: gRes.EPSCAGR5y,
		FCFCAGR3y: gRes.FCFCAGR3y, FCFCAGR5y: gRes.FCFCAGR5y,
		NormalizedGrowthRate: gRes.NormalizedGrowthRate,
		Confidence:           gRes.Confidence, Source: gRes.Source,
		Clamped: gRes.Clamped, RevenueDiscrepancy: gRes.RevenueDiscrepancy,
		InputsSnapshot: gSnap, ModelVersion: growth.ModelVersion,
	}); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert growth: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit growth: %v", err)
	}

	// 6. Persist wacc_metrics
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin wacc: %v", err)
	}
	if err := storage.UpsertWaccMetric(ctx, tx, &storage.WaccMetric{
		SecurityID: sec.ID, AsOf: asOf,
		Wacc: wRes.WACC, CostOfEquity: wRes.Ke, CostOfDebtAfterTax: wRes.KdAfterTax,
		WeightEquity: wRes.WeightEquity, WeightDebt: wRes.WeightDebt,
		Beta: wRes.Beta, BetaObserved: wRes.BetaObserved,
		Source: wRes.Source, Confidence: wRes.Confidence,
		// BetaUpdatedAt, BetaSource, Reasons, Resolved* not in storage.WaccMetric
		InputsSnapshot: wSnap, ModelVersion: wacc.ModelVersion,
	}); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert wacc: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit wacc: %v", err)
	}

	// 7. Persist valuation_results
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin valuation: %v", err)
	}
	if err := storage.UpsertValuationResult(ctx, tx, &storage.ValuationResult{
		SecurityID: sec.ID, AsOf: asOf,
		GrahamBase: vRes.Graham.Base, GrahamBear: vRes.Graham.Bear, GrahamBull: vRes.Graham.Bull,
		DcfBase: vRes.DCF.Base, DcfBear: vRes.DCF.Bear, DcfBull: vRes.DCF.Bull,
		ValuationStatus: string(vRes.Status), ValuationConfidence: string(vRes.Confidence),
		GrahamStatus: string(vRes.Graham.Status), GrahamConfidence: strPtr(string(vRes.Graham.Confidence)),
		DcfStatus: string(vRes.DCF.Status), DcfConfidence: strPtr(string(vRes.DCF.Confidence)),
		GrahamMos: vRes.MOS.GrahamBase, DcfBaseMos: vRes.MOS.DCFBase,
		DcfBearMos: vRes.MOS.DCFBear, DcfBullMos: vRes.MOS.DCFBull,
		ValuationMean: vRes.Uncertainty.Mean, ValuationStddev: vRes.Uncertainty.StdDev,
		ValuationDispersion: vRes.Uncertainty.Dispersion, ValuationComponents: int16(vRes.Uncertainty.Components),
		Reasons: vRes.Reasons,
		Wacc:    wRes.WACC, WaccSource: wRes.Source, WaccConfidence: strPtr(wRes.Confidence),
		InputsSnapshot: vSnap,
	}); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert valuation: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit valuation: %v", err)
	}

	// 8. Create the score trace (for quality/relative/market_context) with the
	// CURRENT revision: the engine stamps it, this fixture never hardcodes it.
	mc := modelcfg.DefaultModelConfig()
	mc.ParameterSetName, mc.ParameterSetID = "base", 0
	qRes := quality.Result{Score: f64(85), Coverage: 0.9, Confidence: quality.ConfidenceHigh}
	relRes := relative.Result{Score: f64(55), Coverage: 0.8, Confidence: relative.ConfidenceMedium}
	scoreIn := score.ScoreInput21{
		Ticker: ticker, AsOf: asOf, Price: basePrice,
		GrahamBase: f64v(*vRes.Graham.Base), DCFBase: f64v(*vRes.DCF.Base),
		GrahamConfidence: string(vRes.Graham.Confidence), DCFConfidence: string(vRes.DCF.Confidence),
		Quality:  score.QualityDetailFrom(&qRes),
		Relative: score.RelativeDetailFrom(&relRes),
		SMA50:    f64v(basePrice * 1.05), SMA200: f64v(basePrice),
		Momentum6m: f64v(0.03), Momentum12m: f64v(0.01),
		MarginOfSafety: mc.TargetMarginOfSafety,
	}
	scRes := score.CalculateScore21(scoreIn, mc)
	raw, err := score.BuildTrace21(scoreIn, scRes).Marshal()
	if err != nil {
		t.Fatalf("marshal trace: %v", err)
	}

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin scores: %v", err)
	}
	if err := storage.UpsertScore(ctx, tx, &storage.Score{
		SecurityID: sec.ID, AsOf: asOf, Score: scRes.Score, Signal: scRes.Signal,
		Justification: scRes.Justification, InputsSnapshot: raw, ModelVersion: scRes.ModelVersion,
		ParameterSetID: nil, // no parameter set in test DB
	}); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert score: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit score: %v", err)
	}
}

func growthSerie(n int, start, cagr float64) []growth.Point {
	base := time.Date(2020, 9, 30, 0, 0, 0, 0, time.UTC)
	out := make([]growth.Point, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, growth.Point{
			PeriodEnd:   base.AddDate(i, 0, 0),
			Value:       start * math.Pow(1+cagr, float64(i)),
			AvailableAt: base.AddDate(i, 0, 0).AddDate(0, 2, 0),
		})
	}
	return out
}

func f64v(v float64) *float64 { return &v }

func ptrTime(t time.Time) *time.Time { return &t }

func strPtr(s string) *string { return &s }
