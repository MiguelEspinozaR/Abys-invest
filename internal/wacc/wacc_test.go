package wacc

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }

func approx(t *testing.T, got *float64, want, tol float64, label string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: se esperaba %v, got nil", label, want)
	}
	if math.Abs(*got-want) > tol {
		t.Fatalf("%s: esperado %v, got %v (tol %v)", label, want, *got, tol)
	}
}

// base es el caso normal de M6a: E y D observados, beta observada de Yahoo y
// Rf/ERP/Kd/tax de configuración (no hay proveedor para ellos, D7).
func base() Inputs {
	return Inputs{
		Ticker:      "AAPL",
		EquityValue: f(100),
		DebtValue:   f(40),
		Beta:        f(1.2),
	}
}

func TestWACCCAPMHybridConBetaObservada(t *testing.T) {
	cfg := DefaultConfig()
	res := Calculate(base(), cfg)

	// Ke = Rf + beta x ERP = 4.5 + 1.2 x 5.5 = 11.1
	// Kd_at = Kd x (1 - tax/100) = 6 x 0.79 = 4.74
	// wE = 100/140, wD = 40/140
	// WACC = 0.714286 x 11.1 + 0.285714 x 4.74 = 9.282857
	approx(t, res.Ke, 11.1, 1e-9, "cost of equity")
	approx(t, res.KdAfterTax, 4.74, 1e-9, "cost of debt after tax")
	approx(t, res.WeightEquity, 100.0/140.0, 1e-9, "weight equity")
	approx(t, res.WeightDebt, 40.0/140.0, 1e-9, "weight debt")
	approx(t, res.WACC, 9.2828571428, 1e-6, "wacc")

	if res.Source != SourceCAPMHybrid {
		t.Fatalf("source: esperado %q, got %q", SourceCAPMHybrid, res.Source)
	}
	if res.Confidence != ConfidenceMedium {
		t.Fatalf("confidence: esperado %q, got %q", ConfidenceMedium, res.Confidence)
	}
	if !res.BetaObserved {
		t.Fatal("BetaObserved debe ser true")
	}
	approx(t, res.Beta, 1.2, 1e-9, "beta usada")
	if res.ModelVersion != ModelVersion {
		t.Fatalf("model_version: %q", res.ModelVersion)
	}
	// Los parámetros de configuración quedan registrados como resueltos, con su
	// procedencia: la fila puede auditar de dónde salió cada número.
	approx(t, res.RiskFreeRate, cfg.RiskFreeRate, 1e-9, "rf resuelto")
	approx(t, res.EquityRiskPremium, cfg.EquityRiskPremium, 1e-9, "erp resuelto")
	approx(t, res.CostOfDebt, cfg.CostOfDebt, 1e-9, "kd resuelto")
	approx(t, res.TaxRate, cfg.TaxRate, 1e-9, "tax resuelto")
}

// TestWACCCAPMIndividualTodoObservado fija el techo de la taxonomía (aún no
// alcanzable en producción, D7): con Rf/ERP/Kd/tax observados la fila sale
// capm_individual/high y el WACC es idéntico.
func TestWACCCAPMIndividualTodoObservado(t *testing.T) {
	cfg := DefaultConfig()
	in := base()
	in.RiskFreeRate, in.EquityRiskPremium = f(4.5), f(5.5)
	in.CostOfDebt, in.TaxRate = f(6.0), f(21.0)

	hybrid := Calculate(base(), cfg)
	res := Calculate(in, cfg)
	if res.Source != SourceCAPMIndividual {
		t.Fatalf("source: esperado %q, got %q", SourceCAPMIndividual, res.Source)
	}
	if res.Confidence != ConfidenceHigh {
		t.Fatalf("confidence: esperado %q, got %q", ConfidenceHigh, res.Confidence)
	}
	if res.WACC == nil || hybrid.WACC == nil || math.Abs(*res.WACC-*hybrid.WACC) > 1e-12 {
		t.Fatalf("el WACC debe ser idéntico: %v vs %v", res.WACC, hybrid.WACC)
	}
}

func TestWACCBetaNoObservadaCaeAFallback(t *testing.T) {
	cfg := DefaultConfig()
	for name, beta := range map[string]*float64{"nil": nil, "cero": f(0), "negativa": f(-1), "NaN": f(math.NaN())} {
		t.Run(name, func(t *testing.T) {
			in := base()
			in.Beta = beta
			res := Calculate(in, cfg)
			approx(t, res.WACC, cfg.Fallback, 1e-9, "wacc = fallback")
			if res.Source != SourceConfiguredFallback || res.Confidence != ConfidenceLow {
				t.Fatalf("taxonomía: %q/%q", res.Source, res.Confidence)
			}
			if res.BetaObserved {
				t.Fatal("BetaObserved debe ser false")
			}
			// La beta asumida se conserva como evidencia del POR QUÉ degrada,
			// pero no se usa en el WACC.
			approx(t, res.Beta, cfg.BetaAssumed, 1e-9, "beta asumida")
			if res.WeightEquity != nil {
				t.Fatal("sin CAPM no debe haber pesos inventados")
			}
		})
	}

	// Fallback <= 0 → nil en vez de un número inventado.
	cfgZero := DefaultConfig()
	cfgZero.Fallback = 0
	in := base()
	in.Beta = nil
	if res := Calculate(in, cfgZero); res.WACC != nil {
		t.Fatalf("con Fallback=0 se esperaba WACC nil, got %v", *res.WACC)
	}
}

func TestWACCFallbackSinDeuda(t *testing.T) {
	cfg := DefaultConfig()
	in := base()
	in.DebtValue = nil
	res := Calculate(in, cfg)
	approx(t, res.WACC, cfg.Fallback, 1e-9, "wacc = fallback")
	if res.Source != SourceConfiguredFallback || res.Confidence != ConfidenceLow {
		t.Fatalf("taxonomía: %q/%q", res.Source, res.Confidence)
	}
	// La beta observada sigue siendo observada: lo que falta es la deuda.
	if !res.BetaObserved {
		t.Fatal("BetaObserved debe seguir siendo true")
	}
}

func TestWACCAllEquity(t *testing.T) {
	cfg := DefaultConfig()
	in := base()
	in.DebtValue = f(0)
	res := Calculate(in, cfg)
	approx(t, res.WeightDebt, 0, 1e-12, "wD")
	approx(t, res.WeightEquity, 1, 1e-12, "wE")
	approx(t, res.WACC, *res.Ke, 1e-12, "WACC = Ke")
	if res.Source != SourceCAPMHybrid || res.Confidence != ConfidenceMedium {
		t.Fatalf("una estructura all-equity OBSERVADA no degrada la taxonomía: %q/%q", res.Source, res.Confidence)
	}
}

func TestWACCRecortaTaxRate(t *testing.T) {
	in := base()
	in.TaxRate = f(80) // error de unidad en un hecho XBRL, no un tipo impositivo
	res := Calculate(in, DefaultConfig())
	approx(t, res.TaxRate, MaxTaxRate, 1e-9, "tax recortada")
	approx(t, res.KdAfterTax, 6*0.5, 1e-9, "kd_at con tax 50")

	in.TaxRate = f(-5)
	res = Calculate(in, DefaultConfig())
	approx(t, res.TaxRate, 0, 1e-9, "tax negativa → 0")
	approx(t, res.KdAfterTax, 6, 1e-9, "kd_at sin impuesto")

	// El recorte también se aplica al valor de Config.
	cfgHigh := DefaultConfig()
	cfgHigh.TaxRate = 99
	res = Calculate(base(), cfgHigh)
	approx(t, res.TaxRate, MaxTaxRate, 1e-9, "tax de config recortada")
}

func TestWACCEntradasInvalidas(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Inputs, *Config)
	}{
		{"Ke <= 0 por ERP negativo extremo", func(in *Inputs, _ *Config) { in.EquityRiskPremium = f(-100) }},
		{"Rf negativo que hace Ke <= 0", func(in *Inputs, _ *Config) { in.RiskFreeRate = f(-100) }},
		{"E = 0", func(in *Inputs, _ *Config) { in.EquityValue = f(0) }},
		{"E = nil", func(in *Inputs, _ *Config) { in.EquityValue = nil }},
		{"E + D <= 0", func(in *Inputs, _ *Config) { in.EquityValue, in.DebtValue = f(40), f(-80) }},
		{"E no finito", func(in *Inputs, _ *Config) { in.EquityValue = f(math.Inf(1)) }},
		{"Kd no finito en config", func(_ *Inputs, c *Config) { c.CostOfDebt = math.NaN() }},
		{"Rf no finito en config", func(_ *Inputs, c *Config) { c.RiskFreeRate = math.Inf(1) }},
		{"beta observada no finita", func(in *Inputs, _ *Config) { in.Beta = f(math.Inf(1)) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, c := base(), DefaultConfig()
			tt.mut(&in, &c)
			res := Calculate(in, c) // nunca debe hacer panic
			if res.WACC != nil && (math.IsNaN(*res.WACC) || math.IsInf(*res.WACC, 0)) {
				t.Fatalf("WACC no finito: %v", *res.WACC)
			}
			if res.WACC == nil {
				return // nil es una salida válida (nada inventado)
			}
			// Lo que se devuelve es el fallback marcado, nunca un NaN disfrazado.
			if res.Source != SourceConfiguredFallback {
				t.Fatalf("con entradas inválidas la fuente debe degradar, got %q (wacc %v)", res.Source, *res.WACC)
			}
			approx(t, res.WACC, c.Fallback, 1e-9, "wacc = fallback")
		})
	}
}

func TestWACCEsDeterminista(t *testing.T) {
	cfg := DefaultConfig()
	in := base()
	in.AsOf = in.AsOf.Add(0)
	r1, r2 := Calculate(in, cfg), Calculate(in, cfg)
	if !reflect.DeepEqual(r1, r2) {
		t.Fatalf("Calculate no determinista:\n%+v\n%+v", r1, r2)
	}
	s1, err1 := r1.Snapshot(in, cfg)
	s2, err2 := r2.Snapshot(in, cfg)
	if err1 != nil || err2 != nil {
		t.Fatalf("snapshot: %v / %v", err1, err2)
	}
	if !reflect.DeepEqual(s1, s2) {
		t.Fatal("Snapshot no determinista")
	}
}

func TestWACCConfigFromEnv(t *testing.T) {
	def := DefaultConfig()
	if got := ConfigFromEnv(); got != def {
		t.Fatalf("sin env debe devolver los defaults: %+v vs %+v", got, def)
	}
	t.Setenv("WACC_RISK_FREE_RATE", "3.75")
	t.Setenv("WACC_EQUITY_RISK_PREMIUM", "6")
	t.Setenv("WACC_BETA_ASSUMED", "1.2")
	t.Setenv("WACC_COST_OF_DEBT", "5.5")
	t.Setenv("WACC_TAX_RATE", "25")
	t.Setenv("WACC_FALLBACK", "8")
	cfg := ConfigFromEnv()
	if cfg.RiskFreeRate != 3.75 || cfg.EquityRiskPremium != 6 || cfg.BetaAssumed != 1.2 ||
		cfg.CostOfDebt != 5.5 || cfg.TaxRate != 25 || cfg.Fallback != 8 {
		t.Fatalf("overrides no aplicados: %+v", cfg)
	}
	// Env inválida → default (nunca 0 silencioso ni NaN).
	t.Setenv("WACC_RISK_FREE_RATE", "no-numero")
	t.Setenv("WACC_FALLBACK", "")
	if cfg := ConfigFromEnv(); cfg.RiskFreeRate != def.RiskFreeRate || cfg.Fallback != def.Fallback {
		t.Fatalf("env inválida debe conservar los defaults: %+v", cfg)
	}
}

// TestParseSnapshotModelVersionGate verifies the model_version gate rejects
// empty versions, unknown versions and accepts the supported one.
func TestParseSnapshotModelVersionGate(t *testing.T) {
	validSnap := `{"inputs":{"ticker":"AAPL","equity_value":100,"debt_value":40,"beta":1.2},"config":{},"result":{"model_version":"1.0.0"},"model_version":"1.0.0"}`

	// Correct version → OK
	_, _, _, err := ParseSnapshot([]byte(validSnap))
	if err != nil {
		t.Fatalf("versión soportada debe parsear: %v", err)
	}

	// Empty version → ErrSnapshotIncomplete
	emptyVer := `{"inputs":{"ticker":"AAPL","equity_value":100,"debt_value":40,"beta":1.2},"config":{},"result":{},"model_version":""}`
	_, _, _, err = ParseSnapshot([]byte(emptyVer))
	if err == nil || !strings.Contains(err.Error(), "falta model_version") {
		t.Fatalf("versión vacía debe dar ErrSnapshotIncomplete, got: %v", err)
	}

	// Unsupported version → ErrUnsupportedModelVersion
	unsupported := `{"inputs":{"ticker":"AAPL","equity_value":100,"debt_value":40,"beta":1.2},"config":{},"result":{},"model_version":"9.9.9"}`
	_, _, _, err = ParseSnapshot([]byte(unsupported))
	if err == nil || !strings.Contains(err.Error(), "no soportada") {
		t.Fatalf("versión no soportada debe dar ErrUnsupportedModelVersion, got: %v", err)
	}

	// Empty snapshot → ErrSnapshotIncomplete
	_, _, _, err = ParseSnapshot([]byte{})
	if err == nil || !strings.Contains(err.Error(), "snapshot vacío") {
		t.Fatalf("snapshot vacío debe dar ErrSnapshotIncomplete, got: %v", err)
	}

	// Missing ticker → ErrSnapshotIncomplete
	noTicker := `{"inputs":{"equity_value":100,"debt_value":40,"beta":1.2},"config":{},"result":{"model_version":"1.0.0"},"model_version":"1.0.0"}`
	_, _, _, err = ParseSnapshot([]byte(noTicker))
	if err == nil || !strings.Contains(err.Error(), "falta ticker") {
		t.Fatalf("falta ticker debe dar ErrSnapshotIncomplete, got: %v", err)
	}
}

// TestParseSnapshotRoundTrip verifies that Snapshot() → ParseSnapshot() preserves
// Inputs, Config and Result with float tolerance (determinism §27).
func TestParseSnapshotRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RiskFreeRate = 4.25
	cfg.EquityRiskPremium = 5.75
	cfg.BetaAssumed = 1.15
	cfg.CostOfDebt = 6.5
	cfg.TaxRate = 23.0
	cfg.Fallback = 8.5

	in := Inputs{
		Ticker:      "AAPL",
		AsOf:        time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC),
		EquityValue: f(100e9),
		DebtValue:   f(40e9),
		Beta:        f(1.25),
		BetaAsOf:    ptrTime(time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)),
		BetaSource:  BetaSourceHistory,
		Reasons:     []string{"beta_history"},
	}

	res := Calculate(in, cfg)
	snap, err := res.Snapshot(in, cfg)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	parsedIn, parsedCfg, parsedRes, err := ParseSnapshot(snap)
	if err != nil {
		t.Fatalf("ParseSnapshot: %v", err)
	}

	// Verify Inputs
	if parsedIn.Ticker != in.Ticker {
		t.Errorf("Ticker: got %q, want %q", parsedIn.Ticker, in.Ticker)
	}
	if !parsedIn.AsOf.Equal(in.AsOf) {
		t.Errorf("AsOf: got %v, want %v", parsedIn.AsOf, in.AsOf)
	}
	if (parsedIn.EquityValue == nil) != (in.EquityValue == nil) {
		t.Errorf("EquityValue nil mismatch")
	}
	if parsedIn.EquityValue != nil && in.EquityValue != nil && math.Abs(*parsedIn.EquityValue-*in.EquityValue) > 1e-6 {
		t.Errorf("EquityValue: got %v, want %v", *parsedIn.EquityValue, *in.EquityValue)
	}
	if (parsedIn.DebtValue == nil) != (in.DebtValue == nil) {
		t.Errorf("DebtValue nil mismatch")
	}
	if parsedIn.DebtValue != nil && in.DebtValue != nil && math.Abs(*parsedIn.DebtValue-*in.DebtValue) > 1e-6 {
		t.Errorf("DebtValue: got %v, want %v", *parsedIn.DebtValue, *in.DebtValue)
	}
	if (parsedIn.Beta == nil) != (in.Beta == nil) {
		t.Errorf("Beta nil mismatch")
	}
	if parsedIn.Beta != nil && in.Beta != nil && math.Abs(*parsedIn.Beta-*in.Beta) > 1e-6 {
		t.Errorf("Beta: got %v, want %v", *parsedIn.Beta, *in.Beta)
	}
	if parsedIn.BetaSource != in.BetaSource {
		t.Errorf("BetaSource: got %q, want %q", parsedIn.BetaSource, in.BetaSource)
	}

	// Verify Config
	if math.Abs(parsedCfg.RiskFreeRate-cfg.RiskFreeRate) > 1e-9 {
		t.Errorf("RiskFreeRate: got %v, want %v", parsedCfg.RiskFreeRate, cfg.RiskFreeRate)
	}
	if math.Abs(parsedCfg.EquityRiskPremium-cfg.EquityRiskPremium) > 1e-9 {
		t.Errorf("EquityRiskPremium: got %v, want %v", parsedCfg.EquityRiskPremium, cfg.EquityRiskPremium)
	}
	if math.Abs(parsedCfg.BetaAssumed-cfg.BetaAssumed) > 1e-9 {
		t.Errorf("BetaAssumed: got %v, want %v", parsedCfg.BetaAssumed, cfg.BetaAssumed)
	}
	if math.Abs(parsedCfg.CostOfDebt-cfg.CostOfDebt) > 1e-9 {
		t.Errorf("CostOfDebt: got %v, want %v", parsedCfg.CostOfDebt, cfg.CostOfDebt)
	}
	if math.Abs(parsedCfg.TaxRate-cfg.TaxRate) > 1e-9 {
		t.Errorf("TaxRate: got %v, want %v", parsedCfg.TaxRate, cfg.TaxRate)
	}
	if math.Abs(parsedCfg.Fallback-cfg.Fallback) > 1e-9 {
		t.Errorf("Fallback: got %v, want %v", parsedCfg.Fallback, cfg.Fallback)
	}

	// Verify Result
	if (parsedRes.WACC == nil) != (res.WACC == nil) {
		t.Errorf("WACC nil mismatch")
	}
	if parsedRes.WACC != nil && res.WACC != nil && math.Abs(*parsedRes.WACC-*res.WACC) > 1e-6 {
		t.Errorf("WACC: got %v, want %v", *parsedRes.WACC, *res.WACC)
	}
	if parsedRes.Source != res.Source {
		t.Errorf("Source: got %q, want %q", parsedRes.Source, res.Source)
	}
	if parsedRes.Confidence != res.Confidence {
		t.Errorf("Confidence: got %q, want %q", parsedRes.Confidence, res.Confidence)
	}
	if parsedRes.ModelVersion != res.ModelVersion {
		t.Errorf("ModelVersion: got %q, want %q", parsedRes.ModelVersion, res.ModelVersion)
	}
	if parsedRes.BetaObserved != res.BetaObserved {
		t.Errorf("BetaObserved: got %v, want %v", parsedRes.BetaObserved, res.BetaObserved)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
