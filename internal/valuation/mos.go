package valuation

// calcMOS is §11 for one valuation I:
//
//	MOS% = 100 × (I − Price) / I
//
// It returns nil — NEVER 0 — when I is nil/non-positive (a non-positive
// intrinsic value has no interpretable margin) or when the price is missing.
// A negative MOS (price above the intrinsic value) keeps its sign: it is
// information, not an error.
func calcMOS(intrinsic, price *float64) *float64 {
	i := sane(intrinsic, true)
	if i == nil {
		return nil
	}
	if price == nil || !isFinite(*price) || *price <= 0 {
		return nil
	}
	out := 100 * (*i - *price) / *i
	if !isFinite(out) {
		return nil
	}
	return &out
}

// CalcMOS computes the four margins §11 names (graham_base, dcf_bear,
// dcf_base, dcf_bull) plus the target threshold so a client can draw the
// line. graham_bear/graham_bull deliberately have no MOS in M6b: §11 does not
// ask for them.
//
// Without a price the four are nil and the result carries the reason
// `no_price`: the job then does not persist the row (there is no comparison to
// make) and the API still returns the rest of the block.
func CalcMOS(in Inputs, g, d Method, cfg Config) MarginOfSafety {
	mos := MarginOfSafety{Target: cfg.TargetMOS}
	if in.Price == nil || !isFinite(*in.Price) || *in.Price <= 0 {
		mos.Reason = ReasonNoPrice
		return mos
	}
	mos.GrahamBase = calcMOS(g.Base, in.Price)
	mos.DCFBear = calcMOS(d.Bear, in.Price)
	mos.DCFBase = calcMOS(d.Base, in.Price)
	mos.DCFBull = calcMOS(d.Bull, in.Price)
	return mos
}

// HasValue reports whether at least one of the four margins could be computed.
// It is what the persistence layer uses to skip a row that would carry nothing.
func (m MarginOfSafety) HasValue() bool {
	return m.GrahamBase != nil || m.DCFBear != nil || m.DCFBase != nil || m.DCFBull != nil
}
