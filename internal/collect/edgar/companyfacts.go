package edgar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// CompanyFacts mirrors the SEC companyfacts JSON payload (fields used by M1).
type CompanyFacts struct {
	CIK        int64  `json:"cik"`
	EntityName string `json:"entityName"`
	Facts      struct {
		USGAAP map[string]CompanyFactsConcept `json:"us-gaap"`
	} `json:"facts"`
}

// CompanyFactsConcept is one US-GAAP concept holding one or more unit buckets.
type CompanyFactsConcept struct {
	Label       string                       `json:"label"`
	Description string                       `json:"description"`
	Units       map[string][]CompanyFactsVal `json:"units"`
}

// CompanyFactsVal is one XBRL data point. `Val` is a number when present and
// null/absent when the fact had no value.
type CompanyFactsVal struct {
	Start string       `json:"start"`
	End   string       `json:"end"`
	Val   *json.Number `json:"val"`
	Accn  string       `json:"accn"`
	FY    *int         `json:"fy"`
	FP    string       `json:"fp"`
	Form  string       `json:"form"`
	Filed string       `json:"filed"`
	Frame string       `json:"frame"`
}

// XBRLFact is a single extracted SEC EDGAR XBRL data point.
type XBRLFact struct {
	Concept      string     // US-GAAP concept name, e.g. "Revenues"
	Unit         string     // e.g. "USD", "shares", "USD/shares"
	Value        float64    // parsed value (0 when HasValue is false)
	HasValue     bool       // false when the fact carried no value (null)
	RawValue     string     // original numeric string as reported
	StartDate    *time.Time // nil for "instant" facts
	EndDate      time.Time  // period end
	FormType     string     // e.g. "10-K", "10-Q"
	FilingDate   time.Time  // filed date
	Accession    string
	FiscalYear   *int
	FiscalPeriod string // e.g. "FY", "Q1"
	Frame        string
}

// ParseCompanyFacts decodes a companyfacts JSON payload into flat XBRL facts.
// Facts with an empty period end are skipped (malformed); facts without a value
// are kept with HasValue=false so the period structure remains visible.
func ParseCompanyFacts(data []byte) ([]XBRLFact, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var cf CompanyFacts
	if err := dec.Decode(&cf); err != nil {
		return nil, fmt.Errorf("edgar: decode companyfacts: %w", err)
	}

	var facts []XBRLFact
	for concept, c := range cf.Facts.USGAAP {
		for unit, entries := range c.Units {
			for _, e := range entries {
				f, ok := parseValEntry(concept, unit, e)
				if !ok {
					continue
				}
				facts = append(facts, f)
			}
		}
	}
	return facts, nil
}

func parseValEntry(concept, unit string, e CompanyFactsVal) (XBRLFact, bool) {
	end, err := time.Parse("2006-01-02", e.End)
	if err != nil {
		return XBRLFact{}, false
	}

	f := XBRLFact{
		Concept:      concept,
		Unit:         unit,
		EndDate:      end,
		FormType:     e.Form,
		Accession:    e.Accn,
		FiscalYear:   e.FY,
		FiscalPeriod: e.FP,
		Frame:        e.Frame,
	}
	if e.Start != "" && e.Start != "0000-00-00" {
		if start, err := time.Parse("2006-01-02", e.Start); err == nil {
			f.StartDate = &start
		}
	}
	if e.Filed != "" && e.Filed != "0000-00-00" {
		if filed, err := time.Parse("2006-01-02", e.Filed); err == nil {
			f.FilingDate = filed
		}
	}
	if e.Val != nil {
		v, err := strconv.ParseFloat(e.Val.String(), 64)
		if err != nil {
			return XBRLFact{}, false
		}
		f.Value = v
		f.HasValue = true
		f.RawValue = e.Val.String()
	}
	return f, true
}
