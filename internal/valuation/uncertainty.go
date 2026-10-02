package valuation

import "math"

// CalcUncertainty is §19: the dispersion of the valuation, computed over the
// minimum component set §19 names, discarding nils:
//
//	graham_base, dcf_bear, dcf_base, dcf_bull
//	mean       = Σ / n
//	std (pop.) = √(Σ(x − mean)² / n)
//	dispersion = std / mean
//
// Fewer than two valid components, or a non-positive mean, gives a nil
// dispersion with Components = n: NEVER a 0, because "no data" and "zero
// dispersion" are different facts (§21 conservative rule). Identical values DO
// give dispersion = 0 — that is a real measurement.
//
// The dispersion is exposed RAW (user decision A4): it never degrades the
// confidence in M6b, and no threshold is read from the environment.
func CalcUncertainty(g, d Method) Uncertainty {
	values := make([]float64, 0, 4)
	for _, v := range []*float64{g.Base, d.Bear, d.Base, d.Bull} {
		if v != nil && isFinite(*v) {
			values = append(values, *v)
		}
	}
	u := Uncertainty{Components: len(values)}
	if len(values) < 2 {
		return u
	}
	mean := 0.0
	for _, v := range values {
		mean += v
	}
	mean /= float64(len(values))
	if !isFinite(mean) || mean <= 0 {
		return u // a non-positive mean has no interpretable relative dispersion
	}
	variance := 0.0
	for _, v := range values {
		variance += (v - mean) * (v - mean)
	}
	variance /= float64(len(values))
	std := math.Sqrt(variance)
	if !isFinite(std) || std < 0 {
		return u
	}
	u.Mean = &mean
	u.StdDev = &std
	disp := std / mean
	if isFinite(disp) {
		u.Dispersion = &disp
	}
	return u
}
