// Package compare implements the M3 comparables endpoints domain logic:
//   - the sector peer set and the security's own historical valuation
//     (SPEC §13.5.3) → /compare/comparables, /compare/history;
//   - the normalized asset comparison and risk metrics (plan T6/D8,
//     CompareAssets + NormalizePerformance in normalize.go) → /compare.
package compare

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
)

// DefaultPeersLimit caps the peer rows returned for a comparison.
const DefaultPeersLimit = 10

// ValueMetric is the canonical (dominion-column) metric we expose in
// comparisons, aligned with the score engine keys.
type ValueMetric struct {
	Metric string   `json:"metric"`
	Value  *float64 `json:"value,omitempty"`
}

// Peer is a sector peer with its latest derived metrics.
type Peer struct {
	ID      int64         `json:"id"`
	Ticker  string        `json:"ticker"`
	Metrics []ValueMetric `json:"metrics,omitempty"`
}

// ComparablesResult is the /compare/comparables response body.
type ComparablesResult struct {
	Ticker     string              `json:"ticker"`
	Sector     string              `json:"sector,omitempty"`
	Peers      []Peer              `json:"peers"`
	PeerCount  int                 `json:"peer_count"`
	Medians    map[string]*float64 `json:"medians,omitempty"`
	SecurityID int64               `json:"security_id"`
}

// ComputeComparables resolves the sector peer set for a ticker (limit caps
// the returned rows; peers are ordered by ticker for determinism). A security
// without sector returns an empty result (validation happens in the handler).
func ComputeComparables(ctx context.Context, pool *pgxpool.Pool, ticker string, limit int) (*ComparablesResult, error) {
	if limit <= 0 {
		limit = DefaultPeersLimit
	}
	sec, err := storage.GetSecurityByTicker(ctx, pool, ticker)
	if err != nil {
		return nil, err // pgx.ErrNoRows → handler 404
	}
	res := &ComparablesResult{
		Ticker: sec.Ticker, Sector: deref(sec.Sector),
		SecurityID: sec.ID, Peers: []Peer{}, Medians: map[string]*float64{},
	}
	if sec.Sector == nil || *sec.Sector == "" {
		return res, nil
	}

	sc, err := storage.GetSectorComparables(ctx, pool, *sec.Sector, sec.ID)
	if err != nil {
		return nil, fmt.Errorf("compare: comparables sector %q: %w", *sec.Sector, err)
	}
	res.PeerCount = sc.SecurityCount
	res.Medians = sc.Medians

	rows, err := pool.Query(ctx, `
SELECT s.id, s.ticker
FROM securities s
WHERE s.sector = $1 AND s.status = 'active' AND s.id <> $2
ORDER BY s.ticker
LIMIT $3`, *sec.Sector, sec.ID, limit)
	if err != nil {
		return nil, fmt.Errorf("compare: peers de sector %q: %w", *sec.Sector, err)
	}
	defer rows.Close()

	for rows.Next() {
		var p Peer
		if err := rows.Scan(&p.ID, &p.Ticker); err != nil {
			return nil, fmt.Errorf("compare: scan peer: %w", err)
		}
		metrics, err := storage.GetLatestMetrics(ctx, pool, p.ID)
		if err != nil && err != pgx.ErrNoRows {
			return nil, fmt.Errorf("compare: métricas del peer %s: %w", p.Ticker, err)
		}
		p.Metrics = toValueMetrics(metrics)
		res.Peers = append(res.Peers, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("compare: rows de peers: %w", err)
	}
	return res, nil
}

// HistoryResult is the /compare/history response body: the security's own
// daily closes over the requested lookback.
type HistoryResult struct {
	Ticker     string         `json:"ticker"`
	SecurityID int64          `json:"security_id"`
	Years      int            `json:"years"`
	Rows       []HistoryPrice `json:"rows"`
}

// HistoryPrice is one daily close (adjusted) within the history snapshot.
type HistoryPrice struct {
	Date  time.Time `json:"date"`
	Close float64   `json:"close"`
}

// ComputeHistory returns the daily closes of a ticker for the last 'years'
// calendar years (ordered ascending). No data → empty rows (validation 404 in
// the handler).
func ComputeHistory(ctx context.Context, pool *pgxpool.Pool, ticker string, years int) (*HistoryResult, error) {
	if years <= 0 {
		years = 5
	}
	sec, err := storage.GetSecurityByTicker(ctx, pool, ticker)
	if err != nil {
		return nil, err
	}
	from := time.Now().UTC().AddDate(-years, 0, 0)
	prices, err := storage.GetDailyPricesBySecurity(ctx, pool, sec.ID, from, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("compare: precios de %s: %w", ticker, err)
	}
	out := &HistoryResult{Ticker: ticker, SecurityID: sec.ID, Years: years, Rows: []HistoryPrice{}}
	for _, p := range prices {
		out.Rows = append(out.Rows, HistoryPrice{Date: p.Date, Close: p.AdjustedClose})
	}
	return out, nil
}

func toValueMetrics(dms []storage.DerivedMetric) []ValueMetric {
	out := make([]ValueMetric, 0, len(dms))
	for _, dm := range dms {
		out = append(out, ValueMetric{Metric: dm.Metric, Value: dm.Value})
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
