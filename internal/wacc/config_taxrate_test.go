package wacc

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/miky/abys-invest/internal/modelcfg"
)

func modelcfgDefaultWithTax(tax float64) modelcfg.ModelConfig {
	mc := modelcfg.DefaultModelConfig()
	mc.QualityTaxRate = tax
	mc.TaxRate = tax
	return mc
}

func captureWarnings(t *testing.T) *strings.Builder {
	t.Helper()
	var sb strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&sb, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &sb
}

// TestConfigFromEnvWarnsDeprecatedTaxRate is the CA of (f): calling the engine
// config DIRECTLY with the deprecated WACC_TAX_RATE must say so.
//
// Why it matters: internal/wacc.ConfigFromEnv is a real entry point (the engine
// is usable on its own), and before this warning it read WACC_TAX_RATE in
// silence while internal/quality read QUALITY_TAX_RATE in silence. Two
// independent engines, two tax rates, no message — the operator had no way to
// learn that the knob they configured was not the knob the other engine used.
func TestConfigFromEnvWarnsDeprecatedTaxRate(t *testing.T) {
	sb := captureWarnings(t)
	t.Setenv("WACC_TAX_RATE", "25")

	cfg := ConfigFromEnv()
	// Compat: the value is still honoured (documented fallback).
	if cfg.TaxRate != 25 {
		t.Errorf("el valor deprecado sigue aplicándose (compat), TaxRate = %v", cfg.TaxRate)
	}
	if !strings.Contains(sb.String(), "WACC_TAX_RATE") ||
		!strings.Contains(sb.String(), "QUALITY_TAX_RATE") {
		t.Errorf("se esperaba el aviso de deprecación nombrando ambas variables, log: %q", sb.String())
	}
}

// TestConfigFromEnvTaxRateConflictSaysWhoWins: with both defined, the warning must
// say which one has priority, so the operator is not left guessing.
func TestConfigFromEnvTaxRateConflictSaysWhoWins(t *testing.T) {
	sb := captureWarnings(t)
	t.Setenv("WACC_TAX_RATE", "25")
	t.Setenv("QUALITY_TAX_RATE", "18")

	cfg := ConfigFromEnv()
	if cfg.TaxRate != 25 {
		t.Errorf("compat: este lector sigue usando WACC_TAX_RATE, = %v", cfg.TaxRate)
	}
	if !strings.Contains(sb.String(), "manda") {
		t.Errorf("el aviso de conflicto debe decir que QUALITY_TAX_RATE manda, log: %q", sb.String())
	}
}

// TestConfigFromEnvNoWarningWhenCanonical: the canonical QUALITY_TAX_RATE alone
// must be silent — this engine never read it directly, so warning would be noise.
func TestConfigFromEnvNoWarningWhenOnlyQualityTaxRate(t *testing.T) {
	sb := captureWarnings(t)
	t.Setenv("QUALITY_TAX_RATE", "18")

	if cfg := ConfigFromEnv(); cfg.TaxRate != DefaultTaxRate {
		t.Errorf("sin WACC_TAX_RATE se conserva el default del engine, = %v", cfg.TaxRate)
	}
	if strings.Contains(sb.String(), "WACC_TAX_RATE deprecated") {
		t.Errorf("sin la variable deprecada no debe haber aviso de deprecación, log: %q", sb.String())
	}
}

// TestConfigFromEnvTaxRateInvalidStillFallsBack: the warning must not have
// replaced the validation — a NaN/invalid WACC_TAX_RATE is still refused in
// favour of the default (a NaN tax rate would poison every WACC).
func TestConfigFromEnvTaxRateInvalidStillFallsBack(t *testing.T) {
	captureWarnings(t)
	t.Setenv("WACC_TAX_RATE", "NaN")

	if cfg := ConfigFromEnv(); cfg.TaxRate != DefaultTaxRate {
		t.Errorf("WACC_TAX_RATE=NaN → default %v, got %v", DefaultTaxRate, cfg.TaxRate)
	}
}

// TestConfigFromModelConfigTaxRatePrecedence pins the precedence between the two
// paths: a parameter set (via ModelConfig) overrides both env and the deprecated
// direct read, which is what makes the set-aware chain (item g) consistent.
func TestConfigFromModelConfigTaxRatePrecedence(t *testing.T) {
	captureWarnings(t)
	t.Setenv("WACC_TAX_RATE", "25")

	mc := modelcfgDefaultWithTax(20)
	cfg := ConfigFromModelConfig(mc)
	if cfg.TaxRate != 20 {
		t.Errorf("el ModelConfig (parameter set) manda sobre el env: TaxRate = %v", cfg.TaxRate)
	}
}
