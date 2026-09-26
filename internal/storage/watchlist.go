// Watchlist personal (plan M5): alta/baja/listado sobre la tabla `watchlist`
// (migración 010), que referencia securities(id) con UNIQUE(security_id).
// Las tres operaciones son idempotentes porque la API expone PUT/DELETE
// idempotentes (SPEC §11bis CA-M5-2).
//
// Contrato de WatchlistItem.ID: el listado proyecta s.id (el id del security en
// el catálogo), NO w.id (la PK de la fila de watchlist). Así el id es el mismo
// que devuelve GET /securities/search y el que usa scores.security_id, y es
// estable entre bajas y re-altas del mismo valor (con w.id cambiaría en cada
// re-alta). Decisión del orquestador 2026-09-25 (hallazgo F1 de la REVIEW de M5).
package storage

import (
	"context"
	"fmt"
)

// watchlistColumns proyecta s.id en el campo id de WatchlistItem: el id del
// security en el catálogo (ver la nota de contrato de este archivo). El resto
// de columnas vienen del catálogo y la ordenación usa la fila de watchlist.
const watchlistColumns = `s.id, s.ticker, s.name, s.exchange, s.sector, s.industry, w.created_at`

// AddToWatchlist adds a security to the watchlist. Re-adding a security already
// in the list is a no-op (ON CONFLICT DO NOTHING), never an error. A
// securityID outside the catalog fails with the FK violation.
func AddToWatchlist(ctx context.Context, q DBTX, securityID int64) error {
	_, err := q.Exec(ctx, `
INSERT INTO watchlist (security_id)
VALUES ($1)
ON CONFLICT (security_id) DO NOTHING`, securityID)
	if err != nil {
		return fmt.Errorf("storage: add to watchlist security %d: %w", securityID, err)
	}
	return nil
}

// RemoveFromWatchlist deletes the watchlist entry of a security. Removing one
// that is not in the list is a no-op (idempotent), never an error.
func RemoveFromWatchlist(ctx context.Context, q DBTX, securityID int64) error {
	_, err := q.Exec(ctx, `DELETE FROM watchlist WHERE security_id = $1`, securityID)
	if err != nil {
		return fmt.Errorf("storage: remove from watchlist security %d: %w", securityID, err)
	}
	return nil
}

// ListWatchlist returns the watchlist joined with the catalog detail, ordered
// by addition order (created_at ASC, w.id ASC as tie-break for rows inserted in
// the same transaction/clock tick). Never returns nil on success.
//
// ID is the securities.id of the joined row (see the package note), so it
// matches the id that GET /securities/search returns for the same ticker.
func ListWatchlist(ctx context.Context, q DBTX) ([]WatchlistItem, error) {
	rows, err := q.Query(ctx, `
SELECT `+watchlistColumns+`
FROM watchlist w
JOIN securities s ON s.id = w.security_id
ORDER BY w.created_at ASC, w.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("storage: list watchlist: %w", err)
	}
	defer rows.Close()

	out := make([]WatchlistItem, 0, 8)
	for rows.Next() {
		var it WatchlistItem
		if err := rows.Scan(&it.ID, &it.Ticker, &it.Name, &it.Exchange, &it.Sector, &it.Industry, &it.CreatedAt); err != nil {
			return nil, fmt.Errorf("storage: list watchlist scan: %w", err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list watchlist rows: %w", err)
	}
	return out, nil
}
