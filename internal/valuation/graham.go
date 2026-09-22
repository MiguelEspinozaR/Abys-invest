// Package valuation implements the intrinsic-value engines of SPEC §13.1
// (Benjamin Graham) and the simplified DCF of the M3 plan, both deterministic
// pure functions: same inputs -> same output.
//
// Conservative data rule: a nil result means "insufficient data" (never
// fabricate a value); the caller records the missing inputs in its snapshot.
package valuation

import "math"

// defaultGrowth is GROWTH_RATE_DEFAULT: g = 7% yearly EPS growth (plan D2).
const defaultGrowth = 7.0

// sane returns v when it is a valid finite number (and > 0 when
// positiveOnly is set), nil otherwise.
func sane(v *float64, positiveOnly bool) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return nil
	}
	if positiveOnly && *v <= 0 {
		return nil
	}
	return v
}

// CalcGrahamIntrinsic implements SPEC §13.1:
//
//	multiple  = (2 × g) + 8.5
//	intrinsic = multiple × EPS
//
// growthRate is in percent (e.g. 7 = 7%); EPS is the expected EPS (M3:
// EPS of the latest completed fiscal year, plan D2). Returns nil when EPS is
// nil or <= 0 (never a negative intrinsic value).
func CalcGrahamIntrinsic(growthRate float64, eps *float64) *float64 {
	if growthRate <= 0 {
		growthRate = defaultGrowth
	}
	e := sane(eps, true)
	if e == nil {
		return nil
	}
	multiple := (2*growthRate + 8.5)
	out := multiple * *e
	return &out
}
