package valuation

import "math"

// discountRate is the discount rate ACTUALLY used by the DCF, with the
// provenance of its level (plan D9). It is carried around instead of being
// hidden in a formula: a configured discount rate is never presented as an
// individual WACC (§7 "No fabricar WACC si faltan inputs críticos").
type discountRate struct {
	rate   float64 // %
	source string  // taxonomy of the WACCSource* constants
	reason string  // degradation reason ("" at level 1)
	level  int     // 1..4
}

// resolveDiscountRate applies the 4 precedence levels of D9:
//
//  1. wacc_metrics.wacc           → capm_individual | capm_hybrid
//  2. wacc_metrics.cost_of_equity → cost_of_equity      (reason wacc_cost_of_equity)
//  3. WACC_FALLBACK               → configured_fallback (reason wacc_configured)
//  4. DCF_DISCOUNT_RATE           → legacy_discount_env (reason wacc_legacy_env)
//
// Levels 3 and 4 only exist when they are CONFIGURED: with a non-positive
// fallback the function returns a non-finite rate, which makes the DCF
// unavailable with its reason instead of inventing a discount rate.
//
// CRITICAL (B2 fix): a persisted row with wacc_source=configured_fallback
// and a numeric WACC (the configured fallback value) must be classified as
// level 3 (wacc_configured), NOT level 1. The source taxonomy wins over the
// presence of a numeric value.
func resolveDiscountRate(in Inputs, cfg Config) discountRate {
	// Level 3 (configured_fallback) takes precedence over level 1 when the
	// source explicitly says so — even if a numeric WACC is present (it is the
	// configured fallback value, not an observed CAPM WACC).
	if in.WACCSource == WACCSourceConfiguredFB {
		if f := cfg.WACCFallback; f > 0 && isFinite(f) {
			return discountRate{rate: f, source: WACCSourceConfiguredFB,
				reason: ReasonWACCConfigured, level: 3}
		}
		// Configured fallback source but no positive fallback configured:
		// no usable rate.
		return discountRate{rate: math.NaN(), source: WACCSourceConfiguredFB,
			reason: ReasonWACCConfigured, level: 0}
	}

	if w := sane(in.WACC, true); w != nil {
		src := in.WACCSource
		if src != WACCSourceCAPMIndividual && src != WACCSourceCAPMHybrid {
			// wacc_metrics only admits those two or configured_fallback; a row
			// with a WACC and an unexpected source is still an individual one,
			// but we do not relabel it: keep the recorded source verbatim.
			if src == "" {
				src = WACCSourceCAPMIndividual
			}
		}
		return discountRate{rate: *w, source: src, level: 1}
	}
	if ke := sane(in.CostOfEquity, true); ke != nil {
		return discountRate{rate: *ke, source: WACCSourceCostOfEquity,
			reason: ReasonWACCCostOfEquity, level: 2}
	}
	if f := cfg.WACCFallback; f > 0 && isFinite(f) {
		return discountRate{rate: f, source: WACCSourceConfiguredFB,
			reason: ReasonWACCConfigured, level: 3}
	}
	if f := cfg.DiscountFallback; f > 0 && isFinite(f) {
		return discountRate{rate: f, source: WACCSourceLegacyDiscountE,
			reason: ReasonWACCLegacyEnv, level: 4}
	}
	// No discount rate at all: the DCF stays unavailable (never a made-up one).
	return discountRate{rate: math.NaN(), source: "", reason: ReasonWACCConfigured, level: 0}
}

// flatPath reports whether the path is constant, which lets dcfValue evaluate
// the projection exactly as the M3 formula did (math.Pow instead of a
// year-by-year product).
func flatPath(path []float64) bool {
	if len(path) == 0 {
		return false
	}
	for _, g := range path {
		if g != path[0] {
			return false
		}
	}
	return true
}

// growthTransition returns the per-year growth path §7 requires, in percent.
//
//	g_target = g_terminal
//	g_anchor = min(g_initial, g_target + capPP)   ← caps the abrupt jump
//	g_t      = g_anchor + (g_target − g_anchor) × (t−1)/(N−1)
//
// enabled=false returns the FLAT path of the M3 formula (g_initial for every
// year), which keeps the historical DCF reproducible and makes the regression
// test possible. years<=1 returns a single-element path (the terminal value:
// with one projected year there is no interpolation). years<=0 returns nil.
func growthTransition(gInitial, gTerminal, capPP float64, years int, enabled bool) []float64 {
	if years <= 0 {
		return nil
	}
	if !enabled {
		return repeat(gInitial, years)
	}
	if years == 1 {
		return []float64{gTerminal}
	}
	anchor := gInitial
	if capped := gTerminal + capPP; anchor > capped {
		anchor = capped
	}
	path := make([]float64, years)
	span := float64(years - 1)
	for i := 0; i < years; i++ {
		t := float64(i) / span
		path[i] = anchor + (gTerminal-anchor)*t
	}
	return path
}

// repeat returns a slice of n copies of v.
func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// dcfValue is the DCF of §7 with a per-year growth path:
//
//  1. Project FCF year by year along `path`.
//  2. Terminal value = FCF_N × (1 + g_terminal) / (wacc − g_terminal).
//  3. Discount every cash flow (explicit + terminal) to present at wacc.
//  4. Subtract net debt (nil ⇒ 0, and the caller records net_debt_unknown).
//  5. Divide by shares outstanding → intrinsic value per share.
//
// With a FLAT path this is exactly the M3 formula (the regression test fixes
// bit-for-bit equality with it). Returns nil when the value is not a finite
// positive number: never a 0 (§21).
func dcfValue(fcf *float64, gPath []float64, wacc, terminalGrowthPct float64, years int, shares *float64, netDebt *float64) *float64 {
	sh := sane(shares, true)
	if sh == nil || len(gPath) == 0 {
		return nil
	}
	if !isFinite(wacc) || !isFinite(terminalGrowthPct) || wacc <= terminalGrowthPct {
		return nil // perpetuity degenerada (§21)
	}
	n := years
	if n <= 0 {
		n = len(gPath)
	}
	if n > len(gPath) {
		n = len(gPath)
	}

	dr := wacc / 100
	gt := terminalGrowthPct / 100

	// (1) projection + (3) discounting, and (2) the perpetuity at the end of the
	// horizon discounted back. The order of operations below is the M3 one.
	pv := 0.0
	ff := *fcf
	if flatPath(gPath) {
		g := gPath[0] / 100
		for t := 1; t <= n; t++ {
			ff = *fcf * math.Pow(1+g, float64(t))
			pv += ff / math.Pow(1+dr, float64(t))
		}
	} else {
		for t := 1; t <= n; t++ {
			ff *= 1 + gPath[t-1]/100
			pv += ff / math.Pow(1+dr, float64(t))
		}
	}
	terminal := ff * (1 + gt) / (dr - gt)
	pv += terminal / math.Pow(1+dr, float64(n))

	nd := 0.0
	if netDebt != nil {
		nd = *netDebt
	}
	equity := pv - nd
	if !isFinite(equity) || equity <= 0 {
		return nil // un equity no positivo no es un "valor" (§21)
	}
	out := equity / *sh
	if !isFinite(out) || out <= 0 {
		return nil
	}
	return &out
}

// CalcDCFScenarios implements §7 + §8 for the DCF:
//
//	bear = (g − 4, wacc + 1.5, terminal − 0.5)
//	base = (g,     wacc,       terminal)
//	bull = (g + 3, wacc − 1.0, terminal + 0.5)
//
// The discount rate of D9 is applied to the BASE value and its delta is added
// per scenario, so every precedence level propagates to the three scenarios.
// If `wacc − terminal <= 0` in a scenario, that DCF is nil WITH ITS REASON and
// the next precedence level is NOT attempted: degrading to a configured rate to
// force a number would be fabricating it (§7).
//
// §21 conditions, each with its own reason and never a zero:
//
//	FCF nil or <= 0      → fcf_non_positive
//	shares nil or <= 0   → shares_non_positive
//	no discount rate     → wacc_configured (nothing to discount with)
//	horizon <= 0         → invalid_horizon
//	wacc <= terminal     → wacc_below_terminal_growth (that scenario)
//	net debt nil         → treated as 0 AND net_debt_unknown recorded
func CalcDCFScenarios(in Inputs, cfg Config, gBase float64, rate discountRate) Method {
	m := Method{Status: StatusUnavailable}

	if in.FreeCashFlow == nil || *in.FreeCashFlow <= 0 || !isFinite(*in.FreeCashFlow) {
		m.Reasons = []string{ReasonFCFNonPositive}
		return m
	}
	if in.SharesOutstanding == nil || *in.SharesOutstanding <= 0 || !isFinite(*in.SharesOutstanding) {
		m.Reasons = []string{ReasonSharesNonPositive}
		return m
	}
	if cfg.HorizonYears <= 0 {
		m.Reasons = []string{ReasonInvalidHorizon}
		return m
	}
	if rate.level == 0 || !isFinite(rate.rate) {
		m.Reasons = []string{ReasonWACCConfigured}
		return m
	}
	if rate.reason != "" {
		m.Reasons = dedupe(m.Reasons, rate.reason)
	}
	if !isFinite(gBase) || gBase <= 0 {
		m.Reasons = dedupe(m.Reasons, ReasonGrowthNonPositive)
		return m
	}
	// Equity after net debt is unknown: the value is computed assuming 0, but
	// the fact is recorded (it is a real quality signal for §20).
	if in.NetDebt == nil {
		m.Reasons = dedupe(m.Reasons, ReasonNetDebtUnknown)
	}

	scenarios := []struct {
		gDelta, waccDelta, termDelta float64
		ptr                          **float64
	}{
		{cfg.BearGrowthDelta, cfg.BearWACCDelta, cfg.BearTerminalDelta, &m.Bear},
		{0, 0, 0, &m.Base},
		{cfg.BullGrowthDelta, cfg.BullWACCDelta, cfg.BullTerminalDelta, &m.Bull},
	}
	for _, s := range scenarios {
		g := gBase + s.gDelta
		w := rate.rate + s.waccDelta
		term := cfg.TerminalGrowth + s.termDelta
		if !isFinite(g) || g <= 0 {
			m.Reasons = dedupe(m.Reasons, ReasonGrowthNonPositive)
			continue
		}
		if !isFinite(w) || !isFinite(term) || w <= term {
			m.Reasons = dedupe(m.Reasons, ReasonWACCBelowTerminal)
			continue
		}
		path := growthTransition(g, term, cfg.TransitionCapPP, cfg.HorizonYears, cfg.Transition)
		v := dcfValue(in.FreeCashFlow, path, w, term, cfg.HorizonYears, in.SharesOutstanding, in.NetDebt)
		if v == nil {
			m.Reasons = dedupe(m.Reasons, ReasonNonPositiveValue)
			continue
		}
		*s.ptr = v
	}

	if m.scenarioCount() == 0 {
		m.Status = StatusUnavailable
		return m
	}
	m.Status = StatusAvailable
	// Method confidence = worst of the confidences of the inputs the DCF used
	// (growth and discount rate). Never above what the inputs support (§20).
	levels := []Confidence{}
	if in.GrowthConfidence != "" {
		levels = append(levels, in.GrowthConfidence)
	}
	if in.WACCConfidence != "" && rate.level == 1 {
		levels = append(levels, in.WACCConfidence)
	}
	if len(levels) > 0 {
		m.Confidence = worst(levels...)
	}
	return m
}

// calcSensitivity is the §8 grid WACC × growth with one point per cell. It
// uses the BASE growth path of each cell and the resolved discount rate with
// the scenario deltas; it never changes the main result (it is a diagnostic).
//
// steps<=1 returns the base cell only. steps>1 returns `steps` evenly spaced
// points per axis from the bear value to the bull value, both included, in a
// deterministic order (WACC outer, growth inner).
func calcSensitivity(in Inputs, cfg Config, gBase float64, rate discountRate) []SensitivityPoint {
	if rate.level == 0 || !isFinite(rate.rate) || in.FreeCashFlow == nil ||
		in.SharesOutstanding == nil || cfg.HorizonYears <= 0 {
		return nil
	}
	steps := cfg.SensitivitySteps
	if steps < 1 {
		steps = 1
	}
	waccs := axis(rate.rate, cfg.BearWACCDelta, cfg.BullWACCDelta, steps)
	growths := axis(gBase, cfg.BearGrowthDelta, cfg.BullGrowthDelta, steps)

	out := make([]SensitivityPoint, 0, len(waccs)*len(growths))
	for _, w := range waccs {
		for _, g := range growths {
			term := cfg.TerminalGrowth
			cell := SensitivityPoint{Growth: g, WACC: w}
			path := growthTransition(g, term, cfg.TransitionCapPP, cfg.HorizonYears, cfg.Transition)
			if v := dcfValue(in.FreeCashFlow, path, w, term, cfg.HorizonYears, in.SharesOutstanding, in.NetDebt); v != nil {
				cell.DCFBase = *v
			}
			out = append(out, cell)
		}
	}
	return out
}

// axis returns n values from base+bearDelta to base+bullDelta. With an odd n
// the middle point is EXACTLY base (§8: the base scenario is the centre of the
// grid, not a point next to it), so the grid always contains the value the
// result itself reports.
func axis(base, bearDelta, bullDelta float64, n int) []float64 {
	if n <= 1 {
		return []float64{base}
	}
	lo, hi := base+bearDelta, base+bullDelta
	out := make([]float64, n)
	span := float64(n - 1)
	for i := 0; i < n; i++ {
		out[i] = lo + (hi-lo)*(float64(i)/span)
	}
	if n%2 == 1 {
		out[n/2] = base
	}
	return out
}
