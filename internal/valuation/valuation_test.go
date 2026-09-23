package valuation

import "testing"

func f64(v float64) *float64 { return &v }

func TestConsensusBoth(t *testing.T) {
	v := CalcIntrinsicValue(IntrinsicInput{
		GrowthRate: 7, EPS: f64(6.42), FreeCashFlow: f64(110e9),
		DCFDiscountRate: 10, DCFHorizon: 5, TerminalGrowth: 2.5,
		SharesOutstanding: f64(15.4e9), NetDebt: f64(-50e9),
	})
	if v.Graham == nil || v.DCF == nil || v.Consensus == nil {
		t.Fatalf("se esperaban ambos valores: %+v", v)
	}
	// Graham≈144.45, DCF≈121.16 → consensus = promedio ≈ 132.8 (decisión
	// 2026-09-23: promedio de ambos, no el menor).
	want := (*v.Graham + *v.DCF) / 2
	if *v.Consensus != want {
		t.Fatalf("consensus debe ser el promedio de ambos (%v), got %v", want, *v.Consensus)
	}
	if v.ModelVersion != "1.1.0" {
		t.Fatalf("model_version inesperado: %v", v.ModelVersion)
	}
}

func TestConsensusOnlyGraham(t *testing.T) {
	v := CalcIntrinsicValue(IntrinsicInput{
		GrowthRate: 7, EPS: f64(6.42), NetDebt: f64(-50e9), // sin FCF → DCF nil
	})
	if v.Graham == nil || v.DCF != nil {
		t.Fatalf("setup: %+v", v)
	}
	if v.Consensus == nil || *v.Consensus != *v.Graham {
		t.Fatalf("consensus debe ser Graham: %+v", v.Consensus)
	}
}

func TestConsensusOnlyDCF(t *testing.T) {
	v := CalcIntrinsicValue(IntrinsicInput{
		GrowthRate: 7, FreeCashFlow: f64(110e9), DCFDiscountRate: 10,
		DCFHorizon: 5, TerminalGrowth: 2.5, SharesOutstanding: f64(15.4e9),
		NetDebt: f64(-50e9), // sin EPS → Graham nil
	})
	if v.Graham != nil || v.DCF == nil {
		t.Fatalf("setup: %+v", v)
	}
	if v.Consensus == nil || *v.Consensus != *v.DCF {
		t.Fatalf("consensus debe ser DCF: %+v", v.Consensus)
	}
}

func TestConsensusNone(t *testing.T) {
	v := CalcIntrinsicValue(IntrinsicInput{GrowthRate: 7})
	if v.Graham != nil || v.DCF != nil || v.Consensus != nil {
		t.Fatalf("sin inputs nada debe calcularse: %+v", v)
	}
}

// TestDeterminism — mismo input 1000 veces → mismo output (CA-4).
func TestDeterminism(t *testing.T) {
	input := IntrinsicInput{
		GrowthRate: 7, EPS: f64(6.42), FreeCashFlow: f64(110e9),
		DCFDiscountRate: 10, DCFHorizon: 5, TerminalGrowth: 2.5,
		SharesOutstanding: f64(15.4e9), NetDebt: f64(-50e9),
	}
	first := CalcIntrinsicValue(input)
	for i := 0; i < 1000; i++ {
		again := CalcIntrinsicValue(input)
		if *first.Graham != *again.Graham || *first.DCF != *again.DCF || *first.Consensus != *again.Consensus {
			t.Fatalf("no determinista en iteración %d", i)
		}
	}
}
