package backtest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/growth"
	"github.com/miky/abys-invest/internal/modelcfg"
	"github.com/miky/abys-invest/internal/quality"
	"github.com/miky/abys-invest/internal/relative"
	"github.com/miky/abys-invest/internal/score"
	"github.com/miky/abys-invest/internal/valuation"
	"github.com/miky/abys-invest/internal/wacc"
)

// This file is the score replay of §28/B13 and it is deliberately NOT a second
// backtest engine. The SMA backtest above stays exactly as it is: different
// question (would the rule have traded?), different inputs (the price series),
// different output (trades and equity curve). This one answers a narrower and
// harder question: DOES A PERSISTED SCORE REPRODUCE ITSELF, and what did the
// market do afterwards?
//
// WHY NO RESULT TABLE
// --------------------
// A replay is reproducible by construction: the trace is the whole input. A
// results table would add a second thing that can disagree with the trace, and
// nothing would gain: every cell is recomputable with the same command. Hence
// "no new tables" is a property of the design, not a limitation.
//
// WHY THE FORWARD RETURNS ARE "n/d" SO OFTEN
// ------------------------------------------
// The window is [as_of + N calendar days, as_of + N + 5]. A stock that stopped
// trading (delisted, acquired, halted) has no bar in that window, and the honest
// answer is n/d — not the nearest bar, which would be a return of a different
// period wearing the right label.

// ForwardReturn is the market outcome of ONE horizon.
type ForwardReturn struct {
	HorizonDays int        `json:"horizon_days"`
	Price       *float64   `json:"price,omitempty"`
	AsOf        *time.Time `json:"as_of,omitempty"`
	ReturnPct   *float64   `json:"return_pct,omitempty"`
	// Reason is "no_forward_price" when the window has no bar, and empty when the
	// return exists.
	Reason string `json:"reason,omitempty"`
}

// ReplayHorizons are the three horizons of §28.
var ReplayHorizons = []int{20, 60, 365}

// MaxForwardLagDays is the tolerance of §28: the bar is looked for in
// [as_of+N, as_of+N+5] calendar days, so a weekend or a holiday does not turn a
// valid horizon into "no data", while a 3-week gap still does.
const MaxForwardLagDays = 5

// ReplayResult is one security replayed from its persisted trace.
type ReplayResult struct {
	Ticker       string    `json:"ticker"`
	AsOf         time.Time `json:"as_of"`
	ModelVersion string    `json:"model_version"`
	// StoredScore is what the row says; ReplayedScore is what the trace
	// reproduces. A difference is a BUG of determinism and is reported, never
	// hidden behind an average.
	StoredScore   int  `json:"stored_score"`
	ReplayedScore int  `json:"replayed_score"`
	Reproduces    bool `json:"reproduces"`
	// DivergenceReason is set when the two differ.
	DivergenceReason string          `json:"divergence_reason,omitempty"`
	ParameterSetID   *int64          `json:"parameter_set_id,omitempty"`
	ParameterSet     string          `json:"parameter_set,omitempty"`
	Signal           string          `json:"signal"`
	WeightUsed       float64         `json:"weight_used"`
	BasePrice        *float64        `json:"base_price,omitempty"`
	Forward          []ForwardReturn `json:"forward_returns"`
	// Error is the per-security failure (never aborts the batch).
	Error string `json:"error,omitempty"`
	// Full chain fields (populated only when ReplayOptions.FullChain=true).
	ChainSteps   *ChainSteps       `json:"chain_steps,omitempty"`
	ComputedFrom map[string]string `json:"computed_from,omitempty"`
}

// FullChainReplayResult is the result of a full chain replay (growth → wacc →
// valuation → quality → relative → market_context → score).
type FullChainReplayResult struct {
	ReplayResult
	// ChainSteps records the intermediate results of each engine in the chain.
	ChainSteps ChainSteps `json:"chain_steps,omitempty"`
	// ComputedFrom records whether each step was recomputed from stored inputs
	// or traced from the score's trace (P1-4: honest replay scope).
	ComputedFrom map[string]string `json:"computed_from,omitempty"`
}

// ChainSteps holds the intermediate results of the full replay chain.
type ChainSteps struct {
	Growth    *growth.Result    `json:"growth,omitempty"`
	WACC      *wacc.Result      `json:"wacc,omitempty"`
	Valuation *valuation.Result `json:"valuation,omitempty"`
	Quality   *quality.Result   `json:"quality,omitempty"`
	Relative  *relative.Result  `json:"relative,omitempty"`
	Score     *score.Result21   `json:"score,omitempty"`
}

// ReplayOptions configures one replay pass.
type ReplayOptions struct {
	// TickersCSV empty = every ticker with a score row at or before AsOf.
	TickersCSV string
	// AsOf is the replay date. Empty = the most recent score of each ticker.
	AsOf time.Time
	// ParameterSet overrides the configuration used for the REPLAY only. Empty
	// means "reproduce with the weights stored in the trace", which is the §28
	// property. A non-empty name re-scores the trace with that set's weights, and
	// Reproduces is then expected to be false — that is the point of asking.
	ParameterSet string
	// FullChain enables the full growth→wacc→valuation→quality→relative→score
	// replay from the raw inputs_snapshots of each engine (B12/B13). When false
	// (default), only the score trace is replayed (fast path). When true, the
	// entire chain is re-derived from the stored snapshots.
	FullChain bool
	// AllRevisions replays EVERY persisted score row of each ticker (all as_of,
	// and every model_version) instead of only the latest one.
	//
	// Default false keeps the historical report intact: one row per ticker is
	// "how does the model I am running TODAY score what I know NOW", which is the
	// question the §28 reproducibility gate asks. Auditing the HISTORY — did the
	// model reproduce its own past scores, row by row, across revisions? — is a
	// different question that the default cannot answer, because it silently
	// drops every as_of but the last.
	AllRevisions bool
}

// ReplayScores replays the scores of §28: it re-reads each persisted trace,
// recomputes the score from it, and measures the forward returns of the three
// horizons.
//
// Per-security failures never abort the pass: a single unreadable trace must not
// hide the twenty that replayed correctly.
func ReplayScores(ctx context.Context, pool *pgxpool.Pool, opts ReplayOptions) ([]ReplayResult, error) {
	if pool == nil {
		return nil, fmt.Errorf("backtest: pool nil")
	}
	mc, err := replayModelConfig(ctx, pool, opts.ParameterSet)
	if err != nil {
		return nil, err
	}
	rows, err := replayCandidates(ctx, pool, opts)
	if err != nil {
		return nil, err
	}
	out := make([]ReplayResult, 0, len(rows))
	for _, r := range rows {
		if opts.FullChain {
			full, err := replayFullChain(ctx, pool, r, mc, opts.ParameterSet != "")
			if err != nil {
				// On full chain error, fall back to trace-only replay with error noted
				fallback := replayOne(ctx, pool, r, mc, opts.ParameterSet != "")
				fallback.Error = appendErr(fallback.Error, "full_chain: "+err.Error())
				out = append(out, fallback)
			} else {
				// Return the full chain result with ChainSteps and ComputedFrom
				out = append(out, ReplayResult{
					Ticker:           full.Ticker,
					AsOf:             full.AsOf,
					ModelVersion:     full.ModelVersion,
					StoredScore:      full.StoredScore,
					ReplayedScore:    full.ReplayedScore,
					Reproduces:       full.Reproduces,
					DivergenceReason: full.DivergenceReason,
					ParameterSetID:   full.ParameterSetID,
					ParameterSet:     full.ParameterSet,
					Signal:           full.Signal,
					WeightUsed:       full.WeightUsed,
					BasePrice:        full.BasePrice,
					Forward:          full.Forward,
					Error:            full.Error,
					ChainSteps:       &full.ChainSteps,
					ComputedFrom:     full.ComputedFrom,
				})
			}
		} else {
			out = append(out, replayOne(ctx, pool, r, mc, opts.ParameterSet != ""))
		}
	}
	return out, nil
}

// scoredRow is a score row to replay.
type scoredRow struct {
	Ticker string
	AsOf   time.Time
	Score  int
	Signal string
	// ModelVersion and ParameterSetID identify the stored row.
	ModelVersion   string
	ParameterSetID *int64
	Snapshot       []byte
	SecurityID     int64
}

func replayCandidates(ctx context.Context, pool *pgxpool.Pool, opts ReplayOptions) ([]scoredRow, error) {
	q := `SELECT s.ticker, sc.as_of, sc.score, sc.signal, sc.model_version,
	             sc.parameter_set_id, sc.inputs_snapshot, sc.security_id
	      FROM scores sc
	      JOIN securities s ON s.id = sc.security_id`
	args := []any{}
	// hasWhere tracks whether a WHERE clause was already emitted. It CANNOT be
	// inferred from len(args): the max(as_of) branch adds a WHERE with no
	// placeholder at all, and a second WHERE would be a syntax error.
	hasWhere := false
	switch {
	case opts.AllRevisions:
		// No as_of filter at all: every persisted revision of every selected
		// ticker is a candidate. Ordering below keeps the output stable.
		if !opts.AsOf.IsZero() {
			// -as-of still means "up to and including this date" when combined
			// with -all-revisions: the audit is bounded, not the history.
			q += ` WHERE sc.as_of <= $1`
			args = append(args, opts.AsOf)
			hasWhere = true
		}
	case opts.AsOf.IsZero():
		q += ` WHERE sc.as_of = (SELECT max(as_of) FROM scores WHERE security_id = sc.security_id)`
		hasWhere = true
	default:
		q += ` WHERE sc.as_of <= $1`
		args = append(args, opts.AsOf)
		hasWhere = true
	}
	if t := tickerList(opts.TickersCSV); len(t) > 0 {
		if hasWhere {
			q += ` AND s.ticker = ANY($` + fmt.Sprint(len(args)+1) + `)`
		} else {
			q += ` WHERE s.ticker = ANY($1)`
		}
		args = append(args, t)
	}
	q += ` ORDER BY s.ticker, sc.as_of DESC`
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("backtest: candidatos de replay: %w", err)
	}
	defer rows.Close()
	var out []scoredRow
	for rows.Next() {
		var r scoredRow
		if err := rows.Scan(&r.Ticker, &r.AsOf, &r.Score, &r.Signal, &r.ModelVersion,
			&r.ParameterSetID, &r.Snapshot, &r.SecurityID); err != nil {
			return nil, fmt.Errorf("backtest: scan candidato: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backtest: rows candidatos: %w", err)
	}
	return out, nil
}

func replayOne(ctx context.Context, pool *pgxpool.Pool, r scoredRow, mc modelcfg.ModelConfig, override bool) ReplayResult {
	res := ReplayResult{
		Ticker: r.Ticker, AsOf: r.AsOf, ModelVersion: r.ModelVersion,
		StoredScore: r.Score, ReplayedScore: r.Score, Reproduces: true,
		ParameterSetID: r.ParameterSetID, Signal: r.Signal,
	}

	// Una fila de una revisión SIN trace (1.1.0/2.0.0) no es un error: su
	// inputs_snapshot es un ScoreInput, no un Trace21. Se marca como omitida con
	// su motivo y se cuenta aparte, para que "1/3 reproducidos" signifique tres
	// scores que no se pudieron comprobar y no tres fallos del replay.
	//
	// El trace es la MISMA forma para 2.1.0 (historia) y 2.2.0 (vigente, M6c-T1
	// W6b): el gate es por presencia de trace, no por cuál de las dos lo escribió.
	if r.ModelVersion != score.ModelVersion21 && r.ModelVersion != score.ModelVersion22 {
		res.Error = "skip:model_version=" + r.ModelVersion + " (sin trace 2.1.0/2.2.0)"
		res.Reproduces = false
		return res
	}
	// B12/B13: the trace gate. An unreadable trace is reported as an error for
	// THIS security, never silently skipped.
	trace, err := score.ParseTrace(r.Snapshot)
	if err != nil {
		res.Error = "trace_ilegible: " + err.Error()
		res.Reproduces = false
		return res
	}
	if trace.TraceVersion != score.TraceVersion {
		res.Error = fmt.Sprintf("trace_version %q", trace.TraceVersion)
		res.Reproduces = false
		return res
	}

	var replay score.Result21
	if override {
		// Un set distinto al almacenado: se re-puntúa el MISMO trace con los
		// pesos de ese set. El resultado NO tiene por qué coincidir, y la
		// diferencia es la respuesta.
		replay, err = score.RecomputeWithWeights(trace, mc)
	} else {
		replay, err = score.RecomputeFromTrace(trace, mc)
	}
	if err != nil {
		res.Error = "replay: " + err.Error()
		res.Reproduces = false
		return res
	}
	res.ReplayedScore = replay.Score
	res.WeightUsed = replay.WeightUsed
	res.ParameterSet = replay.ParameterSet
	if replay.Score != r.Score {
		res.Reproduces = false
		if override {
			res.DivergenceReason = "parameter_set_override"
		} else {
			res.DivergenceReason = "no_determinista"
		}
	}

	base, err := adjustedCloseAt(ctx, pool, r.SecurityID, r.AsOf)
	if err != nil {
		res.Error = appendErr(res.Error, "precio_base: "+err.Error())
	}
	res.BasePrice = base
	res.Forward = ForwardReturns(ctx, pool, r.SecurityID, r.AsOf, base, ReplayHorizons...)
	return res
}

func appendErr(cur, msg string) string {
	if cur == "" {
		return msg
	}
	return cur + "; " + msg
}

// ForwardReturns measures the three horizons from the ADJUSTED close of as_of.
//
// adjusted_close, not close: a score written the day before a 4:1 split is worth
// exactly as much the day after, and only the adjusted series says so.
func ForwardReturns(ctx context.Context, pool *pgxpool.Pool, securityID int64, asOf time.Time, base *float64, horizons ...int) []ForwardReturn {
	out := make([]ForwardReturn, 0, len(horizons))
	for _, h := range horizons {
		fr := ForwardReturn{HorizonDays: h}
		if base == nil || *base <= 0 {
			fr.Reason = ReasonNoForwardPrice
			out = append(out, fr)
			continue
		}
		p, at, err := adjustedCloseInWindow(ctx, pool, securityID, asOf, h)
		if err != nil || p == nil || *p <= 0 {
			fr.Reason = ReasonNoForwardPrice
			out = append(out, fr)
			continue
		}
		ret := (*p / *base - 1) * 100
		fr.Price, fr.AsOf, fr.ReturnPct = p, at, &ret
		out = append(out, fr)
	}
	return out
}

// ReasonNoForwardPrice is the n/d of §28.
const ReasonNoForwardPrice = "no_forward_price"

func adjustedCloseAt(ctx context.Context, pool *pgxpool.Pool, securityID int64, asOf time.Time) (*float64, error) {
	var v *float64
	err := pool.QueryRow(ctx, `
SELECT adjusted_close FROM daily_prices
WHERE security_id = $1 AND date <= $2 AND adjusted_close IS NOT NULL
ORDER BY date DESC LIMIT 1`, securityID, asOf).Scan(&v)
	if err != nil {
		return nil, fmt.Errorf("daily_prices @%s: %w", asOf.Format("2006-01-02"), err)
	}
	return v, nil
}

// adjustedCloseInWindow finds the first bar in [as_of+N, as_of+N+lag] (ADR §28).
func adjustedCloseInWindow(ctx context.Context, pool *pgxpool.Pool, securityID int64, asOf time.Time, horizonDays int) (*float64, *time.Time, error) {
	from := asOf.AddDate(0, 0, horizonDays)
	to := from.AddDate(0, 0, MaxForwardLagDays)
	var v *float64
	var at *time.Time
	err := pool.QueryRow(ctx, `
SELECT adjusted_close, date FROM daily_prices
WHERE security_id = $1 AND date >= $2 AND date <= $3 AND adjusted_close IS NOT NULL
ORDER BY date ASC LIMIT 1`, securityID, from, to).Scan(&v, &at)
	if err != nil {
		return nil, nil, err
	}
	return v, at, nil
}

func tickerList(csv string) []string {
	var out []string
	for _, p := range strings.Split(csv, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, strings.ToUpper(p))
		}
	}
	return out
}

func replayModelConfig(ctx context.Context, pool *pgxpool.Pool, name string) (modelcfg.ModelConfig, error) {
	if name == "" {
		// Replay does not need a set: the weights live in the trace. The config is
		// only used for the provenance echoed in the result.
		return modelcfg.ModelConfigFromEnv(), nil
	}
	_, mc, err := modelcfg.Resolve(ctx, pool, name)
	if err != nil {
		return modelcfg.ModelConfig{}, fmt.Errorf("backtest: parameter set %q: %w", name, err)
	}
	return mc, nil
}

// replayFullChain performs the complete growth→wacc→valuation→quality→relative→score
// replay from the stored inputs_snapshots of each engine (B12/B13).
func replayFullChain(ctx context.Context, pool *pgxpool.Pool, r scoredRow, mc modelcfg.ModelConfig, override bool) (FullChainReplayResult, error) {
	res := FullChainReplayResult{
		ReplayResult: ReplayResult{
			Ticker: r.Ticker, AsOf: r.AsOf, ModelVersion: r.ModelVersion,
			StoredScore: r.Score, ReplayedScore: r.Score, Reproduces: true,
			ParameterSetID: r.ParameterSetID, Signal: r.Signal,
		},
		ComputedFrom: make(map[string]string),
	}

	computedFrom := res.ComputedFrom

	// 1. growth: read growth_metrics.inputs_snapshot @ as_of
	grSnap, err := getSnapshot(ctx, pool, "growth_metrics", r.SecurityID, r.AsOf)
	if err != nil {
		return res, fmt.Errorf("growth snapshot: %w", err)
	}
	gIn, _, _, err := growth.ParseSnapshot(grSnap)
	if err != nil {
		return res, fmt.Errorf("growth parse: %w", err)
	}
	// Recompute growth from the SAME resolved ModelConfig the rest of the chain
	// uses, instead of reading GROWTH_* in isolation. No functional change today
	// (ModelConfig carries no growth knob yet, §28 RESERVED), but the replay no
	// longer has a step that silently ignores the parameter set the caller asked
	// for: when a growth knob does land, this step picks it up with the chain.
	gCfgReplay := growth.ConfigFromModelConfig(mc)
	gResReplay := growth.Calculate(gIn, gCfgReplay)
	res.ChainSteps.Growth = &gResReplay
	computedFrom["growth"] = "recomputed"

	// 2. wacc: read wacc_metrics.inputs_snapshot @ as_of
	wrSnap, err := getSnapshot(ctx, pool, "wacc_metrics", r.SecurityID, r.AsOf)
	if err != nil {
		return res, fmt.Errorf("wacc snapshot: %w", err)
	}
	wIn, _, _, err := wacc.ParseSnapshot(wrSnap)
	if err != nil {
		return res, fmt.Errorf("wacc parse: %w", err)
	}
	// Recompute wacc with the parameter set's WACC config
	wCfgReplay := wacc.ConfigFromModelConfig(mc)
	wResReplay := wacc.Calculate(wIn, wCfgReplay)
	res.ChainSteps.WACC = &wResReplay
	computedFrom["wacc"] = "recomputed"

	// 3. valuation: read valuation_results.inputs_snapshot @ as_of
	vrSnap, err := getSnapshot(ctx, pool, "valuation_results", r.SecurityID, r.AsOf)
	if err != nil {
		return res, fmt.Errorf("valuation snapshot: %w", err)
	}
	var vrSnapData valuation.Snapshot
	if err := json.Unmarshal(vrSnap, &vrSnapData); err != nil {
		return res, fmt.Errorf("valuation parse: %w", err)
	}
	// The valuation snapshot has Inputs, Config, and Result. We need to recompute.
	vIn := vrSnapData.Inputs
	// Inject the recomputed growth and wacc results
	vIn.NormalizedGrowthRate = gResReplay.NormalizedGrowthRate
	vIn.GrowthConfidence = valuation.Confidence(gResReplay.Confidence)
	vIn.GrowthSource = gResReplay.Source
	vIn.GrowthModelVersion = growth.ModelVersion
	vIn.WACC = wResReplay.WACC
	vIn.CostOfEquity = wResReplay.Ke
	vIn.WACCSource = wResReplay.Source
	vIn.WACCConfidence = valuation.Confidence(wResReplay.Confidence)
	vIn.WACCModelVersion = wacc.ModelVersion
	vIn.BetaObserved = wResReplay.BetaObserved
	vCfgReplay := valuation.ConfigFromEnv() // valuation config from env for now
	vResReplay := valuation.Calculate(vIn, vCfgReplay)
	res.ChainSteps.Valuation = &vResReplay
	computedFrom["valuation"] = "recomputed"

	// 4. quality: try to recompute from fundamentals if available, else use trace
	trace, err := score.ParseTrace(r.Snapshot)
	if err != nil {
		return res, fmt.Errorf("trace parse: %w", err)
	}
	// Check if we have fundamentals for quality recomputation
	// For now, use trace (recomputation would need fundamentals at as_of)
	if trace.Quality == nil {
		return res, fmt.Errorf("trace missing quality")
	}
	qRes := trace.Quality
	res.ChainSteps.Quality = &quality.Result{
		Score:         qRes.Score,
		Coverage:      qRes.Coverage,
		Confidence:    qRes.Confidence,
		TaxRateSource: qRes.TaxRateSource,
		SubScores:     qRes.SubScores,
		Reasons:       qRes.Reasons,
		ModelVersion:  quality.ModelVersion,
	}
	computedFrom["quality"] = "traced"

	// 5. relative: same approach (would need sector/historical medians to recompute)
	if trace.Relative != nil {
		rRes := trace.Relative
		res.ChainSteps.Relative = &relative.Result{
			Score:           rRes.Score,
			SectorScore:     rRes.SectorScore,
			HistoricalScore: rRes.HistoricalScore,
			Coverage:        rRes.Coverage,
			Confidence:      rRes.Confidence,
			Reasons:         rRes.Reasons,
			ModelVersion:    relative.ModelVersion,
		}
		computedFrom["relative"] = "traced"
	} else {
		computedFrom["relative"] = "traced"
	}

	// 6. market_context: recompute from price series if available
	// For now, use trace
	if trace.MarketContext != nil {
		computedFrom["market_context"] = "traced"
	} else {
		computedFrom["market_context"] = "traced"
	}

	// Get base price for MarginOfSafety calculation (call once)
	basePrice, err := adjustedCloseAt(ctx, pool, r.SecurityID, r.AsOf)
	if err != nil {
		res.Error = appendErr(res.Error, "precio_base: "+err.Error())
	}
	marginOfSafety := mc.TargetMarginOfSafety

	// Build score input from chain results - guard against nil mctx
	var sma50, sma200, momentum6m, momentum12m *float64
	if trace.MarketContext != nil {
		sma50 = trace.MarketContext.SMA50
		sma200 = trace.MarketContext.SMA200
		momentum6m = trace.MarketContext.Momentum6m
		momentum12m = trace.MarketContext.Momentum12m
	}

	// Build score input from chain results
	scoreIn := score.ScoreInput21{
		Ticker: r.Ticker, AsOf: r.AsOf,
		Price:            0, // Will be filled from basePrice
		GrahamBase:       vResReplay.Graham.Base,
		DCFBase:          vResReplay.DCF.Base,
		GrahamConfidence: string(vResReplay.Graham.Confidence),
		DCFConfidence:    string(vResReplay.DCF.Confidence),
		Quality:          score.QualityDetailFrom(res.ChainSteps.Quality),
		Relative:         score.RelativeDetailFrom(res.ChainSteps.Relative),
		SMA50:            sma50,
		SMA200:           sma200,
		Momentum6m:       momentum6m,
		Momentum12m:      momentum12m,
		MarginOfSafety:   marginOfSafety,
	}
	if basePrice != nil {
		scoreIn.Price = *basePrice
	}

	scoreRes := score.CalculateScore21(scoreIn, mc)
	res.ChainSteps.Score = &scoreRes
	res.ReplayedScore = scoreRes.Score
	res.WeightUsed = scoreRes.WeightUsed
	res.ParameterSet = scoreRes.ParameterSet
	if scoreRes.Score != r.Score {
		res.Reproduces = false
		if override {
			res.DivergenceReason = "parameter_set_override"
		} else {
			res.DivergenceReason = "no_determinista"
		}
	}

	res.BasePrice = basePrice
	res.Forward = ForwardReturns(ctx, pool, r.SecurityID, r.AsOf, basePrice, ReplayHorizons...)
	return res, nil
}

// getSnapshot reads the inputs_snapshot from the specified table for a security/as_of.
// Uses a map to avoid SQL interpolation of table names (P1-4).
func getSnapshot(ctx context.Context, pool *pgxpool.Pool, table string, securityID int64, asOf time.Time) ([]byte, error) {
	queries := map[string]string{
		"growth_metrics":    `SELECT inputs_snapshot FROM growth_metrics WHERE security_id = $1 AND as_of = $2`,
		"wacc_metrics":      `SELECT inputs_snapshot FROM wacc_metrics WHERE security_id = $1 AND as_of = $2`,
		"valuation_results": `SELECT inputs_snapshot FROM valuation_results WHERE security_id = $1 AND as_of = $2`,
	}
	query, ok := queries[table]
	if !ok {
		return nil, fmt.Errorf("getSnapshot: tabla desconocida %q", table)
	}
	var snap []byte
	err := pool.QueryRow(ctx, query, securityID, asOf).Scan(&snap)
	if err != nil {
		return nil, err
	}
	return snap, nil
}
