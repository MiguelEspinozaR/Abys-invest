package quality

import (
	"math"
	"testing"
)

// W5 (M6c-T1): la tasa observada entra al motor y el tope de ADR D26 se lee por
// PROCEDENCIA. Los fixtures se reutilizan de quality_test.go (aaplInputs, ptr,
// asOf, hasReason, metricOf): aquí no se construye ningún Inputs nuevo, solo se
// cambian los campos de tasa y de alineación que W5 añade.

// 1) Con `derived` la tasa observada sustituye a la configurada en NOPAT, y el
// ROIC sube cuando baja el impuesto. Misma entrada, distinta procedencia.
func TestW5DerivedTaxRateFeedsROIC(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.TaxRate != 21 {
		t.Fatalf("cfg.TaxRate = %v, esperado 21 (default)", cfg.TaxRate)
	}
	base := Calculate(aaplInputs(), cfg) // configured → 21 %

	in := aaplInputs()
	in.TaxRate = ptr(10)
	in.TaxRateSource = TaxRateSourceDerived
	res := Calculate(in, cfg)

	if res.TaxRateSource != TaxRateSourceDerived {
		t.Fatalf("TaxRateSource = %q, esperado %q", res.TaxRateSource, TaxRateSourceDerived)
	}
	got, ok := res.Metrics[MetricROIC]
	if !ok || got == nil {
		t.Fatalf("roic ausente: %v", res.Reasons)
	}
	invested := 73.733 + 98.657 - 35.934
	want := (133.050 * (1 - 10.0/100.0)) / invested
	if math.Abs(*got-want) > 1e-12 {
		t.Fatalf("roic con tasa 10 = %.12f, esperado %.12f", *got, want)
	}
	if base.Metrics[MetricROIC] == nil {
		t.Fatalf("roic de la referencia ausente: %v", base.Reasons)
	}
	if *got <= *base.Metrics[MetricROIC] {
		t.Fatalf("roic derivado %.12f debe ser MAYOR que el configurado %.12f (menos impuesto)",
			*got, *base.Metrics[MetricROIC])
	}
}

// 2) Una tasa observada fuera de [0, 50] se RECHAZA (ADR D32, el caso AVGO de
// −1,75 %): el motor cae a cfg.TaxRate y lo dice con tax_rate_out_of_range.
func TestW5TaxRateOutOfRange(t *testing.T) {
	cfg := DefaultConfig()
	wantROIC := *Calculate(aaplInputs(), cfg).Metrics[MetricROIC] // configured → 21 %

	for _, bad := range []float64{-1.75, 60} {
		in := aaplInputs()
		in.TaxRate = ptr(bad)
		in.TaxRateSource = TaxRateSourceDerived
		res := Calculate(in, cfg)

		got, ok := res.Metrics[MetricROIC]
		if !ok || got == nil {
			t.Fatalf("tax=%v: roic ausente: %v", bad, res.Reasons)
		}
		if math.Abs(*got-wantROIC) > 1e-12 {
			t.Fatalf("tax=%v: roic = %.12f, esperado el de cfg.TaxRate (21) = %.12f", bad, *got, wantROIC)
		}
		if !hasReason(res.Reasons, ReasonTaxRateOutOfRange) {
			t.Fatalf("tax=%v: Reasons = %v, falta %q", bad, res.Reasons, ReasonTaxRateOutOfRange)
		}
	}
}

// 3) Par fiscal incompleto en el ancla: la procedencia sigue siendo configured y
// el motivo del W4 viaja a Reasons (nunca una tasa inventada).
func TestW5IncompletePairConfigured(t *testing.T) {
	const reasonUnavailable = "tax_rate_unavailable"

	in := aaplInputs()
	in.TaxRate = nil
	in.TaxRateSource = TaxRateSourceConfigured
	in.TaxRateReason = reasonUnavailable
	res := Calculate(in, DefaultConfig())

	if res.TaxRateSource != TaxRateSourceConfigured {
		t.Fatalf("TaxRateSource = %q, esperado %q", res.TaxRateSource, TaxRateSourceConfigured)
	}
	if !hasReason(res.Reasons, reasonUnavailable) {
		t.Fatalf("Reasons = %v, falta %q", res.Reasons, reasonUnavailable)
	}
	if hasReason(res.Reasons, ReasonTaxRateOutOfRange) {
		t.Fatalf("un par incompleto no es una tasa fuera de rango: %v", res.Reasons)
	}
}

// 4) ADR D26 + D32: `derived` levanta el tope de confidence (cov ≥ high ⇒ high,
// sin tax_rate_configured); el MISMO input con `configured` queda en medium y
// con el motivo.
func TestW5ConfidenceLiftedWhenDerived(t *testing.T) {
	cfg := DefaultConfig()

	derivedIn := aaplInputs()
	derivedIn.TaxRate = ptr(15.61)
	derivedIn.TaxRateSource = TaxRateSourceDerived
	derived := Calculate(derivedIn, cfg)

	if derived.Coverage < cfg.CoverageHigh {
		t.Fatalf("coverage = %v, no llega a CoverageHigh %v", derived.Coverage, cfg.CoverageHigh)
	}
	if derived.Confidence != ConfidenceHigh {
		t.Fatalf("con derived la confidence debe ser %q: %q (reasons %v)",
			ConfidenceHigh, derived.Confidence, derived.Reasons)
	}
	if hasReason(derived.Reasons, ReasonTaxRateConfigured) {
		t.Fatalf("con derived no debe figurar tax_rate_configured: %v", derived.Reasons)
	}

	configuredIn := derivedIn // mismo input, solo cambia la procedencia
	configuredIn.TaxRateSource = TaxRateSourceConfigured
	configured := Calculate(configuredIn, cfg)

	if configured.Confidence != ConfidenceMedium {
		t.Fatalf("con configured la confidence debe ser %q: %q (reasons %v)",
			ConfidenceMedium, configured.Confidence, configured.Reasons)
	}
	if !hasReason(configured.Reasons, ReasonTaxRateConfigured) {
		t.Fatalf("con configured debe figurar tax_rate_configured: %v", configured.Reasons)
	}
}

// 5) interest_coverage usa el par ALINEADO del mismo FY (CA-3/CA-4): un motivo
// de alineación anula la métrica y queda visible en el sub-bloque, y un interés
// ≤ 0 nunca produce un ratio invertido.
func TestW5InterestCoverageAlignedPair(t *testing.T) {
	in := aaplInputs()
	in.AlignedOperatingIncome = ptr(100)
	in.AlignedInterestExpense = ptr(4)
	in.InterestReason = ""

	res := Calculate(in, DefaultConfig())
	got, ok := res.Metrics[MetricInterestCoverage]
	if !ok || got == nil {
		t.Fatalf("interest_coverage ausente con par alineado: %v", res.Reasons)
	}
	if math.Abs(*got-25.0) > 1e-12 {
		t.Fatalf("interest_coverage = %v, esperado 25 (100/4)", *got)
	}

	mismatch := in
	mismatch.InterestReason = "interest_period_mismatch"
	res2 := Calculate(mismatch, DefaultConfig())
	if res2.Metrics[MetricInterestCoverage] != nil {
		t.Fatalf("con interés de otro FY la métrica no puede existir: %v", res2.Metrics[MetricInterestCoverage])
	}
	m := metricOf(t, res2, SubSolvency, MetricInterestCoverage)
	if m.Reason != "interest_period_mismatch" {
		t.Fatalf("razón en debt_solvency = %q, esperado interest_period_mismatch", m.Reason)
	}

	zero := in
	zero.InterestReason = ""
	zero.AlignedInterestExpense = ptr(0)
	res3 := Calculate(zero, DefaultConfig())
	if res3.Metrics[MetricInterestCoverage] != nil {
		t.Fatalf("interés 0 debe dar métrica nil, no %v", *res3.Metrics[MetricInterestCoverage])
	}
}

// 6) CA-10: QUALITY_FY_MAX_AGE_DAYS fuera de [1,3650] cae al default 550 con
// warning; dentro del rango se aplica (ConfigFromEnv es el único lector del env).
func TestW5FYMaxAgeFromEnv(t *testing.T) {
	t.Setenv("QUALITY_FY_MAX_AGE_DAYS", "400")
	if got := ConfigFromEnv().FYMaxAgeDays; got != 400 {
		t.Fatalf("FYMaxAgeDays = %d, esperado 400", got)
	}

	t.Setenv("QUALITY_FY_MAX_AGE_DAYS", "0") // fuera de [1,3650]
	if got := ConfigFromEnv().FYMaxAgeDays; got != DefaultQualityFYMaxAgeDays {
		t.Fatalf("FYMaxAgeDays = %d con 0, esperado el default %d", got, DefaultQualityFYMaxAgeDays)
	}
	if DefaultQualityFYMaxAgeDays != 550 {
		t.Fatalf("DefaultQualityFYMaxAgeDays = %d, esperado 550", DefaultQualityFYMaxAgeDays)
	}
}

// 7) P2-1: `derived` sin tasa no puede declarar que hay tasa derivada. El motor
// cae a `configured`, mantiene el tope de ADR D26 (medium aunque la cobertura
// sea alta) y lo dice con tax_rate_configured — nunca con un silencio que
// parecería una derivación limpia, ni con un out_of_range (no hay tasa que
// recortar).
func TestW5DerivedWithoutTaxRateFallsBackToConfigured(t *testing.T) {
	in := aaplInputs()
	in.TaxRate = nil
	in.TaxRateSource = TaxRateSourceDerived
	res := Calculate(in, DefaultConfig())

	if res.TaxRateSource != TaxRateSourceConfigured {
		t.Fatalf("TaxRateSource = %q, esperado %q (derived sin tasa no existe)",
			res.TaxRateSource, TaxRateSourceConfigured)
	}
	if hasReason(res.Reasons, ReasonTaxRateOutOfRange) {
		t.Fatalf("sin tasa no hay out_of_range: %v", res.Reasons)
	}
	if res.Score == nil {
		t.Fatalf("score ausente: %v", res.Reasons)
	}
	if !hasReason(res.Reasons, ReasonTaxRateConfigured) {
		t.Fatalf("debe declarar tax_rate_configured: %v", res.Reasons)
	}
	// ADR D26: una tasa configurada (aunque el caller la etiquetara derived)
	// mantiene el tope en medium con cobertura alta.
	if res.Coverage >= DefaultConfig().CoverageHigh && res.Confidence != ConfidenceMedium {
		t.Fatalf("confidence = %q con cobertura %v: el tope D26 exige medium",
			res.Confidence, res.Coverage)
	}
	// El ROIC del caso derived-sin-tasa es idéntico al configured puro: mismo
	// número, misma procedencia efectiva.
	want := *Calculate(aaplInputs(), DefaultConfig()).Metrics[MetricROIC]
	got := res.Metrics[MetricROIC]
	if got == nil || math.Abs(*got-want) > 1e-12 {
		t.Fatalf("roic = %v, esperado el de cfg.TaxRate = %v", got, want)
	}
}
