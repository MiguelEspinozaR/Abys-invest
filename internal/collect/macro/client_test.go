package macro

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

func TestParseBLSResponse(t *testing.T) {
	data := loadFixture(t, "bls_cpi_response.json")
	rows, err := ParseBLSResponse(data)
	if err != nil {
		t.Fatalf("ParseBLSResponse: %v", err)
	}
	if len(rows) < 70 {
		t.Fatalf("se esperaban >= 70 observaciones mensuales, hay %d", len(rows))
	}
	// El fixture cubre M01/2020 hasta M08/2026 (BLS devuelve en orden descendente).
	seen := map[string]bool{}
	for _, d := range rows {
		seen[d.Year+"|"+d.Period] = true
	}
	if !seen["2020|M01"] {
		t.Fatal("falta M01/2020 en las observaciones")
	}
	if !seen["2026|M08"] {
		t.Fatal("falta M08/2026 en las observaciones")
	}
	// Todos los periodos son mensuales (M01..M12).
	for _, d := range rows {
		if _, err := DatumDate(d); err != nil {
			t.Fatalf("fecha inválida %s %s: %v", d.Year, d.Period, err)
		}
		if _, err := DatumValue(d); err != nil {
			t.Fatalf("valor inválido %q: %v", d.Value, err)
		}
	}
}

func TestDatumDateConversion(t *testing.T) {
	cases := []struct {
		period string
		year   string
		want   string
	}{
		{"M01", "2024", "2024-01-01"},
		{"M12", "2023", "2023-12-01"},
		{"M06", "2026", "2026-06-01"},
	}
	for _, c := range cases {
		got, err := DatumDate(BLSDatum{Period: c.period, Year: c.year})
		if err != nil {
			t.Fatalf("case %+v: error %v", c, err)
		}
		if got.Format("2006-01-02") != c.want {
			t.Fatalf("case %+v: got %v want %s", c, got.Format("2006-01-02"), c.want)
		}
	}
}

func TestParseBLSRequestFailed(t *testing.T) {
	body := `{"status":"REQUEST_FAILED","message":["Quota exceeded"],"Results":{}}`
	if _, err := ParseBLSResponse([]byte(body)); err == nil {
		t.Fatal("se esperaba ErrRequestFailed")
	}
}

func TestParseBLSInvalidJSON(t *testing.T) {
	if _, err := ParseBLSResponse([]byte(`{bad`)); err == nil {
		t.Fatal("se esperaba error de parseo")
	}
}

// TestDatumDateEdgeCases verifies DatumDate and DatumValue directly.
func TestDatumDateEdgeCases(t *testing.T) {
	// M13 (anual) no debe ser convertible.
	if _, err := DatumDate(BLSDatum{Period: "M13", Year: "2024"}); err == nil {
		t.Fatal("M13 no debe ser una fecha mensual válida")
	}
	if _, err := DatumDate(BLSDatum{Period: "M00", Year: "2024"}); err == nil {
		t.Fatal("M00 no debe ser válido")
	}
	if _, err := DatumDate(BLSDatum{Period: "M01", Year: "abcd"}); err == nil {
		t.Fatal("año inválido no debe ser válido")
	}
	// Valores.
	if v, err := DatumValue(BLSDatum{Value: "317.671"}); err != nil || v != 317.671 {
		t.Fatalf("DatumValue: got %v %v", v, err)
	}
	if _, err := DatumValue(BLSDatum{Value: "abc"}); err == nil {
		t.Fatal("valor no numérico debe fallar")
	}
}

func TestFetchSeries(t *testing.T) {
	fixture := loadFixture(t, "bls_cpi_response.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("método inesperado: %s", r.Method)
		}
		if r.URL.Path != "/publicAPI/v2/timeseries/data/" {
			t.Errorf("path inesperado: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer srv.Close()

	client := NewClient("", WithBaseURL(srv.URL), WithRequestsPerMinute(60))
	rows, err := client.FetchSeries(context.Background(), "CPI", "2020", "2026")
	if err != nil {
		t.Fatalf("FetchSeries: %v", err)
	}
	if len(rows) < 70 {
		t.Fatalf("observaciones insuficientes: %d", len(rows))
	}
	for _, r := range rows {
		if r.SeriesCode != "CUSR0000SA0" {
			t.Fatalf("series_code incorrecto: %s", r.SeriesCode)
		}
		if r.Unit != "index" || r.Frequency != "monthly" || r.Source != "bls" {
			t.Fatalf("metadatos incorrectos: %+v", r)
		}
	}
	// Monotonicidad de fechas (orden del payload: BLS devuelve ascendente).
	for i := 1; i < len(rows); i++ {
		if rows[i].Date.Before(rows[i-1].Date) {
			t.Fatalf("fechas desordenadas en %v", rows[i].Date)
		}
	}
}

func TestResolveSeries(t *testing.T) {
	meta, err := ResolveSeries("cpi")
	if err != nil {
		t.Fatalf("ResolveSeries('cpi') lower-case: %v", err)
	}
	if meta.Code != "CUSR0000SA0" {
		t.Fatalf("code inesperado: %s", meta.Code)
	}
	if _, err := ResolveSeries("NOPE"); err == nil {
		t.Fatal("serie no soportada debe fallar")
	}
}

func TestSupportedSeriesHasCPI(t *testing.T) {
	keys := GetSupportedSeries()
	found := false
	for _, k := range keys {
		if k == "CPI" {
			found = true
		}
	}
	if !found {
		t.Fatalf("CPI no está en las series soportadas: %v", keys)
	}
}

func TestClientRequestFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"REQUEST_FAILED","message":["Quota exceeded"],"Results":{}}`))
	}))
	defer srv.Close()

	client := NewClient("", WithBaseURL(srv.URL), WithRequestsPerMinute(60))
	if _, err := client.FetchSeries(context.Background(), "CPI", "2020", "2026"); err == nil {
		t.Fatal("REQUEST_FAILED no generó error")
	}
}

func TestClientRetry(t *testing.T) {
	var calls atomic.Int32
	fixture := loadFixture(t, "bls_cpi_response.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer srv.Close()

	client := NewClient("", WithBaseURL(srv.URL), WithRequestsPerMinute(60), WithRetryBase(time.Millisecond))
	rows, err := client.FetchSeries(context.Background(), "CPI", "2020", "2026")
	if err != nil {
		t.Fatalf("FetchSeries con retry falló: %v (calls=%d)", err, calls.Load())
	}
	if len(rows) < 70 {
		t.Fatalf("observaciones insuficientes tras retry: %d", len(rows))
	}
	if calls.Load() < 3 {
		t.Fatalf("se esperaban reintentos, hubo %d llamadas", calls.Load())
	}
}

func TestValidateYearRange(t *testing.T) {
	if err := ValidateYearRange("2020", "2026"); err != nil {
		t.Fatalf("rango válido falló: %v", err)
	}
	if err := ValidateYearRange("2026", "2020"); err == nil {
		t.Fatal("rango invertido debe fallar")
	}
	if err := ValidateYearRange("abc", "2020"); err == nil {
		t.Fatal("año inválido debe fallar")
	}
}
