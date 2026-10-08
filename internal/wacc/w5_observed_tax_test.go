package wacc

import (
	"math"
	"testing"
)

// W5 (M6c-T1), CA-7: pasar la tasa OBSERVADA al motor cambia el número, nunca
// la CLASIFICACIÓN de la fuente.
//
// El fixture es el mismo base() de wacc_test.go, el único que produce
// capm_hybrid (beta observada + E/D observados, Rf/ERP/Kd/tax desde Config):
// la tasa observada no puede convertirlo en capm_individual, porque ese techo
// exige los CUATRO parámetros observados (Rf, ERP, Kd y tax a la vez).
//
// Por qué importa: un rate derivado que "mejorara" la fuente haría que el
// mismo ticker cambiara de confidence medium a high solo por un hecho fiscal,
// rompiendo la taxonomía de ADR D31.
func TestW5ObservedTaxKeepsCapmHybrid(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.TaxRate != 21 {
		t.Fatalf("cfg.TaxRate = %v, esperado 21 (default): el caso sin tasa debe leer la config", cfg.TaxRate)
	}

	// (a) Misma entrada observada (beta 1.2, E=100, D=40) + tasa derivada 15.61.
	in := base()
	in.TaxRate = f(15.61)
	res := Calculate(in, cfg)

	if res.Source != SourceCAPMHybrid {
		t.Fatalf("source con tasa observada = %q, esperado %q", res.Source, SourceCAPMHybrid)
	}
	if res.Confidence != ConfidenceMedium {
		t.Fatalf("confidence = %q, esperado %q", res.Confidence, ConfidenceMedium)
	}
	approx(t, res.TaxRate, 15.61, 1e-9, "tax observada resuelta")

	// Kd_after_tax = Kd x (1 - tax/100): la tasa observada alimenta el mismo
	// número que la configurada habría alimentado (Az4).
	if res.KdAfterTax == nil {
		t.Fatal("KdAfterTax ausente")
	}
	if res.CostOfDebt == nil {
		t.Fatal("CostOfDebt ausente")
	}
	approx(t, res.KdAfterTax, *res.CostOfDebt*(1-15.61/100), 1e-9, "kd_at con tasa observada")

	// (b) La MISMA entrada con TaxRate=nil ⇒ la config (21) y, sobre todo, la
	// MISMA fuente: pasar la tasa no reclasifica.
	in2 := base()
	res2 := Calculate(in2, cfg)

	if res2.Source != res.Source {
		t.Fatalf("la tasa observada cambió la fuente: %q → %q (CA-7)", res.Source, res2.Source)
	}
	if res2.Source != SourceCAPMHybrid {
		t.Fatalf("source sin tasa = %q, esperado %q", res2.Source, SourceCAPMHybrid)
	}
	approx(t, res2.TaxRate, 21, 1e-9, "tax de config")

	// La tasa SÍ cambia el número (el WACC baja con menos impuesto), lo que
	// prueba que lo que no cambia es la CLASIFICACIÓN, no que el valor se ignore.
	if res.WACC == nil || res2.WACC == nil {
		t.Fatal("WACC ausente en uno de los dos casos")
	}
	if math.Abs(*res.WACC-*res2.WACC) < 1e-12 {
		t.Fatalf("la tasa 15.61 no llegó al WACC: %v == %v", *res.WACC, *res2.WACC)
	}
}
