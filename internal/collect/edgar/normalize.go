package edgar

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
)

const (
	// NormalizeFormType selects the filings normalized in M1 (annual 10-K),
	// alineado con el CA-4 de la SPEC ("10-K anual").
	NormalizeFormType = "10-K"
	// CompanyFactsPayloadType identifies companyfacts staging rows.
	CompanyFactsPayloadType = "company_facts"
)

// NormalizeStaging processes one edgar_staging row (payload_type
// company_facts) end-to-end: parse companyfacts -> map to canonical dictionary
// -> compute derived concepts -> upsert securities by ticker -> batch UPSERT
// fundamentals -> mark as normalized.
//
// The operation runs inside a transaction and is idempotent: re-running a
// normalized row produces the same rows (ON CONFLICT), never duplicates.
func NormalizeStaging(ctx context.Context, pool *pgxpool.Pool, stagingID int64) error {
	row, err := storage.GetStagingByID(ctx, pool, stagingID)
	if err != nil {
		return fmt.Errorf("normalize: load staging %d: %w", stagingID, err)
	}
	if row.PayloadType != CompanyFactsPayloadType {
		return fmt.Errorf("normalize: staging %d payload_type %q no soportado (esperado %q)",
			stagingID, row.PayloadType, CompanyFactsPayloadType)
	}
	if row.Ticker == nil || *row.Ticker == "" {
		return fmt.Errorf("normalize: staging %d sin ticker; no se puede resolver la security", stagingID)
	}

	all, err := canonicalizeCompanyFacts(row.Payload)
	if err != nil {
		return fmt.Errorf("normalize: canonicalización de staging %d: %w", stagingID, err)
	}
	if len(all) == 0 {
		return fmt.Errorf("normalize: staging %d (ticker %s) sin hechos canónicos 10-K", stagingID, *row.Ticker)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("normalize: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Asegurar security por ticker/CIK (entityName del payload como nombre).
	sec, err := storage.UpsertSecurity(ctx, tx, &storage.Security{
		Ticker:   *row.Ticker,
		CIK:      row.CIK,
		Name:     entityNameOrDefault(companyFactsEntityName(row.Payload), *row.Ticker),
		Type:     "stock",
		Currency: "USD",
		Status:   "active",
	})
	if err != nil {
		return fmt.Errorf("normalize: upsert security %s: %w", *row.Ticker, err)
	}

	// Batch UPSERT de fundamentals (idempotente por clave canónica).
	if err := storage.UpsertFundamentals(ctx, tx, buildFundamentals(sec.ID, all)); err != nil {
		return fmt.Errorf("normalize: upsert fundamentals de staging %d: %w", stagingID, err)
	}

	// Marcar normalizado en la misma transacción.
	if err := storage.MarkStagingNormalized(ctx, tx, stagingID); err != nil {
		return fmt.Errorf("normalize: marcar staging %d normalizado: %w", stagingID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("normalize: commit staging %d: %w", stagingID, err)
	}
	return nil
}

// NormalizePending processes up to limit pending staging rows. Individual row
// failures do not abort the batch; returns the number of rows normalized and the
// first error encountered (nil when all succeeded).
func NormalizePending(ctx context.Context, pool *pgxpool.Pool, limit int) (int, error) {
	rows, err := storage.GetUnprocessedStaging(ctx, pool, limit)
	if err != nil {
		return 0, fmt.Errorf("normalize: list pending staging: %w", err)
	}

	done := 0
	var firstErr error
	for _, r := range rows {
		if err := NormalizeStaging(ctx, pool, r.ID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		done++
	}
	return done, firstErr
}

// DryRunReport summarizes an in-memory normalization pass (no DB writes).
type DryRunReport struct {
	CanonicalFacts int
	DerivedFacts   int
	Periods        int
	// Sample values for the latest fiscal year in the payload, keyed by
	// canonical concept (informational for -dry-run).
	Sample map[string]float64
}

// NormalizeDryRun runs the canonicalization pipeline in-memory and reports
// counts and sample values so -dry-run can validate without touching a database.
func NormalizeDryRun(payload []byte) (*DryRunReport, error) {
	all, err := canonicalizeCompanyFacts(payload)
	if err != nil {
		return nil, err
	}

	rep := &DryRunReport{Sample: map[string]float64{}}
	lastFY := -1
	periods := map[string]bool{}
	for i := range all {
		f := &all[i]
		if f.SourceConcept == "derived:"+f.Canonical {
			rep.DerivedFacts++
		} else {
			rep.CanonicalFacts++
		}
		periods[periodKey(f)] = true
		if f.FiscalYear != nil && *f.FiscalYear > lastFY {
			lastFY = *f.FiscalYear
		}
	}
	rep.Periods = len(periods)
	for i := range all {
		f := &all[i]
		if f.FiscalYear != nil && *f.FiscalYear == lastFY && f.FiscalPeriod == "FY" && f.HasValue {
			if _, ok := rep.Sample[f.Canonical]; !ok {
				rep.Sample[f.Canonical] = f.Value
			}
		}
	}
	return rep, nil
}

// canonicalizeCompanyFacts runs the shared pipeline: parse companyfacts, keep
// 10-K filings, map to the canonical dictionary, de-duplicate deterministically,
// derive concepts, and sort for stable output.
func canonicalizeCompanyFacts(payload []byte) ([]CanonicalFact, error) {
	facts, err := ParseCompanyFacts(payload)
	if err != nil {
		return nil, fmt.Errorf("parse companyfacts: %w", err)
	}

	var canon []CanonicalFact
	for _, f := range facts {
		if f.FormType != NormalizeFormType {
			continue
		}
		if cf := MapToCanonical(f); cf != nil {
			canon = append(canon, *cf)
		}
	}
	canon = dedupeCanonical(canon)

	derived := ComputeDerived(canon)
	all := append(canon, derived...)
	sortCanonicalStable(all)
	return all, nil
}

// dedupeCanonical keeps, for each (period, canonical), the preferred fact
// (lower dictionary priority, then most recent filing, then larger accession).
func dedupeCanonical(facts []CanonicalFact) []CanonicalFact {
	if len(facts) < 2 {
		return facts
	}
	byKey := map[string]CanonicalFact{}
	for _, f := range facts {
		key := periodKey(&f) + "|" + f.Canonical
		if prev, dup := byKey[key]; !dup || newerFact(&f, &prev) {
			byKey[key] = f
		}
	}
	out := make([]CanonicalFact, 0, len(byKey))
	for _, f := range byKey {
		out = append(out, f)
	}
	return out
}

// sortCanonicalStable orders facts by period then canonical for stable inserts.
func sortCanonicalStable(facts []CanonicalFact) {
	sort.SliceStable(facts, func(i, j int) bool {
		a, b := &facts[i], &facts[j]
		if pa, pb := periodKey(a), periodKey(b); pa != pb {
			return pa < pb
		}
		return a.Canonical < b.Canonical
	})
}

// buildFundamentals converts canonical facts into storage.Fundamental rows.
func buildFundamentals(securityID int64, facts []CanonicalFact) []storage.Fundamental {
	out := make([]storage.Fundamental, 0, len(facts))
	for i := range facts {
		f := &facts[i]

		var val *float64
		var raw *string
		if f.HasValue {
			v := f.Value
			val = &v
			if f.RawValue != "" {
				r := f.RawValue
				raw = &r
			}
		}
		var fy *int16
		if f.FiscalYear != nil {
			v := int16(*f.FiscalYear)
			fy = &v
		}
		var periodStart, filingDate *time.Time
		if f.StartDate != nil {
			s := *f.StartDate
			periodStart = &s
		}
		if !f.FilingDate.IsZero() {
			fd := f.FilingDate
			filingDate = &fd
		}
		sourceFactID := fmt.Sprintf("%s#%s", f.Accession, f.SourceConcept)

		out = append(out, storage.Fundamental{
			SecurityID:   securityID,
			Concept:      f.Canonical,
			Value:        val,
			Unit:         &f.Unit,
			PeriodType:   f.PeriodType,
			PeriodStart:  periodStart,
			PeriodEnd:    f.EndDate,
			FiscalYear:   fy,
			FiscalPeriod: &f.FiscalPeriod,
			FilingDate:   filingDate,
			Source:       "sec_edgar",
			SourceFactID: &sourceFactID,
			RawValue:     raw,
		})
	}
	return out
}

// companyFactsEntityName extracts the top-level entityName of a companyfacts
// payload (used as the security display name).
func companyFactsEntityName(payload []byte) string {
	var meta struct {
		EntityName string `json:"entityName"`
	}
	if err := json.Unmarshal(payload, &meta); err != nil {
		return ""
	}
	return meta.EntityName
}

func entityNameOrDefault(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}
