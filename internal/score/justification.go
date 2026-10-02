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
		parts = append(parts, dimensionSummary(d, input))
	}
	return fmt.Sprintf("%s: score %d/100 — %s. %s.",
		strings.ToUpper(ticker), score, strings.ToUpper(signal), strings.Join(parts, " "))
}

// dimensionSummary returns the deterministic Spanish sentence for one
// dimension. fmtF formats a *float64 with 2 decimals (nil → "n/d").
//
// Since M6b the valuation is TWO sentences (Graham and DCF, each with its own
// confidence and reasons) and an invalid dimension says so explicitly instead
// of printing a score of 50 it never had (ADR D16).
func dimensionSummary(d DimensionScore, input ScoreInput) string {
	f2 := func(v *float64) string {
		if v == nil {
			return "n/d"
		}
		return fmt.Sprintf("%.2f", *v)
	}
	// scoreText renders "87/100" or "inválida (sin dato)".
	scoreText := func(d DimensionScore) string {
		if !d.Valid || d.Score == nil {
			return "inválida"
		}
		return fmt.Sprintf("%.0f/100", *d.Score)
	}
	// methodSentence builds "Graham 112.50 (confianza alta; sin内在...)" style
	// provenance without repeating the template in two places.
	methodSentence := func(label string, intrinsic *float64, confidence string, reasons []string, d DimensionScore) string {
		if !d.Valid {
			if reasons != nil {
				return fmt.Sprintf("%s: sin valor base calculable (%s; dimensión inválida)", label, strings.Join(reasons, ", "))
			}
			return fmt.Sprintf("%s: sin valor base calculable (dimensión inválida)", label)
		}
		extra := ""
		if confidence != "" {
			extra = fmt.Sprintf("; confianza %s", confidence)
		}
		if len(reasons) > 0 {
			extra += fmt.Sprintf("; motivos: %s", strings.Join(reasons, ", "))
		}
		return fmt.Sprintf("%s %s vs precio %.2f con margen %.0f%%%s (dimensión %s)",
			label, f2(intrinsic), input.Price, input.MarginOfSafety, extra, scoreText(d))
	}

	switch d.Name {
	case DimGraham:
		return methodSentence("Valoración Graham:", input.GrahamBase, input.GrahamConfidence, input.GrahamReasons, d)
	case DimDCF:
		return methodSentence("Valoración DCF:", input.DCFBase, input.DCFConfidence, input.DCFReasons, d)
	case DimFundamentals:
		pe := f2(input.Metrics["pe_ratio"])
		roe := f2(input.Metrics["roe"])
		fcf := f2(input.Metrics["fcf_yield"])
		return fmt.Sprintf("Métricas: P/E %s, ROE %s, FCF Yield %s%% (dimensión %s)", pe, roe, fcf, scoreText(d))
	case DimComparables:
		min := input.ComparablesMinSecurities
		if min <= 0 {
			min = DefaultComparablesMinSecurities
		}
		if input.SectorCount < min {
			return fmt.Sprintf("Comparables: sin suficientes pares de sector (%d < %d; dimensión neutral %s)", input.SectorCount, min, scoreText(d))
		}
		return fmt.Sprintf("Comparables: vs mediana de sector con %d empresas (dimensión %s)", input.SectorCount, scoreText(d))
	case DimTrend:
		dir := "neutral"
		if input.SMA50 != nil && input.SMA200 != nil && *input.SMA50 > 0 && *input.SMA200 > 0 {
			if *input.SMA50 > *input.SMA200 {
				dir = "alcista (SMA50>SMA200)"
			} else {
				dir = "bajista (SMA50<SMA200)"
			}
		}
		return fmt.Sprintf("Tendencia: %s (dimensión %s)", dir, scoreText(d))
	default:
		return fmt.Sprintf("%s: %s", d.Name, scoreText(d))
	}
}
