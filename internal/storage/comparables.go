package storage

import (
	"context"
	"fmt"
)

// SectorComparables aggregates the peer set of a sector: how many active
// securities share the (non-null) sector and the median of each persisted
// valuation metric across those peers (SPEC §13.5.3).
type SectorComparables struct {
	SecurityCount int
	Medians       map[string]*float64
}

// GetSectorComparables returns, for a given sector, the number of peer
// securities (excluding excludeSecurityID) and per-metric sector medians
// computed over derived_metrics via percentile_cont(0.5). When fewer than
// the caller's threshold have metrics, the caller degrades the comparables
// dimension (score engine rule).
func GetSectorComparables(ctx context.Context, q DBTX, sector string, excludeSecurityID int64) (SectorComparables, error) {
	out := SectorComparables{Medians: map[string]*float64{}}
	if err := q.QueryRow(ctx, `SELECT count(*) FROM securities
		WHERE sector = $1 AND status = 'active' AND id <> $2`, sector, excludeSecurityID).
		Scan(&out.SecurityCount); err != nil {
		return out, fmt.Errorf("storage: count sector peers %q: %w", sector, err)
	}

	rows, err := q.Query(ctx, `
SELECT m.metric, percentile_cont(0.5) WITHIN GROUP (ORDER BY m.value)
FROM derived_metrics m
JOIN securities s ON s.id = m.security_id
WHERE s.sector = $1 AND s.id <> $2 AND m.value IS NOT NULL
GROUP BY m.metric`, sector, excludeSecurityID)
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
func GetHistoricalMedianMetrics(ctx context.Context, q DBTX, securityID int64, years int) (map[string]*float64, error) {
	if years <= 0 {
		years = 5
	}
	rows, err := q.Query(ctx, `
SELECT metric, percentile_cont(0.5) WITHIN GROUP (ORDER BY value)
FROM derived_metrics
WHERE security_id = $1 AND value IS NOT NULL
  AND as_of >= (SELECT max(as_of) FROM derived_metrics WHERE security_id = $1) - ($2 * INTERVAL '1 year')
GROUP BY metric`, securityID, years)
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

// GetLastClosePrices returns the most recent 'n' daily closes of a security
// (date ascending), used for SMA/momentum inputs of the score engine.
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
