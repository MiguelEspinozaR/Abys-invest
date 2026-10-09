package metrics

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

// W7 de M6c-T1: fixtures AVGO (impuesto observado NEGATIVO, tasa rechazada sin
// clamp) y WMT (interés del mismo ejercicio) con el MISMO formato
// input/expected que aapl_metrics_*.json, ejercitadas con el motor 2.1.0
// (CalculateMetricsV2 + BuildDerivedMetricsV2).
//
// Delimitación con W5 (no duplicar): la DERIVACIÓN de la tasa observada
// (alignedTaxRate, rango [0, wacc.MaxTaxRate], motivo out_of_range) está cubierta
// por internal/pipeline/w5_taxrate_test.go. Lo que fija este fixture es su
// CONSECUENCIA observable en el motor de métricas: con la tasa rechazada, el
// input lleva la configurada (0.21) — ni el valor negativo ni un 0 clampado — y
// el ROIC se calcula con 0.21.

// metricsFixtureV2 is the input schema of the v2 fixtures. It mirrors
// MetricInputV2 but with tags, because MetricInputV2 itself carries no json tags
// (the production input is built in Go, not decoded). The raw tax pair
// (income_tax_expense / pretax_income) is NOT part of MetricInputV2: it travels
// in the fixture only as provenance of the normalized rate.
type metricsFixtureV2 struct {
	SecurityID         int64               `json:"security_id"`
	Ticker             string              `json:"ticker"`
	AsOf               time.Time           `json:"as_of"`
	NetEarnings        *float64            `json:"net_earnings"`
	SharesOutstanding  *float64            `json:"shares_outstanding"`
	Price              float64             `json:"price"`
	ShareholdersEquity *float64            `json:"shareholders_equity"`
	TotalLiabilities   *float64            `json:"total_liabilities"`
	FreeCashFlow       *float64            `json:"free_cash_flow"`
	OperatingIncome    *float64            `json:"operating_income"`
	Revenue            *float64            `json:"revenue"`
	TotalDebt          *float64            `json:"total_debt"`
	Cash               *float64            `json:"cash"`
	EBITDA             *float64            `json:"ebitda"`
	InterestExpense    *float64            `json:"interest_expense"`
	NormalizedTaxRate  *float64            `json:"normalized_tax_rate"`
	IncomeTaxExpense   *float64            `json:"income_tax_expense"`
	PretaxIncome       *float64            `json:"pretax_income"`
	EPSSeries          []MetricSeriesPoint `json:"eps_series"`
	FCFSeries          []MetricSeriesPoint `json:"fcf_series"`
	Provenance         map[string]any      `json:"_provenance"`
}

type metricsExpectedV2 struct {
	Metrics                 map[string]float64 `json:"metrics"`
	ModelVersion            string             `json:"model_version"`
	NormalizedTaxRate       *float64           `json:"normalized_tax_rate"`
	ObservedTaxRatePercent  *float64           `json:"observed_tax_rate_percent"`
	ObservedTaxRateRejected bool               `json:"observed_tax_rate_rejected"`
}

func loadMetricsFixture(t *testing.T, path string, out any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("leer fixture %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("parsear fixture %s: %v", path, err)
	}
}

func (f metricsFixtureV2) toInputV2() MetricInputV2 {
	return MetricInputV2{
		SecurityID:         f.SecurityID,
		Ticker:             f.Ticker,
		AsOf:               f.AsOf,
		NetEarnings:        f.NetEarnings,
		SharesOutstanding:  f.SharesOutstanding,
		Price:              f.Price,
		ShareholdersEquity: f.ShareholdersEquity,
		TotalLiabilities:   f.TotalLiabilities,
		FreeCashFlow:       f.FreeCashFlow,
		OperatingIncome:    f.OperatingIncome,
		Revenue:            f.Revenue,
		TotalDebt:          f.TotalDebt,
		Cash:               f.Cash,
		EBITDA:             f.EBITDA,
		InterestExpense:    f.InterestExpense,
		NormalizedTaxRate:  f.NormalizedTaxRate,
		EPSSeries:          f.EPSSeries,
		FCFSeries:          f.FCFSeries,
	}
}

func TestCalculateMetricsV2Fixtures(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{"avgo_impuesto_negativo", "fixtures/avgo_metrics_input.json", "fixtures/avgo_metrics_expected.json"},
		{"wmt_interes_solo", "fixtures/wmt_metrics_input.json", "fixtures/wmt_metrics_expected.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fx metricsFixtureV2
			loadMetricsFixture(t, tc.input, &fx)
			var exp metricsExpectedV2
			loadMetricsFixture(t, tc.expected, &exp)

			if fx.Provenance == nil {
				t.Fatalf("el fixture debe documentar su procedencia en _provenance")
			}
			in := fx.toInputV2()

			// Consistencia del fixture (no es la derivación de W5, que vive en
			// pipeline): si el par observado está fuera de [0,50] se RECHAZA y el
			// input NO puede llevar ni ese valor ni un 0 clampado; si está dentro,
			// la tasa normalizada ES la derivada.
			if fx.IncomeTaxExpense == nil || fx.PretaxIncome == nil || *fx.PretaxIncome == 0 {
				t.Fatalf("el fixture necesita el par income_tax_expense/pretax_income para documentar la tasa")
			}
			observed := *fx.IncomeTaxExpense / *fx.PretaxIncome * 100
			if exp.ObservedTaxRatePercent == nil || math.Abs(observed-*exp.ObservedTaxRatePercent) > 1e-9 {
				t.Fatalf("observed_tax_rate_percent: got %v want %v", observed, exp.ObservedTaxRatePercent)
			}
			inRange := observed >= 0 && observed <= 50
			if inRange == exp.ObservedTaxRateRejected {
				t.Fatalf("observed_tax_rate_rejected=%v incoherente con la tasa observada %v%%", exp.ObservedTaxRateRejected, observed)
			}
			if in.NormalizedTaxRate == nil {
				t.Fatalf("el input debe llevar una tasa normalizada")
			}
			if exp.ObservedTaxRateRejected {
				if *in.NormalizedTaxRate == 0 {
					t.Fatalf("una tasa rechazada no puede convertirse en 0 (clamp prohibido)")
				}
				if math.Abs(*in.NormalizedTaxRate-observed/100) < 1e-9 {
					t.Fatalf("la tasa rechazada %v%% pasó al motor como si fuera la derivada", observed)
				}
			} else if math.Abs(*in.NormalizedTaxRate-observed/100) > 1e-9 {
				t.Fatalf("tasa derivada: normalizada %v != observada/100 %v", *in.NormalizedTaxRate, observed/100)
			}
			if exp.NormalizedTaxRate != nil && math.Abs(*in.NormalizedTaxRate-*exp.NormalizedTaxRate) > 1e-12 {
				t.Fatalf("normalized_tax_rate del expected (%v) no coincide con el input (%v)", *exp.NormalizedTaxRate, *in.NormalizedTaxRate)
			}

			results := CalculateMetricsV2(in)
			if len(results) != 12 {
				t.Fatalf("se esperaban las 12 métricas de 2.1.0, hay %d", len(results))
			}
			byName := map[string]*float64{}
			for _, r := range results {
				byName[r.Metric] = r.Value
				if len(r.InputsSnapshot) == 0 {
					t.Fatalf("métrica %s sin inputs_snapshot", r.Metric)
				}
				if r.InputsSnapshot["model_version"] != ModelVersion21 {
					t.Fatalf("snapshot de %s sin model_version 2.1.0: %v", r.Metric, r.InputsSnapshot["model_version"])
				}
			}
			for name, want := range exp.Metrics {
				got, ok := byName[name]
				if !ok {
					t.Fatalf("falta la métrica %s en los resultados", name)
				}
				if !approx(got, &want, 1e-6) {
					t.Fatalf("%s: got %v, want %v", name, got, want)
				}
			}

			// El caso AVGO: el ROIC debe ser el calculado con 0.21, NO el que
			// saldría con una tasa clampeada a 0 (que sería mayor).
			if exp.ObservedTaxRateRejected {
				oi, eq, debt, cash := *fx.OperatingIncome, *fx.ShareholdersEquity, *fx.TotalDebt, *fx.Cash
				clamped := oi / (eq + debt - cash)
				got := byName[MetricROIC]
				if got == nil || math.Abs(*got-clamped) < 1e-9 {
					t.Fatalf("ROIC %v parece calculado con tasa 0 (clamp); el rechazo debía dejar 0.21", got)
				}
			}

			// BuildDerivedMetricsV2: 12 filas, versión correcta y snapshot JSON.
			rows, err := BuildDerivedMetricsV2(in, ModelVersion21)
			if err != nil {
				t.Fatalf("BuildDerivedMetricsV2: %v", err)
			}
			if len(rows) != 12 {
				t.Fatalf("se esperaban 12 filas, hay %d", len(rows))
			}
			for _, m := range rows {
				if m.SecurityID != in.SecurityID {
					t.Fatalf("security_id incorrecto: %d", m.SecurityID)
				}
				if m.ModelVersion != ModelVersion21 {
					t.Fatalf("model_version incorrecto: %q", m.ModelVersion)
				}
				if !m.AsOf.Equal(in.AsOf) {
					t.Fatalf("as_of no propagado: %v vs %v", m.AsOf, in.AsOf)
				}
				var snap map[string]any
				if err := json.Unmarshal(m.InputsSnapshot, &snap); err != nil {
					t.Fatalf("inputs_snapshot de %s no es JSON válido: %v", m.Metric, err)
				}
				if got := snap["normalized_tax_rate"]; got == nil {
					t.Fatalf("snapshot de %s sin normalized_tax_rate", m.Metric)
				}
			}
			// modelVersion vacío cae al default 2.1.0.
			def, err := BuildDerivedMetricsV2(in, "")
			if err != nil {
				t.Fatalf("BuildDerivedMetricsV2 default: %v", err)
			}
			if def[0].ModelVersion != ModelVersion21 {
				t.Fatalf("model_version por defecto incorrecto: %q", def[0].ModelVersion)
			}
		})
	}
}
