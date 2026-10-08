package pipeline

import (
	"math"
	"testing"

	"github.com/miky/abys-invest/internal/quality"
)

// W5 (M6c-T1), CA-7/CA-10: alignedTaxRate es la ÚNICA aritmética de la tasa
// observada que comparten qualityrelative, growthwacc y analytics (Az4). Este
// test unitario PURO construye AlignedFYFacts a mano —sin BD, sin storage— y
// fija la tabla completa de salida: la tasa en PERCENT o (0, motivo cerrado).
//
// Regla ADR D32: se RECHAZA, nunca se recorta. Un `rate` fuera de [0,
// wacc.MaxTaxRate] (incluido el NaN, porque NaN >= 0 es false) es un hecho
// roto, no un tipo impositivo.
func TestW5AlignedTaxRateTable(t *testing.T) {
	const outOfRange = quality.ReasonTaxRateOutOfRange

	cases := []struct {
		name       string
		facts      AlignedFYFacts
		wantRate   float64
		wantReason string
		// wantExact marks the cases where the rate itself (not only the reason)
		// is asserted against the arithmetic.
		wantExact bool
	}{
		{
			// 20719 / 132729 x 100 = 15.610002335586042 ≈ 15.61 % (par completo).
			name: "par_completo",
			facts: AlignedFYFacts{
				IncomeTaxExpense: floatPtr(20719),
				PretaxIncome:     floatPtr(132729),
			},
			wantRate:   20719.0 / 132729.0 * 100.0,
			wantReason: "",
			wantExact:  true,
		},
		{
			// Caso AVGO: impuesto NEGATIVE sobre pretax positivo ⇒ rechazo.
			name: "impuesto_negativo_avgo",
			facts: AlignedFYFacts{
				IncomeTaxExpense: floatPtr(-397),
				PretaxIncome:     floatPtr(22693),
			},
			wantRate:   0,
			wantReason: outOfRange,
		},
		{
			// 60 % > wacc.MaxTaxRate (50): error de unidad en un hecho XBRL.
			name: "rate_60_fuera_de_techo",
			facts: AlignedFYFacts{
				IncomeTaxExpense: floatPtr(60),
				PretaxIncome:     floatPtr(100),
			},
			wantRate:   0,
			wantReason: outOfRange,
		},
		{
			// Denominador cero: no hay tasa, y NUNCA un (0, "") que se leería
			// como "tasa observada 0 %".
			name: "pretax_cero",
			facts: AlignedFYFacts{
				IncomeTaxExpense: floatPtr(397),
				PretaxIncome:     floatPtr(0),
			},
			wantRate:   0,
			wantReason: FYReasonTaxRateUnavailable,
		},
		{
			// Par roto (pretax nil) pero el alineador ya explicó por qué: el
			// motivo del caller manda, no se sustituye por unavailable.
			name: "pareja_nil_con_motivo_stale",
			facts: AlignedFYFacts{
				IncomeTaxExpense: floatPtr(397),
				PretaxIncome:     nil,
				TaxRateReason:    FYReasonTaxRateStale,
			},
			wantRate:   0,
			wantReason: FYReasonTaxRateStale,
		},
		{
			// P1-A (CASO QUE MUERDE): el alineador devuelve los VALORES del ancla
			// aunque esté viejo y sólo avisa por TaxRateReason. Par COMPLETO no-nil
			// + motivo stale (GE con ancla 2012, JNJ 2014) NO es derivable: si esta
			// entrada diera rate != 0 el bug de Az2/Az3/CA-4 volvería.
			name: "pareja_completa_pero_stale_no_deriva",
			facts: AlignedFYFacts{
				IncomeTaxExpense: floatPtr(2534),
				PretaxIncome:     floatPtr(17381),
				TaxRateReason:    FYReasonTaxRateStale,
			},
			wantRate:   0,
			wantReason: FYReasonTaxRateStale,
		},
		{
			// Espejo del caso anterior: MISMA pareja, sin motivo ⇒ sí deriva.
			// Prueba que lo que tumba al caso stale es el MOTIVO y no los valores.
			name: "misma_pareja_fresca_si_deriva",
			facts: AlignedFYFacts{
				IncomeTaxExpense: floatPtr(2534),
				PretaxIncome:     floatPtr(17381),
			},
			wantRate:   2534.0 / 17381.0 * 100.0,
			wantReason: "",
			wantExact:  true,
		},
		{
			// NaN en el impuesto: no debe propagarse al WACC ni al NOPAT.
			name: "nan_impuesto",
			facts: AlignedFYFacts{
				IncomeTaxExpense: floatPtr(math.NaN()),
				PretaxIncome:     floatPtr(100),
			},
			wantRate:   0,
			wantReason: outOfRange,
		},
		{
			// NaN en el pretax: tampoco (NaN != 0, así que llega al test de
			// rango y lo rechaza en su forma positiva).
			name: "nan_pretax",
			facts: AlignedFYFacts{
				IncomeTaxExpense: floatPtr(397),
				PretaxIncome:     floatPtr(math.NaN()),
			},
			wantRate:   0,
			wantReason: outOfRange,
		},
		{
			// Ambos nil sin motivo (struct a mano): el guardián de fallback
			// devuelve unavailable, jamás (0, "").
			name:       "pareja_nil_sin_motivo",
			facts:      AlignedFYFacts{},
			wantRate:   0,
			wantReason: FYReasonTaxRateUnavailable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rate, reason := alignedTaxRate(tc.facts)

			if reason != tc.wantReason {
				t.Fatalf("reason = %q, esperado %q", reason, tc.wantReason)
			}
			if math.IsNaN(rate) {
				t.Fatalf("rate = NaN: la tasa rechazada no debe propagar NaN")
			}
			if tc.wantExact {
				if math.Abs(rate-tc.wantRate) > 1e-9 {
					t.Fatalf("rate = %.12f, esperado %.12f", rate, tc.wantRate)
				}
				return
			}
			if rate != tc.wantRate {
				t.Fatalf("rate = %v, esperado %v (un rechazo siempre devuelve 0)", rate, tc.wantRate)
			}
		})
	}
}

// CA-7 explícito: el rechazo NUNCA devuelve una tasa válida con reason vacío
// (eso convertiría un hecho roto en una tasa observada del 0 %).
func TestW5AlignedTaxRateNeverReturnsZeroRateWithoutReason(t *testing.T) {
	for _, f := range []AlignedFYFacts{
		{IncomeTaxExpense: floatPtr(-1), PretaxIncome: floatPtr(100)}, // negativa
		{IncomeTaxExpense: floatPtr(99), PretaxIncome: floatPtr(100)}, // > techo
		{IncomeTaxExpense: floatPtr(1), PretaxIncome: floatPtr(0)},    // pretax 0
		{IncomeTaxExpense: floatPtr(math.NaN()), PretaxIncome: floatPtr(1)},
		{ // P1-A: par completo pero STALE: el motivo domina, nunca una tasa.
			IncomeTaxExpense: floatPtr(2534),
			PretaxIncome:     floatPtr(17381),
			TaxRateReason:    FYReasonTaxRateStale,
		},
	} {
		rate, reason := alignedTaxRate(f)
		if reason == "" {
			t.Fatalf("rate=%v sin motivo: un rechazo no puede leerse como tasa observada", rate)
		}
		if rate != 0 {
			t.Fatalf("rate=%v con motivo %q: el rechazo devuelve 0", rate, reason)
		}
	}
}
