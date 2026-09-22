package yahoo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("fixtures/" + name)
	if err != nil {
		t.Fatalf("leer fixture %s: %v", name, err)
	}
	return data
}

func TestParseChartResponse(t *testing.T) {
	data := loadFixture(t, "chart_aapl_5y.json")
	res, err := ParseChartResponse(data)
	if err != nil {
		t.Fatalf("ParseChartResponse: %v", err)
	}
	if res.Symbol == "" {
		t.Fatal("symbol vacío")
	}
	if res.CurrentPrice <= 0 {
		t.Fatalf("CurrentPrice debe ser > 0, got %v", res.CurrentPrice)
	}
	if len(res.Bars) < 1000 {
		t.Fatalf("se esperaban ~1250 barras, hay %d", len(res.Bars))
	}
	for i, b := range res.Bars {
		if b.Date.IsZero() {
			t.Fatalf("bar %d sin fecha", i)
		}
		if b.Close == nil || *b.Close <= 0 {
			t.Fatalf("bar %d close inválido: %v", i, b.Close)
		}
		if b.High != nil && b.Low != nil && *b.High < *b.Low {
			t.Fatalf("bar %d high < low: %v < %v", i, *b.High, *b.Low)
		}
	}
	// En el fixture 5y todas las barras deben tener adjusted_close válido.
	for i, b := range res.Bars {
		if b.AdjustedClose <= 0 {
			t.Fatalf("bar %d adjusted_close inválido: %v", i, b.AdjustedClose)
		}
	}
}

func TestParseChartResponseEmptyResult(t *testing.T) {
	_, err := ParseChartResponse([]byte(`{"chart":{"result":[],"error":null}}`))
	if err != ErrEmptyResult {
		t.Fatalf("se esperaba ErrEmptyResult, got %v", err)
	}
}

func TestParseChartResponseAPIError(t *testing.T) {
	_, err := ParseChartResponse([]byte(`{"chart":{"result":[],"error":{"code":"Not Found","description":"No data found"}}}`))
	if err == nil {
		t.Fatal("se esperaba error de API")
	}
}

func TestParseChartResponseInvalidJSON(t *testing.T) {
	if _, err := ParseChartResponse([]byte(`{nope`)); err == nil {
		t.Fatal("se esperaba error de parseo")
	}
}

func TestParseChartResponseNoQuote(t *testing.T) {
	body := `{"chart":{"result":[{"meta":{"symbol":"X"},"timestamp":[1],"indicators":{"quote":[]}}],"error":null}}`
	if _, err := ParseChartResponse([]byte(body)); err == nil {
		t.Fatal("se esperaba error por falta de indicators.quote")
	}
}

func TestGetHistorical(t *testing.T) {
	fixture := loadFixture(t, "chart_aapl_5y.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v8/finance/chart/AAPL" {
			t.Errorf("path inesperado: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL), WithRequestsPerSecond(100))
	res, err := client.GetHistorical(context.Background(), "AAPL", "5y", "1d")
	if err != nil {
		t.Fatalf("GetHistorical: %v", err)
	}
	if len(res.Bars) < 1000 {
		t.Fatalf("barras insuficientes: %d", len(res.Bars))
	}
	if res.CurrentPrice <= 0 {
		t.Fatalf("precio actual inválido: %v", res.CurrentPrice)
	}
}

func TestGetQuote(t *testing.T) {
	fixture := loadFixture(t, "chart_aapl_1d.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL), WithRequestsPerSecond(100))
	price, err := client.GetQuote(context.Background(), "AAPL")
	if err != nil {
		t.Fatalf("GetQuote: %v", err)
	}
	if price <= 0 {
		t.Fatalf("precio inválido: %v", price)
	}
}

func TestClientRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fixture := loadFixture(t, "chart_aapl_1d.json")
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL), WithRequestsPerSecond(1000), WithRetryBase(time.Millisecond))
	price, err := client.GetQuote(context.Background(), "AAPL")
	if err != nil {
		t.Fatalf("GetQuote con retry falló: %v (calls=%d)", err, calls.Load())
	}
	if price <= 0 {
		t.Fatalf("precio inválido tras retry: %v", price)
	}
	if calls.Load() < 3 {
		t.Fatalf("se esperaban reintentos, hubo %d llamadas", calls.Load())
	}
}

func TestClientRetryExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL), WithRequestsPerSecond(1000), WithRetryBase(time.Millisecond))
	_, err := client.GetQuote(context.Background(), "AAPL")
	if err == nil {
		t.Fatal("se esperaba error tras agotar reintentos")
	}
}

func TestClientNotFoundNoRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL), WithRequestsPerSecond(1000), WithRetryBase(time.Millisecond))
	_, err := client.GetQuote(context.Background(), "NOPE")
	if err == nil {
		t.Fatal("se esperaba error 404")
	}
	if calls.Load() != 1 {
		t.Fatalf("404 no debe reintentar: %d llamadas", calls.Load())
	}
}

func TestClientRateLimit(t *testing.T) {
	const rps = 50
	const total = 120 // excede 1 segundo a 50 rps? No: 120/50 = 2.4s -> controlamos con reloj.

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(loadFixture(t, "chart_aapl_1d.json"))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL), WithRequestsPerSecond(rps))
	start := time.Now()
	for i := 0; i < total; i++ {
		if _, err := client.GetQuote(context.Background(), "AAPL"); err != nil {
			t.Fatalf("GetQuote %d: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	// A rps=50, total=120 requiere >= 2.38s, y el rate limit no puede ser más
	// rápido que el reloj; verificamos que respete el mínimo teórico (con holgura).
	minExpected := time.Duration(float64(total)/float64(rps)*float64(time.Second)) * 9 / 10
	if elapsed < minExpected {
		t.Fatalf("rate limit violado: %d llamadas en %v (mínimo esperado %v)", total, elapsed, minExpected)
	}
}

func TestNormalizeTicker(t *testing.T) {
	if got := NormalizeTicker(" aapl "); got != "AAPL" {
		t.Fatalf("got %q", got)
	}
}
