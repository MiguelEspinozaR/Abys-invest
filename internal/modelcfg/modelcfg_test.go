package modelcfg

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// fakeRow is a pgx.Row that answers from memory, so Resolve/GetParameterSetByName
// are testable WITHOUT a database (K.1: unitarios sin BD).
type fakeRow struct {
	id        int64
	name      string
	version   string
	raw       []byte
	createdAt time.Time
	err       error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 5 {
		return errors.New("fakeRow: se esperaban 5 destinos")
	}
	if p, ok := dest[0].(*int64); ok {
		*p = r.id
	}
	if p, ok := dest[1].(*string); ok {
		*p = r.name
	}
	if p, ok := dest[2].(*string); ok {
		*p = r.version
	}
	if p, ok := dest[3].(*[]byte); ok {
		*p = r.raw
	}
	if p, ok := dest[4].(*time.Time); ok {
		*p = r.createdAt
	}
	return nil
}

type fakeDB struct {
	rows map[string]fakeRow // por nombre
	// args registra el nombre consultado para comprobar el default.
	args []any
}

func (f *fakeDB) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	name, _ := args[0].(string)
	f.args = append(f.args, name)
	row, ok := f.rows[name]
	if !ok {
		return fakeRow{err: pgx.ErrNoRows}
	}
	return row
}

func setJSON(t *testing.T, params map[string]any) []byte {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func baseDB(t *testing.T) *fakeDB {
	t.Helper()
	return &fakeDB{rows: map[string]fakeRow{
		"base": {id: 1, name: "base", version: ModelVersion, raw: setJSON(t, nil),
			createdAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
	}}
}

// --- codes layer ---------------------------------------------------------

func TestDefaultModelConfigMatchesSection12(t *testing.T) {
	mc := DefaultModelConfig()
	if err := mc.Validate(); err != nil {
		t.Fatalf("los defaults deben ser válidos: %v", err)
	}
	// §18 estricto, literal: los cinco pesos suman 0.90, NO 1.00. El 0.10 que
	// falta queda sin asignar y §18 lo renormaliza en el score (weight_used).
	// Fijar aquí un 1.00 reintroduciría en silencio el amend revertido.
	sum := mc.GrahamWeight + mc.DCFWeight + mc.QualityWeight + mc.RelativeWeight + mc.MarketContextWeight
	if math.Abs(sum-0.90) > 1e-12 {
		t.Fatalf("los pesos §18 suman %.15f, esperado 0.90", sum)
	}
	for name, want := range map[string]float64{
		SubProfitability: 0.20, SubGrowth: 0.20, SubMargins: 0.20,
		SubStability: 0.20, SubSolvency: 0.20,
	} {
		if mc.QualitySubWeights[name] != want {
			t.Fatalf("sub-peso %s = %v, esperado %v", name, mc.QualitySubWeights[name], want)
		}
	}
	// §18 estricto, spelled out one by one (Az3, lectura literal confirmada).
	if mc.GrahamWeight != 0.15 || mc.DCFWeight != 0.20 || mc.QualityWeight != 0.35 ||
		mc.RelativeWeight != 0.15 || mc.MarketContextWeight != 0.05 {
		t.Fatalf("pesos §18: %+v", mc.Weights())
	}
	// M6a/M6b values that must not drift (CA-M6c-14).
	if mc.TargetMarginOfSafety != 30.0 || mc.DCFYears != 5 || mc.TerminalGrowth != 2.5 ||
		mc.RiskFree != 4.5 || mc.EquityRiskPremium != 5.5 || mc.CostOfDebtSpread != 1.5 {
		t.Fatalf("defaults de M6a/M6b alterados: %+v", mc)
	}
}

func TestDefaultModelConfigIsNotShared(t *testing.T) {
	a := DefaultModelConfig()
	a.QualitySubWeights[SubGrowth] = 0.99
	b := DefaultModelConfig()
	if b.QualitySubWeights[SubGrowth] != 0.20 {
		t.Fatalf("el mapa de sub-pesos se comparte entre configs: %v", b.QualitySubWeights)
	}
}

// --- env layer (ADR D20, cierra M6b-H2) ---------------------------------

func TestEnvLayerOverridesDefaults(t *testing.T) {
	t.Setenv("MARKET_CONTEXT_WEIGHT", "0.08")
	t.Setenv("QUALITY_COVERAGE_HIGH", "0.90")
	t.Setenv("QUALITY_WEIGHT_SOLVENCY", "0.30")
	t.Setenv("WACC_RISK_FREE", "4.0")
	mc := ModelConfigFromEnv()
	if mc.MarketContextWeight != 0.08 || mc.QualityCoverageHigh != 0.90 ||
		mc.QualitySubWeights[SubSolvency] != 0.30 || mc.RiskFree != 4.0 {
		t.Fatalf("env no aplicado: %+v", mc)
	}
	if mc.QualitySubWeights[SubGrowth] != 0.20 {
		t.Fatalf("un sub-peso no tocado debe conservar su default: %v", mc.QualitySubWeights)
	}
}

func TestEnvInvalidValuesFallBackToDefault(t *testing.T) {
	for _, tc := range []struct{ env, value, name string }{
		{"QUALITY_COVERAGE_HIGH", "abc", "no numérico"},
		{"QUALITY_COVERAGE_HIGH", "NaN", "NaN"},
		{"QUALITY_COVERAGE_HIGH", "Inf", "+Inf"},
		{"QUALITY_COVERAGE_HIGH", "-Inf", "-Inf"},
		{"QUALITY_COVERAGE_HIGH", "-5", "fuera de rango"},
		{"QUALITY_COVERAGE_HIGH", "4.2", "coverage > 1"},
		{"DCF_HORIZON_YEARS", "0", "horizonte 0"},
		{"DCF_HORIZON_YEARS", "-2", "horizonte negativo"},
		{"WACC_COST_OF_DEBT_SPREAD", "-1", "spread negativo"},
		{"RELATIVE_MIN_METRICS", "99", "min metrics fuera de rango"},
	} {
		t.Setenv(tc.env, tc.value)
		mc := ModelConfigFromEnv()
		def := DefaultModelConfig()
		var got, want float64
		switch tc.env {
		case "QUALITY_COVERAGE_HIGH":
			got, want = mc.QualityCoverageHigh, def.QualityCoverageHigh
		case "DCF_HORIZON_YEARS":
			got, want = float64(mc.DCFYears), float64(def.DCFYears)
		case "WACC_COST_OF_DEBT_SPREAD":
			got, want = mc.CostOfDebtSpread, def.CostOfDebtSpread
		case "RELATIVE_MIN_METRICS":
			got, want = float64(mc.RelativeMinMetrics), float64(def.RelativeMinMetrics)
		}
		if got != want {
			t.Fatalf("%s=%q (%s) → %v, esperado el default %v", tc.env, tc.value, tc.name, got, want)
		}
		if math.IsNaN(got) || math.IsInf(got, 0) {
			t.Fatalf("%s=%q filtró un valor no finito", tc.env, tc.value)
		}
	}
}

func TestEnvBool(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"1", true}, {"true", true}, {"on", true}, {"yes", true},
		{"0", false}, {"false", false}, {"off", false},
		{"maybe", true}, // inválido → default (true)
		{"", true},      // vacío → default
	} {
		t.Setenv("VALUATION_GROWTH_TRANSITION", tc.value)
		if got := ModelConfigFromEnv().GrowthTransition; got != tc.want {
			t.Fatalf("VALUATION_GROWTH_TRANSITION=%q → %v, esperado %v", tc.value, got, tc.want)
		}
	}
}

func TestEnvHelpersRejectNonFinite(t *testing.T) {
	t.Setenv("X_FLOAT", "NaN")
	if got := EnvFloat("X_FLOAT", 1.5); got != 1.5 {
		t.Fatalf("NaN filtrado: %v", got)
	}
	t.Setenv("X_FLOAT", "Inf")
	if got := EnvFloat("X_FLOAT", 1.5); got != 1.5 {
		t.Fatalf("Inf filtrado: %v", got)
	}
	t.Setenv("X_FLOAT", "3")
	if got := EnvFloatRange("X_FLOAT", 1.5, 0, 2); got != 1.5 {
		t.Fatalf("fuera de rango filtrado: %v", got)
	}
	if got := EnvFloatRange("X_FLOAT", 1.5, 0, 5); got != 3 {
		t.Fatalf("dentro de rango debe pasar: %v", got)
	}
	t.Setenv("X_INT", "zz")
	if got := EnvInt("X_INT", 7); got != 7 {
		t.Fatalf("int inválido: %v", got)
	}
	if got := EnvIntRange("X_INT", 7, 0, 3); got != 7 {
		t.Fatalf("int fuera de rango: %v", got)
	}
	t.Setenv("X_BOOL", "quiza")
	if got := EnvBool("X_BOOL", true); !got {
		t.Fatalf("bool inválido: %v", got)
	}
	if got := EnvString("X_STR", "base"); got != "base" {
		t.Fatalf("string: %q", got)
	}
}

// --- Validate ------------------------------------------------------------

func TestValidateWeightsMustFitTheUnitInterval(t *testing.T) {
	// Los defaults §18 (suma 0.90) tienen que ser válidos: son el contrato.
	if err := DefaultModelConfig().Validate(); err != nil {
		t.Fatalf("los defaults §18 (suma 0.90) deben ser válidos: %v", err)
	}
	// La barrera es ">0 y <=1", no "==1": §18 renormaliza el 0.10 sin asignar.
	mc := DefaultModelConfig()
	mc.QualityWeight = 0.45 // 0.90 + 0.10 = 1.00
	if err := mc.Validate(); err != nil {
		t.Fatalf("una suma de exactamente 1 es válida: %v", err)
	}
	mc = DefaultModelConfig()
	mc.QualityWeight = 0.50 // suma 1.05
	if err := mc.Validate(); err == nil {
		t.Fatal("pesos que suman más de 1 deben rechazarse")
	}
	mc = DefaultModelConfig()
	mc.GrahamWeight, mc.DCFWeight, mc.QualityWeight = 0, 0, 0
	mc.RelativeWeight, mc.MarketContextWeight = 0, 0 // suma 0
	if err := mc.Validate(); err == nil {
		t.Fatal("pesos que suman 0 deben rechazarse: no habría nada que renormalizar")
	}

	// §18 permite un peso 0: apagar market_context y moverlo a quality deja
	// 0.15/0.20/0.45/0.15/0 = 0.95, dentro del intervalo unitario.
	mc = DefaultModelConfig()
	mc.MarketContextWeight = 0
	mc.QualityWeight = 0.45
	if err := mc.Validate(); err != nil {
		t.Fatalf("un reparto con market_context 0 es legítimo: %v", err)
	}

	mc = DefaultModelConfig()
	mc.MarketContextWeight = math.NaN()
	if err := mc.Validate(); err == nil {
		t.Fatal("un peso NaN debe rechazarse")
	}
	mc = DefaultModelConfig()
	mc.MarketContextWeight = -0.05
	if err := mc.Validate(); err == nil {
		t.Fatal("un peso negativo debe rechazarse")
	}
	mc = DefaultModelConfig()
	mc.MarketContextWeight = math.Inf(1)
	if err := mc.Validate(); err == nil {
		t.Fatal("un peso +Inf debe rechazarse")
	}
}

func TestValidateRejectsBadThresholdsAndSubWeights(t *testing.T) {
	t.Run("buy <= hold", func(t *testing.T) {
		mc := DefaultModelConfig()
		mc.BuyThreshold, mc.HoldThreshold = 40, 70
		if err := mc.Validate(); err == nil {
			t.Fatal("buy <= hold debe rechazarse")
		}
	})
	t.Run("buy fuera de rango", func(t *testing.T) {
		mc := DefaultModelConfig()
		mc.BuyThreshold = 150
		if err := mc.Validate(); err == nil {
			t.Fatal("buy 150 debe rechazarse")
		}
	})
	t.Run("hold negativo", func(t *testing.T) {
		mc := DefaultModelConfig()
		mc.HoldThreshold = -1
		if err := mc.Validate(); err == nil {
			t.Fatal("hold negativo debe rechazarse")
		}
	})
	t.Run("coverage desordenado", func(t *testing.T) {
		mc := DefaultModelConfig()
		mc.QualityCoverageMedium, mc.QualityCoverageHigh = 0.9, 0.6
		if err := mc.Validate(); err == nil {
			t.Fatal("medium >= high debe rechazarse")
		}
	})
	t.Run("sub-peso ausente", func(t *testing.T) {
		mc := DefaultModelConfig()
		delete(mc.QualitySubWeights, SubStability)
		if err := mc.Validate(); err == nil {
			t.Fatal("falta un sub-bloque debe rechazarse")
		}
	})
	t.Run("sub-peso cero", func(t *testing.T) {
		mc := DefaultModelConfig()
		mc.QualitySubWeights[SubMargins] = 0
		if err := mc.Validate(); err == nil {
			t.Fatal("un sub-peso 0 silenciaría un sub-bloque: debe rechazarse")
		}
	})
	t.Run("sub-pesos que no suman 1", func(t *testing.T) {
		mc := DefaultModelConfig()
		mc.QualitySubWeights[SubGrowth] = 0.4
		if err := mc.Validate(); err == nil {
			t.Fatal("sub-pesos que no suman 1 deben rechazarse")
		}
	})
	t.Run("sub-bloque desconocido", func(t *testing.T) {
		mc := DefaultModelConfig()
		// "moat" no es un sub-bloque de §12: se renombra profitability para que
		// la suma siga siendo 1 y el rechazo tenga que deberse al NOMBRE.
		w := mc.QualitySubWeights[SubProfitability]
		delete(mc.QualitySubWeights, SubProfitability)
		mc.QualitySubWeights["moat"] = w
		if err := mc.Validate(); err == nil {
			t.Fatal("un sub-bloque desconocido debe rechazarse")
		}
	})
	t.Run("mezcla relative", func(t *testing.T) {
		mc := DefaultModelConfig()
		mc.RelativeSectorWeight = 0.9
		if err := mc.Validate(); err == nil {
			t.Fatal("mezcla de relative que no suma 1 debe rechazarse")
		}
	})
	t.Run("MOS y horizonte", func(t *testing.T) {
		mc := DefaultModelConfig()
		mc.TargetMarginOfSafety = 0
		if err := mc.Validate(); err == nil {
			t.Fatal("MOS 0 debe rechazarse")
		}
		mc = DefaultModelConfig()
		mc.DCFYears = 0
		if err := mc.Validate(); err == nil {
			t.Fatal("horizonte 0 debe rechazarse")
		}
	})
}

// --- parameter set layer -------------------------------------------------

func TestResolveMissingParameterSetIsExplicitError(t *testing.T) {
	db := baseDB(t)
	_, cfg, err := Resolve(context.Background(), db, "no_existe")
	if !errors.Is(err, ErrParameterSetNotFound) {
		t.Fatalf("un set inexistente debe ser ErrParameterSetNotFound: %v", err)
	}
	if cfg.ParameterSetName != "" {
		t.Fatalf("una resolución fallida no debe devolver una config etiquetada: %q", cfg.ParameterSetName)
	}
}

func TestResolveEmptyParametersMeansNoOverrides(t *testing.T) {
	// Un peso top-level que no se reequilibra deja de sumar 1: la barrera de §24
	// es exactamente para esto, y el propio default del env es coherente (0.10).
	t.Setenv("QUALITY_COVERAGE_HIGH", "0.90")
	db := baseDB(t)
	ps, cfg, err := Resolve(context.Background(), db, "base")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if ps.ID != 1 || ps.Name != "base" || ps.ModelVersion != ModelVersion {
		t.Fatalf("parameter set: %+v", ps)
	}
	if cfg.QualityCoverageHigh != 0.90 {
		t.Fatalf("`{}` no debe pisar el env: %v", cfg.QualityCoverageHigh)
	}
	if cfg.MarketContextWeight != DefaultMarketContextWeight {
		t.Fatalf("un peso ausente del set conserva su capa inferior: %v", cfg.MarketContextWeight)
	}
	if cfg.ParameterSetID != 1 || cfg.ParameterSetName != "base" {
		t.Fatalf("procedencia: %+v", cfg)
	}
}

func TestResolvePrecedenceCodesEnvSet(t *testing.T) {
	t.Setenv("MARGIN_OF_SAFETY", "35")
	db := baseDB(t)
	db.rows["conservative"] = fakeRow{id: 2, name: "conservative", version: ModelVersion,
		raw:       setJSON(t, map[string]any{"target_margin_of_safety": 40}),
		createdAt: time.Now()}
	_, cfg, err := Resolve(context.Background(), db, "conservative")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.TargetMarginOfSafety != 40 {
		t.Fatalf("el set debe ganar al env: %v", cfg.TargetMarginOfSafety)
	}
	// Lo que el set NO menciona sigue viniendo del env/default, no de un cero.
	if cfg.DCFYears != DefaultDCFYears {
		t.Fatalf("una clave ausente no puede convertirse en cero: dcf_years=%d", cfg.DCFYears)
	}
	if cfg.QualitySubWeights[SubGrowth] != 0.20 {
		t.Fatalf("sub-peso ausente no puede ser cero: %v", cfg.QualitySubWeights)
	}
}

func TestResolvePartialSubWeightsMerge(t *testing.T) {
	db := baseDB(t)
	db.rows["conservative"] = fakeRow{id: 2, name: "conservative", version: ModelVersion,
		raw: setJSON(t, map[string]any{
			"target_margin_of_safety": 40,
			"quality_sub_weights": map[string]any{
				"growth": 0.10, "margins": 0.10, "stability": 0.30, "debt_solvency": 0.30,
			},
		})}
	_, cfg, err := Resolve(context.Background(), db, "conservative")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := map[string]float64{
		SubProfitability: 0.20, SubGrowth: 0.10, SubMargins: 0.10,
		SubStability: 0.30, SubSolvency: 0.30,
	}
	for k, v := range want {
		if cfg.QualitySubWeights[k] != v {
			t.Fatalf("sub-peso %s = %v, esperado %v (%v)", k, cfg.QualitySubWeights[k], v, cfg.QualitySubWeights)
		}
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("la seed conservative debe ser válida: %v", err)
	}
}

func TestResolveInvalidWeightsSumFails(t *testing.T) {
	db := baseDB(t)
	db.rows["roto"] = fakeRow{id: 3, name: "roto", version: ModelVersion,
		raw: setJSON(t, map[string]any{"graham_weight": 0.5})}
	_, _, err := Resolve(context.Background(), db, "roto")
	if err == nil {
		t.Fatal("un set con pesos fuera del intervalo unitario no puede persistirse")
	}
	if !errors.Is(err, ErrParameterSetNotFound) {
		// el error NO es de "no existe": es de validación, y debe decirlo
		if got := err.Error(); got == "" {
			t.Fatal("error vacío")
		}
	}
}

func TestWithParametersIgnoresBadValues(t *testing.T) {
	base := DefaultModelConfig()
	out := base.WithParameters(map[string]any{
		KeyGrahamWeight:         "mucho",            // no numérico
		KeyDCFWeight:            nil,                // nil explícito
		KeyQualityWeight:        999.0,              // fuera de rango [0,1]
		KeyGrowthTransition:     "quizá",            // no booleano
		KeyDCFYears:             7.5,                // no entero
		KeyQualitySubWeights:    "no soy un objeto", // tipo incorrecto
		"clave_del_futuro":      1.0,                // desconocida
		KeyTargetMarginOfSafety: 40.0,               // válida
	})
	if out.GrahamWeight != base.GrahamWeight || out.DCFWeight != base.DCFWeight ||
		out.QualityWeight != base.QualityWeight || !out.GrowthTransition ||
		out.DCFYears != base.DCFYears {
		t.Fatalf("un valor inválido no puede pisar la capa inferior: %+v", out)
	}
	if out.TargetMarginOfSafety != 40.0 {
		t.Fatalf("una clave válida sí se aplica: %v", out.TargetMarginOfSafety)
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("una configuración con refusos sigue siendo válida: %v", err)
	}
	// El original queda intacto (WithParameters es puro).
	if base.TargetMarginOfSafety != DefaultTargetMarginOfSafety {
		t.Fatalf("WithParameters mutó el original: %v", base.TargetMarginOfSafety)
	}
}

func TestSubWeightFallbackIsNotZero(t *testing.T) {
	mc := DefaultModelConfig()
	if got := mc.SubWeight("bloque_inexistente"); got != 0.20 {
		t.Fatalf("un sub-bloque ausente debe caer a 0.20, no a 0: %v", got)
	}
	if got := mc.SubWeight(SubSolvency); got != 0.20 {
		t.Fatalf("sub-peso presente: %v", got)
	}
}

func TestDefaultParameterSetNameFromEnv(t *testing.T) {
	t.Setenv("PARAMETER_SET", "")
	if got := DefaultParameterSetName(); got != "base" {
		t.Fatalf("default = %q", got)
	}
	t.Setenv("PARAMETER_SET", "  conservative ")
	if got := DefaultParameterSetName(); got != "conservative" {
		t.Fatalf("env = %q", got)
	}
}

func TestGetParameterSetByNameErrors(t *testing.T) {
	if _, err := GetParameterSetByName(context.Background(), nil, "base"); err == nil {
		t.Fatal("un DBTX nil debe fallar explícitamente")
	}
	if _, err := GetParameterSetByName(context.Background(), baseDB(t), "  "); err == nil {
		t.Fatal("un nombre vacío debe fallar")
	}
	if _, err := GetParameterSetByName(context.Background(), baseDB(t), "nope"); !errors.Is(err, ErrParameterSetNotFound) {
		t.Fatalf("set inexistente: %v", err)
	}
}

func TestDecodeParametersEdgeCases(t *testing.T) {
	if out, err := decodeParameters(nil); err != nil || len(out) != 0 {
		t.Fatalf("NULL debe ser un mapa vacío: %v %v", out, err)
	}
	if out, err := decodeParameters([]byte("null")); err != nil || len(out) != 0 {
		t.Fatalf("`null` debe ser un mapa vacío: %v %v", out, err)
	}
	if _, err := decodeParameters([]byte("[1,2]")); err == nil {
		t.Fatal("un array no es un objeto de parámetros")
	}
	if _, err := decodeParameters([]byte("{roto")); err == nil {
		t.Fatal("JSON ilegible debe fallar")
	}
}

func TestToFloatAcceptsJSONNumberShapes(t *testing.T) {
	for _, raw := range []any{float64(1.5), json.Number("1.5"), int(1), int64(2), float32(3)} {
		if _, ok := toFloat(raw); !ok {
			t.Fatalf("tipo numérico rechazado: %T", raw)
		}
	}
	for _, raw := range []any{"1.5", true, nil, []any{1}} {
		if _, ok := toFloat(raw); ok {
			t.Fatalf("tipo no numérico aceptado: %T", raw)
		}
	}
}
