package metricver

import "testing"

// M6c-T1 W6a: los 12 métricos de ADR D12 se definen en la revisión VIGENTE
// 2.1.0 (V21). Las filas 2.0.0 (V2) quedan como historia legible; el reader por
// métrica debe apuntar a V21.
func TestDefiningVersionCurrentRevision(t *testing.T) {
	for _, name := range []string{EPS, PE, PB, PCF, PEG, ROE, DE, FCFYield} {
		if got := DefiningVersion(name); got != V1 {
			t.Fatalf("%s debe definirse en %s, es %s", name, V1, got)
		}
	}
	for _, name := range modern {
		if got := DefiningVersion(name); got != V21 {
			t.Fatalf("%s debe definirse en %s (revisión vigente), es %s", name, V21, got)
		}
	}
	if got := DefiningVersion(ROIC); got != V21 {
		t.Fatalf("roic debe definirse en %s, es %s", V21, got)
	}
	if got := DefiningVersion(PE); got != V1 {
		t.Fatalf("pe_ratio debe definirse en %s, es %s", V1, got)
	}
	// Desconocida cae en V1 (fail-safe: no inventa una revisión).
	if got := DefiningVersion("no_existe"); got != V1 {
		t.Fatalf("métrica desconocida debe caer en %s, es %s", V1, got)
	}
	if got := DefiningVersion(""); got != V1 {
		t.Fatalf("métrica vacía debe caer en %s, es %s", V1, got)
	}
}

// AllPairs alimenta a los readers por métrica (storage comparables): los 12
// modernos deben viajar con V21, los 8 legacy con V1.
func TestAllPairsCarriesCurrentRevision(t *testing.T) {
	byName := map[string]string{}
	for _, p := range AllPairs() {
		if _, dup := byName[p.Metric]; dup {
			t.Fatalf("AllPairs duplica la métrica %q", p.Metric)
		}
		byName[p.Metric] = p.ModelVersion
	}
	if len(byName) != len(legacy)+len(modern) {
		t.Fatalf("AllPairs debe cubrir %d métricas, cubre %d", len(legacy)+len(modern), len(byName))
	}
	for _, name := range legacy {
		if byName[name] != V1 {
			t.Fatalf("AllPairs debe direccionar %s a %s, es %s", name, V1, byName[name])
		}
	}
	for _, name := range modern {
		if byName[name] != V21 {
			t.Fatalf("AllPairs debe direccionar %s a %s (vigente), es %s", name, V21, byName[name])
		}
	}
}

// PairsFor reutiliza el binding: la revisión de una métrica pedida es la vigente.
func TestPairsForUsesDefiningVersion(t *testing.T) {
	pairs := PairsFor([]string{ROIC, PE, "no_existe"})
	if len(pairs) != 2 {
		t.Fatalf("PairsFor debe saltar desconocidas y devolver 2 pares, devolvió %d", len(pairs))
	}
	got := map[string]string{}
	for _, p := range pairs {
		got[p.Metric] = p.ModelVersion
	}
	if got[ROIC] != V21 || got[PE] != V1 {
		t.Fatalf("PairsFor devolvió %v, esperado roic=%s pe_ratio=%s", got, V21, V1)
	}
}
