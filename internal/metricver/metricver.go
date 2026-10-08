// Package metricver is a LEAF package: it depends on nothing, so both
// internal/storage (which reads derived_metrics) and internal/metrics (which
// owns the formulas) can consult it without an import cycle.
//
// It answers one question: **in which formula revision is a given metric
// defined?** That answer is what closes R-M6c-1 (ADR D13).
//
// The tension it resolves (DEVIATION FROM A LITERAL READING OF THE PLAN — to be
// validated in REVIEW): SPEC §16 needs nine metrics for `relative`, but ADR D12
// leaves the eight metrics of 1.0.0 untouched, so four of the nine
// (pe_ratio, pb_ratio, fcf_yield, roe) are 1.0.0 forever. Filtering the peer
// medians by a single `model_version = '2.1.0'` would return nil for those four
// and silently cut `relative` to a quarter of its inputs.
//
// So the comparables readers filter PER METRIC, by the revision that metric is
// defined in. That is what R-M6c-1 actually protects against: a median over two
// FORMULA REVISIONS of the same metric is non-deterministic and wrong. It is
// NOT about excluding a metric whose revision is simply older and unchanged.
package metricver

// Revision 1.0.0: the eight metrics of M2 (§13.2/§13.3/§13.4).
const V1 = "1.0.0"

// Revision 2.1.0 (current): the twelve metrics of ADR D12 (SPEC §13/§14).
// M6c-T1 W5 changed their INPUTS — interest and the observed tax rate must come
// from the SAME fiscal year — so the revision that owns them today is 2.1.0,
// not the 2.0.0 they were released as.
const V21 = "2.1.0"

// V2 is the FIRST revision of the twelve metrics of ADR D12. It is superseded
// by V21 but is KEPT as history: rows 762 (and any other 2.0.0 row) were
// already persisted and must remain readable (§26: never overwrite existing
// rows).
const V2 = "2.0.0"

// Pair binds a metric to the revision that defines it.
type Pair struct {
	Metric       string
	ModelVersion string
}

// Metrics of 1.0.0.
const (
	EPS      = "eps"
	PE       = "pe_ratio"
	PB       = "pb_ratio"
	PCF      = "pcf_ratio"
	PEG      = "peg_ratio"
	ROE      = "roe"
	DE       = "de_ratio"
	FCFYield = "fcf_yield"
)

// Metrics of 2.1.0 (ADR D12). The same twelve slugs existed as 2.0.0 (V2) and
// are kept as history; the DEFINING revision of these metrics is now 2.1.0.
const (
	ROIC            = "roic"
	OperatingMargin = "operating_margin"
	FCFMargin       = "fcf_margin"
	NetDebtToEBITDA = "net_debt_to_ebitda"
	InterestCover   = "interest_coverage"
	FCFToDebt       = "fcf_to_debt"
	PositiveEPSYrs  = "positive_eps_years"
	PositiveFCFYrs  = "positive_fcf_years"
	EPSVolatility   = "eps_volatility"
	FCFVolatility   = "fcf_volatility"
	EVEBITDA        = "ev_ebitda"
	EVEBIT          = "ev_ebit"
)

// legacy is the 1.0.0 set.
var legacy = []string{EPS, PE, PB, PCF, PEG, ROE, DE, FCFYield}

// modern is the 2.1.0 set (ADR D12, revision vigente tras M6c-T1 W5).
var modern = []string{ROIC, OperatingMargin, FCFMargin, NetDebtToEBITDA, InterestCover,
	FCFToDebt, PositiveEPSYrs, PositiveFCFYrs, EPSVolatility, FCFVolatility, EVEBITDA, EVEBIT}

// byMetric is the single source of truth of the binding.
var byMetric = func() map[string]string {
	m := make(map[string]string, len(legacy)+len(modern))
	for _, k := range legacy {
		m[k] = V1
	}
	for _, k := range modern {
		m[k] = V21
	}
	return m
}()

// DefiningVersion returns the revision in which a metric is defined. An unknown
// metric falls back to V1: a fail-safe that refuses to invent a revision, so an
// unrecognised row is read with the oldest formulas rather than the newest.
func DefiningVersion(metric string) string {
	if v, ok := byMetric[metric]; ok {
		return v
	}
	return V1
}

// AllPairs returns every metric with its defining revision, in a deterministic
// order. It is what the comparables readers are given.
//
// M6c-T1 review P3-1: the REVISION of every pair comes from byMetric — the
// single source of truth of the binding (no duplicated V1/V21 literals). The
// ORDER stays as declared above (legacy then modern, both in their defined
// order), which is the deterministic order consumers already rely on.
func AllPairs() []Pair {
	out := make([]Pair, 0, len(byMetric))
	for _, k := range legacy {
		out = append(out, Pair{k, byMetric[k]})
	}
	for _, k := range modern {
		out = append(out, Pair{k, byMetric[k]})
	}
	return out
}

// PairsFor returns the pairs of a subset of metrics, skipping unknown ones.
// The revision comes from byMetric, never from a literal.
func PairsFor(metrics []string) []Pair {
	out := make([]Pair, 0, len(metrics))
	for _, m := range metrics {
		if _, ok := byMetric[m]; ok {
			out = append(out, Pair{m, byMetric[m]})
		}
	}
	return out
}

// Split turns pairs into the two parallel arrays the SQL `unnest` needs.
func Split(pairs []Pair) (metrics, versions []string) {
	metrics = make([]string, 0, len(pairs))
	versions = make([]string, 0, len(pairs))
	for _, p := range pairs {
		metrics = append(metrics, p.Metric)
		versions = append(versions, p.ModelVersion)
	}
	return metrics, versions
}
