package wacc

import (
	"os"
	"strconv"
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
//	WACC_RISK_FREE_RATE      4.5  Rf %
//	WACC_EQUITY_RISK_PREMIUM 5.5  ERP %
//	WACC_BETA_ASSUMED        1.0  β usada SOLO si securities.beta está vacía (D17)
//	WACC_COST_OF_DEBT        6.0  Kd pre-tax %
//	WACC_TAX_RATE            21   impuesto % (recortado a [0,50])
//	WACC_FALLBACK            9.0  WACC global de fallback % (<= 0 -> nil)
func ConfigFromEnv() Config {
	cfg := DefaultConfig()
	cfg.RiskFreeRate = envFloat("WACC_RISK_FREE_RATE", cfg.RiskFreeRate)
	cfg.EquityRiskPremium = envFloat("WACC_EQUITY_RISK_PREMIUM", cfg.EquityRiskPremium)
	cfg.BetaAssumed = envFloat("WACC_BETA_ASSUMED", cfg.BetaAssumed)
	cfg.CostOfDebt = envFloat("WACC_COST_OF_DEBT", cfg.CostOfDebt)
	cfg.TaxRate = envFloat("WACC_TAX_RATE", cfg.TaxRate)
	cfg.Fallback = envFloat("WACC_FALLBACK", cfg.Fallback)
	return cfg
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}
