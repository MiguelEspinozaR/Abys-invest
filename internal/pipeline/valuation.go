package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
	"github.com/miky/abys-invest/internal/valuation"
)

// valuationResultCounts resume una pasada del job de valoración (plan C1). Los
// tres contadores son OBSERVABLES distintos y no deben colapsarse:
//
//	Inserted         = fila 2.0.0 persistida
//	SkippedNoPrice   = sin precio ⇒ sin fila (§11 no tiene margen que calcular)
//	Unavailable      = ningún método con valor ⇒ sin fila + reason logueado
type valuationResultCounts struct {
	Inserted       int
	SkippedNoPrice int
	Unavailable    int
}

// runValuationJob computes and persists valuation_results (014) for every
// target security. It is an ISOLATED job (like M6a's growth job) for two
// reasons: it lets a valuation be recomputed without recomputing the metrics,
// and it makes the "no valuation is computable" case visible instead of hidden
// inside the scores job.
//
// It is network-free (fundamentals, prices, growth_metrics, wacc_metrics only),
// which is why it is also part of POST /refresh.
func runValuationJob(ctx context.Context, pool *pgxpool.Pool, securities []storage.Security, dryRun bool) valuationResultCounts {
	var counts valuationResultCounts
	cfg := valuation.ConfigFromEnv() // ADR D6: the engine never reads the env itself

	for _, sec := range securities {
		inserted, err := runValuationOne(ctx, pool, sec, cfg, dryRun)
		switch {
		case err == nil && inserted:
			counts.Inserted++
		case errors.Is(err, errNoPrice):
			counts.SkippedNoPrice++
			slog.Info("valoración omitida (sin precio)", "ticker", sec.Ticker)
		case err == nil:
			// No valuation available: the row would carry nothing, and the CHECK
			// of 014 forbids it anyway. The reason is logged by runValuationOne.
			counts.Unavailable++
		default:
			counts.Unavailable++
			slog.Error("valoración falló (continúa)", "ticker", sec.Ticker, "error", err)
		}
	}

	slog.Info("job valuation terminado",
		"insertadas", counts.Inserted, "sin_precio", counts.SkippedNoPrice,
		"sin_valoracion", counts.Unavailable, "total", len(securities), "dry_run", dryRun)
	return counts
}

// errNoPrice marks a security without a price: without it there is no as_of and
// no margin of safety, so no row is written (not an error, not a failure).
var errNoPrice = errors.New("pipeline: sin precio")

// runValuationOne computes and persists the valuation of ONE security.
//
// The sequence is fixed by plan C1 and by §4 (no look-ahead): price → as_of,
// then the persisted growth and WACC (never recomputed here: M6b WIRES, it does
// not duplicate), then the FY fundamentals AS OF that date (M6a-F1), then the
// pure engine, then one transaction per row.
//
// It returns (persisted, error). `persisted` is false when there was no price or
// no computable valuation, which is NOT an error: it is a real, counted answer.
func runValuationOne(ctx context.Context, pool *pgxpool.Pool, sec storage.Security, cfg valuation.Config, dryRun bool) (bool, error) {
	// 1) Price ⇒ as_of. Identical source as growth/wacc/scores so all the
	//    artifacts of a security share one date.
	priceRow, err := storage.GetLatestPrice(ctx, pool, sec.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, errNoPrice
		}
		return false, fmt.Errorf("precio: %w", err)
	}
	asOf := priceRow.Date

	// 2) Growth and WACC come from the M6a rows, never from the env.
	growth, err := storage.GetLatestGrowthMetric(ctx, pool, sec.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("growth_metrics: %w", err)
	}
	wacc, err := storage.GetLatestWaccMetric(ctx, pool, sec.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("wacc_metrics: %w", err)
	}

	// 3) FY fundamentals with a temporal cut (F1): no 10-K filed after as_of can
	//    inform a valuation dated as_of.
	funds, avail, err := storage.GetLatestFYFundamentalsAsOf(ctx, pool, sec.ID, valuation.ValuationConcepts, asOf)
	if err != nil {
		return false, fmt.Errorf("fundamentales FY as-of: %w", err)
	}

	// 4) Canonical concepts → Inputs. Every derivation is nil-safe: a missing
	//    input leaves the pointer nil and the method reports its reason (§21).
	facts := valuation.DeriveFacts(funds)
	in := valuation.Inputs{
		Ticker:            sec.Ticker,
		AsOf:              asOf,
		Price:             &priceRow.Close,
		EPS:               facts.EPS,
		FreeCashFlow:      facts.FreeCashFlow,
		SharesOutstanding: funds["shares_outstanding"],
		NetDebt:           facts.NetDebt,
	}
	if growth != nil {
		in.NormalizedGrowthRate = growth.NormalizedGrowthRate
		in.GrowthConfidence = valuation.Confidence(growth.Confidence)
		in.GrowthSource = growth.Source
		in.GrowthModelVersion = growth.ModelVersion
	}
	if wacc != nil {
		in.WACC = wacc.Wacc
		in.CostOfEquity = wacc.CostOfEquity
		in.WACCSource = wacc.Source
		in.WACCConfidence = valuation.Confidence(wacc.Confidence)
		in.WACCModelVersion = wacc.ModelVersion
		in.BetaObserved = wacc.BetaObserved
	}

	res := valuation.Calculate(in, cfg)

	// 5) No valuation at all ⇒ no row. The CHECK ck_valuation_results_has_value
	//    would reject it, and a row with six NULL scenarios would be noise.
	if res.Status == valuation.StatusUnavailable {
		slog.Info("sin valoración (motivos)", "ticker", sec.Ticker,
			"as_of", asOf.Format("2006-01-02"), "motivos", res.Reasons)
		return false, nil
	}

	row := valuationRow(sec, priceRow, asOf, avail, in, cfg, res)

	if dryRun {
		slog.Info("valoración (dry-run)", "ticker", sec.Ticker,
			"as_of", asOf.Format("2006-01-02"),
			"graham_base", formatValue(res.Graham.Base), "dcf_base", formatValue(res.DCF.Base),
			"confidence", string(res.Confidence), "wacc_used", formatValue(res.Inputs.WACCUsed),
			"wacc_source", res.Inputs.DiscountSource, "motivos", res.Reasons)
		return false, nil
	}

	// 6) One row, one transaction: growth/wacc are never touched here.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin tx valoración: %w", err)
	}
	if err := storage.UpsertValuationResult(ctx, tx, row); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return false, fmt.Errorf("persistir valoración: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit valoración: %w", err)
	}

	slog.Info("valoración calculada y persistida", "ticker", sec.Ticker,
		"as_of", asOf.Format("2006-01-02"),
		"graham_base", formatValue(res.Graham.Base), "dcf_base", formatValue(res.DCF.Base),
		"confidence", string(res.Confidence), "model_version", valuation.ModelVersion)
	return true, nil
}

// valuationRow maps the PURE Result into the 014 row: no recomputation, no
// interpretation. It is separated from runValuationOne so the mapping can be
// unit-tested without a database.
func valuationRow(sec storage.Security, priceRow *storage.DailyPrice, asOf time.Time, avail storage.FYAvailability,
	in valuation.Inputs, cfg valuation.Config, res valuation.Result) *storage.ValuationResult {

	row := &storage.ValuationResult{
		SecurityID:          sec.ID,
		AsOf:                asOf,
		FundamentalsAsOf:    nilTime(avail.FundamentalsAsOf),
		AvailableAt:         nilTime(avail.AvailableAt),
		Price:               &priceRow.Close,
		Currency:            &sec.Currency,
		GrahamBear:          res.Graham.Bear,
		GrahamBase:          res.Graham.Base,
		GrahamBull:          res.Graham.Bull,
		DcfBear:             res.DCF.Bear,
		DcfBase:             res.DCF.Base,
		DcfBull:             res.DCF.Bull,
		GrahamMos:           res.MOS.GrahamBase,
		DcfBearMos:          res.MOS.DCFBear,
		DcfBaseMos:          res.MOS.DCFBase,
		DcfBullMos:          res.MOS.DCFBull,
		TargetMos:           res.MOS.Target,
		ValuationMean:       res.Uncertainty.Mean,
		ValuationStddev:     res.Uncertainty.StdDev,
		ValuationDispersion: res.Uncertainty.Dispersion,
		ValuationComponents: int16(res.Uncertainty.Components),
		ValuationStatus:     string(res.Status),
		ValuationConfidence: string(res.Confidence),
		GrahamStatus:        string(res.Graham.Status),
		DcfStatus:           string(res.DCF.Status),
		Reasons:             res.Reasons,
		ModelVersion:        res.ModelVersion,
	}
	if row.Reasons == nil {
		// reasons is NOT NULL in the schema with default '{}': a nil slice would
		// insert NULL and fail the insert for a perfectly valid valuation.
		row.Reasons = []string{}
	}
	if res.Graham.Confidence != "" {
		g := string(res.Graham.Confidence)
		row.GrahamConfidence = &g
	}
	if res.DCF.Confidence != "" {
		c := string(res.DCF.Confidence)
		row.DcfConfidence = &c
	}

	// Inputs echoed: growth_metrics/wacc_metrics stay the source of truth.
	row.NormalizedGrowthRate = in.NormalizedGrowthRate
	if in.GrowthSource != "" {
		src := in.GrowthSource
		row.GrowthSource = &src
	}
	if in.GrowthConfidence != "" {
		gc := string(in.GrowthConfidence)
		row.GrowthConfidence = &gc
	}

	// Discount rate ACTUALLY used and its provenance. wacc_source is NOT NULL
	// with a CHECK of 5 values: when NO rate was resolved at all (level 0, the
	// DCF unavailable) the honest value inside the taxonomy is
	// configured_fallback with a NULL wacc, and the reason `wacc_configured` is
	// already in res.Reasons. Inventing a rate here would be exactly what §7
	// forbids.
	row.WaccSource = res.Inputs.DiscountSource
	if row.WaccSource == "" {
		row.WaccSource = valuation.WACCSourceConfiguredFB
	}
	if res.Inputs.WACCUsed != nil {
		row.Wacc = res.Inputs.WACCUsed
		// The confidence of the discount rate only belongs to the row when the
		// rate came from the WACC row itself (level 1); a configured fallback has
		// no wacc_metrics confidence to echo.
		if res.Inputs.DiscountLevel == 1 && in.WACCConfidence != "" {
			wc := string(in.WACCConfidence)
			row.WaccConfidence = &wc
		}
	}

	// §8 grid and §26 snapshot (inputs + the FULL config).
	if len(res.Sensitivity) > 0 {
		if b, err := json.Marshal(res.Sensitivity); err == nil {
			row.Sensitivity = b
		} else {
			slog.Warn("sensibilidad no serializable (continúa)", "ticker", sec.Ticker, "error", err)
		}
	}
	if b, err := valuation.MarshalSnapshot(in, cfg, res); err == nil {
		row.InputsSnapshot = b
	} else {
		slog.Warn("snapshot no serializable (continúa)", "ticker", sec.Ticker, "error", err)
	}
	return row
}

// nilTime returns nil for the zero time: a missing date must be NULL in the DB,
// not 0001-01-01 (which would break every "when was it available" query).
func nilTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// isNaN reports whether the value is NaN (the engine uses NaN internally to mark
// "no discount rate resolved").
func isNaN(v float64) bool { return v != v }
