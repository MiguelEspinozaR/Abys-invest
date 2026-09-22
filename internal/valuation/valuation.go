package valuation

// ModelVersion identifies the valuation formula revision (persisted as-is).
const ModelVersion = "1.0.0"

// IntrinsicInput is the computational input set of CalcIntrinsicValue.
// Pointer fields are nil when the underlying datum was missing/not usable.
type IntrinsicInput struct {
	GrowthRate        float64 // g in percent (Graham); default 7
	EPS               *float64
	FreeCashFlow      *float64
	DCFDiscountRate   float64 // WACC in percent; default 10
	DCFHorizon        int     // projection years; default 5
	TerminalGrowth    float64 // in percent; default 2.5
	SharesOutstanding *float64
	NetDebt           *float64 // total debt − cash; nil = unknown (conservative)
}

// IntrinsicInputs is the JSON-serializable projection of IntrinsicInput.
type IntrinsicInputs struct {
	GrowthRate        float64  `json:"growth_rate"`
	EPS               *float64 `json:"eps,omitempty"`
	FreeCashFlow      *float64 `json:"free_cash_flow,omitempty"`
	DCFDiscountRate   float64  `json:"dcf_discount_rate"`
	DCFHorizon        int      `json:"dcf_horizon_years"`
	TerminalGrowth    float64  `json:"terminal_growth"`
	SharesOutstanding *float64 `json:"shares_outstanding,omitempty"`
	NetDebt           *float64 `json:"net_debt,omitempty"`
}

// IntrinsicValue is the result of CalcIntrinsicValue. Graham/DCF are nil when
// their inputs were insufficient (conservative); Consensus is the lower of
// the two when both are available (plan D4: use the most conservative), the
// single available one, or nil when neither.
type IntrinsicValue struct {
	Graham       *float64        `json:"graham,omitempty"`
	DCF          *float64        `json:"dcf,omitempty"`
	Consensus    *float64        `json:"consensus,omitempty"`
	Inputs       IntrinsicInputs `json:"inputs"`
	ModelVersion string          `json:"model_version"`
}

// CalcIntrinsicValue computes both Graham and DCF from the inputs and derives
// the conservative consensus. Deterministic: same input -> same output.
func CalcIntrinsicValue(input IntrinsicInput) IntrinsicValue {
	graham := CalcGrahamIntrinsic(input.GrowthRate, input.EPS)
	dcf := CalcDCFIntrinsic(input.FreeCashFlow, input.GrowthRate, input.DCFDiscountRate,
		input.TerminalGrowth, input.DCFHorizon, input.SharesOutstanding, input.NetDebt)

	var consensus *float64
	switch {
	case graham != nil && dcf != nil:
		if *graham <= *dcf {
			consensus = graham
		} else {
			consensus = dcf
		}
	case graham != nil:
		consensus = graham
	case dcf != nil:
		consensus = dcf
	}

	return IntrinsicValue{
		Graham:    graham,
		DCF:       dcf,
		Consensus: consensus,
		Inputs: IntrinsicInputs{
			GrowthRate:        input.GrowthRate,
			EPS:               input.EPS,
			FreeCashFlow:      input.FreeCashFlow,
			DCFDiscountRate:   input.DCFDiscountRate,
			DCFHorizon:        input.DCFHorizon,
			TerminalGrowth:    input.TerminalGrowth,
			SharesOutstanding: input.SharesOutstanding,
			NetDebt:           input.NetDebt,
		},
		ModelVersion: ModelVersion,
	}
}
