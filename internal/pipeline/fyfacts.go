package pipeline

import (
	"log/slog"
	"time"

	"github.com/miky/abys-invest/internal/storage"
)

// This file is W4 of M6c-T1: the ALIGNED fiscal-year read of the interest/tax
// trio (Az2/ADR D31). W5 wires it into the quality engine and into
// wacc.Inputs.TaxRate; W4 only exposes the selection and its degradation
// vocabulary, so no engine changes are observable yet.

// Concept slugs of the M6c-T1 interest/tax trio (Az1/ADR D31). They are the
// canonical names the EDGAR normaliser persists. Keeping them as constants stops
// the alignment from depending on a literal repeated across call sites; W5 adds
// them to qualityConcepts.
const (
	conceptOperatingIncome  = "operating_income"
	conceptInterestExpense  = "interest_expense"
	conceptIncomeTaxExpense = "income_tax_expense"
	conceptPretaxIncome     = "pretax_income"
)

// Closed vocabulary of degradation reasons of the fiscal-year alignment
// (Az2/ADR D31). The strings are stable: W5 logs them together with the ticker
// and the anchor period, and tests assert them, so they are not free text.
const (
	// FYReasonNoAnchor: the series carries no annual operating_income, so there
	// is no fiscal year to align the trio to.
	FYReasonNoAnchor = "no_anchor"
	// FYReasonInterestUnavailable: the fresh anchor has no interest_expense, and
	// neither does any other fiscal year.
	FYReasonInterestUnavailable = "interest_unavailable"
	// FYReasonInterestPeriodMismatch: interest_expense exists, but only in a
	// fiscal year other than the anchor. Using it would mix EBIT FY2026 with
	// interest FY2023 (the AAPL case of ADR D31).
	FYReasonInterestPeriodMismatch = "interest_period_mismatch"
	// FYReasonInterestStale: the most recent anchor is older than
	// asOf-maxAgeDays, so not even a complete EBIT/interest pair is usable (the
	// GE/JNJ case of ADR D31).
	FYReasonInterestStale = "interest_stale"
	// FYReasonTaxRateUnavailable: the fresh anchor is missing pretax_income
	// and/or income_tax_expense, so the rate of Az3 cannot be derived.
	FYReasonTaxRateUnavailable = "tax_rate_unavailable"
	// FYReasonTaxRateStale: the most recent anchor is older than
	// asOf-maxAgeDays, so the tax pair is not usable (the GE/JNJ case).
	FYReasonTaxRateStale = "tax_rate_stale"
)

// AlignedFYFacts is the W4 result of aligning M6c-T1's interest/tax trio to a
// SINGLE fiscal year (Az2/ADR D31).
//
// The anchor is the MOST RECENT annual period_end that has operating_income
// (EBIT). interest_expense, income_tax_expense and pretax_income are then read
// ONLY from that exact period_end: a concept that exists only in another fiscal
// year is nil here and shows up as a *_period_mismatch / *_unavailable reason,
// never as a value. A nil value means "absent", never zero (§1).
//
// A non-empty reason means the pair must not be used. The values that do belong
// to the anchor are still returned so the caller can log them, but W5 gates the
// metric on the reason. The reason is empty exactly when the pair is complete at
// a fresh anchor.
//
// fiscal_year / fiscal_period of the anchor are NOT exposed: storage.FYPoint
// (the reused GetFYAnnualSeries shape) carries only period_end, value and
// available_at, and the plan forbids adding SQL unless strictly necessary. The
// alignment therefore keys exclusively on period_end (Az2: "no mezcles fy/fp").
type AlignedFYFacts struct {
	AnchorPeriodEnd  time.Time
	OperatingIncome  *float64
	InterestExpense  *float64
	IncomeTaxExpense *float64
	PretaxIncome     *float64

	// InterestReason is "" when EBIT and interest_expense are both present at
	// AnchorPeriodEnd and the anchor is fresh; otherwise one of FYReasonNoAnchor,
	// FYReasonInterestUnavailable, FYReasonInterestPeriodMismatch,
	// FYReasonInterestStale.
	InterestReason string
	// TaxRateReason is "" when pretax_income and income_tax_expense are both
	// present at AnchorPeriodEnd and the anchor is fresh; otherwise one of
	// FYReasonNoAnchor, FYReasonTaxRateUnavailable, FYReasonTaxRateStale.
	TaxRateReason string
}

// Reasons returns the non-empty degradation reasons in a stable order (interest
// first, tax rate second) without duplicates, so W5 can log a single `reasons`
// field per ticker.
func (f AlignedFYFacts) Reasons() []string {
	var out []string
	seen := map[string]struct{}{}
	for _, r := range [2]string{f.InterestReason, f.TaxRateReason} {
		if r == "" {
			continue
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}

// alignedFYFacts selects the fiscal-year anchor and the trio values of the SAME
// period_end from the annual series returned by storage.GetFYAnnualSeries.
//
// No new SQL: the storage call already filters fiscal_period='FY', duration
// 330-400 days and filing_date <= asOf. The helper re-applies the available_at
// check because the unit tests build synthetic series in memory and must prove
// there is no look-ahead even when the loader is bypassed.
//
// The freshness window is explicit (asOf, maxAgeDays): W5 wires
// QUALITY_FY_MAX_AGE_DAYS=550. An anchor with period_end < asOf-maxAgeDays is
// stale, and staleness dominates completeness, because a stale EBIT cannot feed
// interest_coverage even with a perfect pair. A non-positive maxAgeDays does not
// disable the rule: it only leaves an anchor equal to asOf usable, which is the
// conservative reading.
func alignedFYFacts(ticker string, series map[string][]storage.FYPoint, asOf time.Time, maxAgeDays int) AlignedFYFacts {
	ebit := latestUsablePoint(series[conceptOperatingIncome], asOf)
	if ebit == nil {
		out := AlignedFYFacts{
			InterestReason: FYReasonNoAnchor,
			TaxRateReason:  FYReasonNoAnchor,
		}
		logAlignedFYFacts(ticker, out)
		return out
	}

	out := AlignedFYFacts{
		AnchorPeriodEnd: ebit.PeriodEnd,
		OperatingIncome: floatPtr(ebit.Value),
	}
	// Only the values AT the anchor: a concept of another fiscal year stays nil
	// on purpose (it is the mismatch, not a value to rescue).
	out.InterestExpense = valueAt(series[conceptInterestExpense], ebit.PeriodEnd, asOf)
	out.IncomeTaxExpense = valueAt(series[conceptIncomeTaxExpense], ebit.PeriodEnd, asOf)
	out.PretaxIncome = valueAt(series[conceptPretaxIncome], ebit.PeriodEnd, asOf)

	age := asOf.Sub(ebit.PeriodEnd)
	fresh := age <= time.Duration(maxAgeDays)*24*time.Hour

	switch {
	case !fresh:
		out.InterestReason = FYReasonInterestStale
	case out.InterestExpense == nil:
		if latestUsablePoint(series[conceptInterestExpense], asOf) != nil {
			out.InterestReason = FYReasonInterestPeriodMismatch
		} else {
			out.InterestReason = FYReasonInterestUnavailable
		}
	}

	switch {
	case !fresh:
		out.TaxRateReason = FYReasonTaxRateStale
	case out.IncomeTaxExpense == nil || out.PretaxIncome == nil:
		out.TaxRateReason = FYReasonTaxRateUnavailable
	}

	logAlignedFYFacts(ticker, out)
	return out
}

// logAlignedFYFacts makes each degradation visible with the ticker and the anchor
// period, mirroring the beta_stale log of ADR D29. A usable alignment logs
// nothing.
func logAlignedFYFacts(ticker string, f AlignedFYFacts) {
	reasons := f.Reasons()
	if len(reasons) == 0 {
		return
	}
	anchor := ""
	if !f.AnchorPeriodEnd.IsZero() {
		anchor = f.AnchorPeriodEnd.Format("2006-01-02")
	}
	slog.Debug("quality: alineación fiscal degradada",
		"ticker", ticker, "anchor_period_end", anchor, "reasons", reasons)
}

// latestUsablePoint returns the most recent point observable at asOf, or nil when
// every point is a look-ahead. It does not assume the series is ordered or
// duplicate-free: ties break by later available_at and then by greater value, so
// the result does not depend on the input order. GetFYAnnualSeries already
// deduplicates by period_end, but the unit tests feed synthetic series.
func latestUsablePoint(points []storage.FYPoint, asOf time.Time) *storage.FYPoint {
	var best *storage.FYPoint
	for i := range points {
		p := &points[i]
		if !observableAt(p, asOf) {
			continue
		}
		if betterPoint(p, best) {
			best = p
		}
	}
	return best
}

// valueAt returns the value of the anchor period_end if it is observable, else
// nil. It never falls back to another period_end.
func valueAt(points []storage.FYPoint, periodEnd time.Time, asOf time.Time) *float64 {
	best := pointAt(points, periodEnd, asOf)
	if best == nil {
		return nil
	}
	return floatPtr(best.Value)
}

func pointAt(points []storage.FYPoint, periodEnd time.Time, asOf time.Time) *storage.FYPoint {
	var best *storage.FYPoint
	for i := range points {
		p := &points[i]
		if !p.PeriodEnd.Equal(periodEnd) || !observableAt(p, asOf) {
			continue
		}
		if betterPoint(p, best) {
			best = p
		}
	}
	return best
}

// betterPoint is the deterministic comparison behind both selections: newest
// period_end first, then latest filing (the "most recent restatement wins" rule
// of the storage query), then greater value as a last resort for synthetic
// inputs that violate the one-row-per-period contract.
func betterPoint(p, best *storage.FYPoint) bool {
	if best == nil {
		return true
	}
	if !p.PeriodEnd.Equal(best.PeriodEnd) {
		return p.PeriodEnd.After(best.PeriodEnd)
	}
	if !p.AvailableAt.Equal(best.AvailableAt) {
		return p.AvailableAt.After(best.AvailableAt)
	}
	return p.Value > best.Value
}

// observableAt mirrors the storage no-look-ahead rule: a point without a filing
// date is usable (the column is nullable), a point filed after asOf is not.
func observableAt(p *storage.FYPoint, asOf time.Time) bool {
	return p.AvailableAt.IsZero() || !p.AvailableAt.After(asOf)
}

func floatPtr(v float64) *float64 { return &v }
