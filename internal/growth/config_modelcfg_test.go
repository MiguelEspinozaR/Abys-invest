package growth

import (
	"testing"

	"github.com/miky/abys-invest/internal/modelcfg"
)

// TestConfigFromModelConfigMatchesEnvToday documents the contract of (g):
// ConfigFromModelConfig is the ANCHOR for the full-chain replay, and because
// ModelConfig carries no growth knob yet (§28 RESERVED) it must be exactly
// ConfigFromEnv.
//
// The assertion is deliberately a lock, not a tautology: the day a growth knob
// is added to ModelConfig this test FAILS, forcing whoever adds it to decide
// whether the replay is meant to follow the parameter set (it is) and to update
// the callers accordingly, instead of leaving the replay reading the
// environment while every other step of the same chain follows the set.
func TestConfigFromModelConfigMatchesEnvToday(t *testing.T) {
	// A knob that differs from the default, so the comparison is not comparing
	// two zero-valued configs.
	t.Setenv("GROWTH_EPS_WEIGHT", "0.7")
	t.Setenv("GROWTH_MIN_RATE", "-5")

	env := ConfigFromEnv()
	mc := modelcfg.DefaultModelConfig()
	got := ConfigFromModelConfig(mc)

	if got != env {
		t.Errorf("ConfigFromModelConfig debe coincidir con ConfigFromEnv mientras no exista un knob de growth en ModelConfig:\n got %+v\nenv %+v", got, env)
	}
}

// TestConfigFromModelConfigHonoursEnvLayer proves the anchor is not a frozen
// copy of the defaults: it keeps the codes<env precedence that every engine has,
// so wiring the chain through this function changes nothing for existing users.
func TestConfigFromModelConfigHonoursEnvLayer(t *testing.T) {
	def := DefaultConfig()
	t.Setenv("GROWTH_EPS_WEIGHT", "0.7")
	t.Setenv("GROWTH_MAX_RATE", "30")

	got := ConfigFromModelConfig(modelcfg.DefaultModelConfig())
	if got.EPSWeight != 0.7 {
		t.Errorf("GROWTH_EPS_WEIGHT=0.7 debe seguir aplicándose, got %v", got.EPSWeight)
	}
	if got.MaxRate != 30 {
		t.Errorf("GROWTH_MAX_RATE=30 debe seguir aplicándose, got %v", got.MaxRate)
	}
	if def.EPSWeight == got.EPSWeight {
		t.Error("el test no está probando nada: el default coincide con el valor de env")
	}
}
