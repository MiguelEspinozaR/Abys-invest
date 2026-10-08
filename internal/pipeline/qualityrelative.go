package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/metricver"
	"github.com/miky/abys-invest/internal/modelcfg"
	"github.com/miky/abys-invest/internal/quality"
	"github.com/miky/abys-invest/internal/relative"
	"github.com/miky/abys-invest/internal/storage"
	"github.com/miky/abys-invest/internal/valuation"
)

// This file is the M6c staging of §13 (quality) and §16 (relative) into the
// pipeline, and the wiring of the score engine (2.1.0 history / 2.2.0 current)
// that consumes them (B9, B10, B11).
//
// WHY THERE IS NO quality_metrics / relative_results TABLE
// -------------------------------------------------------
// The SPEC persists neither, and /health of B15 counts 14 tables: adding two
// would break that contract for no reader. Quality and relative are FUNCTIONS of
// rows that are already versioned (growth_metrics, derived_metrics 2.0.0,
// valuation_results 2.0.0, fundamentals), so their persisted form is the score's
// trace — inputs_snapshot of scores, written by the score job.
//
// That is a real design constraint, not a shortcut: it means the engines are
// recomputed whenever a score is recomputed, and it means the ONLY durable record
// of a quality result is the trace. Hence the trace gate of ADR D27 matters twice
// here, and hence a `-job quality` pass with no scores job produces diagnostics,
// not a persisted row. The jobs below exist to make the stage order explicit and
// to make the degradation counts visible.

// qualityConcepts are the EDGAR concepts the quality engine reads (§13).
// net_debt is absent from the catalogue as a stored concept on some filings but
// is listed so the lookup is explicit; absent concepts simply come back nil.
var qualityConcepts = []string{
	"operating_income", "shareholders_equity", "total_debt", "cash_and_equivalents",
	"net_debt", "ebitda", "revenues", "free_cash_flow", "net_earnings",
	"total_liabilities", "total_assets", "shares_outstanding",
	"interest_expense", "income_tax_expense", "pretax_income",
}

// qualitySeries are the annual series of §13's stability block.
var qualitySeries = []string{quality.SeriesEPSDiluted, quality.SeriesFreeCashFlow}

// EngineResults holds what the quality and relative stages computed for one
// security. It is passed between stages instead of recomputed, so a single pass
// of `all` computes each engine exactly ONCE per as_of (§27 determinism).
type EngineResults struct {
	Quality  *quality.Result
	Relative *relative.Result
	// SectorCount is kept because it is the fact that decides whether the sector
	// side of relative is usable; logging it is how a silent degradation becomes a
	// visible one.
	SectorCount int
	// Degraded lists the securities for which at least one engine returned a nil
	// score, i.e. the dimension will be excluded from the score by §18.
	Degraded []string
}

// ComputeQualityStage builds and evaluates the quality engine of §13 for one
// security at asOf.
//
// Every datum is read AS OF asOf (ADR D14): fundamentals via
// GetLatestFYFundamentalsAsOf, the growth CAGRs via GetGrowthMetricAsOf. Reading
// the CAGRs with `latest` here would have been the single easiest way to
// introduce look-ahead into the whole milestone, and it would have been invisible:
// the number is plausible, just from the future.
func ComputeQualityStage(ctx context.Context, pool *pgxpool.Pool, sec storage.Security, asOf time.Time, price float64, mc modelcfg.ModelConfig) (*quality.Result, error) {
	funds, _, err := storage.GetLatestFYFundamentalsAsOf(ctx, pool, sec.ID, qualityConcepts, asOf)
	if err != nil {
		return nil, fmt.Errorf("fundamentals para quality: %w", err)
	}
	fySeries, err := storage.GetFYAnnualSeries(ctx, pool, sec.ID, append([]string{"operating_income", "interest_expense", "income_tax_expense", "pretax_income"}, qualitySeries...), asOf, 330, 400)
	if err != nil {
		return nil, fmt.Errorf("series anuales para quality: %w", err)
	}
	series := fySeries
	var gi quality.GrowthInputs
	if g, err := storage.GetGrowthMetricAsOf(ctx, pool, sec.ID, asOf); err == nil {
		gi = quality.GrowthInputs{
			FCFCAGR3y: g.FCFCAGR3y, FCFCAGR5y: g.FCFCAGR5y,
			EPSCAGR3y: g.EPSCAGR3y, EPSCAGR5y: g.EPSCAGR5y,
			RevenueCAGR3y: g.RevenueCAGR3y, Confidence: g.Confidence,
		}
	} else if err != pgx.ErrNoRows {
		return nil, fmt.Errorf("growth_metrics para quality: %w", err)
	}
	qcfg := quality.ConfigFromModelConfig(mc)
	aligned := alignedFYFacts(sec.Ticker, fySeries, asOf, qcfg.FYMaxAgeDays)
	in := quality.Inputs{
		Ticker:                 sec.Ticker,
		AsOf:                   asOf,
		Financials:             funds,
		Series:                 toQualitySeries(series),
		Growth:                 gi,
		Price:                  price,
		SharesOutstanding:      funds["shares_outstanding"],
		MarketCap:              funds["market_cap"],
		TaxRateSource:          quality.TaxRateSourceConfigured,
		InterestReason:         aligned.InterestReason,
		AlignedInterestExpense: aligned.InterestExpense,
		AlignedOperatingIncome: aligned.OperatingIncome,
	}
	// Az4: the ONE observed tax rate of the ticker. The source stays
	// `configured` (set in the literal above) for every rejected or degraded
	// pair, and the reason travels so the score can say why.
	if rate, rateReason := alignedTaxRate(aligned); rateReason == "" {
		t := rate
		in.TaxRate = &t
		in.TaxRateSource = quality.TaxRateSourceDerived
	} else {
		in.TaxRateReason = rateReason
	}
	res := quality.Calculate(in, qcfg)
	return &res, nil
}

// ComputeRelativeStage builds and evaluates the relative engine of §16.
//
// The medians are the version-filtered ones (ADR D13) and the historical window
// is bounded by asOf, so the two sides of the comparison are comparable: a
// relative score computed from a sector median of 2.0.0 metrics against a
// historical median of 1.0.0 metrics is not a valuation, it is a subtraction of
// two unrelated definitions.
func ComputeRelativeStage(ctx context.Context, pool *pgxpool.Pool, sec storage.Security, asOf time.Time, pFcf *float64, mc modelcfg.ModelConfig) (*relative.Result, int, error) {
	mts, err := storage.GetDerivedMetricsBySecurity(ctx, pool, sec.ID, asOf)
	if err != nil {
		return nil, 0, fmt.Errorf("derived_metrics para relative: %w", err)
	}
	in := relative.Inputs{Ticker: sec.Ticker, AsOf: asOf, Metrics: map[string]*float64{}}
	for _, dm := range mts {
		in.Metrics[dm.Metric] = dm.Value
	}
	// ADR D9: p_fcf belongs to valuation_results 2.0.0 and is reused, never
	// recomputed. Without a valuation row the metric is simply absent.
	if pFcf != nil {
		in.Metrics[relative.MetricPFCF] = pFcf
	}
	sectorCount := 0
	if sec.Sector != nil && *sec.Sector != "" {
		sc, err := storage.GetSectorComparables(ctx, pool, *sec.Sector, sec.ID, metricver.AllPairs())
		if err != nil {
			return nil, 0, fmt.Errorf("comparables de sector: %w", err)
		}
		sectorCount = sc.SecurityCount
		in.SectorMedian, in.SectorCount = sc.Medians, sc.SecurityCount
	}
	hm, err := storage.GetHistoricalMedianWithCount(ctx, pool, sec.ID, asOf, mc.RelativeHistoricalYears, metricver.AllPairs())
	if err != nil {
		return nil, 0, fmt.Errorf("mediana histórica: %w", err)
	}
	in.HistoricalMedian, in.HistoricalAsOfCount = hm.Medians, hm.AsOfCount

	res := relative.Calculate(in, relative.ConfigFromModelConfig(mc))
	return &res, sectorCount, nil
}

// toQualitySeries converts the storage series to the engine's shape.
func toQualitySeries(in map[string][]storage.FYPoint) map[string][]quality.SeriesPoint {
	out := map[string][]quality.SeriesPoint{}
	for concept, pts := range in {
		sp := make([]quality.SeriesPoint, 0, len(pts))
		for _, p := range pts {
			sp = append(sp, quality.SeriesPoint{PeriodEnd: p.PeriodEnd, Value: p.Value})
		}
		out[concept] = sp
	}
	return out
}

// valuationPFcf returns the p_fcf of the persisted 2.0.0 row, or nil when there is
// no valuation for this as_of (a legitimate state: the dimension degrades, §18
// renormalises).
func valuationPFcf(ctx context.Context, pool *pgxpool.Pool, sec storage.Security, asOf time.Time) (*float64, error) {
	vr, err := storage.GetValuationResultAsOf(ctx, pool, sec.ID, asOf, valuation.ModelVersion)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("valuation_results: %w", err)
	}
	if len(vr.InputsSnapshot) == 0 {
		return nil, nil
	}
	// B12: el P/FCF de §15 sale del snapshot de la MISMA fila 2.0.0 (ADR D9 lo
	// reusa, no se redefine). Un snapshot que esta build no puede leer NO se
	// degrada a "sin p_fcf": se omite la métrica y el motivo es explícito, para
	// que el peso de la cobertura diga por qué.
	p, perr := valuation.PFcfFromSnapshot(vr.InputsSnapshot)
	if perr != nil {
		slog.Warn("relative: p_fcf no derivable del snapshot de valuation",
			"ticker", sec.Ticker, "as_of", asOf.Format("2006-01-02"), "error", perr)
		return nil, nil
	}
	return p, nil
}

// RunQualityJob is the standalone `-job quality` diagnostic: it evaluates the
// engine and reports how many securities are usable.
//
// It does not write a quality row, because there is no quality row to write (see
// the header). What it DOES do is make the degradation visible per security, which
// is the whole point of running it alone after a fundamentals backfill.
func RunQualityJob(ctx context.Context, pool *pgxpool.Pool, securities []storage.Security, dryRun bool) (usable, degraded int) {
	mc := modelcfg.ModelConfigFromEnv()
	for _, sec := range securities {
		priceRow, err := storage.GetLatestPrice(ctx, pool, sec.ID)
		if err != nil {
			slog.Warn("quality: sin precio", "ticker", sec.Ticker, "error", err)
			degraded++
			continue
		}
		res, err := ComputeQualityStage(ctx, pool, sec, priceRow.Date, priceRow.Close, mc)
		if err != nil {
			slog.Warn("quality no evaluable (continúa)", "ticker", sec.Ticker, "error", err)
			degraded++
			continue
		}
		if res.Score == nil {
			degraded++
			slog.Info("quality sin score (se excluye de §18)", "ticker", sec.Ticker,
				"coverage", res.Coverage, "reasons", res.Reasons)
			continue
		}
		usable++
		if dryRun {
			// CA-4: the degradation reason of the TICKER has to be capturable from
			// this line (GE/JNJ say tax_rate_stale here), and the interest coverage
			// shows NULL when the aligned pair could not be used (P2-4).
			slog.Info("quality (dry-run)", "ticker", sec.Ticker, "score", *res.Score,
				"coverage", res.Coverage, "confidence", res.Confidence,
				"tax_rate_source", res.TaxRateSource, "version", res.ModelVersion,
				"reasons", res.Reasons,
				"interest_coverage", formatValue(res.Metrics[quality.MetricInterestCoverage]))
		}
	}
	slog.Info("job quality terminado", "usables", usable, "degradados", degraded, "total", len(securities))
	return usable, degraded
}

// RunRelativeJob is the standalone `-job relative` diagnostic. Same contract as
// RunQualityJob.
func RunRelativeJob(ctx context.Context, pool *pgxpool.Pool, securities []storage.Security, dryRun bool) (usable, degraded int) {
	mc := modelcfg.ModelConfigFromEnv()
	for _, sec := range securities {
		priceRow, err := storage.GetLatestPrice(ctx, pool, sec.ID)
		if err != nil {
			slog.Warn("relative: sin precio", "ticker", sec.Ticker, "error", err)
			degraded++
			continue
		}
		pFcf, err := valuationPFcf(ctx, pool, sec, priceRow.Date)
		if err != nil {
			slog.Warn("relative: valuation ilegible", "ticker", sec.Ticker, "error", err)
			degraded++
			continue
		}
		res, sectorCount, err := ComputeRelativeStage(ctx, pool, sec, priceRow.Date, pFcf, mc)
		if err != nil {
			slog.Warn("relative no evaluable (continúa)", "ticker", sec.Ticker, "error", err)
			degraded++
			continue
		}
		if res.Score == nil {
			degraded++
			slog.Info("relative sin score (se excluye de §18)", "ticker", sec.Ticker,
				"sector_peers", sectorCount, "hist_as_of", res.PeerCount, "reasons", res.Reasons)
			continue
		}
		usable++
		if dryRun {
			slog.Info("relative (dry-run)", "ticker", sec.Ticker, "score", *res.Score,
				"sector", res.SectorScore, "historical", res.HistoricalScore,
				"coverage", res.Coverage, "confidence", res.Confidence, "version", res.ModelVersion)
		}
	}
	slog.Info("job relative terminado", "usables", usable, "degradados", degraded, "total", len(securities))
	return usable, degraded
}
