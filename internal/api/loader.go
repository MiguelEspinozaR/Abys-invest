package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
	"github.com/miky/abys-invest/internal/valuation"
)

// ValuationSource values of the `valuation_source` field (ADR D14): which of
// the two paths produced `value`.
const (
	ValuationSourcePersisted = "persisted" // fila de valuation_results
	ValuationSourceComputed  = "computed"  // fallback on-the-fly de la API
)

// Env defaults for the valuation/score inputs (documented in .env.example and
// the M3 plan). All are read at process start so results stay deterministic
// per run.
const (
	envGrowthRate        = "GROWTH_RATE_DEFAULT"
	envDiscountRate      = "DCF_DISCOUNT_RATE"
	envHorizon           = "DCF_HORIZON_YEARS"
	envTerminalGrowth    = "DCF_TERMINAL_GROWTH"
	envMarginOfSafety    = "MARGIN_OF_SAFETY"
	envCompMinSecurities = "COMPARABLES_MIN_SECURITIES"
	envCompHistoryYears  = "COMPARABLES_HISTORY_YEARS"

	defaultGrowthRate     = 7.0
	defaultDiscountRate   = 10.0
	defaultHorizon        = 5
	defaultTerminalGrowth = 2.5
	defaultMarginSafety   = 30.0
	defaultCompMinSec     = 5
	defaultCompHistYears  = 5
)

// Parameters buckets the valuation/score parameters used per request.
type Parameters struct {
	GrowthRate        float64
	DiscountRate      float64
	Horizon           int
	TerminalGrowth    float64
	MarginOfSafety    float64
	CompMinSecurities int
	CompHistoryYears  int
}

// LoadParameters reads the valuation/score parameters from the environment.
func LoadParameters() Parameters {
	return Parameters{
		GrowthRate:        envFloat(envGrowthRate, defaultGrowthRate),
		DiscountRate:      envFloat(envDiscountRate, defaultDiscountRate),
		Horizon:           envInt(envHorizon, defaultHorizon),
		TerminalGrowth:    envFloat(envTerminalGrowth, defaultTerminalGrowth),
		MarginOfSafety:    envFloat(envMarginOfSafety, defaultMarginSafety),
		CompMinSecurities: envInt(envCompMinSecurities, defaultCompMinSec),
		CompHistoryYears:  envInt(envCompHistoryYears, defaultCompHistYears),
	}
}

// ValuationDetail is the /valuation/{ticker} response body (SPEC §13.5.1, M6b).
//
// The 1.x body had a flat `value` object plus `upside_pct` "vs. consenso":
// there is no consensus in this system, so `upside_pct` is GONE and the whole
// valuation lives under `value` as the 2.0.0 envelope of valuation.Result
// (graham / dcf / margin_of_safety / uncertainty / confidence / reasons /
// sensitivity / inputs / model_version).
//
// M6a keeps `growth` and `wacc` as OMITTED (pointer) blocks: additive contract,
// so a client that ignores them sees the same valuation, and a security that
// never went through the growth stage omits the key instead of failing (§6).
//
// `value` is a pointer and `valuation_source` tells the client WHICH of the two
// paths produced it (ADR D14):
//
//	"persisted" → read from valuation_results (the daily pipeline's answer)
//	"computed"  → the API recomputed it on the fly because the pipeline has not
//	              run yet for this security (same engine, same canonical facts)
//
// A client that assumes "persisted" always would otherwise silently mix two
// computations; with the flag it can tell, and the score endpoint reads the
// PERSISTED row only.
type ValuationDetail struct {
	Ticker          string              `json:"ticker"`
	Price           *float64            `json:"price,omitempty"`
	Currency        string              `json:"currency,omitempty"`
	ValuationSource string              `json:"valuation_source"` // persisted|computed
	Value           *valuation.Result   `json:"value,omitempty"`
	Growth          *GrowthDetail       `json:"growth,omitempty"`
	WACC            *WaccDetail         `json:"wacc,omitempty"`
	Metrics         map[string]*float64 `json:"metrics,omitempty"`
	AsOf            time.Time           `json:"as_of"` // última fecha de precio
	ComputedAt      time.Time           `json:"computed_at"`
}

// GrowthDetail is the consumer-facing projection of growth_metrics. All the
// rates are percentages (CAGR 10.5 = 10.5%/year) and NULL fields are
// `omitempty`, never 0: normalized_growth_rate == nil means "insufficient
// data" and must not be confused with a measured 0% (§1 conservative rule).
type GrowthDetail struct {
	NormalizedGrowthRate *float64 `json:"normalized_growth_rate,omitempty"` // % (nil = sin datos)
	Source               string   `json:"source"`                           // eps_fcf_3y|revenue_3y|insufficient_data...
	Confidence           string   `json:"confidence"`                       // high|medium|low
	RevenueCAGR3y        *float64 `json:"revenue_cagr_3y,omitempty"`
	RevenueCAGR5y        *float64 `json:"revenue_cagr_5y,omitempty"`
	EPSCAGR3y            *float64 `json:"eps_cagr_3y,omitempty"`
	EPSCAGR5y            *float64 `json:"eps_cagr_5y,omitempty"`
	FCFCAGR3y            *float64 `json:"fcf_cagr_3y,omitempty"`
	FCFCAGR5y            *float64 `json:"fcf_cagr_5y,omitempty"`
	Clamped              bool     `json:"clamped"`             // el clamp [-10, 25]% se aplicó
	RevenueDiscrepancy   bool     `json:"revenue_discrepancy"` // revenue como checksum divergió
	AsOf                 string   `json:"as_of"`               // fecha del último precio usado
	ModelVersion         string   `json:"model_version"`       // 1.0.0
}

// WaccDetail is the consumer-facing projection of wacc_metrics. WACC,
// CostOfEquity, CostOfDebtAfterTax, RiskFreeRate and EquityRiskPremium are
// percentages. Source distinguishes a per-company CAPM (Yahoo beta) from the
// configured fallback (beta assumed) — the fallback is never hidden.
type WaccDetail struct {
	WACC               *float64 `json:"wacc,omitempty"`                   // % (nil = sin estructura de capital)
	CostOfEquity       *float64 `json:"cost_of_equity,omitempty"`         // % Ke = Rf + beta x ERP
	CostOfDebtAfterTax *float64 `json:"cost_of_debt_after_tax,omitempty"` // % Kd x (1 - tax)
	RiskFreeRate       *float64 `json:"risk_free_rate,omitempty"`         // % constante configurable
	EquityRiskPremium  *float64 `json:"equity_risk_premium,omitempty"`    // % constante configurable
	Beta               *float64 `json:"beta,omitempty"`                   // observada o asumida
	BetaObserved       bool     `json:"beta_observed"`                    // false = Config.BetaAssumed
	Source             string   `json:"source"`                           // capm_individual|capm_hybrid|configured_fallback
	Confidence         string   `json:"confidence"`                       // high|medium|low
	AsOf               string   `json:"as_of"`
	ModelVersion       string   `json:"model_version"`
}

// LoadValuation resolves the valuation detail for a ticker (M6b D4/E1).
//
// It NEVER calls the engine when a persisted 2.0.0 row exists: the daily
// pipeline is the owner of the valuation, and an endpoint recomputing it would
// make the API a second, divergent source of truth (the exact risk of ADR D14).
// Only when there is no row at all does it fall back to the same engine with
// the same canonical facts, flagged as "computed" (§ below).
func LoadValuation(ctx context.Context, pool *pgxpool.Pool, params Parameters, ticker string) (*ValuationDetail, error) {
	sec, err := storage.GetSecurityByTicker(ctx, pool, ticker)
	if err != nil {
		return nil, err // pgx.ErrNoRows → 404
	}

	detail := &ValuationDetail{
		Ticker:     sec.Ticker,
		Currency:   sec.Currency,
		Metrics:    map[string]*float64{},
		ComputedAt: time.Now().UTC(),
	}

	// as_of = fecha del último precio: the valuation of "today" is the one of the
	// last close the database knows, exactly as the pipeline does.
	var asOf time.Time
	if last, err := storage.GetLatestPrice(ctx, pool, sec.ID); err == nil {
		price := last.Close
		detail.Price = &price
		detail.AsOf = last.Date
		asOf = last.Date
	}

	if mts, err := storage.GetLatestMetrics(ctx, pool, sec.ID); err == nil {
		for _, dm := range mts {
			detail.Metrics[dm.Metric] = dm.Value
		}
	}

	// Growth and WACC blocks (M6a, unchanged): additive and optional.
	if gm, err := storage.GetLatestGrowthMetric(ctx, pool, sec.ID); err == nil {
		detail.Growth = &GrowthDetail{
			NormalizedGrowthRate: gm.NormalizedGrowthRate,
			Source:               gm.Source,
			Confidence:           gm.Confidence,
			RevenueCAGR3y:        gm.RevenueCAGR3y,
			RevenueCAGR5y:        gm.RevenueCAGR5y,
			EPSCAGR3y:            gm.EPSCAGR3y,
			EPSCAGR5y:            gm.EPSCAGR5y,
			FCFCAGR3y:            gm.FCFCAGR3y,
			FCFCAGR5y:            gm.FCFCAGR5y,
			Clamped:              gm.Clamped,
			RevenueDiscrepancy:   gm.RevenueDiscrepancy,
			AsOf:                 gm.AsOf.Format("2006-01-02"),
			ModelVersion:         gm.ModelVersion,
		}
	}
	if wm, err := storage.GetLatestWaccMetric(ctx, pool, sec.ID); err == nil {
		detail.WACC = &WaccDetail{
			WACC:               wm.Wacc,
			CostOfEquity:       wm.CostOfEquity,
			CostOfDebtAfterTax: wm.CostOfDebtAfterTax,
			RiskFreeRate:       wm.RiskFreeRate,
			EquityRiskPremium:  wm.EquityRiskPremium,
			Beta:               wm.Beta,
			BetaObserved:       wm.BetaObserved,
			Source:             wm.Source,
			Confidence:         wm.Confidence,
			AsOf:               wm.AsOf.Format("2006-01-02"),
			ModelVersion:       wm.ModelVersion,
		}
	}

	// (1) Persisted row: the source of truth of the daily pipeline.
	if row, err := storage.GetLatestValuationResult(ctx, pool, sec.ID); err == nil {
		detail.Value = valuationResultFromRow(row, sec.Ticker)
		detail.ValuationSource = ValuationSourcePersisted
		return detail, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	// (2) No row: recompute with the SAME engine and the SAME canonical concept
	// list the pipeline uses (valuation.ValuationConcepts + DeriveFacts), so the
	// fallback cannot answer something the pipeline would not have answered.
	// `params` is intentionally NOT used for the valuation: M6b wires the
	// PERSISTED growth/wacc (M6a) and never the legacy env defaults below, which
	// remain for the score parameters only.
	in := valuation.Inputs{Ticker: sec.Ticker, AsOf: asOf}
	if detail.Price != nil {
		in.Price = detail.Price
	}
	funds, _, err := storage.GetLatestFYFundamentalsAsOf(ctx, pool, sec.ID, valuation.ValuationConcepts, asOf)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if funds != nil {
		facts := valuation.DeriveFacts(funds)
		in.EPS, in.FreeCashFlow, in.NetDebt = facts.EPS, facts.FreeCashFlow, facts.NetDebt
		in.SharesOutstanding = funds["shares_outstanding"]
	}
	if gm, err := storage.GetLatestGrowthMetric(ctx, pool, sec.ID); err == nil {
		in.NormalizedGrowthRate = gm.NormalizedGrowthRate
		in.GrowthConfidence = valuation.Confidence(gm.Confidence)
		in.GrowthSource = gm.Source
		in.GrowthModelVersion = gm.ModelVersion
	}
	if wm, err := storage.GetLatestWaccMetric(ctx, pool, sec.ID); err == nil {
		in.WACC, in.CostOfEquity = wm.Wacc, wm.CostOfEquity
		in.WACCSource = wm.Source
		in.WACCConfidence = valuation.Confidence(wm.Confidence)
		in.WACCModelVersion = wm.ModelVersion
		in.BetaObserved = wm.BetaObserved
	}
	res := valuation.Calculate(in, valuation.ConfigFromEnv())
	detail.Value = &res
	detail.ValuationSource = ValuationSourceComputed
	return detail, nil
}

// valuationResultFromRow rebuilds the 2.0.0 `value` envelope from a persisted
// valuation_results row (the inverse of the pipeline's row mapper).
//
// The response contract is a projection, NOT a copy of the columns: the two
// paths (persisted / computed) must produce byte-comparable JSON for the same
// inputs, and the two extra §15 metrics (PEG, P/FCF) have no column — they are
// derived again from the SAME persisted inputs_snapshot with the SAME pure
// function, so they cannot disagree with the pipeline.
//
// Everything that only exists in the snapshot (per-method reasons, WACCUsed,
// discount provenance, growth fallback flag) is read from it, which is exactly
// what §26 persists it for. A row without a usable snapshot degrades to the
// columns it does have instead of failing the request.
func valuationResultFromRow(row *storage.ValuationResult, ticker string) *valuation.Result {
	var snap valuation.Snapshot
	if len(row.InputsSnapshot) > 0 {
		if err := json.Unmarshal(row.InputsSnapshot, &snap); err != nil {
			slog.Warn("valuation_results: inputs_snapshot ilegible; se proyecta desde las columnas", "error", err)
		}
	}

	// The snapshot carries what has no column (per-method reasons, WACCUsed,
	// discount provenance, growth fallback). Without it, rebuild the inputs from
	// the columns that DO exist instead of pretending they were unknown.
	in := snap.Inputs
	if in.Ticker == "" {
		in.Ticker = ticker
		in.AsOf = row.AsOf
		in.Price = row.Price
		in.NormalizedGrowthRate = row.NormalizedGrowthRate
		in.GrowthSource = derefOr(row.GrowthSource, "")
		in.GrowthConfidence = valuation.Confidence(derefOr(row.GrowthConfidence, ""))
		in.WACC, in.WACCSource = row.Wacc, row.WaccSource
		in.WACCConfidence = valuation.Confidence(derefOr(row.WaccConfidence, ""))
		in.GrowthFallbackUsed = row.NormalizedGrowthRate == nil
	}

	res := &valuation.Result{
		Ticker: ticker,
		AsOf:   row.AsOf.Format("2006-01-02"),
		Status: valuation.Status(row.ValuationStatus),
		Graham: valuation.Method{
			Status:     valuation.Status(row.GrahamStatus),
			Bear:       row.GrahamBear,
			Base:       row.GrahamBase,
			Bull:       row.GrahamBull,
			Confidence: confidenceOf(row.GrahamConfidence),
			Reasons:    snap.Graham.Reasons,
		},
		DCF: valuation.Method{
			Status:     valuation.Status(row.DcfStatus),
			Bear:       row.DcfBear,
			Base:       row.DcfBase,
			Bull:       row.DcfBull,
			Confidence: confidenceOf(row.DcfConfidence),
			Reasons:    snap.DCF.Reasons,
		},
		MOS: valuation.MarginOfSafety{
			GrahamBase: row.GrahamMos,
			DCFBear:    row.DcfBearMos,
			DCFBase:    row.DcfBaseMos,
			DCFBull:    row.DcfBullMos,
			Target:     row.TargetMos,
		},
		Uncertainty: valuation.Uncertainty{
			Mean:       row.ValuationMean,
			StdDev:     row.ValuationStddev,
			Dispersion: row.ValuationDispersion,
			Components: int(row.ValuationComponents),
		},
		Confidence:   valuation.Confidence(row.ValuationConfidence),
		Reasons:      row.Reasons,
		Sensitivity:  sensitivityFromRow(row.Sensitivity),
		Inputs:       in,
		ModelVersion: row.ModelVersion,
	}
	res.PEG, res.PFcf = pegFromInputs(in)

	return res
}

// pegFromInputs recomputes the §15 additive metrics from persisted inputs with
// the engine's own pure function. They have no column (they are not valuation
// values), so recomputing them from the SAME inputs is what guarantees the
// "persisted" and "computed" responses cannot disagree about them.
func pegFromInputs(in valuation.Inputs) (peg, pFcf *float64) {
	g, fallback := 0.0, true
	if in.NormalizedGrowthRate != nil {
		g, fallback = *in.NormalizedGrowthRate, in.GrowthFallbackUsed
	}
	return valuation.CalcPEG(in, g, fallback)
}

// derefOr is the NULL-column helper of the projection: a NULL string stays ""
// (absent) instead of being invented as "medium"/"unknown".
func derefOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

// sensitivityFromRow parses the JSONB grid; an unreadable grid yields nil (the
// valuation is still returned, only the §8 reproducibility artifact is missing).
func sensitivityFromRow(raw []byte) []valuation.SensitivityPoint {
	if len(raw) == 0 {
		return nil
	}
	var grid []valuation.SensitivityPoint
	if err := json.Unmarshal(raw, &grid); err != nil {
		slog.Warn("valuation_results: grid de sensibilidad ilegible; se omite", "error", err)
		return nil
	}
	return grid
}

// confidenceOf maps a NULL confidence column to the empty string: nil means the
// pipeline could not classify it, and "" keeps it absent instead of inventing
// "medium".
func confidenceOf(c *string) valuation.Confidence {
	if c == nil {
		return ""
	}
	return valuation.Confidence(*c)
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}
