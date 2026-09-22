package valuation

import "testing"

func f32(v float64) *float64 { return &v }

// TestGrahamIntrinsicReference — SPEC §13.1: EPS=6.42, g=7 →
// (2×7+8.5)×6.42 = 22.5×6.42 = 144.45 (plan D2, test de referencia AAPL).
func TestGrahamIntrinsicReference(t *testing.T) {
	eps := f32(6.42)
	got := CalcGrahamIntrinsic(7, eps)
	if got == nil {
		t.Fatal("intrinsic no calculado")
	}
	if *got != 144.45 {
		t.Fatalf("esperado 144.45, got %v", *got)
	}
}

// TestGrahamDefaultGrowth — g<=0 cae al default 7 (GROWTH_RATE_DEFAULT).
func TestGrahamDefaultGrowth(t *testing.T) {
	eps := f32(6.42)
	got := CalcGrahamIntrinsic(0, eps)
	if got == nil || *got != 144.45 {
		t.Fatalf("default growth falló: %v", got)
	}
}

// TestGrahamNilEPS — regla conservadora: EPS faltante → nil.
func TestGrahamNilEPS(t *testing.T) {
	if got := CalcGrahamIntrinsic(7, nil); got != nil {
		t.Fatalf("EPS nil debe dar nil, got %v", *got)
	}
}

// TestGrahamNegativeEPS — EPS<=0 nunca es un valor válido.
func TestGrahamNegativeEPS(t *testing.T) {
	eps := f32(-5)
	if got := CalcGrahamIntrinsic(7, eps); got != nil {
		t.Fatalf("EPS<=0 debe dar nil (conservador), got %v", *got)
	}
}
