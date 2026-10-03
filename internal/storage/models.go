package storage

import "time"

// Security is a row from the securities catalog.
type Security struct {
	ID        int64     `json:"id" db:"id"`
	Ticker    string    `json:"ticker" db:"ticker"`
	CIK       string    `json:"cik" db:"cik"`
	Name      string    `json:"name" db:"name"`
	Type      string    `json:"type" db:"type"`
	Currency  string    `json:"currency" db:"currency"`
	Status    string    `json:"status" db:"status"`
	Exchange  *string   `json:"exchange,omitempty" db:"exchange"`
	Sector    *string   `json:"sector,omitempty" db:"sector"`
	Industry  *string   `json:"industry,omitempty" db:"industry"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
	// Beta is the OBSERVED beta from Yahoo defaultKeyStatistics (migrations/011,
	// plan D17): reference data of the security, not a calculation result, for the
	// same reason sector/industry live here. nil = never observed (foreign/ADR
	// with a null beta, or a sector job that has not run yet) and it degrades the
	// WACC to configured_fallback instead of assuming a value.
	Beta *float64 `json:"beta,omitempty" db:"beta"`
	// BetaUpdatedAt dates the observation: it distinguishes "never arrived" from
	// "arrived and stopped arriving" (risk R4 of the M6a plan).
	BetaUpdatedAt *time.Time `json:"beta_updated_at,omitempty" db:"beta_updated_at"`
}

// Fundamental is a normalized XBRL fact mapped to the canonical dictionary.
type Fundamental struct {
	ID           int64      `json:"id" db:"id"`
	SecurityID   int64      `json:"security_id" db:"security_id"`
	Concept      string     `json:"concept" db:"concept"`
	Value        *float64   `json:"value,omitempty" db:"value"`
	Unit         *string    `json:"unit,omitempty" db:"unit"`
	PeriodType   string     `json:"period_type" db:"period_type"`
	PeriodStart  *time.Time `json:"period_start,omitempty" db:"period_start"`
	PeriodEnd    time.Time  `json:"period_end" db:"period_end"`
	FiscalYear   *int16     `json:"fiscal_year,omitempty" db:"fiscal_year"`
	FiscalPeriod *string    `json:"fiscal_period,omitempty" db:"fiscal_period"`
	FilingDate   *time.Time `json:"filing_date,omitempty" db:"filing_date"`
	Source       string     `json:"source" db:"source"`
	SourceFactID *string    `json:"source_fact_id,omitempty" db:"source_fact_id"`
	RawValue     *string    `json:"raw_value,omitempty" db:"raw_value"`
	CreatedAt    time.Time  `json:"created_at" db:"created_at"`
}

// EdgarStaging is a raw SEC EDGAR payload awaiting normalization.
type EdgarStaging struct {
	ID          int64      `json:"id" db:"id"`
	CIK         string     `json:"cik" db:"cik"`
	Ticker      *string    `json:"ticker,omitempty" db:"ticker"`
	Accession   string     `json:"accession" db:"accession"`
	FormType    string     `json:"form_type" db:"form_type"`
	FilingDate  time.Time  `json:"filing_date" db:"filing_date"`
	PeriodEnd   *time.Time `json:"period_end,omitempty" db:"period_end"`
	Payload     []byte     `json:"payload" db:"payload"`
	PayloadType string     `json:"payload_type" db:"payload_type"`
	IngestedAt  time.Time  `json:"ingested_at" db:"ingested_at"`
	Normalized  bool       `json:"normalized" db:"normalized"`
}

// XBRLConceptMap is the persisted canonical dictionary (migrations/005).
type XBRLConceptMap struct {
	ID            int64   `json:"id" db:"id"`
	XBRLConcept   string  `json:"xbrl_concept" db:"xbrl_concept"`
	CanonicalName string  `json:"canonical_name" db:"canonical_name"`
	UnitExpected  *string `json:"unit_expected,omitempty" db:"unit_expected"`
	Notes         *string `json:"notes,omitempty" db:"notes"`
}

// DailyPrice is one row of daily OHLCV prices (migrations/006, source yahoo).
type DailyPrice struct {
	ID            int64     `json:"id" db:"id"`
	SecurityID    int64     `json:"security_id" db:"security_id"`
	Date          time.Time `json:"date" db:"date"`
	Open          *float64  `json:"open,omitempty" db:"open"`
	High          *float64  `json:"high,omitempty" db:"high"`
	Low           *float64  `json:"low,omitempty" db:"low"`
	Close         float64   `json:"close" db:"close"`
	AdjustedClose float64   `json:"adjusted_close" db:"adjusted_close"`
	Volume        *int64    `json:"volume,omitempty" db:"volume"`
	Source        string    `json:"source" db:"source"`
	CreatedAt     time.Time `json:"created_at" db:"created_at"`
}

// MacroSeries is one observation of a US macro series (migrations/007).
type MacroSeries struct {
	ID         int64     `json:"id" db:"id"`
	SeriesCode string    `json:"series_code" db:"series_code"`
	Date       time.Time `json:"date" db:"date"`
	Value      float64   `json:"value" db:"value"`
	Unit       string    `json:"unit" db:"unit"`
	Frequency  string    `json:"frequency" db:"frequency"`
	Source     string    `json:"source" db:"source"`
	SourceID   *string   `json:"source_id,omitempty" db:"source_id"`
	CreatedAt  time.Time `json:"created_at" db:"created_at"`
}

// DerivedMetric is one materialized metric (migrations/008). inputs_snapshot
// holds the exact input values used (ADR-0004); value is NULL when inputs
// were insufficient (never silently zeroed).
type DerivedMetric struct {
	ID             int64     `json:"id" db:"id"`
	SecurityID     int64     `json:"security_id" db:"security_id"`
	AsOf           time.Time `json:"as_of" db:"as_of"`
	Metric         string    `json:"metric" db:"metric"`
	Value          *float64  `json:"value,omitempty" db:"value"`
	InputsSnapshot []byte    `json:"inputs_snapshot,omitempty" db:"inputs_snapshot"`
	ModelVersion   string    `json:"model_version" db:"model_version"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
}

// ParameterSet is a named, persisted bundle of configuration overrides
// (SPEC §26, ADR D20/D28). Precedence is code < env < parameter set, and
// `parameters = {}` means "no overrides at all", NOT "empty configuration".
type ParameterSet struct {
	ID           int64     `json:"id" db:"id"`
	Name         string    `json:"name" db:"name"`
	ModelVersion string    `json:"model_version" db:"model_version"`
	Parameters   []byte    `json:"parameters" db:"parameters"`
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
}

// BetaObservation is one row of beta_history (ADR D29): the canonical series of
// beta observations. securities.beta is only the CACHE of the most recent row;
// the invariant "cache == last history row" is maintained by
// UpdateSecurityReference in one transaction and has its own test.
type BetaObservation struct {
	ID         int64     `json:"id" db:"id"`
	SecurityID int64     `json:"security_id" db:"security_id"`
	AsOf       time.Time `json:"as_of" db:"as_of"`
	Beta       float64   `json:"beta" db:"beta"`
	Source     string    `json:"source" db:"source"`
	FetchedAt  time.Time `json:"fetched_at" db:"fetched_at"`
}

// WatchlistItem is one row of the personal watchlist (migrations/010) joined
// with the catalog detail the UI needs. There is exactly one row per security
// (uq_watchlist_security).
//
// ID is the securities.id of the security in the catalog (NOT the watchlist
// row PK w.id): it is the same id that GET /securities/search returns for that
// ticker and the one scores.security_id uses, and it stays stable across a
// delete + re-add of the same security. CreatedAt is the addition order shown in
// the list (it does come from the watchlist row).
//
// M5.1: Score y Signal son el último score persistido del security (LEFT JOIN
// LATERAL sobre scores, plan B3) para que el dashboard dibuje las tarjetas de
// "Mi watchlist" sin N+1 requests. NO llevan omitempty: null explícito
// significa "el security todavía no tiene score" y la UI distingue ambos casos
// sin convenciones (los campos de catálogo que sí pueden faltar, exchange /
// sector / industry, conservan su omitempty previo).
type WatchlistItem struct {
	ID        int64     `json:"id" db:"id"`
	Ticker    string    `json:"ticker" db:"ticker"`
	Name      string    `json:"name" db:"name"`
	Exchange  *string   `json:"exchange,omitempty" db:"exchange"`
	Sector    *string   `json:"sector,omitempty" db:"sector"`
	Industry  *string   `json:"industry,omitempty" db:"industry"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	// Último score persistido del security (null si aún no tiene).
	Score  *int    `json:"score" db:"score"`
	Signal *string `json:"signal" db:"signal"`
}

// Score is one persisted score row (migrations/009). It holds the score 0-100,
// the Spanish signal (comprar/mantener/vender), the template-generated
// justification and the JSONB snapshot of the exact inputs used (ADR-0004).
type Score struct {
	ID             int64     `json:"id" db:"id"`
	SecurityID     int64     `json:"security_id" db:"security_id"`
	AsOf           time.Time `json:"as_of" db:"as_of"`
	Score          int       `json:"score" db:"score"`
	Signal         string    `json:"signal" db:"signal"`
	Justification  string    `json:"justification" db:"justification"`
	InputsSnapshot []byte    `json:"inputs_snapshot,omitempty" db:"inputs_snapshot"`
	ModelVersion   string    `json:"model_version" db:"model_version"`
	// ParameterSetID is NULL for every score written before M6c (1.1.0/2.0.0):
	// they predate parameter sets and are NOT rewritten (§26). From 2.1.0 on it
	// is NOT NULL, and it is part of the row identity (uq_scores, 4 columns), so
	// two sets coexist in the same as_of instead of overwriting each other.
	ParameterSetID *int64    `json:"parameter_set_id,omitempty" db:"parameter_set_id"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
}

// GrowthMetric is one row of growth_metrics (migrations/012, SPEC v2 §5, plan
// M6a): the Growth Engine result for (security, as_of, model_version). A nil
// NormalizedGrowthRate is NOT a zero: it means "insufficient data" and comes
// with Confidence = low and Source = insufficient_data (§5).
type GrowthMetric struct {
	ID          int64      `json:"id" db:"id"`
	SecurityID  int64      `json:"security_id" db:"security_id"`
	AsOf        time.Time  `json:"as_of" db:"as_of"`
	AvailableAt *time.Time `json:"available_at,omitempty" db:"available_at"`
	// FundamentalsAsOf is the period_end of the last annual fiscal year used
	// (traceability §4: which books the growth was measured on).
	FundamentalsAsOf     *time.Time `json:"fundamentals_as_of,omitempty" db:"fundamentals_as_of"`
	RevenueCAGR3y        *float64   `json:"revenue_cagr_3y,omitempty" db:"revenue_cagr_3y"`
	RevenueCAGR5y        *float64   `json:"revenue_cagr_5y,omitempty" db:"revenue_cagr_5y"`
	EPSCAGR3y            *float64   `json:"eps_cagr_3y,omitempty" db:"eps_cagr_3y"`
	EPSCAGR5y            *float64   `json:"eps_cagr_5y,omitempty" db:"eps_cagr_5y"`
	FCFCAGR3y            *float64   `json:"fcf_cagr_3y,omitempty" db:"fcf_cagr_3y"`
	FCFCAGR5y            *float64   `json:"fcf_cagr_5y,omitempty" db:"fcf_cagr_5y"`
	NormalizedGrowthRate *float64   `json:"normalized_growth_rate,omitempty" db:"normalized_growth_rate"`
	Confidence           string     `json:"growth_confidence" db:"growth_confidence"`
	Source               string     `json:"growth_source" db:"growth_source"`
	Clamped              bool       `json:"growth_clamped" db:"growth_clamped"`
	RevenueDiscrepancy   bool       `json:"revenue_discrepancy" db:"revenue_discrepancy"`
	InputsSnapshot       []byte     `json:"inputs_snapshot,omitempty" db:"inputs_snapshot"`
	ModelVersion         string     `json:"model_version" db:"model_version"`
	CalculationTimestamp time.Time  `json:"calculation_timestamp" db:"calculation_timestamp"`
	CreatedAt            time.Time  `json:"created_at" db:"created_at"`
}

// WaccMetric is one row of wacc_metrics (migrations/013, SPEC v2 §7, plan M6a).
// Source/Confidence follow the same strict shape: capm_individual/high,
// capm_hybrid/medium or configured_fallback/low. A nil Wacc means the engine
// refused to invent one (WACC_FALLBACK <= 0 without observed inputs).
type WaccMetric struct {
	ID           int64      `json:"id" db:"id"`
	SecurityID   int64      `json:"security_id" db:"security_id"`
	AsOf         time.Time  `json:"as_of" db:"as_of"`
	AvailableAt  *time.Time `json:"available_at,omitempty" db:"available_at"`
	EquityValue  *float64   `json:"equity_value,omitempty" db:"equity_value"`
	DebtValue    *float64   `json:"debt_value,omitempty" db:"debt_value"`
	Beta         *float64   `json:"beta,omitempty" db:"beta"`
	BetaObserved bool       `json:"beta_observed" db:"beta_observed"`
	// BetaUpdatedAt is copied from securities.beta_updated_at: the row shows
	// WHICH observation of the beta produced this WACC (risk R4).
	BetaUpdatedAt        *time.Time `json:"beta_updated_at,omitempty" db:"beta_updated_at"`
	CostOfEquity         *float64   `json:"cost_of_equity,omitempty" db:"cost_of_equity"`
	CostOfDebtPreTax     *float64   `json:"cost_of_debt_pretax,omitempty" db:"cost_of_debt_pretax"`
	CostOfDebtAfterTax   *float64   `json:"cost_of_debt_after_tax,omitempty" db:"cost_of_debt_after_tax"`
	TaxRate              *float64   `json:"tax_rate,omitempty" db:"tax_rate"`
	RiskFreeRate         *float64   `json:"risk_free_rate,omitempty" db:"risk_free_rate"`
	EquityRiskPremium    *float64   `json:"equity_risk_premium,omitempty" db:"equity_risk_premium"`
	Wacc                 *float64   `json:"wacc,omitempty" db:"wacc"`
	WeightEquity         *float64   `json:"weight_equity,omitempty" db:"weight_equity"`
	WeightDebt           *float64   `json:"weight_debt,omitempty" db:"weight_debt"`
	Source               string     `json:"wacc_source" db:"wacc_source"`
	Confidence           string     `json:"wacc_confidence" db:"wacc_confidence"`
	InputsSnapshot       []byte     `json:"inputs_snapshot,omitempty" db:"inputs_snapshot"`
	ModelVersion         string     `json:"model_version" db:"model_version"`
	CalculationTimestamp time.Time  `json:"calculation_timestamp" db:"calculation_timestamp"`
	CreatedAt            time.Time  `json:"created_at" db:"created_at"`
}

// ValuationResult is one row of valuation_results (migrations/014, SPEC v2
// §6-§11/§19-§21, plan M6b ADR D4). One row per
// (security_id, as_of, model_version): the SIX scenarios (graham/dcf ×
// bear/base/bull), the four margins of safety of §11, the §19 uncertainty, the
// §10/§20/§21 status + confidence and the reasons behind them.
//
// Every scenario/MOS/uncertainty field is a POINTER: NULL means "not
// computable", never 0 (§21), and a consumer can tell a real zero apart from a
// missing value. Status + Confidence are NOT pointers because they are always
// present: an unavailable valuation is a real, persisted answer.
//
// Wacc/WaccSource are the discount rate ACTUALLY used and its provenance (the
// 5-value taxonomy of D9, wider than the 3 of wacc_metrics): the rate is never
// invisible, so a configured discount rate can never be mistaken for a CAPM one.
type ValuationResult struct {
	ID         int64     `json:"id" db:"id"`
	SecurityID int64     `json:"security_id" db:"security_id"`
	AsOf       time.Time `json:"as_of" db:"as_of"`
	// ParameterSetID stays NULL for the three 2.0.0 rows: they were computed
	// before parameter sets existed and M6c does not recalculate them (§9).
	ParameterSetID *int64 `json:"parameter_set_id,omitempty" db:"parameter_set_id"`
	// AvailableAt is the max(filing_date) of the facts actually used (SPEC §4),
	// NOT the growth/wacc one: this row owns its provenance (deuda M6a-F1).
	AvailableAt      *time.Time `json:"available_at,omitempty" db:"available_at"`
	FundamentalsAsOf *time.Time `json:"fundamentals_as_of,omitempty" db:"fundamentals_as_of"`
	// Price is the close used as the valuation price (the denominator input of
	// the §11 margin of safety), not a valuation result.
	Price    *float64 `json:"price,omitempty" db:"price"`
	Currency *string  `json:"currency,omitempty" db:"currency"`

	// Inputs echoed for auditability. growth_metrics / wacc_metrics remain the
	// source of truth for the growth and the WACC; these are copies of what the
	// engine really consumed.
	NormalizedGrowthRate *float64 `json:"normalized_growth_rate,omitempty" db:"normalized_growth_rate"`
	GrowthSource         *string  `json:"growth_source,omitempty" db:"growth_source"`
	GrowthConfidence     *string  `json:"growth_confidence,omitempty" db:"growth_confidence"`
	Wacc                 *float64 `json:"wacc,omitempty" db:"wacc"`
	WaccSource           string   `json:"wacc_source" db:"wacc_source"`
	WaccConfidence       *string  `json:"wacc_confidence,omitempty" db:"wacc_confidence"`

	// §6/§8 scenarios: nil = not computable, never 0.
	GrahamBear *float64 `json:"graham_bear,omitempty" db:"graham_bear"`
	GrahamBase *float64 `json:"graham_base,omitempty" db:"graham_base"`
	GrahamBull *float64 `json:"graham_bull,omitempty" db:"graham_bull"`
	DcfBear    *float64 `json:"dcf_bear,omitempty" db:"dcf_bear"`
	DcfBase    *float64 `json:"dcf_base,omitempty" db:"dcf_base"`
	DcfBull    *float64 `json:"dcf_bull,omitempty" db:"dcf_bull"`

	// §11 margin of safety, in percent. graham_mos is the base scenario only
	// (§11 does not ask for graham bear/bull).
	GrahamMos  *float64 `json:"graham_mos,omitempty" db:"graham_mos"`
	DcfBearMos *float64 `json:"dcf_bear_mos,omitempty" db:"dcf_bear_mos"`
	DcfBaseMos *float64 `json:"dcf_base_mos,omitempty" db:"dcf_base_mos"`
	DcfBullMos *float64 `json:"dcf_bull_mos,omitempty" db:"dcf_bull_mos"`
	TargetMos  float64  `json:"target_mos" db:"target_mos"`

	// §19 valuation uncertainty (population std / mean over the 4 components).
	ValuationMean       *float64 `json:"valuation_mean,omitempty" db:"valuation_mean"`
	ValuationStddev     *float64 `json:"valuation_stddev,omitempty" db:"valuation_stddev"`
	ValuationDispersion *float64 `json:"valuation_dispersion,omitempty" db:"valuation_dispersion"`
	ValuationComponents int16    `json:"valuation_components" db:"valuation_components"`

	// §10/§20/§21 availability and confidence. The CHECK of 014 forbids a row
	// with every scenario NULL, so ValuationStatus can only be 'available' here.
	ValuationStatus     string  `json:"valuation_status" db:"valuation_status"`
	ValuationConfidence string  `json:"valuation_confidence" db:"valuation_confidence"`
	GrahamStatus        string  `json:"graham_status" db:"graham_status"`
	GrahamConfidence    *string `json:"graham_confidence,omitempty" db:"graham_confidence"`
	DcfStatus           string  `json:"dcf_status" db:"dcf_status"`
	DcfConfidence       *string `json:"dcf_confidence,omitempty" db:"dcf_confidence"`

	Reasons              []string  `json:"reasons" db:"reasons"`
	Sensitivity          []byte    `json:"sensitivity,omitempty" db:"sensitivity"`
	InputsSnapshot       []byte    `json:"inputs_snapshot,omitempty" db:"inputs_snapshot"`
	ModelVersion         string    `json:"model_version" db:"model_version"`
	CalculationTimestamp time.Time `json:"calculation_timestamp" db:"calculation_timestamp"`
	CreatedAt            time.Time `json:"created_at" db:"created_at"`
}
