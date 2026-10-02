package valuation

// Canonical concepts of SPEC §8: the valuation consumes only these three, all
// PERSISTED and all as-of filtered by the loader (F1). Everything is derived
// here with pure, nil-safe helpers so the same rules apply no matter which
// layer calls them (storage loader today, future re-computation tomorrow):
//
//	EPS     = net_earnings / shares_outstanding      (diluted, FY)
//	FCF     = operating_cash_flow + capex            (capex is negative)
//	net debt= total_debt − cash_and_equivalents
//
// A missing input yields nil — NEVER 0 — and the method that needs it becomes
// unavailable with its reason (§21).
const (
	ConceptEPS     = "eps"
	ConceptFCF     = "free_cash_flow"
	ConceptNetDebt = "net_debt"
)

// EPSFrom derives EPS with the FY diluted share count. nil when either side is
// missing, non-finite, non-positive or the quotient is not positive: an EPS
// that cannot be trusted blocks Graham, but it is reported as a reason, never
// as a zero EPS that would produce a zero valuation.
func EPSFrom(netEarnings, shares *float64) *float64 {
	ne := sane(netEarnings, false)
	sh := sane(shares, true)
	if ne == nil || sh == nil {
		return nil
	}
	return sane(ptr(*ne / *sh), true)
}

// FCFFrom returns the canonical free cash flow of §8. capex is expected to be
// negative (that is how it is stored), so it is ADDED. When capex is missing
// the FCF is nil rather than an OCF-only guess.
func FCFFrom(ocf, capex *float64) *float64 {
	o := sane(ocf, false)
	c := sane(capex, false)
	if o == nil || c == nil {
		return nil
	}
	sum := *o + *c
	if !isFinite(sum) {
		return nil
	}
	return &sum
}

// FCFFromEarnings is the §8 fallback for companies without OCF:
// FCF = net_earnings + depreciation_amortization − capex. It stays in the
// engine (single source of truth) even though M6b reads the value persisted by
// M6a; the loader reuses it for rows where OCF is not available.
func FCFFromEarnings(netEarnings, da, capex *float64) *float64 {
	ne := sane(netEarnings, false)
	d := sane(da, false)
	c := sane(capex, false)
	if ne == nil || d == nil || c == nil {
		return nil
	}
	sum := *ne + *d + *c
	if !isFinite(sum) {
		return nil
	}
	return &sum
}

// NetDebtFrom returns total debt − cash. A nil input yields nil (unknown), NOT
// 0: the DCF then computes without it AND records `net_debt_unknown` so the
// confidence can reflect the gap (§20/§21).
func NetDebtFrom(totalDebt, cash *float64) *float64 {
	td := sane(totalDebt, false)
	c := sane(cash, false)
	if td == nil || c == nil {
		return nil
	}
	return ptr(*td - *c)
}

// ValuationConcepts is the SINGLE list of canonical FY concepts the valuation
// consumes (plan C1/ADR D14). It lives in the engine package, not in the API or
// the pipeline, because a second copy is how the two consumers drift: a concept
// missing here returns nil without error and silently degrades a method.
//
//	net_earnings + shares_outstanding → EPS
//	free_cash_flow, else operating_cash_flow + capex → FCF
//	total_debt − cash_and_equivalents     → net debt
//	depreciation_amortization             → §8 FCF fallback when OCF is missing
//
// `capital_expenditures` is listed next to `capex` on purpose: the plan names the
// long name, but the concept this database actually carries is `capex`. Asking
// only for the non-existent one would disable the OCF fallback for EVERY
// security without a single error message.
var ValuationConcepts = []string{
	"net_earnings",
	"shares_outstanding",
	"free_cash_flow",
	"operating_cash_flow",
	"capex",
	"capital_expenditures",
	"depreciation_amortization",
	"cash_and_equivalents",
	"total_debt",
}

// Facts are the three canonical numbers the valuation derives from a row of FY
// facts (§8). Every field is nil when it cannot be derived from the data; nil
// is never 0 (§1).
type Facts struct {
	EPS          *float64
	FreeCashFlow *float64
	NetDebt      *float64
}

// DeriveFacts converts a `concept → value` map (the shape the storage FY
// loaders return) into the canonical Facts, applying §8 in ONE place:
//
//	EPS      = net_earnings / shares_outstanding
//	FCF      = free_cash_flow when the concept exists, else operating_cash_flow + capex
//	net debt = total_debt − cash_and_equivalents
//
// The API (on-the-fly fallback) and the pipeline (persisted rows) both call it,
// so a security can never be valued with one derivation in one endpoint and a
// different one in the other. The map keys are the names in ValuationConcepts.
func DeriveFacts(facts map[string]*float64) Facts {
	if facts == nil {
		return Facts{}
	}
	capex := facts["capex"]
	if capex == nil {
		capex = facts["capital_expenditures"]
	}
	fcf := FCFFrom(facts["operating_cash_flow"], capex)
	if stored := sane(facts["free_cash_flow"], false); stored != nil {
		// The concept the database already carries wins when it exists; the
		// OCF + capex derivation is the fallback for securities without it.
		fcf = stored
	}
	return Facts{
		EPS:          EPSFrom(facts["net_earnings"], facts["shares_outstanding"]),
		FreeCashFlow: fcf,
		NetDebt:      NetDebtFrom(facts["total_debt"], facts["cash_and_equivalents"]),
	}
}
