package growth

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"
)

// serie construye N puntos anuales con CAGR constant: value0*(1+g)^i. Es la
// forma más explícita de fijar el CAGR esperado sin repetir raíces cúbicas en
// cada test.
func serie(n int, start, cagr float64) []Point {
	base := time.Date(2015, 9, 30, 0, 0, 0, 0, time.UTC)
	out := make([]Point, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Point{
			PeriodEnd:   base.AddDate(i, 0, 0),
			Value:       start * math.Pow(1+cagr, float64(i)),
			AvailableAt: base.AddDate(i, 0, 0).AddDate(0, 2, 0), // 10-K ~2 meses después
		})
	}
	return out
}

func approx(t *testing.T, got *float64, want, tol float64, label string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: se esperaba %v, got nil", label, want)
	}
	if math.Abs(*got-want) > tol {
		t.Fatalf("%s: esperado %v, got %v (tol %v)", label, want, *got, tol)
	}
}

// --- CAGR -------------------------------------------------------------------

func TestCAGR(t *testing.T) {
	tests := []struct {
		name           string
		initial, final float64
		years          int
		want           *float64 // nil = sin CAGR
	}{
		{"(110/100)^3", 100, 133.1, 3, f(10.0)},
		{"4 años", 100, 146.41, 4, f(10.0)},
		{"negativo con valores positivos decrecientes", 100, 72.9, 3, f(-10.0)},
		{"1 año", 100, 110, 1, f(10.0)},
		{"years = 0", 100, 110, 0, nil},
		{"years negativo", 100, 110, -1, nil},
		{"initial = 0", 0, 110, 3, nil},
		{"final = 0", 100, 0, 3, nil},
		{"initial negativo", -100, -133.1, 3, nil},
		{"final no finito", 100, math.Inf(1), 3, nil},
		{"initial NaN", math.NaN(), 110, 3, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CAGR(tt.initial, tt.final, tt.years)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("se esperaba nil, got %v", *got)
				}
				return
			}
			approx(t, got, *tt.want, 1e-6, "cagr")
		})
	}
}

// TestCAGRNoUsaMasPuntosDeLosNecesarios: la ventana es exacta
// (years+1 puntos), verificado sobre una serie de 8 puntos donde un error de
// "últimos N" daría un CAGR distinto.
func TestCAGRNoUsaMasPuntosDeLosNecesarios(t *testing.T) {
	pts := serie(8, 10, 0.10) // 10, 11, 12.1, 13.31, ...
	approx(t, CAGR(pts[0].Value, pts[3].Value, 3), 10.0, 1e-9, "4 puntos = 3 años exactos")
	approx(t, CAGR(pts[4].Value, pts[7].Value, 3), 10.0, 1e-9, "los últimos 4 = 3 años exactos")
}

func f(v float64) *float64 { return &v }

// --- Selección de ventana y fuente -----------------------------------------

func TestCalculatePrefiereVentana3y(t *testing.T) {
	in := Inputs{Ticker: "T", AsOf: time.Now(), Series: Series{EPS: serie(6, 1, 0.10), FCF: serie(6, 1, 0.10)}}
	res := Calculate(in, DefaultConfig())
	if res.NormalizedGrowthRate == nil {
		t.Fatal("se esperaba tasa con 6 puntos")
	}
	approx(t, res.NormalizedGrowthRate, 10.0, 1e-6, "normalized")
	if res.Source != SourceEPSFCF3y {
		t.Fatalf("source: esperado %q, got %q", SourceEPSFCF3y, res.Source)
	}
	if res.EPSCAGR3y == nil || res.FCFCAGR3y == nil {
		t.Fatal("con 6 puntos deben existir ambos CAGR 3y")
	}
	// Los 6 puntos dan también la ventana de 5 años: los seis CAGR se calculan
	// siempre que la serie lo permita (CA-M6a-1), pero la TASA normalizada sale
	// de la ventana primaria. Revenue no está sembrado aquí: su CAGR queda nil
	// (ausente ≠ 0).
	if res.EPSCAGR5y == nil || res.FCFCAGR5y == nil {
		t.Fatal("con 6 puntos también debe existir la ventana 5y de EPS y FCF")
	}
	if res.RevenueCAGR5y != nil || res.RevenueCAGR3y != nil {
		t.Fatal("sin serie de revenue sus CAGR deben quedar nil")
	}
	approx(t, res.EPSCAGR5y, 10.0, 1e-6, "eps_cagr_5y")
}

func TestCalculateFallback5y(t *testing.T) {
	// Solo EPS: 4 puntos exactos → 3y sigue siendo preferible.
	in := Inputs{Series: Series{EPS: serie(4, 1, 0.08)}}
	res := Calculate(in, DefaultConfig())
	if res.Source != SourceEPS3y {
		t.Fatalf("4 puntos: esperado %q, got %q", SourceEPS3y, res.Source)
	}

	// 3 puntos: no hay ventana de 3 años ni de 5 → nil.
	res = Calculate(Inputs{Series: Series{EPS: serie(3, 1, 0.08)}}, DefaultConfig())
	if res.NormalizedGrowthRate != nil {
		t.Fatalf("3 puntos: se esperaba nil, got %v", *res.NormalizedGrowthRate)
	}
	if res.Source != SourceInsufficientData {
		t.Fatalf("3 puntos: source esperado %q, got %q", SourceInsufficientData, res.Source)
	}

	// Hueco en la ventana 3y: el punto que abre la ventana de 3 años es
	// NEGATIVO (pérdidas), así que su CAGR no está definido, pero el punto que
	// abre la de 5 años es positivo. Es el caso real (empresa con pérdidas hace
	// 3 años) en el que la ventana de reserva existe: source = eps_5y.
	pts := serie(6, 1, 0.08)
	pts[2].Value = -0.5 // abre la ventana 3y
	res = Calculate(Inputs{Series: Series{EPS: pts}}, DefaultConfig())
	if res.Source != SourceEPS5y {
		t.Fatalf("hueco en 3y: esperado %q, got %q (3y=%v)", SourceEPS5y, res.Source, res.EPSCAGR3y)
	}
	approx(t, res.NormalizedGrowthRate, 8.0, 1e-6, "normalized desde la ventana 5y")
	if res.Confidence != ConfidenceMedium {
		t.Fatalf("una sola serie en la ventana de reserva no es high: %q", res.Confidence)
	}
}

func TestCalculateBlendEPSFCF(t *testing.T) {
	in := Inputs{Series: Series{EPS: serie(4, 1, 0.10), FCF: serie(4, 1, 0.20)}}
	res := Calculate(in, DefaultConfig())
	approx(t, res.NormalizedGrowthRate, 15.0, 1e-6, "50/50 → 15")
	if res.Source != SourceEPSFCF3y {
		t.Fatalf("source: %q", res.Source)
	}

	// Solo EPS: el blend no inventa el FCF ausente.
	res = Calculate(Inputs{Series: Series{EPS: serie(4, 1, 0.10)}}, DefaultConfig())
	approx(t, res.NormalizedGrowthRate, 10.0, 1e-6, "solo EPS")
	if res.Source != SourceEPS3y {
		t.Fatalf("source solo EPS: %q", res.Source)
	}

	// Pesos 0.7/0.3 → 0.7*10 + 0.3*20 = 13.
	cfg := DefaultConfig()
	cfg.EPSWeight, cfg.FCFWeight = 0.7, 0.3
	res = Calculate(in, cfg)
	approx(t, res.NormalizedGrowthRate, 13.0, 1e-6, "0.7/0.3 → 13")
}

// TestCalculateUsaUltimosNMas1Puntos: con 8 puntos, la ventana 3y usa
// exactamente los últimos 4 (confirma pick y que no depende del total).
func TestCalculateUsaUltimosNMas1Puntos(t *testing.T) {
	// 4 primeros puntos con CAGR 100%, 4 últimos con 10%: si `pick` tomara los
	// primeros, el CAGR 3y saldría ~100%.
	pts := append(serie(4, 1, 1.0), serie(4, 10, 0.10)...)
	pts = pts[:8]
	// los últimos 4 parten de 10: 10, 11, 12.1, 13.31
	res := Calculate(Inputs{Series: Series{EPS: pts, FCF: pts}}, DefaultConfig())
	approx(t, res.EPSCAGR3y, 10.0, 1e-6, "eps_cagr_3y usa los últimos 4")
	if len(pts) != 8 {
		t.Fatal("la serie de test debe tener 8 puntos")
	}
}

func TestCalculateRevenueSoloEsLowConfidence(t *testing.T) {
	res := Calculate(Inputs{Series: Series{Revenue: serie(4, 100, 0.12)}}, DefaultConfig())
	approx(t, res.NormalizedGrowthRate, 12.0, 1e-6, "revenue 12")
	if res.Confidence != ConfidenceLow {
		t.Fatalf("confidence: esperado %q, got %q", ConfidenceLow, res.Confidence)
	}
	if res.Source != SourceRevenue3y {
		t.Fatalf("source: esperado %q, got %q", SourceRevenue3y, res.Source)
	}
}

func TestCalculateDetectaDiscrepanciaRevenue(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DiscrepancyPP = 10
	in := Inputs{Series: Series{EPS: serie(4, 1, 0.05), FCF: serie(4, 1, 0.05), Revenue: serie(4, 100, 0.25)}}

	res := Calculate(in, cfg)
	if !res.RevenueDiscrepancy {
		t.Fatal("25% vs 5% = 20pp > 10pp: esperaba revenue_discrepancy")
	}
	if res.Confidence == ConfidenceHigh {
		t.Fatal("la discrepancia debe degradar la confidence")
	}

	// Exactamente en el umbral → sin discrepancia.
	in.Series.Revenue = serie(4, 100, 0.15)
	res = Calculate(in, cfg)
	if res.RevenueDiscrepancy {
		t.Fatal("exactamente 10pp: no hay discrepancia")
	}
	if res.Confidence != ConfidenceHigh {
		t.Fatalf("sin discrepancia debe quedar high, got %q", res.Confidence)
	}
}

func TestCalculateLimitaCrecimientoExtraordinario(t *testing.T) {
	in := Inputs{Series: Series{EPS: serie(4, 1, 1.20), FCF: serie(4, 1, 1.20)}}
	res := Calculate(in, DefaultConfig())
	approx(t, res.NormalizedGrowthRate, 25.0, 1e-9, "clamp max")
	if !res.Clamped {
		t.Fatal("clamped debe ser true")
	}
	if res.Confidence == ConfidenceHigh {
		t.Fatal("el clamp debe degradar la confidence")
	}

	in = Inputs{Series: Series{EPS: serie(4, 1, -0.40), FCF: serie(4, 1, -0.40)}}
	res = Calculate(in, DefaultConfig())
	approx(t, res.NormalizedGrowthRate, -10.0, 1e-9, "clamp min")
	if !res.Clamped {
		t.Fatal("clamped debe ser true en el suelo")
	}
}

func TestCalculateDatosInsuficientes(t *testing.T) {
	for name, in := range map[string]Inputs{
		"series vacías":    {},
		"solo 1 punto":     {Series: Series{EPS: serie(1, 1, 0.1)}},
		"solo 2 puntos":    {Series: Series{FCF: serie(2, 1, 0.1)}},
		"puntos noposit":   {Series: Series{EPS: []Point{{Value: 0}, {Value: 0}, {Value: 0}, {Value: 0}}}},
		"solo un concepto": {Series: Series{Revenue: serie(2, 100, 0.1)}},
	} {
		t.Run(name, func(t *testing.T) {
			res := Calculate(in, DefaultConfig())
			if res.NormalizedGrowthRate != nil {
				t.Fatalf("se esperaba nil, got %v", *res.NormalizedGrowthRate)
			}
			if res.Confidence != ConfidenceLow {
				t.Fatalf("confidence: esperado low, got %q", res.Confidence)
			}
			if res.Source != SourceInsufficientData {
				t.Fatalf("source: esperado insufficient_data, got %q", res.Source)
			}
		})
	}
}

func TestCalculateConfidenceMatrix(t *testing.T) {
	eps, fcf := serie(4, 1, 0.10), serie(4, 1, 0.10)
	slow, slowFCF := serie(4, 1, 0.05), serie(4, 1, 0.05)
	fast, fastFCF := serie(4, 1, 1.20), serie(4, 1, 1.20)
	rev := serie(4, 100, 0.12)
	// Serie de 6 con la ventana 3y inservible (punto negativo) → la de reserva
	// de 5 años es la que decide.
	eps5 := serie(6, 1, 0.10)
	fcf5 := serie(6, 1, 0.10)
	eps5[2].Value, fcf5[2].Value = -1, -1

	tests := []struct {
		name string
		in   Inputs
		want string
	}{
		{"EPS+FCF 3y → high", Inputs{Series: Series{EPS: eps, FCF: fcf}}, ConfidenceHigh},
		{"solo EPS 3y → medium", Inputs{Series: Series{EPS: eps}}, ConfidenceMedium},
		{"EPS+FCF 5y → medium", Inputs{Series: Series{EPS: eps5, FCF: fcf5}}, ConfidenceMedium},
		{"revenue solo → low", Inputs{Series: Series{Revenue: rev}}, ConfidenceLow},
		{"con revenue discrepante → degrada", Inputs{Series: Series{EPS: slow, FCF: slowFCF, Revenue: serie(4, 100, 0.25)}}, ConfidenceMedium},
		{"con clamp → degrada", Inputs{Series: Series{EPS: fast, FCF: fastFCF}}, ConfidenceMedium},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := Calculate(tt.in, DefaultConfig())
			if res.Confidence != tt.want {
				t.Fatalf("confidence: esperado %q, got %q (source %q, clamped %v, disc %v)",
					tt.want, res.Confidence, res.Source, res.Clamped, res.RevenueDiscrepancy)
			}
		})
	}
}

func TestCalculateEsDeterminista(t *testing.T) {
	in := Inputs{Ticker: "AAPL", AsOf: time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC), Series: Series{
		Revenue: serie(6, 100, 0.08), EPS: serie(6, 1, 0.12), FCF: serie(6, 1, 0.15),
	}}
	cfg := DefaultConfig()
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

func TestSnapshotIncluyeInputsYConfig(t *testing.T) {
	in := Inputs{Ticker: "AAPL", AsOf: time.Now(), Series: Series{EPS: serie(4, 1, 0.1)}}
	cfg := DefaultConfig()
	res := Calculate(in, cfg)
	snap, err := res.Snapshot(in, cfg)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(snap, &decoded); err != nil {
		t.Fatalf("snapshot no es JSON válido: %v", err)
	}
	for _, key := range []string{"series", "config", "model_version"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("falta %q en el snapshot: %s", key, snap)
		}
	}
	if decoded["model_version"] != ModelVersion {
		t.Fatalf("model_version: %v", decoded["model_version"])
	}
	series, _ := decoded["series"].(map[string]any)
	eps, _ := series["eps"].([]any)
	if len(eps) != 4 {
		t.Fatalf("el snapshot debe guardar los 4 puntos de EPS, got %d", len(eps))
	}
}

// TestEffectiveRateUsaFallback documenta el contrato de M6b sin cablearlo:
// con normalized = nil, el llamador sigue usando GROWTH_RATE_DEFAULT (7).
func TestEffectiveRateUsaFallback(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FallbackRate = 7
	if got := Calculate(Inputs{}, cfg).EffectiveRate(cfg); got != 7 {
		t.Fatalf("fallback legacy: esperado 7, got %v", got)
	}
	res := Calculate(Inputs{Series: Series{EPS: serie(4, 1, 0.10)}}, cfg)
	if got := res.EffectiveRate(cfg); math.Abs(got-10) > 1e-9 {
		t.Fatalf("con tasa medida debe devolver la medida: %v", got)
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("GROWTH_WINDOW_PRIMARY_YEARS", "2")
	t.Setenv("GROWTH_MAX_RATE", "30")
	t.Setenv("GROWTH_EPS_WEIGHT", "no-numero")
	cfg := ConfigFromEnv()
	if cfg.WindowPrimaryYears != 2 {
		t.Fatalf("GROWTH_WINDOW_PRIMARY_YEARS override: %d", cfg.WindowPrimaryYears)
	}
	if cfg.MaxRate != 30 {
		t.Fatalf("GROWTH_MAX_RATE override: %v", cfg.MaxRate)
	}
	if cfg.EPSWeight != DefaultConfig().EPSWeight {
		t.Fatalf("env inválida debe conservar el default: %v", cfg.EPSWeight)
	}
	if cfg.WindowFallbackYears != DefaultConfig().WindowFallbackYears {
		t.Fatalf("sin env debe usar el default: %d", cfg.WindowFallbackYears)
	}
}
