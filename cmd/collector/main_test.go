package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/miky/abys-invest/internal/collect/edgar"
)

// CA-6: un error de proveedor (red/HTTP) se propaga como error logueable y el
// worker no paniquea. Se verifica con un endpoint que devuelve 500.
func TestWorkerDoesNotPanicOnProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := edgar.NewClient("AbysInvest/test (contact@example.test)",
		edgar.WithBaseURL(srv.URL),
		edgar.WithRetryBase(1), // backoff mínimo para el test
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if err := dryRunCompany(context.Background(), client, "0000320193"); err == nil {
		t.Fatal("se esperaba error del proveedor (500), got nil")
	}
	// Sin panic -> el worker continúa; la cobertura del bucle de main() queda
	// garantizada por resolveCompany devolviendo error sin paniquear.
	if _, _, err := resolveCompany(nil, "APBR"); err == nil {
		t.Fatal("resolver ticker sin catálogo debe fallar (no paniquear)")
	}
}
