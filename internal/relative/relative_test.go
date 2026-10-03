package relative

import (
	"math"
	"testing"
	"time"

	"github.com/miky/abys-invest/internal/reason"
)

func ptr(v float64) *float64 { return &v }

var asOf = time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)

// values/medians are the nine §16 metrics of a plausible company and of its
// sector: cheaper than the median on the multiples, better on the returns.
func values() map[string]*float64 {
	return map[string]*float64{
		MetricPE: ptr(18), MetricPB: ptr(6), MetricPFCF: ptr(22),
		MetricEVEBITDA: ptr(12), MetricEVEBIT: ptr(16),
		MetricFCFYield: ptr(4.5), MetricROE: ptr(0.35), MetricROIC: ptr(0.28),
		MetricNetDebtToEBITDA: ptr(1.2),
	}
}

func medians() map[string]*float64 {
	return map[string]*float64{
		MetricPE: ptr(24), MetricPB: ptr(5), MetricPFCF: ptr(30),
		MetricEVEBITDA: ptr(14), MetricEVEBIT: ptr(20),
		MetricFCFYield: ptr(3.0), MetricROE: ptr(0.18), MetricROIC: ptr(0.15),
		MetricNetDebtToEBITDA: ptr(2.5),
	}
}

func TestMetricNamesAndDirections(t *testing.T) {
	if len(MetricNames()) != 9 {
		t.Fatalf("métricas = %d, esperado 9 (§16)", len(MetricNames()))
	}
	lower := map[string]bool{
		MetricPE: true, MetricPB: true, MetricPFCF: true,
		MetricEVEBITDA: true, MetricEVEBIT: true, MetricNetDebtToEBITDA: true,
		MetricFCFYield: false, MetricROE: false, MetricROIC: false,
	}
	for name, want := range lower {
		if got := LowerIsBetter(name); got != want {
			t.Fatalf("dirección de %s = %v, esperado %v", name, got, want)
		}
	}
}

func TestBlendIsExactly06And04(t *testing.T) {
	in := Inputs{AsOf: asOf, Metrics: values(), SectorMedian: medians(),
		SectorCount: 12, HistoricalMedian: medians(), HistoricalAsOfCount: 5}
	res := Calculate(in, DefaultConfig())
	if res.Score == nil || res.SectorScore == nil || res.HistoricalScore == nil {
		t.Fatalf("ambos lados deben ser utilizables: %+v", res)
	}
	want := 0.6**res.SectorScore + 0.4**res.HistoricalScore
	if math.Abs(*res.Score-want) > 1e-12 {
		t.Fatalf("mezcla = %v, esperado %v", *res.Score, want)
	}
	// Los lados se exponen SEPARADOS (§16).
	if len(res.Sides) != 2 || res.Sides[0].Name != "sector" || res.Sides[1].Name != "historical" {
		t.Fatalf("lados: %+v", res.Sides)
	}
	if res.PeerCount != 12 {
		t.Fatalf("peer_count = %d", res.PeerCount)
	}
	if len(res.Reasons) != 0 {
		t.Fatalf("con ambos lados no debe haber razones de degradación: %v", res.Reasons)
	}
}

func TestPerMetricDirectionAndBand(t *testing.T) {
	// PE 18 vs mediana 24 → 25% mejor → 100.
	if got := relativeScore(ptr(18), ptr(24), true, 0.2); got == nil || *got != 100 {
		t.Fatalf("PE = %v, esperado 100", got)
	}
	// Exactamente la mediana → 50 (el único 50 legítimo del motor).
	if got := relativeScore(ptr(24), ptr(24), true, 0.2); got == nil || *got != 50 {
		t.Fatalf("mediana = %v, esperado 50", got)
	}
	// FCF yield 4.5 vs 3.0 → +50% mejor → 100 (mayor es mejor).
	if got := relativeScore(ptr(4.5), ptr(3.0), false, 0.2); got == nil || *got != 100 {
		t.Fatalf("fcf_yield = %v, esperado 100", got)
	}
	// Peor que la mediana → 0, y el clamp funciona.
	if got := relativeScore(ptr(48), ptr(24), true, 0.2); got == nil || *got != 0 {
		t.Fatalf("PE muy caro = %v, esperado 0", got)
	}
	if got := relativeScore(ptr(0.5), ptr(3.0), false, 0.2); got == nil || *got != 0 {
		t.Fatalf("fcf_yield muy bajo = %v, esperado 0", got)
	}
	// Lineal: 10% mejor → 75.
	if got := relativeScore(ptr(21.6), ptr(24), true, 0.2); got == nil || math.Abs(*got-75) > 1e-9 {
		t.Fatalf("10%% mejor = %v, esperado 75", got)
	}
}

func TestNoBranchEverReturns50(t *testing.T) {
	// The explicit test the plan asks for (ADR D8 / CA-M6c-7): no combination of
	// missing data may produce the neutral 50 that M4b returned.
	cases := map[string]Inputs{
		"sin nada":             {AsOf: asOf},
		"sin sector":           {AsOf: asOf, Metrics: values(), HistoricalMedian: medians(), HistoricalAsOfCount: 5},
		"sector con 4 peers":   {AsOf: asOf, Metrics: values(), SectorMedian: medians(), SectorCount: 4, HistoricalMedian: medians(), HistoricalAsOfCount: 5},
		"sector con 0 peers":   {AsOf: asOf, Metrics: values(), SectorMedian: medians(), SectorCount: 0},
		"sin medianas":         {AsOf: asOf, Metrics: values(), SectorCount: 20, HistoricalAsOfCount: 5},
		"sin valores":          {AsOf: asOf, SectorMedian: medians(), SectorCount: 20, HistoricalAsOfCount: 5},
		"medianas a cero":      {AsOf: asOf, Metrics: values(), SectorMedian: map[string]*float64{MetricPE: ptr(0), MetricPB: ptr(0), MetricPFCF: ptr(0), MetricEVEBITDA: ptr(0), MetricEVEBIT: ptr(0), MetricFCFYield: ptr(0), MetricROE: ptr(0), MetricROIC: ptr(0), MetricNetDebtToEBITDA: ptr(0)}, SectorCount: 20, HistoricalMedian: medians(), HistoricalAsOfCount: 5},
		"una sola métrica":     {AsOf: asOf, Metrics: map[string]*float64{MetricPE: ptr(18)}, SectorMedian: medians(), SectorCount: 20, HistoricalMedian: medians(), HistoricalAsOfCount: 5},
		"NaN en el valor":      {AsOf: asOf, Metrics: map[string]*float64{MetricPE: ptr(math.NaN()), MetricPB: ptr(3), MetricPFCF: ptr(20), MetricROE: ptr(0.2)}, SectorMedian: medians(), SectorCount: 20, HistoricalMedian: medians(), HistoricalAsOfCount: 5},
		"histórico sin as_of":  {AsOf: asOf, Metrics: values(), SectorMedian: medians(), SectorCount: 20, HistoricalMedian: medians(), HistoricalAsOfCount: 0},
		"histórico de 3 as_of": {AsOf: asOf, Metrics: values(), SectorMedian: medians(), SectorCount: 20, HistoricalMedian: medians(), HistoricalAsOfCount: 3},
	}
	for name, in := range cases {
		res := Calculate(in, DefaultConfig())
		if res.Score != nil && math.Abs(*res.Score-50) < 1e-12 {
			t.Fatalf("%s: la dimensión salió 50 neutro, prohibido (CA-M6c-7)", name)
		}
		if res.Score != nil && (*res.Score < 0 || *res.Score > 100) {
			t.Fatalf("%s: score fuera de rango: %v", name, *res.Score)
		}
		if res.Score == nil && res.Confidence != ConfidenceLow {
			t.Fatalf("%s: sin score la confianza debe ser low: %q", name, res.Confidence)
		}
		if res.Score == nil && !hasReason(res.Reasons, ReasonInsufficientComparables) {
			t.Fatalf("%s: sin score debe decir insufficient_comparables: %v", name, res.Reasons)
		}
		for _, m := range res.Metrics {
			if m.Score != nil && *m.Score == 50 && (m.Value == nil || m.Median == nil) {
				t.Fatalf("%s: un 50 con dato ausente es un neutro disfrazado", name)
			}
		}
	}
}

func TestSectorBelowThresholdIsNilEvenWithMedian(t *testing.T) {
	in := Inputs{AsOf: asOf, Metrics: values(), SectorMedian: medians(),
		SectorCount: 4, HistoricalMedian: medians(), HistoricalAsOfCount: 5}
	res := Calculate(in, DefaultConfig())
	if res.SectorScore != nil {
		t.Fatalf("con 4 peers no hay score sectorial: %v", *res.SectorScore)
	}
	for _, m := range res.Metrics {
		if m.Median != nil || m.Score != nil {
			t.Fatalf("%s: la mediana sectorial descartada no debe exponerse", m.Name)
		}
	}
	if res.Score == nil {
		t.Fatal("el lado histórico debe salvar el resultado")
	}
	if !hasReason(res.Reasons, ReasonOnlyHistorical) {
		t.Fatalf("razones: %v", res.Reasons)
	}
	if res.Confidence == ConfidenceHigh {
		t.Fatal("un solo lado debe REDUCIR la confianza")
	}
}

func TestOnlySectorDropsConfidence(t *testing.T) {
	in := Inputs{AsOf: asOf, Metrics: values(), SectorMedian: medians(),
		SectorCount: 12, HistoricalAsOfCount: 0}
	full := Calculate(Inputs{AsOf: asOf, Metrics: values(), SectorMedian: medians(),
		SectorCount: 12, HistoricalMedian: medians(), HistoricalAsOfCount: 5}, DefaultConfig())
	one := Calculate(in, DefaultConfig())
	if one.Score == nil || *one.Score != *full.SectorScore {
		t.Fatalf("un solo lado debe usarse tal cual: %v vs %v", one.Score, full.SectorScore)
	}
	if one.Confidence == full.Confidence {
		t.Fatalf("la confianza debe bajar: %q vs %q", one.Confidence, full.Confidence)
	}
	if !hasReason(one.Reasons, ReasonOnlySector) || !hasReason(one.Reasons, ReasonNoHistoricalMedian) {
		t.Fatalf("razones: %v", one.Reasons)
	}
}

func TestMinMetricsPerSide(t *testing.T) {
	// Dos métricas utilizables < MinMetrics (3) → lado nil.
	in := Inputs{AsOf: asOf,
		Metrics:             map[string]*float64{MetricPE: ptr(18), MetricPB: ptr(4)},
		SectorMedian:        medians(),
		SectorCount:         20,
		HistoricalAsOfCount: 5,
	}
	res := Calculate(in, DefaultConfig())
	if res.SectorScore != nil {
		t.Fatal("con 2 de 9 métricas el lado sectorial debe ser nil")
	}
	if res.Score != nil {
		t.Fatal("sin ningún lado el score es nil")
	}
	// Con MinMetrics = 2 el mismo caso sí produce score (el corte es configurable).
	cfg := DefaultConfig()
	cfg.MinMetrics = 2
	res = Calculate(in, cfg)
	if res.Score == nil {
		t.Fatal("con min_metrics=2 debe producir score")
	}
}

func TestReasonsAreNormalized(t *testing.T) {
	t.Setenv(reason.EnvMaxReasons, "12")
	in := Inputs{AsOf: asOf, Metrics: values(), SectorMedian: medians(), SectorCount: 4}
	res := Calculate(in, DefaultConfig())
	for i := 1; i < len(res.Reasons); i++ {
		if res.Reasons[i] == res.Reasons[i-1] {
			t.Fatalf("razones duplicadas: %v", res.Reasons)
		}
	}
	if res.AvailableAt != asOf || res.ModelVersion != ModelVersion {
		t.Fatalf("procedencia: %v %v", res.AvailableAt, res.ModelVersion)
	}
}

func TestDeterminism(t *testing.T) {
	in := Inputs{AsOf: asOf, Metrics: values(), SectorMedian: medians(),
		SectorCount: 9, HistoricalMedian: medians(), HistoricalAsOfCount: 5}
	a, b := Calculate(in, DefaultConfig()), Calculate(in, DefaultConfig())
	if *a.Score != *b.Score || a.Confidence != b.Confidence || a.Coverage != b.Coverage {
		t.Fatal("Calculate no es determinista")
	}
}

func TestConfigFromEnvAndValidate(t *testing.T) {
	t.Setenv("RELATIVE_SECTOR_WEIGHT", "0.7")
	t.Setenv("RELATIVE_HISTORICAL_WEIGHT", "0.3")
	t.Setenv("RELATIVE_HISTORICAL_YEARS", "7")
	t.Setenv("RELATIVE_MIN_METRICS", "2")
	t.Setenv("COMPARABLES_MIN_SECURITIES", "8")
	t.Setenv("RELATIVE_ADVANTAGE_PCT", "0.25")
	cfg := ConfigFromEnv()
	if cfg.SectorWeight != 0.7 || cfg.HistoricalWeight != 0.3 || cfg.HistoricalYears != 7 ||
		cfg.MinMetrics != 2 || cfg.MinSecurities != 8 || cfg.AdvantagePct != 0.25 {
		t.Fatalf("env: %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	t.Setenv("RELATIVE_SECTOR_WEIGHT", "abc")
	t.Setenv("COMPARABLES_MIN_SECURITIES", "0")
	cfg = ConfigFromEnv()
	if cfg.SectorWeight != DefaultSectorWeight || cfg.MinSecurities != DefaultComparablesMinSecurities {
		t.Fatalf("env inválido debe caer al default: %+v", cfg)
	}
	cfg = DefaultConfig()
	cfg.SectorWeight = 0.9
	if err := cfg.Validate(); err == nil {
		t.Fatal("mezcla que no suma 1 debe rechazarse")
	}
	cfg = DefaultConfig()
	cfg.MinMetrics = 12
	if err := cfg.Validate(); err == nil {
		t.Fatal("min_metrics > 9 debe rechazarse")
	}
}

func hasReason(rs []string, want string) bool {
	for _, r := range rs {
		if r == want {
			return true
		}
	}
	return false
}
