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

// WatchlistItem is one row of the personal watchlist (migrations/010) joined
// with the catalog detail the UI needs. There is exactly one row per security
// (uq_watchlist_security).
//
// ID is the securities.id of the security in the catalog (NOT the watchlist
// row PK w.id): it is the same id that GET /securities/search returns for that
// ticker and the one scores.security_id uses, and it stays stable across a
// delete + re-add of the same security. CreatedAt is the addition order shown in
// the list (it does come from the watchlist row).
type WatchlistItem struct {
	ID        int64     `json:"id" db:"id"`
	Ticker    string    `json:"ticker" db:"ticker"`
	Name      string    `json:"name" db:"name"`
	Exchange  *string   `json:"exchange,omitempty" db:"exchange"`
	Sector    *string   `json:"sector,omitempty" db:"sector"`
	Industry  *string   `json:"industry,omitempty" db:"industry"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
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
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
}
