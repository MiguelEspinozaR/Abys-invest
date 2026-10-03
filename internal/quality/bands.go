package quality

import (
	"fmt"
	"math"
)

// Band is the documented, linear 0-100 mapping of ONE metric (plan A8 / ADR U2).
//
// Every cut point lives in Config.Bands, never inside a formula: a band is a
// DECISION about what "good" means for a business, and a decision that is
// hidden inside an expression can neither be reviewed nor moved by a Parameter
// Set.
//
// Two cuts, linear in between:
//
//	higher is better:  v >= Good → 100 ; v <= Bad → 0
//	lower  is better:  v <= Good → 100 ; v >= Bad → 0
//
// Degenerate band (Good == Bad): a step at the cut, so the mapping stays
// deterministic and total instead of dividing by zero. Equal Good/Bad is a
// configuration mistake, not a reason to panic.
type Band struct {
	Good           float64
	Bad            float64
	HigherIsBetter bool
}

// Score maps v to 0-100. NaN and ±Inf are refused: a non-finite value has no
// place on a 0-100 scale, and letting it through would propagate a NaN into the
// coverage and into the score.
func (b Band) Score(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	// One formula for both directions: with Good < Bad ("menor es mejor") the
	// denominator is negative and the mapping inverts by itself.
	good, bad := b.Good, b.Bad
	if good == bad {
		if v >= good {
			return 100
		}
		return 0
	}
	out := 100 * (v - bad) / (good - bad)
	if out < 0 {
		return 0
	}
	if out > 100 {
		return 100
	}
	return out
}

// Valid reports whether the band is usable (finite cuts, ordered as declared).
func (b Band) Valid() error {
	if math.IsNaN(b.Good) || math.IsInf(b.Good, 0) || math.IsNaN(b.Bad) || math.IsInf(b.Bad, 0) {
		return fmt.Errorf("banda con cortes no finitos (%v/%v)", b.Good, b.Bad)
	}
	if b.Good == b.Bad {
		return fmt.Errorf("banda degenerada: good == bad == %v", b.Good)
	}
	if b.HigherIsBetter && b.Good <= b.Bad {
		return fmt.Errorf("banda 'mayor es mejor' con good %v <= bad %v", b.Good, b.Bad)
	}
	if !b.HigherIsBetter && b.Good >= b.Bad {
		return fmt.Errorf("banda 'menor es mejor' con good %v >= bad %v", b.Good, b.Bad)
	}
	return nil
}

// Bands are the calibration of §13/§14, documented one by one.
//
// The three anchors the plan fixes explicitly (A8, calibrated against the real
// AAPL FY2025 figures) are:
//
//	ROIC               >= 0.30 → 100   (AAPL 0.7702)
//	NetDebtToEBITDA    <= 1.0  → 100   (AAPL 0.4333)
//	FCFToDebt          >= 1.0  → 100   (AAPL 1.0011)
//
// The remaining cuts follow the same principle: a level a good business reaches
// and a deteriorating one does not. They are deliberately coarse (two cuts per
// metric) because a fine-grained score would claim a precision that a single
// annual report does not have (§32: honest bands, not invented indicators).
type Bands struct {
	ROIC            Band `json:"roic"`
	ROE             Band `json:"roe"`
	OperatingMargin Band `json:"operating_margin"`
	FCFMargin       Band `json:"fcf_margin"`
	FCFYield        Band `json:"fcf_yield"`

	// Growth cuts are PERCENTAGES/YEAR (fcf_cagr_3y = -3.9451 means -3.95%/y).
	FCFCAGR3y     Band `json:"fcf_cagr_3y"`
	FCFCAGR5y     Band `json:"fcf_cagr_5y"`
	EPSCAGR3y     Band `json:"eps_cagr_3y"`
	EPSCAGR5y     Band `json:"eps_cagr_5y"`
	RevenueCAGR3y Band `json:"revenue_cagr_3y"`

	// PositiveEPSYears/PositiveFCFYears are scored on the RATIO of positive years
	// over the observed years (0-1), NOT on the raw count: the count is not
	// comparable between a company with four years of history and one with ten,
	// and the persisted metric keeps the count that §13 defines.
	PositiveEPSYears Band `json:"positive_eps_years"`
	PositiveFCFYears Band `json:"positive_fcf_years"`

	// Volatility is stddev/|mean|, so it is LOWER-is-better.
	EPSVolatility Band `json:"eps_volatility"`
	FCFVolatility Band `json:"fcf_volatility"`

	NetDebtToEBITDA  Band `json:"net_debt_to_ebitda"`
	FCFToDebt        Band `json:"fcf_to_debt"`
	InterestCoverage Band `json:"interest_coverage"`
}

// DefaultBands is the calibration of M6c, in the units of each metric:
// ratios and yields are FRACTIONS (0.0196 = 1.96%), growth rates are
// PERCENTAGES per year, the volatility metrics are unitless coefficients of
// variation and the two "years" metrics are scored as ratios.
func DefaultBands() Bands {
	return Bands{
		ROIC:            Band{Good: 0.30, Bad: 0.05, HigherIsBetter: true},
		ROE:             Band{Good: 0.20, Bad: 0.05, HigherIsBetter: true},
		OperatingMargin: Band{Good: 0.25, Bad: 0.05, HigherIsBetter: true},
		FCFMargin:       Band{Good: 0.20, Bad: 0.03, HigherIsBetter: true},
		FCFYield:        Band{Good: 0.06, Bad: 0.01, HigherIsBetter: true},

		FCFCAGR3y:     Band{Good: 10, Bad: -5, HigherIsBetter: true},
		FCFCAGR5y:     Band{Good: 10, Bad: -5, HigherIsBetter: true},
		EPSCAGR3y:     Band{Good: 12, Bad: -5, HigherIsBetter: true},
		EPSCAGR5y:     Band{Good: 12, Bad: -5, HigherIsBetter: true},
		RevenueCAGR3y: Band{Good: 10, Bad: 0, HigherIsBetter: true},

		PositiveEPSYears: Band{Good: 1.0, Bad: 0.5, HigherIsBetter: true},
		PositiveFCFYears: Band{Good: 1.0, Bad: 0.5, HigherIsBetter: true},

		EPSVolatility: Band{Good: 0.10, Bad: 0.60, HigherIsBetter: false},
		FCFVolatility: Band{Good: 0.15, Bad: 0.80, HigherIsBetter: false},

		NetDebtToEBITDA: Band{Good: 1.0, Bad: 4.0, HigherIsBetter: false},
		FCFToDebt:       Band{Good: 1.0, Bad: 0.10, HigherIsBetter: true},
		// Alcanzable solo con la ingesta de M6c-T1 (interest_expense). La banda se
		// declara igualmente para que la métrica tenga una definición completa el
		// día que exista el dato, y no para sugerir que hoy se puede calcular.
		InterestCoverage: Band{Good: 8.0, Bad: 1.0, HigherIsBetter: true},
	}
}

// All returns the bands in the canonical order of the metric list, so that a
// validation error is reported deterministically.
func (b Bands) All() []struct {
	Metric string
	Band   Band
} {
	return []struct {
		Metric string
		Band   Band
	}{
		{MetricROIC, b.ROIC},
		{MetricROE, b.ROE},
		{MetricOperatingMargin, b.OperatingMargin},
		{MetricFCFMargin, b.FCFMargin},
		{MetricFCFYield, b.FCFYield},
		{MetricFCFCAGR3y, b.FCFCAGR3y},
		{MetricFCFCAGR5y, b.FCFCAGR5y},
		{MetricEPSCAGR3y, b.EPSCAGR3y},
		{MetricEPSCAGR5y, b.EPSCAGR5y},
		{MetricRevenueCAGR3y, b.RevenueCAGR3y},
		{MetricPositiveEPSYears, b.PositiveEPSYears},
		{MetricPositiveFCFYears, b.PositiveFCFYears},
		{MetricEPSVolatility, b.EPSVolatility},
		{MetricFCFVolatility, b.FCFVolatility},
		{MetricNetDebtToEBITDA, b.NetDebtToEBITDA},
		{MetricFCFToDebt, b.FCFToDebt},
		{MetricInterestCoverage, b.InterestCoverage},
	}
}

// Validate checks every band, so a Parameter Set that breaks a cut fails before
// any score is persisted.
func (b Bands) Validate() error {
	for _, e := range b.All() {
		if err := e.Band.Valid(); err != nil {
			return fmt.Errorf("quality: banda de %s: %w", e.Metric, err)
		}
	}
	return nil
}
