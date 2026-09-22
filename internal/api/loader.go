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
var valuationConcepts = []string{
	"net_earnings", "shares_outstanding", "shareholders_equity",
	"total_liabilities", "free_cash_flow", "revenue", "long_term_debt",
	"short_term_debt", "cash_and_equivalents", "operating_cash_flow", "capex",
}

// ValuationDetail is the /valuation/{ticker} response body.
type ValuationDetail struct {
	Ticker     string                   `json:"ticker"`
	Price      *float64                 `json:"price,omitempty"`
	Currency   string                   `json:"currency,omitempty"`
	Value      valuation.IntrinsicValue `json:"value"`
	UpsideMTM  *float64                 `json:"upside_pct,omitempty"` // vs. consenso
	Metrics    map[string]*float64      `json:"metrics,omitempty"`
	AsOf       time.Time                `json:"as_of"` // última fecha de precio
	ComputedAt time.Time                `json:"computed_at"`
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
