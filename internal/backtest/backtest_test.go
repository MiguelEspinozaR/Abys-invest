package backtest

import (
	"testing"
	"time"
)

func day(i int) time.Time {
	return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i)
}

// TestCalculateSMA verifica la media móvil simple exacta.
func TestCalculateSMA(t *testing.T) {
	closes := []float64{1, 2, 3, 4, 5, 6}
	sma := calculateSMA(closes, 3)
	want := []float64{0, 0, 2, 3, 4, 5} // primeras dos ventanas incompletas
	for i, w := range want {
		if sma[i] != w {
			t.Fatalf("sma[%d] = %v, quiere %v", i, sma[i], w)
		}
	}
}

// TestRunSMAUptrend: serie monotónicamente alcista → entrada única y sin
// salida hasta el final (posada abierta).
func TestRunSMAUptrend(t *testing.T) {
	n := 260
	dates := make([]time.Time, n)
	closes := make([]float64, n)
	for i := 0; i < n; i++ {
		dates[i] = day(i)
		closes[i] = 100 + float64(i)*0.5 // sube siempre
	}
	res := runSMABacktestOnSeries(dates, closes, SMAConfig{Fast: 10, Slow: 30})
	if res.TotalTrades != 1 {
		t.Fatalf("un único trade esperado, got %d", res.TotalTrades)
	}
	trade := res.Trades[0]
	if !trade.Open {
		t.Fatal("el trade debería estar abierto al final de un mercado alcista")
	}
	if res.TotalReturnPct <= 0 {
		t.Fatalf("retorno total esperado >0, got %v", res.TotalReturnPct)
	}
}

// TestRunSMADowntrend: serie bajista → ningún trade (sin cruces alcistas).
func TestRunSMADowntrend(t *testing.T) {
	n := 260
	dates := make([]time.Time, n)
	closes := make([]float64, n)
	for i := 0; i < n; i++ {
		dates[i] = day(i)
		closes[i] = 100 - float64(i)*0.5
	}
	res := runSMABacktestOnSeries(dates, closes, SMAConfig{Fast: 10, Slow: 30})
	if res.TotalTrades != 0 {
		t.Fatalf("sin trades esperados en tendencia bajista, got %d", res.TotalTrades)
	}
}

// TestRunSMACompleteCycle: tendencia alcista → bajista produce entrada y salida.
func TestRunSMACompleteCycle(t *testing.T) {
	n := 400
	dates := make([]time.Time, n)
	closes := make([]float64, n)
	// 200 días subiendo, 200 bajando.
	for i := 0; i < n; i++ {
		dates[i] = day(i)
		if i < 200 {
			closes[i] = 100 + float64(i)
		} else {
			closes[i] = 300 - float64(i-200)
		}
	}
	res := runSMABacktestOnSeries(dates, closes, SMAConfig{Fast: 10, Slow: 30})
	if res.TotalTrades < 1 {
		t.Fatalf("se esperaba al menos un trade cíclico, got %d", res.TotalTrades)
	}
	if res.EndDate.Before(res.StartDate) {
		t.Fatal("fechas invertidas")
	}
	// El resultado debe ser determinista: dos ejecuciones idénticas.
	a := runSMABacktestOnSeries(dates, closes, SMAConfig{Fast: 10, Slow: 30})
	b := runSMABacktestOnSeries(dates, closes, SMAConfig{Fast: 10, Slow: 30})
	if a.FinalCapital != b.FinalCapital || len(a.Trades) != len(b.Trades) {
		t.Fatal("backtest no determinista")
	}
	// Métricas D10 presentes y coherentes (base: activo subyacente).
	if res.TotalReturn <= 0 {
		t.Fatalf("total_return esperado >0, got %v", res.TotalReturn)
	}
	if res.CAGR == 0 && res.TotalReturn != 0 {
		t.Fatal("cagr no calculado para una ventana con días positivos")
	}
	if res.MaxDrawdown > 0 {
		t.Fatalf("max_drawdown debe ser <=0, got %v", res.MaxDrawdown)
	}
	if res.TotalTrades != len(res.Trades) {
		t.Fatalf("total_trades debe coincidir con trades, got %d / %d", res.TotalTrades, len(res.Trades))
	}
}

func TestSMACapitalAccounting(t *testing.T) {
	// Entrada a 150 (cruce alcista en idx3), salida a 100 (cruce bajista en
	// idx4) → −33.33% sobre el capital inicial.
	dates := []time.Time{day(0), day(1), day(2), day(3), day(4), day(5)}
	closes := []float64{100, 100, 100, 150, 100, 80}
	res := runSMABacktestOnSeries(dates, closes, SMAConfig{
		Fast: 1, Slow: 2, InitialCapital: 1000,
	})
	if res.TotalTrades != 1 {
		t.Fatalf("1 trade esperado, got %d", res.TotalTrades)
	}
	if res.Trades[0].EntryPrice != 150 || res.Trades[0].ExitPrice != 100 {
		t.Fatalf("entrada/salida incorrectas: %+v", res.Trades[0])
	}
	if got := res.TotalReturnPct; got > -33.2 || got < -33.4 {
		t.Fatalf("retorno esperado ≈ -33.33%%, got %v", got)
	}
	// final_capital debe reflejar el retorno aplicado sobre capital inicial.
	if res.FinalCapital > 668 || res.FinalCapital < 664 {
		t.Fatalf("final_capital esperado ≈ 666.67 (1000 × (1-33.33%%)), got %v", res.FinalCapital)
	}
}
