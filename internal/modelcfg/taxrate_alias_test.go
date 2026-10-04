package modelcfg

import (
	"log/slog"
	"strings"
	"testing"
)

// captureWarnings swaps the default logger for one writing into a buffer and
// restores it, so the deprecation warnings are ASSERTED instead of assumed.
func captureWarnings(t *testing.T) *strings.Builder {
	t.Helper()
	var sb strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&sb, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &sb
}

// TestTaxRateAliasAloneIsUsedAsQualityTaxRate is the first half of the CA of (e):
// a set that still carries the deprecated `tax_rate` keeps WORKING (its value is
// the tax rate) and warns. Before the fix the alias wrote ModelConfig.TaxRate
// alone, so the tax applied to NOPAT/ROIC (quality) silently stayed at the layer
// below: one knob, two different effective rates.
func TestTaxRateAliasAloneIsUsedAsQualityTaxRate(t *testing.T) {
	sb := captureWarnings(t)

	base := DefaultModelConfig()
	out := base.WithParameters(map[string]any{KeyTaxRate: 25.0})

	if out.QualityTaxRate != 25.0 {
		t.Errorf("tax_rate=25 debe fijar el impuesto, QualityTaxRate = %v", out.QualityTaxRate)
	}
	if out.TaxRate != out.QualityTaxRate {
		t.Errorf("una sola fuente de impuestos: TaxRate = %v, QualityTaxRate = %v",
			out.TaxRate, out.QualityTaxRate)
	}
	if !strings.Contains(sb.String(), "deprecad") {
		t.Errorf("un alias deprecado debe avisar, log: %q", sb.String())
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("el set con el alias debe seguir siendo válido: %v", err)
	}
}

// TestTaxRateConflictQualityTaxRateWins is the second half: with both keys the
// CANONICAL one wins, with a warning, so a set cannot reintroduce the divergence.
func TestTaxRateConflictQualityTaxRateWins(t *testing.T) {
	sb := captureWarnings(t)

	base := DefaultModelConfig()
	out := base.WithParameters(map[string]any{
		KeyTaxRate:        25.0, // deprecado: debe perder
		KeyQualityTaxRate: 18.0, // canónico: debe ganar
	})

	if out.QualityTaxRate != 18.0 {
		t.Errorf("con ambas claves gana quality_tax_rate, QualityTaxRate = %v", out.QualityTaxRate)
	}
	if out.TaxRate != 18.0 {
		t.Errorf("TaxRate debe seguir a QualityTaxRate, = %v", out.TaxRate)
	}
	if !strings.Contains(sb.String(), "quality_tax_rate") {
		t.Errorf("el conflicto debe nombrarse en el aviso, log: %q", sb.String())
	}
}

// TestQualityTaxRateAloneDoesNotWarn: the canonical key is not deprecated, so it
// must not produce noise.
func TestQualityTaxRateAloneDoesNotWarn(t *testing.T) {
	sb := captureWarnings(t)

	base := DefaultModelConfig()
	out := base.WithParameters(map[string]any{KeyQualityTaxRate: 15.0})

	if out.QualityTaxRate != 15.0 || out.TaxRate != 15.0 {
		t.Errorf("quality_tax_rate=15 debe aplicarse a ambos campos: %+v", out)
	}
	if sb.String() != "" {
		t.Errorf("la clave canónica no debe avisar, log: %q", sb.String())
	}
}

// TestTaxRateInvariantHolds is the invariant the plan asks to be TESTED rather
// than just intended: for ANY accepted tax key of a set, the two fields are
// equal afterwards, so internal/wacc and internal/quality can never read
// different rates.
func TestTaxRateInvariantHolds(t *testing.T) {
	captureWarnings(t)

	base := DefaultModelConfig()
	cases := []map[string]any{
		{KeyTaxRate: 25.0},
		{KeyQualityTaxRate: 18.0},
		{KeyTaxRate: 25.0, KeyQualityTaxRate: 18.0},
		{KeyTaxRate: "no es un número"},    // inválido: conserva la capa inferior
		{KeyTaxRate: 999.0},                // fuera de rango [0,50]
		{KeyTaxRate: 25.0, KeyDCFYears: 6}, // acompañante válido
	}
	for _, params := range cases {
		out := base.WithParameters(params)
		if out.TaxRate != out.QualityTaxRate {
			t.Errorf("params %v → TaxRate %v != QualityTaxRate %v",
				params, out.TaxRate, out.QualityTaxRate)
		}
	}
}

// TestResolveTaxRateAliasDoesNotMutateInput guards the purity of WithParameters:
// params comes from the DB JSONB and a caller may reuse the map.
func TestResolveTaxRateAliasDoesNotMutateInput(t *testing.T) {
	captureWarnings(t)

	params := map[string]any{KeyTaxRate: 25.0}
	base := DefaultModelConfig()
	if out := base.WithParameters(params); out.QualityTaxRate != 25.0 {
		t.Fatalf("el alias debe aplicarse: %v", out.QualityTaxRate)
	}
	if _, still := params[KeyTaxRate]; !still {
		t.Fatal("WithParameters mutó el mapa de entrada")
	}
}
