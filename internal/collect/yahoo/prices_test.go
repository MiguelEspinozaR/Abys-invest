package yahoo

import (
	"testing"
	"time"
)

// TestQuoteBarFromChartResult fija el contrato §22: la fecha del quote es la
// del ÚLTIMO bar que reportó el exchange (nunca time.Now()) y el precio es el
// regularMarketPrice (cierre regular, no ajustado).
func TestQuoteBarFromChartResult(t *testing.T) {
	data := loadFixture(t, "chart_aapl_1d.json")
	res, err := ParseChartResponse(data)
	if err != nil {
		t.Fatalf("ParseChartResponse: %v", err)
	}
	bar, err := quoteBarFromChartResult(res)
	if err != nil {
		t.Fatalf("quoteBarFromChartResult: %v", err)
	}
	if len(res.Bars) == 0 {
		t.Fatal("el fixture debe tener barras")
	}
	last := res.Bars[len(res.Bars)-1]
	if !bar.Date.Equal(last.Date) {
		t.Fatalf("fecha: esperado %s (último bar), got %s", last.Date.Format("2006-01-02"), bar.Date.Format("2006-01-02"))
	}
	if bar.Date.After(time.Now().UTC().Add(24 * time.Hour)) {
		t.Fatalf("fecha futura imposible: %s", bar.Date)
	}
	if bar.Price != res.CurrentPrice {
		t.Fatalf("precio: esperado %v, got %v", res.CurrentPrice, bar.Price)
	}
	if bar.Price <= 0 {
		t.Fatalf("precio no positivo: %v", bar.Price)
	}
	if bar.Symbol != "AAPL" {
		t.Fatalf("símbolo: %q", bar.Symbol)
	}
}

func TestQuoteBarFromChartResultSinBarras(t *testing.T) {
	if _, err := quoteBarFromChartResult(&ChartResult{Symbol: "AAPL"}); err == nil {
		t.Fatal("se esperaba error sin barras")
	}
	if _, err := quoteBarFromChartResult(nil); err == nil {
		t.Fatal("se esperaba error con resultado nil")
	}
}

func TestQuoteBarFromChartResultPrecioInvalido(t *testing.T) {
	for name, price := range map[string]float64{"cero": 0, "negativo": -1.5} {
		t.Run(name, func(t *testing.T) {
			res := &ChartResult{
				Symbol:       "AAPL",
				CurrentPrice: price,
				Bars:         []OHLCVBar{{Date: time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC), Close: f64(200)}},
			}
			if _, err := quoteBarFromChartResult(res); err == nil {
				t.Fatal("se esperaba error con precio no positivo")
			}
		})
	}
}
