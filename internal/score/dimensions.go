package score

import "math"

// scoreValuation scores the price against the intrinsic values (Graham and
// DCF) with the user margin of safety (plan D5):
//   - price < intrinsic × (1 − margin/100) → 100
//   - linear decay to 0 as price approaches intrinsic
//   - price >= intrinsic → 0
//
// When both Graham and DCF are available the dimension is the weighted
// average (60% Graham, 40% DCF); with one, that one; with none, 50 (neutral,
// sin datos).
func scoreValuation(price float64, graham, dcf *float64, marginOfSafety float64) float64 {
	single := func(intrinsic *float64) *float64 {
		if intrinsic == nil || *intrinsic <= 0 || price <= 0 {
			return nil
		}
		floor := *intrinsic * (1 - marginOfSafety/100)
		if price <= floor {
			out := 100.0
			return &out
		}
		if price >= *intrinsic {
			out := 0.0
			return &out
		}
		// Decaimiento lineal: floor→100, intrinsic→0.
		out := 100 * (*intrinsic - price) / (*intrinsic - floor)
		out = clamp(out, 0, 100)
		return &out
	}

	g := single(graham)
	d := single(dcf)
	switch {
	case g != nil && d != nil:
		return 0.6**g + 0.4**d
	case g != nil:
		return *g
	case d != nil:
		return *d
	default:
		return 50
	}
}

// percentileToBand convierte un valor a banda según umbrales crecientes donde
// "menor es mejor". bandas: [{hi, score}...] ordenadas ascendente por hi;
// valor <= bandas[0].hi → scores[0]; > último → 10.
func percentileToBand(value, b1, b2, b3, b4 float64, s100, s75, s50, s25, s10 float64) float64 {
	switch {
	case value <= b1:
		return s100
	case value <= b2:
		return s75
	case value <= b3:
		return s50
	case value <= b4:
		return s25
	default:
		return s10
	}
}

// scoreFundamentals averages the six metric sub-scores (P/E, P/B, FCF Yield,
// ROE, D/E, PEG) against fixed thresholds (plan D5). Missing metrics are
// skipped; with no usable metric the dimension is neutral (50).
// Units follow derived_metrics: roe y fcf_yield como fracción con fcf_yield
// ya en porcentaje x100 (metrics.CalcFCFYield); roe se escala a porcentaje.
func scoreFundamentals(metrics map[string]*float64) float64 {
	scores := []float64{}
	// P/E: <15→100, 15-20→75, 20-30→50, 30-40→25, >40→10
	if v, ok := metrics["pe_ratio"]; ok {
		scores = append(scores, percentileToBand(*v, 15, 20, 30, 40, 100, 75, 50, 25, 10))
	}
	// P/B: <1→100, 1-2→75, 2-5→50, 5-10→25, >10→10
	if v, ok := metrics["pb_ratio"]; ok {
		scores = append(scores, percentileToBand(*v, 1, 2, 5, 10, 100, 75, 50, 25, 10))
	}
	// FCF Yield (%): >8→100, 5-8→75, 3-5→50, 1-3→25, <1→10
	if v, ok := metrics["fcf_yield"]; ok {
		scores = append(scores, percentileToBand(*v, 1, 3, 5, 8, 10, 25, 50, 75, 100))
	}
	// ROE (fracción; ×100 → %): >20→100, 15-20→75, 10-15→50, 5-10→25, <5→10
	if v, ok := metrics["roe"]; ok {
		scores = append(scores, percentileToBand(*v*100, 5, 10, 15, 20, 10, 25, 50, 75, 100))
	}
	// D/E: <0.5→100, 0.5-1→75, 1-1.5→50, 1.5-2→25, >2→10
	if v, ok := metrics["de_ratio"]; ok {
		scores = append(scores, percentileToBand(*v, 0.5, 1, 1.5, 2, 100, 75, 50, 25, 10))
	}
	// PEG: <1→100, 1-1.5→75, 1.5-2→50, 2-3→25, >3→10
	if v, ok := metrics["peg_ratio"]; ok {
		scores = append(scores, percentileToBand(*v, 1, 1.5, 2, 3, 100, 75, 50, 25, 10))
	}

	if len(scores) == 0 {
		return 50
	}
	sum := 0.0
	for _, s := range scores {
		sum += s
	}
	return sum / float64(len(scores))
}

// relativeScore devuelve el sub-score relativo de un valor frente a la
// mediana: 100 si es >=20% mejor, 0 si es >=20% peor, lineal entre mediana y
// el punto de "mejor". lowerIsBetter indica la dirección de la bondad.
func relativeScore(value, median *float64, lowerIsBetter bool) *float64 {
	if value == nil || median == nil || *value <= 0 || *median <= 0 {
		return nil
	}
	diff := (*value - *median) / math.Abs(*median) // >0 = peor si lowerIsBetter
	if lowerIsBetter {
		// diff +0.2 → 0; 0 → 50; −0.2 → 100
		out := 50 - 250*diff
		return &out
	}
	// mayor es mejor: diff 0.2 → 100; 0 → 50; −0.2 → 0
	out := 50 + 250*diff
	return &out
}

// scoreComparables blends the relative value of the ticker's metrics against
// the sector median (SPEC §13.5.3) and its own historical median. When fewer
// than 'minSecurities' peers share the sector, or there is no overlapping
// data, the dimension degrades to neutral (50). Sector 60% / histórico 40%.
func scoreComparables(metrics, sectorMedian, historicalMedian map[string]*float64, sectorCount int, minSecurities int) float64 {
	if minSecurities <= 0 {
		minSecurities = DefaultComparablesMinSecurities
	}
	if sectorCount < minSecurities {
		return 50
	}

	// Métricas con dirección conocida.
	type dir struct {
		name          string
		lowerIsBetter bool
	}
	directions := []dir{
		{"pe_ratio", true}, {"pb_ratio", true}, {"de_ratio", true}, {"peg_ratio", true},
		{"roe", false}, {"fcf_yield", false},
	}

	avg := func(base map[string]*float64) float64 {
		var subs []float64
		for _, d := range directions {
			s := relativeScore(metrics[d.name], base[d.name], d.lowerIsBetter)
			if s != nil {
				subs = append(subs, clamp(*s, 0, 100))
			}
		}
		if len(subs) == 0 {
			return 50
		}
		sum := 0.0
		for _, s := range subs {
			sum += s
		}
		return sum / float64(len(subs))
	}

	sector := avg(sectorMedian)
	if len(sectorMedian) == 0 {
		sector = 50
	}
	historical := avg(historicalMedian)
	if len(historicalMedian) == 0 {
		historical = 50
	}
	return 0.6*sector + 0.4*historical
}

// scoreTrend scores the price trend dimension: SMA crossover as base
// (SMA50>SMA200 → alcista 80, bajista 30, cercano al cruce → 55, sin SMA →
// 50) blended 70/30 with momentum (6m/12m) when available; momentum-only or
// absent data → base neutral 50.
func scoreTrend(sma50, sma200, momentum6m, momentum12m *float64) float64 {
	base := 50.0
	if sma50 != nil && sma200 != nil && *sma50 > 0 && *sma200 > 0 {
		ratio := *sma50 / *sma200
		switch {
		case ratio > 1.02:
			base = 80
		case ratio < 0.98:
			base = 30
		default:
			base = 55 // proximidad al cruce
		}
	}

	mom := func(m *float64) *float64 {
		if m == nil {
			return nil
		}
		var s float64
		switch {
		case *m > 0.10:
			s = 90
		case *m >= 0:
			s = 70
		case *m > -0.10:
			s = 40
		default:
			s = 15
		}
		return &s
	}
	m6, m12 := mom(momentum6m), mom(momentum12m)
	if m6 == nil && m12 == nil {
		return base
	}
	if m6 != nil && m12 != nil {
		return 0.7*base + 0.3*((*m6+*m12)/2)
	}
	only := m6
	if only == nil {
		only = m12
	}
	return 0.7*base + 0.3**only
}
