package valuation

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"strconv"
	"testing"
)

// loadFixture reads a JSON fixture from fixtures/ into Inputs. The fixture file
// is a valuation.Inputs in JSON (the schema the engine and the pipeline share),
// so a fixture can feed the pure engine directly.
func loadFixture(t *testing.T, name string) Inputs {
	t.Helper()
	data, err := os.ReadFile("fixtures/" + name)
	if err != nil {
		t.Fatalf("leer fixture %s: %v", name, err)
	}
	var in Inputs
	if err := json.Unmarshal(data, &in); err != nil {
		t.Fatalf("parsear fixture %s: %v", name, err)
	}
	return in
}

// TestAMZNAcceptance210 is the C2 acceptance of SPEC abys-valuation-210 over
// the AMZN fixture:
//
//	Graham bear ≈ 49.88        (computed although g_bear = -0.25 ≤ 0)
//	DCF bear non-nil           (computed although g ≤ 0)
//	uncertainty.components = 4 (graham_base + dcf bear/base/bull)
//	no `incomplete_scenarios`  (both methods keep their three scenarios)
func TestAMZNAcceptance210(t *testing.T) {
	in := loadFixture(t, "amzn_valuation_input.json")
	cfg := DefaultConfig()
	res := Calculate(in, cfg)

	if res.Status != StatusAvailable {
		t.Fatalf("status = %s (%v), want available", res.Status, res.Reasons)
	}
	if res.ModelVersion != ModelVersion {
		t.Errorf("model_version = %s, want %s", res.ModelVersion, ModelVersion)
	}

	// Acceptance 1: Graham bear ≈ 49.88 (2g+8.5 with g=-0.25 is still positive).
	if res.Graham.Bear == nil {
		t.Fatal("Graham bear must be computed when 2g+8.5 > 0, even with g <= 0")
	}
	if math.Abs(*res.Graham.Bear-49.88) > 1e-6 {
		t.Errorf("Graham bear = %v, want 49.88 ±1e-6", *res.Graham.Bear)
	}
	// The bear travelled through a non-positive growth: the informational reason
	// must be there, and the domain reason must NOT be.
	if !hasReason(res.Graham.Reasons, ReasonGrowthNonPositive) {
		t.Errorf("Graham reasons = %v, want the informational %s", res.Graham.Reasons, ReasonGrowthNonPositive)
	}
	if hasReason(res.Graham.Reasons, ReasonGrahamGrowthOutOfDomain) {
		t.Errorf("Graham reasons = %v, must not be out of domain (2g+8.5 = 8 > 0)", res.Graham.Reasons)
	}

	// Acceptance 2: DCF bear computed even though gBase - delta <= 0.
	if res.DCF.Bear == nil || res.DCF.Base == nil || res.DCF.Bull == nil {
		t.Fatalf("the three DCF scenarios must be computed: %+v", res.DCF)
	}

	// Acceptance 3 + 4.
	if res.Uncertainty.Components != 4 {
		t.Errorf("components = %d, want 4 (%+v)", res.Uncertainty.Components, res.Uncertainty)
	}
	if hasReason(res.Reasons, ReasonIncompleteScenarios) {
		t.Errorf("reasons = %v, incomplete_scenarios must disappear for AMZN", res.Reasons)
	}
	if res.Confidence == ConfidenceLow {
		t.Errorf("confidence = %s, want at least medium for AMZN (%v)", res.Confidence, res.Reasons)
	}
}

// TestBRKBMOSNot99 is the C1 acceptance over the BRK-B fixture: with the
// share-class normalisation applied, BRK-B must NOT produce a MOS ≈ 99.9% nor
// an FCF yield > 100%, and no `unit_mismatch` may be emitted.
func TestBRKBMOSNot99(t *testing.T) {
	in := loadFixture(t, "brkb_valuation_input.json")
	res := Calculate(in, DefaultConfig())

	if res.Status != StatusAvailable {
		t.Fatalf("status = %s (%v), want available after the BRK-B normalisation", res.Status, res.Reasons)
	}

	// Normalisation echo: the inputs used carry the B-class numbers.
	if res.Inputs.EPS == nil || math.Abs(*res.Inputs.EPS-30000.0/ClassRatioBRK) > 1e-9 {
		t.Errorf("EPS = %v, want EPS_A/1500 = 20", valueOr(res.Inputs.EPS))
	}
	if res.Inputs.SharesOutstanding == nil {
		t.Fatalf("shares = nil, want A-count × 1500")
	}

	// MOS far from 99.9%: Graham base = 20×(2×7.2+8.5) = 458 vs price 470.
	if res.MOS.GrahamBase == nil || *res.MOS.GrahamBase > -1 {
		t.Errorf("graham MOS = %v, want a normal single-digit margin, never ≈99.9", res.MOS.GrahamBase)
	}

	// FCF yield well below 100% with the B-class market cap.
	price := *res.Inputs.Price
	capVal := price * *res.Inputs.SharesOutstanding
	yield := *res.Inputs.FreeCashFlow / capVal
	if !(yield > 0 && yield < 1) {
		t.Errorf("FCF yield = %.6f, want 0 < yield < 1 after the normalisation", yield)
	}

	if hasReason(res.Reasons, ReasonUnitMismatch) {
		t.Errorf("reasons = %v, unit_mismatch must not fire for a normalised BRK-B", res.Reasons)
	}
	if hasReason(res.Graham.Reasons, ReasonUnitMismatch) || hasReason(res.DCF.Reasons, ReasonUnitMismatch) {
		t.Errorf("methods must not be withdrawn: graham=%v dcf=%v", res.Graham.Reasons, res.DCF.Reasons)
	}
}

// TestUnitMismatchExcludesMethod is the C1 guard behaviour: a method whose
// value or inputs are incoherent becomes unavailable with `unit_mismatch`, and
// at the valuation level the reason is reported AND the method leaves the
// result (its base is nil → the score consumer renormalises weight_used).
func TestUnitMismatchExcludesMethod(t *testing.T) {
	t.Run("intrinsic over 20x price withdraws the method", func(t *testing.T) {
		price := f64v(10)
		m := Method{Status: StatusAvailable, Bear: f64v(1000), Base: f64v(1000), Bull: f64v(1000)}
		out, issues := applyPlausibility(m, price, m.Base, nil, nil)
		if out.Status != StatusUnavailable {
			t.Fatalf("status = %s, want unavailable", out.Status)
		}
		if out.Base != nil {
			t.Error("base must be nil: the value is withdrawn, never clipped")
		}
		if !hasReason(out.Reasons, ReasonUnitMismatch) || !hasReason(issues, ReasonUnitMismatch) {
			t.Errorf("reasons = %v / issues = %v, want %s", out.Reasons, issues, ReasonUnitMismatch)
		}
	})

	t.Run("P/E < 1 excludes Graham and FCF yield > 100% excludes the DCF", func(t *testing.T) {
		in := goodInputs() // price 100, shares 1e9, wacc 9.5, growth 7
		in.Ticker = "UNIT-MISMATCH"
		in.EPS = f64v(500)            // P/E = 0.2 < 1
		in.FreeCashFlow = f64v(200e9) // FCF yield = 200e9/(100×1e9) = 200%
		res := Calculate(in, DefaultConfig())

		for name, m := range map[string]Method{"graham": res.Graham, "dcf": res.DCF} {
			if m.Status != StatusUnavailable {
				t.Errorf("%s = %s, want unavailable when its inputs mismatch the price units", name, m.Status)
			}
			if !hasReason(m.Reasons, ReasonUnitMismatch) {
				t.Errorf("%s reasons = %v, want %s", name, m.Reasons, ReasonUnitMismatch)
			}
		}
		if res.Graham.Base != nil || res.DCF.Base != nil {
			t.Error("withdrawn methods must carry no base value")
		}
		if !hasReason(res.Reasons, ReasonUnitMismatch) {
			t.Errorf("valuation reasons = %v, want %s (so the persisted row and the score justification can cite it)", res.Reasons, ReasonUnitMismatch)
		}
		if res.MOS.HasValue() {
			t.Errorf("no MOS may survive over withdrawn methods: %+v", res.MOS)
		}
	})
}

// TestNormalizeShareClass pins the C1 hardcoded BRK-A/BRK-B conversion and the
// SPEC formula it implements.
func TestNormalizeShareClass(t *testing.T) {
	eps, shares := NormalizeShareClass("BRK-B", f64v(30000), f64v(1450000))
	if eps == nil || shares == nil || math.Abs(*eps-20) > 1e-9 || math.Abs(*shares-1450000*1500) > 1e-9 {
		t.Errorf("BRK-B: eps = %v, shares = %v, want 20 / %v", valueOr(eps), valueOr(shares), 1450000*1500)
	}
	// A-class ticker and the fallback pass through untouched.
	if e, s := NormalizeShareClass("BRK-A", f64v(30000), f64v(1450000)); *e != 30000 || *s != 1450000 {
		t.Errorf("BRK-A must be untouched: %v / %v", *e, *s)
	}
	if e, s := NormalizeShareClass("AAPL", f64v(5), f64v(1e9)); *e != 5 || *s != 1e9 {
		t.Errorf("non-BRK must be untouched: %v / %v", *e, *s)
	}
	// Spec formula with both classes explicit matches the single-count path
	// when shares_B = 0 and proves the ratio is exactly 1500.
	epsB, sharesTot := ClassTotals(30000, 1450000, 0)
	if math.Abs(epsB-20) > 1e-9 || math.Abs(sharesTot-1450000*1500) > 1e-9 {
		t.Errorf("ClassTotals = %v / %v, want 20 / %v", epsB, sharesTot, 1450000*1500)
	}
}

// TestParseSnapshotAcceptsHistory is the R1 gate of 2.1.0: the reader decodes
// the snapshots of 2.1.0 (current) AND 2.0.0 (history) and refuses anything
// else, and PFcfFromSnapshot keeps working over the historical revision.
func TestParseSnapshotAcceptsHistory(t *testing.T) {
	in := goodInputs()
	cfg := DefaultConfig()
	res := Calculate(in, cfg)
	raw, err := MarshalSnapshot(in, cfg, res)
	if err != nil {
		t.Fatalf("MarshalSnapshot: %v", err)
	}

	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.ModelVersion != ModelVersion {
		t.Fatalf("snapshot version = %q, want %q", snap.ModelVersion, ModelVersion)
	}

	// Current revision (2.1.0) parses.
	if _, err := ParseSnapshot(raw); err != nil {
		t.Fatalf("snapshot 2.1.0 must parse: %v", err)
	}

	// Historical revision (2.0.0) parses: R1 — the history stays readable.
	snap.ModelVersion = ModelVersion20
	raw20, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSnapshot(raw20); err != nil {
		t.Fatalf("snapshot 2.0.0 must keep parsing (R1): %v", err)
	}

	// The P/FCF reader works over the history too.
	p, err := PFcfFromSnapshot(raw20)
	if err != nil {
		t.Fatalf("PFcfFromSnapshot on a 2.0.0 snapshot: %v", err)
	}
	if p == nil {
		t.Fatal("P/FCF must be computable from the 2.0.0 history snapshot")
	}

	// Unknown revisions are refused with the typed error.
	snap.ModelVersion = "9.9.9"
	rawBad, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSnapshot(rawBad); !errors.Is(err, ErrUnsupportedModelVersion) {
		t.Fatalf("unknown revision must give ErrUnsupportedModelVersion, got %v", err)
	}
}

// valueOr dereferences a pointer for error messages (nil → "<nil>").
func valueOr(v *float64) string {
	if v == nil {
		return "<nil>"
	}
	return strconv.FormatFloat(*v, 'g', 6, 64)
}
