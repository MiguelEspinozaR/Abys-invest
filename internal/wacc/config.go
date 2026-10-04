package wacc

import (
	"log/slog"
	"os"

	"github.com/miky/abys-invest/internal/modelcfg"
)

// Defaults of the WACC engine (SPEC §7, plan D7/D14). All rates are
// PERCENTAGES. They live in Config and not in the formulas so that M6c's
// ModelConfig (§24) can move them to parameter sets without touching the math.
const (
	DefaultRiskFreeRate      = 4.5
	DefaultEquityRiskPremium = 5.5
	DefaultBetaAssumed       = 1.0
	DefaultCostOfDebt        = 6.0
	DefaultTaxRate           = 21.0
	DefaultFallback          = 9.0
)

// DefaultConfig is the deterministic default configuration.
func DefaultConfig() Config {
	return Config{
		RiskFreeRate:      DefaultRiskFreeRate,
		EquityRiskPremium: DefaultEquityRiskPremium,
		BetaAssumed:       DefaultBetaAssumed,
		CostOfDebt:        DefaultCostOfDebt,
		TaxRate:           DefaultTaxRate,
		Fallback:          DefaultFallback,
	}
}

// ConfigFromEnv reads the WACC_* parameters on top of DefaultConfig. Missing
// or unparseable values fall back to the default (a bad env var must not break
// the pipeline). It is the only function of the package that reads the
// environment; Calculate stays pure (§27).
//
// Canonical env names (P1-3):
//
//	WACC_RISK_FREE           4.5  Rf % (alias: WACC_RISK_FREE_RATE)
//	WACC_EQUITY_RISK_PREMIUM 5.5  ERP %
//	WACC_COST_OF_DEBT_SPREAD 1.5  Kd spread over Rf % (alias: WACC_COST_OF_DEBT as absolute Kd)
//	WACC_TAX_RATE            21   impuesto % (recortado a [0,50])
//	WACC_BETA_ASSUMED        1.0  β usada SOLO si no hay beta observada (D17)
//	WACC_FALLBACK            9.0  WACC global de fallback % (<= 0 -> nil)
func ConfigFromEnv() Config {
	cfg := DefaultConfig()
	// RiskFree: canonical WACC_RISK_FREE, fallback to legacy WACC_RISK_FREE_RATE
	rf := modelcfg.EnvFloat("WACC_RISK_FREE", 0)
	if rf == 0 {
		rf = modelcfg.EnvFloat("WACC_RISK_FREE_RATE", cfg.RiskFreeRate)
	}
	cfg.RiskFreeRate = rf

	cfg.EquityRiskPremium = modelcfg.EnvFloat("WACC_EQUITY_RISK_PREMIUM", cfg.EquityRiskPremium)

	// CostOfDebt: canonical WACC_COST_OF_DEBT_SPREAD (spread over Rf), fallback to legacy WACC_COST_OF_DEBT (absolute)
	spread := modelcfg.EnvFloat("WACC_COST_OF_DEBT_SPREAD", 0)
	if spread != 0 {
		cfg.CostOfDebt = cfg.RiskFreeRate + spread
	} else {
		cfg.CostOfDebt = modelcfg.EnvFloat("WACC_COST_OF_DEBT", cfg.CostOfDebt)
	}

	// WACC_TAX_RATE is DEPRECATED (alias of QUALITY_TAX_RATE): the tax rate has
	// ONE canonical name so that NOPAT (quality/ROIC) and after-tax Kd cannot be
	// taxed at two different rates in one score. The value is still read as a
	// documented fallback (compat), but saying so out loud is the point: before
	// this warning, a deployment could set WACC_TAX_RATE, see it work, and never
	// learn that the quality block was using a different number.
	if os.Getenv("WACC_TAX_RATE") != "" {
		if os.Getenv("QUALITY_TAX_RATE") != "" {
			slog.Warn("WACC_TAX_RATE y QUALITY_TAX_RATE están ambos definidos; QUALITY_TAX_RATE manda")
		} else {
			slog.Warn("WACC_TAX_RATE deprecated, use QUALITY_TAX_RATE")
		}
	}
	cfg.TaxRate = modelcfg.EnvFloatRange("WACC_TAX_RATE", cfg.TaxRate, 0, 50)
	cfg.BetaAssumed = modelcfg.EnvFloat("WACC_BETA_ASSUMED", cfg.BetaAssumed)
	cfg.Fallback = modelcfg.EnvFloat("WACC_FALLBACK", cfg.Fallback)
	return cfg
}

// ConfigFromModelConfig builds a wacc.Config from a resolved ModelConfig.
// This is the path used by the pipeline so that parameter set overrides (e.g.
// WACC_RISK_FREE, WACC_EQUITY_RISK_PREMIUM, WACC_COST_OF_DEBT_SPREAD,
// WACC_TAX_RATE, WACC_BETA_ASSUMED from the parameter set) actually reach the
// engine. Precedence: code defaults < env < parameter set (via ModelConfig).
// Note: the ModelConfig uses CostOfDebtSpread (spread over Rf) while the engine
// uses CostOfDebt (absolute pre-tax rate). The conversion is:
// CostOfDebt = RiskFree + CostOfDebtSpread.
func ConfigFromModelConfig(mc modelcfg.ModelConfig) Config {
	// Start from env-applied config (code < env), then apply parameter set overrides from mc
	cfg := ConfigFromEnv()
	cfg.RiskFreeRate = mc.RiskFree
	cfg.EquityRiskPremium = mc.EquityRiskPremium
	cfg.CostOfDebt = mc.RiskFree + mc.CostOfDebtSpread // Kd = Rf + spread
	cfg.TaxRate = mc.TaxRate                           // WACC_TAX_RATE from ModelConfig (P1-3)
	cfg.BetaAssumed = mc.BetaAssumed                   // WACC_BETA_ASSUMED from ModelConfig (P1-3)
	// Fallback stays from env (WACC_FALLBACK)
	return cfg
}
