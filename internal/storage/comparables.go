package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/miky/abys-invest/internal/metricver"
)

// SectorComparables aggregates the peer set of a sector: how many active
// securities share the (non-null) sector and the median of each persisted
// valuation metric across those peers (SPEC §13.5.3).
type SectorComparables struct {
	SecurityCount int
	Medians       map[string]*float64
}

// ErrNoMetricVersions is returned when a reader is called without the
// metric/revision binding.
//
// The binding is MANDATORY on purpose (ADR D13, R-M6c-1): a reader that can be
// called without it is a reader that will eventually be called without it, and
// that is exactly the non-deterministic median over mixed formula revisions that
// R-M6c-1 describes. Failing loudly beats returning a plausible wrong number.
var ErrNoMetricVersions = errors.New("storage: metric revisions not informed (ADR D13)")

// GetSectorComparables returns, for a given sector, the number of peer
// securities (excluding excludeSecurityID) and per-metric sector medians
// computed over derived_metrics via percentile_cont(0.5). When fewer than
// the caller's threshold have metrics, the caller degrades the comparables
// dimension (score engine rule).
//
// pairs binds each metric to the revision that defines it and is filtered in the
// JOIN, i.e. BEFORE percentile_cont aggregates — never in a HAVING, which would
// only relabel an already mixed set (ADR D13).
func GetSectorComparables(ctx context.Context, q DBTX, sector string, excludeSecurityID int64, pairs []metricver.Pair) (SectorComparables, error) {
	out := SectorComparables{Medians: map[string]*float64{}}
	if len(pairs) == 0 {
		return out, ErrNoMetricVersions
	}
	if err := q.QueryRow(ctx, `SELECT count(*) FROM securities
		WHERE sector = $1 AND status = 'active' AND id <> $2`, sector, excludeSecurityID).
		Scan(&out.SecurityCount); err != nil {
		return out, fmt.Errorf("storage: count sector peers %q: %w", sector, err)
	}
	metricNames, modelVersions := metricver.Split(pairs)

	rows, err := q.Query(ctx, `
WITH expected AS (
    SELECT * FROM unnest($3::text[], $4::text[]) AS t(metric, model_version)
)
SELECT m.metric, percentile_cont(0.5) WITHIN GROUP (ORDER BY m.value)
FROM derived_metrics m
JOIN expected e ON e.metric = m.metric AND e.model_version = m.model_version
JOIN securities s ON s.id = m.security_id
WHERE s.sector = $1 AND s.id <> $2 AND m.value IS NOT NULL
GROUP BY m.metric`, sector, excludeSecurityID, metricNames, modelVersions)
	if err != nil {
		return out, fmt.Errorf("storage: sector median metrics %q: %w", sector, err)
	}
	defer rows.Close()
	for rows.Next() {
		var metric string
		var med *float64
		if err := rows.Scan(&metric, &med); err != nil {
			return out, fmt.Errorf("storage: scan sector median %q: %w", sector, err)
		}
		out.Medians[metric] = med
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("storage: sector median rows %q: %w", sector, err)
	}
	return out, nil
}

// GetHistoricalMedianMetrics returns the per-metric median of the security's
// own derived_metrics over the last 'years' years (SPEC §13.5.3: own history,
// 5-10 years). The window ends at the security's latest as_of.
//
// pairs binds each metric to its defining revision (ADR D13), filtered BEFORE the
// percentile as in GetSectorComparables.
func GetHistoricalMedianMetrics(ctx context.Context, q DBTX, securityID int64, years int, pairs []metricver.Pair) (map[string]*float64, error) {
	if len(pairs) == 0 {
		return nil, ErrNoMetricVersions
	}
	if years <= 0 {
		years = 5
	}
	metricNames, modelVersions := metricver.Split(pairs)
	rows, err := q.Query(ctx, `
WITH expected AS (
    SELECT * FROM unnest($3::text[], $4::text[]) AS t(metric, model_version)
),
win AS (
    SELECT max(as_of) AS max_as_of FROM derived_metrics WHERE security_id = $1
)
SELECT m.metric, percentile_cont(0.5) WITHIN GROUP (ORDER BY m.value)
FROM derived_metrics m
JOIN expected e ON e.metric = m.metric AND e.model_version = m.model_version
CROSS JOIN win w
WHERE m.security_id = $1 AND m.value IS NOT NULL
  AND m.as_of >= w.max_as_of - ($2 * INTERVAL '1 year')
GROUP BY m.metric`, securityID, years, metricNames, modelVersions)
	if err != nil {
		return nil, fmt.Errorf("storage: historical median metrics for security %d: %w", securityID, err)
	}
	defer rows.Close()

	out := map[string]*float64{}
	for rows.Next() {
		var metric string
		var med *float64
		if err := rows.Scan(&metric, &med); err != nil {
			return nil, fmt.Errorf("storage: scan historical median %d: %w", securityID, err)
		}
		out[metric] = med
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: historical median rows %d: %w", securityID, err)
	}
	return out, nil
}

// GetLastClosePrices returns the most recent 'n' daily bars of a security
// (date ascending), used for the SMA/momentum inputs of the score engine.
//
// Returns the WHOLE row on purpose (the caller decides which price it needs):
// the score's trend dimension reads `adjusted_close`, while the valuation uses
// the raw `close` of the last bar as the valuation price (§22). Keeping both in
// the result is what lets both consumers be explicit about which one they use
// instead of assuming.
func GetLastClosePrices(ctx context.Context, q DBTX, securityID int64, n int) ([]DailyPrice, error) {
	if n <= 0 {
		n = 400
	}
	rows, err := q.Query(ctx, `SELECT `+dailyPriceColumns+` FROM (
		SELECT `+dailyPriceColumns+` FROM daily_prices
		WHERE security_id = $1 ORDER BY date DESC LIMIT $2) t
		ORDER BY date ASC`, securityID, n)
	if err != nil {
		return nil, fmt.Errorf("storage: last close prices for security %d: %w", securityID, err)
	}
	defer rows.Close()

	var out []DailyPrice
	for rows.Next() {
		var p DailyPrice
		if err := scanDailyPrice(rows, &p); err != nil {
			return nil, fmt.Errorf("storage: last close prices scan %d: %w", securityID, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: last close prices rows %d: %w", securityID, err)
	}
	return out, nil
}

// HistoricalMedian is the result of GetHistoricalMedianWithCount.
type HistoricalMedian struct {
	Medians map[string]*float64 `json:"medians"`
	// AsOfCount is how many DISTINCT as_of contributed to the median. It is the
	// difference between "its own history agrees with me" and "one single day of
	// history agrees with me": a median of one point is not a median, and §16 needs
	// to be able to say so.
	AsOfCount int `json:"as_of_count"`
}

// GetHistoricalMedianWithCount is GetHistoricalMedianMetrics with two differences
// that the pipeline of M6c needs:
//
//  1. it returns the number of distinct as_of behind the medians, and
//  2. its window is bounded ABOVE by asOf.
//
// The second one is the NO LOOK-AHEAD rule (ADR D14) applied to the historical
// median. The M6b function took `max(as_of)` as the end of the window, which is
// correct only when the caller wants the newest history; for a score dated
// as_of = 2026-08-20, a median that included 2026-12-31 would silently inform a
// past score. Bounding the window is what makes a replayed score the same score.
func GetHistoricalMedianWithCount(ctx context.Context, q DBTX, securityID int64, asOf time.Time, years int, pairs []metricver.Pair) (HistoricalMedian, error) {
	var out HistoricalMedian
	if len(pairs) == 0 {
		return out, ErrNoMetricVersions
	}
	if years <= 0 {
		years = 5
	}
	metricNames, modelVersions := metricver.Split(pairs)
	rows, err := q.Query(ctx, `
WITH expected AS (
    SELECT * FROM unnest($3::text[], $4::text[]) AS t(metric, model_version)
),
win AS (
    SELECT max(as_of) AS max_as_of FROM derived_metrics
    WHERE security_id = $1 AND as_of <= $5
)
SELECT m.metric,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY m.value),
       count(DISTINCT m.as_of)
FROM derived_metrics m
JOIN expected e ON e.metric = m.metric AND e.model_version = m.model_version
CROSS JOIN win w
WHERE m.security_id = $1 AND m.value IS NOT NULL AND m.as_of <= $5
  AND m.as_of >= w.max_as_of - ($2 * INTERVAL '1 year')
GROUP BY m.metric`, securityID, years, metricNames, modelVersions, asOf)
	if err != nil {
		return out, fmt.Errorf("storage: historical median with count for security %d: %w", securityID, err)
	}
	defer rows.Close()

	out.Medians = map[string]*float64{}
	for rows.Next() {
		var metric string
		var med *float64
		var n int
		if err := rows.Scan(&metric, &med, &n); err != nil {
			return out, fmt.Errorf("storage: scan historical median with count %d: %w", securityID, err)
		}
		out.Medians[metric] = med
		// El conteo es por métrica; se queda el MÁXIMO observado: es el número de
		// días que respaldan la mediana más poblada, y un máximo (no un promedio)
		// evita que una métrica con 1 punto "diluya" el respaldo de otra con 20.
		if n > out.AsOfCount {
			out.AsOfCount = n
		}
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("storage: historical median with count rows %d: %w", securityID, err)
	}
	return out, nil
}
