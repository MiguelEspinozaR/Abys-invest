package valuation

import "testing"

// TestDCFReference — fixture AAPL del plan D3: FCF=$110B, g=7%, WACC=10%,
// terminal 2.5%, horizon 5, shares=15.4B, netDebt=-$50B.
// Cálculo a mano: PV explícito 506.60B + PV terminal 1309.21B − (−50B) =
// 1865.82B → 1865.82/15.4 = $121.1570/acción.
// NOTA: el plan menciona "≈$195" pero su propia fórmula con estos inputs da
// $121.16; el test fija el valor verificable a mano (desviación documentada).
func f64v(v float64) *float64 { return &v }

func TestDCFReference(t *testing.T) {
	fcf := f64v(110e9)
	shares := f64v(15.4e9)
	netDebt := f64v(-50e9)
	got := CalcDCFIntrinsic(fcf, 7, 10, 2.5, 5, shares, netDebt)
	if got == nil {
		t.Fatal("DCF no calculado")
	}
	want := 121.1570
	if diff := *got - want; diff > 0.01 || diff < -0.01 {
		t.Fatalf("DCF esperado ~%v, got %v", want, *got)
	}
}

// TestDCFNilFCF — regla conservadora: FCF faltante → nil.
func TestDCFNilFCF(t *testing.T) {
	shares := f64v(15.4e9)
	netDebt := f64v(-50e9)
	if got := CalcDCFIntrinsic(nil, 7, 10, 2.5, 5, shares, netDebt); got != nil {
		t.Fatalf("FCF nil debe dar nil, got %v", *got)
	}
	neg := f64v(-1)
	if got := CalcDCFIntrinsic(neg, 7, 10, 2.5, 5, shares, netDebt); got != nil {
		t.Fatalf("FCF<=0 debe dar nil, got %v", *got)
	}
}

// TestDCFNilShares — shares faltante → nil.
func TestDCFNilShares(t *testing.T) {
	fcf := f64v(110e9)
	netDebt := f64v(-50e9)
	if got := CalcDCFIntrinsic(fcf, 7, 10, 2.5, 5, nil, netDebt); got != nil {
		t.Fatalf("shares nil debe dar nil, got %v", *got)
	}
}

// TestDCFNilNetDebt — deuda neta desconocida → nil (conservador).
func TestDCFNilNetDebt(t *testing.T) {
	fcf := f64v(110e9)
	shares := f64v(15.4e9)
	if got := CalcDCFIntrinsic(fcf, 7, 10, 2.5, 5, shares, nil); got != nil {
		t.Fatalf("net_debt nil debe dar nil, got %v", *got)
	}
}

// TestDCFDegeneratePerpetuity — WACC <= terminal growth no es calculable.
func TestDCFDegeneratePerpetuity(t *testing.T) {
	fcf := f64v(110e9)
	shares := f64v(15.4e9)
	if got := CalcDCFIntrinsic(fcf, 7, 10, 11, 5, shares, f64v(-50e9)); got != nil {
		t.Fatalf("wacc<=g_terminal debe dar nil, got %v", *got)
	}
}

func TestDCFDefaults(t *testing.T) {
	fcf := f64v(110e9)
	shares := f64v(15.4e9)
	// wacc/horizon/growth con 0 → defaults (7,10,5); mismo resultado.
	a := CalcDCFIntrinsic(fcf, 7, 10, 2.5, 5, shares, f64v(-50e9))
	b := CalcDCFIntrinsic(fcf, 0, 0, 2.5, 0, shares, f64v(-50e9))
	if a == nil || b == nil || *a != *b {
		t.Fatalf("defaults inconsistentes: %v vs %v", a, b)
	}
}
