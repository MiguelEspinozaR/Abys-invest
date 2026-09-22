//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/miky/abys-invest/internal/storage"
)

// E2E: tras aplicar migraciones, /health responde 200 con la BD conectada
// (T10, punto 3 del plan: "Health check responde 200 después de ingesta").
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
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body no es JSON: %v", err)
	}
	if body["status"] != "ok" || body["database"] != "connected" {
		t.Fatalf("body inesperado: %v", body)
	}
	if body["version"] == "" {
		t.Fatal("falta version en /health")
	}
}
