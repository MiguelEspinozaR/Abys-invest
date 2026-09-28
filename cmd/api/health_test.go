package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/miky/abys-invest/internal/api"
)

// CA-8/CA-2 (unit, sin BD): sin base configurada /health responde 503 degraded.
//
// M5.2: el body se deserializa en map[string]any (NO en map[string]string) porque
// la ampliación aditiva añade `latency_ms` (número) y `tables` (array), y
// encoding/json falla al meter un número o un array en un map de strings. Se
// conservan las aserciones del contrato M1 y se AÑADE la prueba de que la
// ampliación es aditiva: sin BD el 503 NO incluye ninguno de los 4 campos nuevos
// (ni siquiera null).
func TestHealthDegradedWithoutDB(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)

	healthHandler(nil)(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("sin BD se esperaba 503, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body no es JSON: %v", err)
	}
	if body["status"] != "degraded" || body["database"] != "disconnected" {
		t.Fatalf("body inesperado: %v", body)
	}
	if body["version"] != api.Version {
		t.Fatalf("version inesperado: %v (want %q)", body["version"], api.Version)
	}
	// Contrato M1 exacto: 3 claves, los 4 campos nuevos ausentes.
	if len(body) != 3 {
		t.Fatalf("el 503 debe tener solo 3 claves, got %d: %v", len(body), body)
	}
	for _, key := range []string{"latency_ms", "postgres_version", "db_size", "tables"} {
		if _, ok := body[key]; ok {
			t.Errorf("el 503 no debe incluir %q: %v", key, body)
		}
	}
}

// El handler nunca debe paniquear si el contexto está cancelado.
func TestHealthHandlerNeverPanics(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)

	healthHandler(nil)(rec, req) // no debe paniquear
	if rec.Code == 0 {
		t.Fatal("el handler no respondió")
	}
}
