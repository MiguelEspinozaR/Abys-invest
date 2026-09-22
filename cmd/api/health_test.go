package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// CA-8/CA-2 (unit, sin BD): sin base configurada /health responde 503 degraded.
func TestHealthDegradedWithoutDB(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)

	healthHandler(nil)(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("sin BD se esperaba 503, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body no es JSON: %v", err)
	}
	if body["status"] != "degraded" || body["database"] != "disconnected" {
		t.Fatalf("body inesperado: %v", body)
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
