package score

import (
	"fmt"
	"strings"
)

// generateJustification builds a deterministic textual justification from the
// score inputs using fixed templates (never an LLM). Pattern (plan D5):
//
//	"{ticker}: score {score}/100 — {SIGNAL}. {dimension_summaries}."
func generateJustification(ticker string, score int, signal string, dims []DimensionScore, input ScoreInput) string {
	var parts []string
	for _, d := range dims {
		parts = append(parts, dimensionSummary(d.Name, d.Score, input))
	}
	return fmt.Sprintf("%s: score %d/100 — %s. %s.",
		strings.ToUpper(ticker), score, strings.ToUpper(signal), strings.Join(parts, " "))
}

// dimensionSummary returns the deterministic Spanish sentence for one
// dimension. fmtF formats a *float64 with 2 decimals (nil → "n/d").
func dimensionSummary(name string, s float64, input ScoreInput) string {
	f2 := func(v *float64) string {
		if v == nil {
			return "n/d"
		}
		return fmt.Sprintf("%.2f", *v)
	}
	switch name {
	case DimValuation:
		pf := fmt.Sprintf("%.2f", input.Price)
		switch {
		case input.GrahamIntrinsic != nil && input.DCFIntrinsic != nil:
			return fmt.Sprintf("Valoración: precio %s vs Graham %s y DCF %s con margen %.0f%% (dimensión %.0f/100)",
				pf, f2(input.GrahamIntrinsic), f2(input.DCFIntrinsic), input.MarginOfSafety, s)
		case input.GrahamIntrinsic != nil:
			return fmt.Sprintf("Valoración: precio %s vs Graham %s con margen %.0f%% (dimensión %.0f/100)",
				pf, f2(input.GrahamIntrinsic), input.MarginOfSafety, s)
		case input.DCFIntrinsic != nil:
			return fmt.Sprintf("Valoración: precio %s vs DCF %s con margen %.0f%% (dimensión %.0f/100)",
				pf, f2(input.DCFIntrinsic), input.MarginOfSafety, s)
		default:
			return fmt.Sprintf("Valoración: sin valor intrínseco calculable (datos insuficientes; dimensión %.0f/100)", s)
		}
	case DimFundamentals:
		pe := f2(input.Metrics["pe_ratio"])
		roe := f2(input.Metrics["roe"])
		fcf := f2(input.Metrics["fcf_yield"])
		return fmt.Sprintf("Métricas: P/E %s, ROE %s, FCF Yield %s%% (dimensión %.0f/100)", pe, roe, fcf, s)
	case DimComparables:
		min := input.ComparablesMinSecurities
		if min <= 0 {
			min = DefaultComparablesMinSecurities
		}
		if input.SectorCount < min {
			return fmt.Sprintf("Comparables: sin suficientes pares de sector (%d < %d; dimensión neutral %.0f/100)", input.SectorCount, min, s)
		}
		return fmt.Sprintf("Comparables: vs mediana de sector con %d empresas (dimensión %.0f/100)", input.SectorCount, s)
	case DimTrend:
		dir := "neutral"
		if input.SMA50 != nil && input.SMA200 != nil && *input.SMA50 > 0 && *input.SMA200 > 0 {
			if *input.SMA50 > *input.SMA200 {
				dir = "alcista (SMA50>SMA200)"
			} else {
				dir = "bajista (SMA50<SMA200)"
			}
		}
		return fmt.Sprintf("Tendencia: %s (dimensión %.0f/100)", dir, s)
	default:
		return fmt.Sprintf("%s: %.0f/100", name, s)
	}
}
