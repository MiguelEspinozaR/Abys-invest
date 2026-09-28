package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestHealthCountQueryListsMainTables (M5.2, decisión D1): el SQL de conteos es
// UNA query UNION ALL con una rama count(*) por tabla del esquema público, en el
// orden de healthTableNames y con exactamente 8 uniones (9 ramas). Fijar la
// sentencia completa evita deriva accidental (tabla duplicada, orden cambiado o
// `public.` olvidado, que rompería con un search_path distinto).
func TestHealthCountQueryListsMainTables(t *testing.T) {
	sql := buildCountQuery(healthTableNames)

	if got := strings.Count(sql, "UNION ALL"); got != 8 {
		t.Fatalf("se esperaban 8 UNION ALL, got %d: %s", got, sql)
	}
	if got := strings.Count(sql, "count(*)"); got != len(healthTableNames) {
		t.Fatalf("se esperaban %d count(*), got %d: %s", len(healthTableNames), got, sql)
	}

	var b strings.Builder
	for i, name := range healthTableNames {
		if i > 0 {
			b.WriteString(" UNION ALL ")
		}
		b.WriteString("SELECT '" + name + "' AS name, count(*) AS rows FROM public." + name)
	}
	if want := b.String(); sql != want {
		t.Fatalf("SQL inesperado:\n got: %s\nwant: %s", sql, want)
	}
}

// TestParsePostgresVersion (M5.2, decisión D5): el parseo es una función pura,
// testeable sin BD. current_setting('server_version') devuelve
// "18.6 (Ubuntu 18.6-1.pgdg26.04+2)" y la UI quiere solo "18.6".
func TestParsePostgresVersion(t *testing.T) {
	for raw, want := range map[string]string{
		"18.6 (Ubuntu 18.6-1.pgdg26.04+2)": "18.6",
		"16.4":                             "16.4",
		"17":                               "17",
		"":                                 "",
		"weird":                            "weird",
		"  15.2 (Debian)  ":                "15.2",
	} {
		if got := parsePostgresVersion(raw); got != want {
			t.Errorf("parsePostgresVersion(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestHealthResponseOmitsEmptyMetrics (M5.2, CA-M5.2-1): fija el contrato
// ADITIVO byte a byte. Sin BD la respuesta serializa EXACTAMENTE las 3 claves
// del contrato M1 (sin `latency_ms: null` ni similar), y un 0.00 de latencia
// SÍ sale (por eso el campo es *float64: `omitempty` borraría un float a 0).
func TestHealthResponseOmitsEmptyMetrics(t *testing.T) {
	degraded := HealthResponse{Status: "degraded", Database: "disconnected", Version: Version}

	raw, err := json.Marshal(degraded)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := fmt.Sprintf(`{"status":"degraded","database":"disconnected","version":%q}`, Version)
	if string(raw) != want {
		t.Fatalf("el 503 debe serializar solo las 3 claves del contrato M1:\n got: %s\nwant: %s", raw, want)
	}

	// Un decode a mapa confirma que no hay claves fantasma (p. ej. null).
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(fields) != 3 {
		t.Fatalf("se esperaban 3 claves, got %d (%v)", len(fields), fields)
	}

	// Con métricas presentes, los 4 campos nuevos salen; con latencia 0.00
	// también (de ahí el puntero).
	ok := HealthResponse{
		Status: "ok", Database: "connected", Version: Version,
		LatencyMS:       ptr(0.0),
		PostgresVersion: "18.6",
		DBSize:          "21 MB",
		Tables:          []HealthTableCount{{Name: "securities", Rows: 10428}},
	}
	raw, err = json.Marshal(ok)
	if err != nil {
		t.Fatalf("marshal ok: %v", err)
	}
	for _, key := range []string{`"latency_ms":0`, `"postgres_version":"18.6"`, `"db_size":"21 MB"`, `"tables":[{"name":"securities","rows":10428}]`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("el 200 debe incluir %s: %s", key, raw)
		}
	}
}
