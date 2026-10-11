package valuation

// C1 — share-class normalisation (SPEC abys-valuation-210, user decision
// 2026-10-11).
//
// The EPS and the share count the valuation consumes must belong to the SAME
// share class as the price (a BRK-B price compared with an A-class EPS is the
// classic unit mismatch C1 exists to catch). Berkshire's classes trade at a
// fixed 1500:1 ratio and the fundamentals arrive in the A class, so the engine
// converts them when the price is a B-class one.
//
// The ratio is HARDCODED on purpose: the user decision of 2026-10-11 chose to
// wire BRK-A/BRK-B, not to build a generic ratio table (a general table needs
// per-class share data the Inputs schema does not carry — see the assumption
// note in NormalizeShareClass).
const ClassRatioBRK = 1500.0

// isBRKClassB reports whether the ticker is the B class of the only hardcoded
// multi-class pair. "BRK-B" is the canonical EDGAR ticker of the class.
func isBRKClassB(ticker string) bool { return ticker == "BRK-B" }

// scalePtr returns a pointer to v*factor (nil in, nil out). Helper of
// NormalizeShareClass with exactly two uses, kept private so the conversion is
// not reused for anything else.
func scalePtr(v *float64, factor float64) *float64 {
	if v == nil {
		return nil
	}
	out := *v * factor
	if !isFinite(out) {
		return nil
	}
	return &out
}

// NormalizeShareClass expresses EPS and shares_outstanding in the class of the
// price for the given ticker, returning the original pointers untouched when no
// conversion applies.
//
// For BRK-B (price per B share with A-class fundamentals):
//
//	EPS_B   = EPS_A / 1500
//	shares_B = shares_A × 1500
//
// which leaves the net earnings and the market cap invariant — the conversion
// changes the CLASS of the numbers, never the economics.
//
// ASSUMPTION (single share count): Inputs carries ONE shares_outstanding value.
// The SPEC formula `shares_total = shares_A × 1500 + shares_B` needs both
// classes, which the schema does not separate; the loader supplies the
// consolidated/A-class count, so the B-equivalent count is `shares_A × 1500`.
// If a per-class split is ingested in the future, this is the single place to
// extend.
func NormalizeShareClass(ticker string, eps, shares *float64) (normEPS, normShares *float64) {
	if !isBRKClassB(ticker) {
		return eps, shares
	}
	return scalePtr(eps, 1.0/ClassRatioBRK), scalePtr(shares, ClassRatioBRK)
}

// ClassTotals is the SPEC formula with BOTH class counts explicit, exported for
// the tests: shares_total = shares_A × ratio + shares_B works out to the same
// number as the single-count conversion above when shares_B is 0, and it proves
// the ratio constant is the one the user decided (1500).
func ClassTotals(epsA, sharesA, sharesB float64) (epsB, sharesBTotal float64) {
	return epsA / ClassRatioBRK, sharesA*ClassRatioBRK + sharesB
}
