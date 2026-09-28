//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/miky/abys-invest/internal/storage"
)

// E2E: tras aplicar migraciones, /health responde 200 con la BD conectada
// (T10, punto 3 del plan: "Health check responde 200 después de ingesta").
//
// M5.2: el body se deserializa en un struct LOCAL con tags JSON explícitos
// (independiente del tipo de producción, para no tautologizar) porque la
// ampliación aditiva añade `latency_ms` (número) y `tables` (array), y
// encoding/json falla al meterlos en el `map[string]string` que se usaba antes.
// Se conservan las aserciones M1 (200 / ok / connected / version no vacío) y se
// añaden las de los campos nuevos.
//
// Run: DATABASE_URL=... go test ./cmd/api/... -tags=integration -count=1 -v

func TestHealthConnectedAfterMigrations(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL requerido para el test de integración de salud")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := storage.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pool.Close()

	if err := storage.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatalf("migraciones: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthHandler(pool)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("con BD se esperaba 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Status          string   `json:"status"`
		Database        string   `json:"database"`
		Version         string   `json:"version"`
		LatencyMS       *float64 `json:"latency_ms"`
		PostgresVersion string   `json:"postgres_version"`
		DBSize          string   `json:"db_size"`
		Tables          []struct {
			Name string `json:"name"`
			Rows int64  `json:"rows"`
		} `json:"tables"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body no es JSON: %v", err)
	}
	if body.Status != "ok" || body.Database != "connected" {
		t.Fatalf("body inesperado: %s", rec.Body.String())
	}
	if body.Version == "" {
		t.Fatal("falta version en /health")
	}

	// Métricas ampliadas (M5.2): la latencia del ping llega como número (puede
	// ser 0.00 → por eso es puntero en el tipo de producción), la versión de PG
	// como "18.6" y el tamaño humanizado.
	if body.LatencyMS == nil {
		t.Fatalf("con BD debe venir latency_ms: %s", rec.Body.String())
	}
	if *body.LatencyMS < 0 {
		t.Fatalf("latency_ms no puede ser negativo: %v", *body.LatencyMS)
	}
	if !regexp.MustCompile(`^\d+\.\d+$`).MatchString(body.PostgresVersion) {
		t.Fatalf("postgres_version inesperado: %q", body.PostgresVersion)
	}
	if body.DBSize == "" {
		t.Fatalf("falta db_size en /health: %s", rec.Body.String())
	}

	// Conteos: 9 tablas del esquema público, nombres no vacíos, filas >= 0 y
	// securities poblada (la migración 002 inserta el catálogo).
	if len(body.Tables) != 9 {
		t.Fatalf("se esperaban 9 tablas, got %d: %s", len(body.Tables), rec.Body.String())
	}
	var total int64
	var securities int64 = -1
	for _, row := range body.Tables {
		if row.Name == "" || row.Rows < 0 {
			t.Fatalf("fila inválida: %+v", row)
		}
		total += row.Rows
		if row.Name == "securities" {
			securities = row.Rows
		}
	}
	if total <= 0 {
		t.Fatalf("la suma de filas debe ser > 0, got %d", total)
	}
	if securities <= 0 {
		t.Fatalf("securities.rows debe ser > 0, got %d", securities)
	}
}
