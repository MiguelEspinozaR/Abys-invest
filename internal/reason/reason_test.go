package reason

import "testing"

func TestNormalizeTrimDedupeAndOrder(t *testing.T) {
	t.Setenv(EnvMaxReasons, "12")
	got := Normalize([]string{
		"  beta_missing  ",
		"tax_rate_configured",
		"beta_missing", // duplicate
		"",             // empty
		"   ",          // blank
		"beta_stale",
	})
	want := []string{"beta_missing", "tax_rate_configured", "beta_stale"}
	if len(got) != len(want) {
		t.Fatalf("longitud: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("orden estable no respetado en %d: %q != %q (%v)", i, got[i], want[i], got)
		}
	}
}

func TestNormalizeEmpty(t *testing.T) {
	if got := Normalize(nil); got != nil {
		t.Fatalf("nil debe seguir nil: %v", got)
	}
	if got := Normalize([]string{"", "   "}); got != nil {
		t.Fatalf("solo blancos debe dar nil: %v", got)
	}
}

func TestNormalizeKeepsFirstAppearanceOrder(t *testing.T) {
	t.Setenv(EnvMaxReasons, "12")
	got := Normalize([]string{"c", "a", "b", "a", "c"})
	if len(got) != 3 || got[0] != "c" || got[1] != "a" || got[2] != "b" {
		t.Fatalf("orden de primera aparición: %v", got)
	}
}

func TestNormalizeCapDefault12(t *testing.T) {
	t.Setenv(EnvMaxReasons, "")
	var in []string
	for i := 0; i < 30; i++ {
		in = append(in, "r"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	got := Normalize(in)
	if len(got) != DefaultMaxReasons {
		t.Fatalf("tope por defecto = %d, esperado %d", len(got), DefaultMaxReasons)
	}
	// Los 30 sintéticos son únicos: el corte es por longitud, no por duplicado.
	for i := 1; i < len(got); i++ {
		if got[i-1] == got[i] {
			t.Fatalf("el tope introduzió un duplicado: %v", got)
		}
	}
}

func TestMaxReasonsEnvOverride(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want int
	}{
		{"5", 5},
		{"1", 1},
		{"100", 100},
		{"0", 12},  // un 0 escondería TODAS las razones → default, no 0
		{"-3", 12}, // negativo no es un tope → default
		{"101", 12},
		{"999", 12},
		{"abc", 12},
		{"", 12},
	} {
		t.Setenv(EnvMaxReasons, tc.env)
		if got := MaxReasons(); got != tc.want {
			t.Fatalf("MODEL_MAX_REASONS=%q → %d, esperado %d", tc.env, got, tc.want)
		}
	}
}

func TestNormalizeLimitExplicit(t *testing.T) {
	t.Setenv(EnvMaxReasons, "12")
	got := NormalizeLimit([]string{"a", "b", "c", "d"}, 2)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("tope explícito: %v", got)
	}
	// Un tope explícito no depende del env: es lo que usa el informe del backtest.
	if got := NormalizeLimit([]string{"a", "b"}, 0); len(got) != 1 {
		t.Fatalf("un tope <1 debe caer a 1, no vaciar la lista: %v", got)
	}
}

func TestNormalizeUnknownReasonIsPreserved(t *testing.T) {
	t.Setenv(EnvMaxReasons, "12")
	got := Normalize([]string{"future_reason_from_a_newer_engine"})
	if len(got) != 1 || got[0] != "future_reason_from_a_newer_engine" {
		t.Fatalf("una razón desconocida no se filtra: %v", got)
	}
}

func TestNormalizeMap(t *testing.T) {
	got := NormalizeMap(map[string]string{
		" roic ": " debt_unavailable ",
		"eps":    "   ",
		"":       "x",
	})
	if len(got) != 1 || got["roic"] != "debt_unavailable" {
		t.Fatalf("mapa de razones: %v", got)
	}
	if NormalizeMap(nil) != nil {
		t.Fatal("mapa vacío debe dar nil")
	}
	if NormalizeMap(map[string]string{"a": " "}) != nil {
		t.Fatal("todas las razones vacías debe dar nil")
	}
}
