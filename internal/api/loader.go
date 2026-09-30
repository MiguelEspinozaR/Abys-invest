package api

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
	"github.com/miky/abys-invest/internal/valuation"
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

// valuationConcepts are the canonical FY concepts needed by the valuation.
// "revenues" (no singular): it is the concept name that storage derives from
// us-gaap:Revenues, and the singular never matched any row. The bug was
// invisible: a missing concept returns nil and the valuation simply has no
// revenue, without error.
var valuationConcepts = []string{
	"net_earnings", "shares_outstanding", "shareholders_equity",
	"total_liabilities", "free_cash_flow", "revenues", "long_term_debt",
	"short_term_debt", "cash_and_equivalents", "operating_cash_flow", "capex",
}

// ValuationDetail is the /valuation/{ticker} response body.
//
// M6a adds `growth` and `wacc` as OMITTED (pointer) blocks: additive contract,
// so a client that ignores them sees exactly the same response as before, and
// a security that has never been through the growth stage simply omits the key
// instead of failing the request (§6). They are inert values: the formulas keep
// using params.GrowthRate / params.DiscountRate until M6b wires them in.
type ValuationDetail struct {
	Ticker     string                   `json:"ticker"`
	Price      *float64                 `json:"price,omitempty"`
	Currency   string                   `json:"currency,omitempty"`
	Value      valuation.IntrinsicValue `json:"value"`
	UpsideMTM  *float64                 `json:"upside_pct,omitempty"` // vs. consenso
	Growth     *GrowthDetail            `json:"growth,omitempty"`
	WACC       *WaccDetail              `json:"wacc,omitempty"`
	Metrics    map[string]*float64      `json:"metrics,omitempty"`
	AsOf       time.Time                `json:"as_of"` // última fecha de precio
	ComputedAt time.Time                `json:"computed_at"`
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

// LoadValuation resolves the valuation detail for a ticker (plan D4):
//
//	Graham = EPS último FY × (2g+8.5) × 4.4/AAA;
//	DCF    = FCF FY (canónica, fallback OCF−capex) con WACC y terminal.
func LoadValuation(ctx context.Context, pool *pgxpool.Pool, params Parameters, ticker string) (*ValuationDetail, error) {
	sec, err := storage.GetSecurityByTicker(ctx, pool, ticker)
	if err != nil {
		return nil, err // pgx.ErrNoRows → 404
	}
	funds, err := storage.GetLatestFYFundamentals(ctx, pool, sec.ID, valuationConcepts)
	if err != nil {
		return nil, err
	}

	input := valuation.IntrinsicInput{
		GrowthRate:        params.GrowthRate,
		DCFDiscountRate:   params.DiscountRate,
		DCFHorizon:        params.Horizon,
		TerminalGrowth:    params.TerminalGrowth,
		EPS:               ratio(funds["net_earnings"], funds["shares_outstanding"]),
		FreeCashFlow:      fcfFrom(funds),
		SharesOutstanding: funds["shares_outstanding"],
		NetDebt:           netDebtFrom(funds),
	}
	iv := valuation.CalcIntrinsicValue(input)

	detail := &ValuationDetail{
		Ticker:     sec.Ticker,
		Currency:   sec.Currency,
		Value:      iv,
		Metrics:    map[string]*float64{},
		ComputedAt: time.Now().UTC(),
	}

	mts, err := storage.GetLatestMetrics(ctx, pool, sec.ID)
	if err == nil {
		for _, dm := range mts {
			detail.Metrics[dm.Metric] = dm.Value
		}
	}
	last, err := storage.GetLatestPrice(ctx, pool, sec.ID)
	if err == nil {
		price := last.Close
		detail.Price = &price
		detail.AsOf = last.Date
		if iv.Consensus != nil && *iv.Consensus > 0 && price > 0 {
			up := (*iv.Consensus/price - 1) * 100
			detail.UpsideMTM = &up
		}
	}

	if gm, err := storage.GetLatestGrowthMetric(ctx, pool, sec.ID); err == nil {
		gd := &GrowthDetail{
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
		detail.Growth = gd
	}
	if wm, err := storage.GetLatestWaccMetric(ctx, pool, sec.ID); err == nil {
		wd := &WaccDetail{
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
		detail.WACC = wd
	}
	return detail, nil
}

// fcfFrom prefers the canonical free_cash_flow concept and falls back to
// operating_cash_flow + capex (capex negativo en GAAP → resta).
func fcfFrom(funds map[string]*float64) *float64 {
	if v := funds["free_cash_flow"]; v != nil {
		return v
	}
	ocf, capex := funds["operating_cash_flow"], funds["capex"]
	if ocf == nil || capex == nil {
		return nil
	}
	out := *ocf + *capex
	return &out
}

// netDebtFrom = long_term_debt + short_term_debt − cash_and_equivalents; nil
// cuando falta algún componente (conservador: el DCF no compensa ese caso).
func netDebtFrom(funds map[string]*float64) *float64 {
	lt, st, cash := funds["long_term_debt"], funds["short_term_debt"], funds["cash_and_equivalents"]
	if lt == nil || st == nil || cash == nil {
		return nil
	}
	out := *lt + *st - *cash
	return &out
}

func ratio(a, b *float64) *float64 {
	if a == nil || b == nil || *b <= 0 {
		return nil
	}
	out := *a / *b
	return &out
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
