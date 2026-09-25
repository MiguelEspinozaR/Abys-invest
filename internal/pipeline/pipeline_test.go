package pipeline

import (
	"context"
	"strings"
	"testing"
)

// Los endpoints de refresh degradan limpiamente cuando la BD no está
// disponible (patrón del router: pool nil -> error, nunca panic).
func TestRefreshMetricsAndScoresPoolNil(t *testing.T) {
	_, err := RefreshMetricsAndScores(context.Background(), nil, DefaultGrowth, false)
	if err == nil {
		t.Fatal("RefreshMetricsAndScores con pool nil debería fallar")
	}
	if !strings.Contains(err.Error(), "pool nil") {
		t.Fatalf("mensaje de error inesperado: %v", err)
	}
}

func TestForceRefreshPoolNil(t *testing.T) {
	_, err := ForceRefresh(context.Background(), nil, nil, DefaultGrowth, "AbysInvest/test", false)
	if err == nil {
		t.Fatal("ForceRefresh con pool nil debería fallar")
	}
	if !strings.Contains(err.Error(), "pool nil") {
		t.Fatalf("mensaje de error inesperado: %v", err)
	}
}

func TestRunAnalyticsPoolNil(t *testing.T) {
	if _, err := RunAnalytics(context.Background(), nil, "AAPL", DefaultGrowth, false, AnalyticsJobAll); err == nil {
		t.Fatal("RunAnalytics con pool nil debería fallar")
	}
}

func TestRunAnalyticsUnknownJob(t *testing.T) {
	// job inválido se rechaza antes de tocar la BD.
	if _, err := RunAnalytics(context.Background(), nil, "AAPL", DefaultGrowth, false, "otro"); err == nil {
		t.Fatal("job desconocido debería fallar")
	}
}

// TestParseScoreParamsEnv verifica que los parámetros de entorno del job
// scores se resuelven con los mismos defaults que los endpoints de la API.
func TestParseScoreParamsEnv(t *testing.T) {
	t.Setenv("MARGIN_OF_SAFETY", "25")
	t.Setenv("COMPARABLES_MIN_SECURITIES", "3")
	t.Setenv("COMPARABLES_HISTORY_YEARS", "4")

	p := parseScoreParams(8.5)
	if p.Growth != 8.5 || p.MarginSafety != 25 || p.CompMinSecurities != 3 || p.CompHistoryYears != 4 {
		t.Fatalf("parseScoreParams inesperado: %+v", p)
	}

	t.Setenv("MARGIN_OF_SAFETY", "no-num")
	t.Setenv("COMPARABLES_MIN_SECURITIES", "")
	t.Setenv("COMPARABLES_HISTORY_YEARS", "")
	def := parseScoreParams(0)
	if def.Growth != 0 || def.MarginSafety != 30 || def.CompMinSecurities != 5 || def.CompHistoryYears != 5 {
		t.Fatalf("defaults de parseScoreParams inesperados: %+v", def)
	}
}
