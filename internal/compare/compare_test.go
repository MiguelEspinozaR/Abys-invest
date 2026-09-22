package compare

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/miky/abys-invest/internal/storage"
)

func series(dates []string, closes []float64) []storage.DailyPrice {
	out := make([]storage.DailyPrice, len(closes))
	for i := range closes {
		d, err := time.Parse("2006-01-02", dates[i])
		if err != nil {
			panic(err)
		}
		out[i] = storage.DailyPrice{Date: d, AdjustedClose: closes[i]}
	}
	return out
}

func approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// TestNormalizePerformanceBase100: la normalización arranca en 100.0 y
// refleja price/firstPrice*100 (cálculo manual).
func TestNormalizePerformanceBase100(t *testing.T) {
	bars := series([]string{"2024-01-01", "2024-01-02", "2024-01-03"}, []float64{100, 50, 200})
	norm := NormalizePerformance(bars)
	want := []DataPoint{
		{Date: "2024-01-01", Index: 100},
		{Date: "2024-01-02", Index: 50},
		{Date: "2024-01-03", Index: 200},
	}
	if norm[0].Index != 100 || !reflect.DeepEqual(norm, want) {
		t.Fatalf("normalización incorrecta: %+v", norm)
	}
}

// TestAnnualizedVolatilityManual: log returns [ln(1.1), ln(1.1), ln(1/1.1)]
// sobre closes [100,110,121,110]. sd_sample = 0.110055, ×√252 ≈ 1.74710.
func TestAnnualizedVolatilityManual(t *testing.T) {
	bars := series([]string{"2024-01-01", "2024-01-02", "2024-01-03", "2024-01-04"}, []float64{100, 110, 121, 110})
	got := AnnualizedVolatility(bars)
	if !approx(got, 1.74710, 1e-4) {
		t.Fatalf("vol anualizada = %v, quiere ≈1.74710", got)
	}
}

// TestMaxDrawdownManual: pico 121 → valle 110 ⇒ −9.0909%.
func TestMaxDrawdownManual(t *testing.T) {
	bars := series([]string{"2024-01-01", "2024-01-02", "2024-01-03", "2024-01-04"}, []float64{100, 110, 121, 110})
	if got := MaxDrawdown(bars); !approx(got, -0.090909, 1e-5) {
		t.Fatalf("max drawdown = %v, quiere ≈-0.090909", got)
	}
}

// TestSharpeRatioZeroRF: mean/sd ×√252 sobre los mismos log returns
// (mean=0.0317701, sd=0.110055) ⇒ Sharpe = 4.5825757 (√21) con rf=0.
func TestSharpeRatioZeroRF(t *testing.T) {
	bars := series([]string{"2024-01-01", "2024-01-02", "2024-01-03", "2024-01-04"}, []float64{100, 110, 121, 110})
	if got := SharpeRatio(bars, 0); !approx(got, 4.5825757, 1e-6) {
		t.Fatalf("sharpe = %v, quiere ≈4.5825757", got)
	}
}

// TestCompareAssetsDeterminismAndFiltering: same input → same output; desde
// una fecha dada las series arrancan en ese bar con índice 100; el orden de
// tickers es determinista (orden alfabético).
func TestCompareAssetsDeterminismAndFiltering(t *testing.T) {
	aapl := series([]string{
		"2024-01-01", "2024-01-02", "2024-01-03", "2024-01-04", "2024-01-05",
	}, []float64{100, 110, 121, 110, 99})
	msft := series([]string{
		"2024-01-01", "2024-01-02", "2024-01-03", "2024-01-04", "2024-01-05",
	}, []float64{200, 190, 209, 210, 200})
	all := map[string][]storage.DailyPrice{"MSFT": msft, "AAPL": aapl}

	from, _ := time.Parse("2006-01-02", "2024-01-03")
	a := CompareAssets(all, from, time.Time{}, 0)
	b := CompareAssets(all, from, time.Time{}, 0)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("CompareAssets no determinista")
	}
	keys := make([]string, 0, len(a.NormalizedPerformance))
	for k := range a.NormalizedPerformance {
		keys = append(keys, k)
	}
	sort.Strings(keys) // map iteration is random; determinism lives in content
	if len(keys) != 2 || keys[0] != "AAPL" || keys[1] != "MSFT" {
		t.Fatalf("tickers deben estar ordenados alfabéticamente, got %v", keys)
	}
	if idx := a.NormalizedPerformance["AAPL"][0]; idx.Index != 100 || idx.Date != "2024-01-03" {
		t.Fatalf("tras filtrar, la serie debe iniciar en el primer bar del rango con índice 100, got %+v", idx)
	}
	if len(a.NormalizedPerformance["AAPL"]) != 3 {
		t.Fatalf("rango esperado de 3 barras para AAPL, got %d", len(a.NormalizedPerformance["AAPL"]))
	}
	if _, ok := a.RiskMetrics["AAPL"]; !ok {
		t.Fatal("risk_metrics debe incluir AAPL")
	}
}

// TestCompareAssetsGolden: fixture compare_aapl_msft.json fija la salida
// exacta para dos series de síntesis (protege contra regresiones).
func TestCompareAssetsGolden(t *testing.T) {
	golden := "fixtures/compare_aapl_msft.json"
	raw, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("fixture no encontrado: %v", err)
	}
	var want ComparisonResult
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("fixture inválido: %v", err)
	}
	aapl := series([]string{
		"2024-01-01", "2024-01-02", "2024-01-03", "2024-01-04", "2024-01-05",
	}, []float64{100, 103, 101, 108, 112})
	msft := series([]string{
		"2024-01-01", "2024-01-02", "2024-01-03", "2024-01-04", "2024-01-05",
	}, []float64{50, 49, 52, 51, 55})
	got := CompareAssets(map[string][]storage.DailyPrice{"MSFT": msft, "AAPL": aapl}, time.Time{}, time.Time{}, 0)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("salida distinta del golden:\n got %+v\nwant %+v", got, want)
	}
}
