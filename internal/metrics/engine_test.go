package metrics

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

func approx(a, b *float64, tol float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return math.Abs(*a-*b) <= tol
}

// --- Fórmulas individuales (§13) ---

func TestCalcEPS(t *testing.T) {
	// net_earnings=100B, shares=15B -> EPS≈6.6667
	got := CalcEPS(f64(100e9), f64(15e9))
	if !approx(got, f64(6.6667), 0.001) {
		t.Fatalf("EPS: got %v, want ~6.6667", got)
	}
	// shares NULL -> nil
	if got := CalcEPS(f64(100e9), nil); got != nil {
		t.Fatalf("EPS con shares NULL debe ser nil, got %v", got)
	}
	// shares 0 -> nil (denominador no válido)
	if got := CalcEPS(f64(100e9), f64(0)); got != nil {
		t.Fatalf("EPS con shares 0 debe ser nil, got %v", got)
	}
	// net earnings negativo es válido (pérdidas): EPS negativo.
	if got := CalcEPS(f64(-10e9), f64(15e9)); got == nil || *got >= 0 {
		t.Fatalf("EPS con pérdidas debe ser negativo, got %v", got)
	}
}

func TestCalcPE(t *testing.T) {
	// price=175, EPS≈6.6667 -> PE≈26.25
	got := CalcPE(f64(175), f64(100.0/15))
	if !approx(got, f64(26.25), 0.001) {
		t.Fatalf("PE: got %v, want ~26.25", got)
	}
	if got := CalcPE(f64(175), nil); got != nil {
		t.Fatalf("PE con EPS NULL debe ser nil, got %v", got)
	}
	if got := CalcPE(f64(175), f64(0)); got != nil {
		t.Fatalf("PE con EPS 0 debe ser nil, got %v", got)
	}
}

func TestCalcPB(t *testing.T) {
	// market_cap=2.625e12, equity=62e9 -> PB≈42.34
	got := CalcPB(2.625e12, f64(62e9))
	if !approx(got, f64(42.34), 0.01) {
		t.Fatalf("PB: got %v, want ~42.34", got)
	}
	if got := CalcPB(2.625e12, nil); got != nil {
		t.Fatalf("PB con equity NULL debe ser nil, got %v", got)
	}
	if got := CalcPB(2.625e12, f64(0)); got != nil {
		t.Fatalf("PB con equity 0 debe ser nil, got %v", got)
	}
	// market_cap <= 0 no tiene sentido
	if got := CalcPB(0, f64(62e9)); got != nil {
		t.Fatalf("PB con market_cap 0 debe ser nil, got %v", got)
	}
}

func TestCalcPCF(t *testing.T) {
	got := CalcPCF(2.625e12, f64(110e9))
	if !approx(got, f64(23.86), 0.01) {
		t.Fatalf("PCF: got %v, want ~23.86", got)
	}
	if got := CalcPCF(2.625e12, nil); got != nil {
		t.Fatalf("PCF con FCF NULL debe ser nil, got %v", got)
	}
}

func TestCalcPEG(t *testing.T) {
	got := CalcPEG(f64(26.25), f64(7))
	if !approx(got, f64(3.75), 0.001) {
		t.Fatalf("PEG: got %v, want 3.75", got)
	}
	if got := CalcPEG(f64(26.25), f64(0)); got != nil {
		t.Fatalf("PEG con g=0 debe ser nil, got %v", got)
	}
	if got := CalcPEG(nil, f64(7)); got != nil {
		t.Fatalf("PEG con PE NULL debe ser nil, got %v", got)
	}
}

func TestCalcROE(t *testing.T) {
	got := CalcROE(f64(100e9), f64(62e9))
	if !approx(got, f64(1.61), 0.01) {
		t.Fatalf("ROE: got %v, want ~1.61", got)
	}
	if got := CalcROE(f64(100e9), nil); got != nil {
		t.Fatalf("ROE con equity NULL debe ser nil, got %v", got)
	}
}

func TestCalcDE(t *testing.T) {
	got := CalcDE(f64(290e9), f64(62e9))
	if !approx(got, f64(4.68), 0.01) {
		t.Fatalf("D/E: got %v, want ~4.68", got)
	}
	if got := CalcDE(f64(290e9), nil); got != nil {
		t.Fatalf("D/E con equity NULL debe ser nil, got %v", got)
	}
}

func TestCalcFCFYield(t *testing.T) {
	got := CalcFCFYield(f64(110e9), 2.625e12)
	if !approx(got, f64(4.19), 0.01) {
		t.Fatalf("FCF Yield: got %v, want ~4.19%%", got)
	}
	if got := CalcFCFYield(nil, 2.625e12); got != nil {
		t.Fatalf("FCF Yield con FCF NULL debe ser nil, got %v", got)
	}
}

// --- Motor (engine) ---

// aaplInput builds the canonical AAPL-like input from fixtures.
func aaplInput(t *testing.T) MetricInput {
	t.Helper()
	var raw struct {
		SecurityID         int64     `json:"security_id"`
		Ticker             string    `json:"ticker"`
		AsOf               time.Time `json:"as_of"`
		NetEarnings        *float64  `json:"net_earnings"`
		SharesOutstanding  *float64  `json:"shares_outstanding"`
		Price              float64   `json:"price"`
		ShareholdersEquity *float64  `json:"shareholders_equity"`
		TotalLiabilities   *float64  `json:"total_liabilities"`
		FreeCashFlow       *float64  `json:"free_cash_flow"`
		GrowthRate         float64   `json:"growth_rate"`
	}
	data, err := os.ReadFile("fixtures/aapl_metrics_input.json")
	if err != nil {
		t.Fatalf("leer fixture: %v", err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parsear fixture: %v", err)
	}
	return MetricInput{
		SecurityID: raw.SecurityID, Ticker: raw.Ticker, AsOf: raw.AsOf,
		NetEarnings: raw.NetEarnings, SharesOutstanding: raw.SharesOutstanding,
		Price: raw.Price, ShareholdersEquity: raw.ShareholdersEquity,
		TotalLiabilities: raw.TotalLiabilities, FreeCashFlow: raw.FreeCashFlow,
		GrowthRate: raw.GrowthRate,
	}
}

func TestCalculateMetrics(t *testing.T) {
	input := aaplInput(t)
	results := CalculateMetrics(input)
	if len(results) != 8 {
		t.Fatalf("se esperaban 8 métricas, hay %d", len(results))
	}

	expected := map[string]float64{}
	raw, err := os.ReadFile("fixtures/aapl_metrics_expected.json")
	if err != nil {
		t.Fatalf("leer expected: %v", err)
	}
	var exp struct {
		Metrics map[string]float64 `json:"metrics"`
	}
	if err := json.Unmarshal(raw, &exp); err != nil {
		t.Fatalf("parsear expected: %v", err)
	}
	expected = exp.Metrics

	byName := map[string]*float64{}
	for _, r := range results {
		byName[r.Metric] = r.Value
		if len(r.InputsSnapshot) == 0 {
			t.Fatalf("métrica %s sin inputs_snapshot", r.Metric)
		}
		// snapshot completo: todos los inputs presentes
		for _, k := range []string{"ticker", "price", "market_cap", "net_earnings", "shares_outstanding", "shareholders_equity", "total_liabilities", "free_cash_flow"} {
			if _, ok := r.InputsSnapshot[k]; !ok {
				t.Fatalf("métrica %s: snapshot sin clave %s", r.Metric, k)
			}
		}
	}

	if len(results) == 8 {
		for name, want := range expected {
			got, ok := byName[name]
			if !ok {
				t.Fatalf("falta métrica %s en los resultados", name)
			}
			if !approx(got, &want, 0.001) {
				t.Fatalf("%s: got %v, want %v", name, got, want)
			}
		}
	}
}

func TestCalculateMetricsMissingInputs(t *testing.T) {
	input := aaplInput(t)
	input.SharesOutstanding = nil
	input.ShareholdersEquity = nil
	input.FreeCashFlow = nil
	input.TotalLiabilities = nil

	results := CalculateMetrics(input)
	byName := map[string]*float64{}
	for _, r := range results {
		byName[r.Metric] = r.Value
	}
	// Sin shares: no hay market_cap -> P/B, P/FCF, FCF Yield nil; EPS nil -> PE nil.
	for _, name := range []string{"eps", "pe_ratio", "pb_ratio", "pcf_ratio", "peg_ratio", "fcf_yield"} {
		if v := byName[name]; v != nil {
			t.Fatalf("%s con inputs faltantes debe ser nil, got %v", name, v)
		}
	}
	// ROE y D/E siguen nil (equity NULL). No debe haber panics.
	if v := byName["roe"]; v != nil {
		t.Fatalf("roe con equity NULL debe ser nil, got %v", v)
	}
	if v := byName["de_ratio"]; v != nil {
		t.Fatalf("de_ratio con equity NULL debe ser nil, got %v", v)
	}
}

func TestDeterminism(t *testing.T) {
	input := aaplInput(t)
	first := CalculateMetrics(input)
	for i := 0; i < 1000; i++ {
		next := CalculateMetrics(input)
		if len(next) != len(first) {
			t.Fatalf("iteración %d: cantidad de métricas cambió", i)
		}
		for j := range first {
			if first[j].Metric != next[j].Metric {
				t.Fatalf("iteración %d: orden no determinista: %s vs %s", i, first[j].Metric, next[j].Metric)
			}
			if !approx(first[j].Value, next[j].Value, 0) {
				t.Fatalf("iteración %d: valor no determinista en %s", i, first[j].Metric)
			}
		}
	}
}

func TestGrowthRateDefault(t *testing.T) {
	input := aaplInput(t)
	input.GrowthRate = 0 // debe caer al default 7

	results := CalculateMetrics(input)
	for _, r := range results {
		if r.Metric == "peg_ratio" {
			if v := r.InputsSnapshot["growth_rate"]; v != float64(7) {
				t.Fatalf("growth_rate default esperado 7, got %v", v)
			}
		}
	}
}

func TestBuildDerivedMetrics(t *testing.T) {
	input := aaplInput(t)
	rows, err := BuildDerivedMetrics(input, "")
	if err != nil {
		t.Fatalf("BuildDerivedMetrics: %v", err)
	}
	if len(rows) != 8 {
		t.Fatalf("se esperaban 8 filas, hay %d", len(rows))
	}
	for _, m := range rows {
		if m.SecurityID != input.SecurityID {
			t.Fatalf("security_id incorrecto: %d", m.SecurityID)
		}
		if m.ModelVersion != DefaultModelVersion {
			t.Fatalf("model_version por defecto incorrecto: %q", m.ModelVersion)
		}
		if len(m.InputsSnapshot) == 0 {
			t.Fatalf("snapshot vacío para %s", m.Metric)
		}
		// El snapshot debe ser JSON válido
		var snap map[string]any
		if err := json.Unmarshal(m.InputsSnapshot, &snap); err != nil {
			t.Fatalf("inputs_snapshot de %s no es JSON válido: %v", m.Metric, err)
		}
	}
	// as_of propagado
	if !rows[0].AsOf.Equal(input.AsOf) {
		t.Fatalf("as_of no propagado: %v vs %v", rows[0].AsOf, input.AsOf)
	}
}
