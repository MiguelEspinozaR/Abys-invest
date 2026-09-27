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
//
// M5.1: ListWatchlist añade el último score del security (score/signal nullable)
// y ListWatchlistTickers expone los tickers guardados para el universo del
// pipeline (plan B3).
package storage

import (
	"context"
	"fmt"
)

// watchlistColumns proyecta s.id en el campo id de WatchlistItem: el id del
// security en el catálogo (ver la nota de contrato de este archivo). El resto
// de columnas vienen del catálogo y la ordenación usa la fila de watchlist.
// M5.1 añade ls.score/ls.signal (último score del security, null si no tiene)
// proyectados por el LEFT JOIN LATERAL de ListWatchlist.
const watchlistColumns = `s.id, s.ticker, s.name, s.exchange, s.sector, s.industry, w.created_at, ls.score, ls.signal`

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
//
// M5.1: el LEFT JOIN LATERAL añade score/signal del ÚLTIMO score persistido
// (ORDER BY as_of DESC, id DESC) sin N+1: una fila por security y el índice
// idx_scores_security_asof (migración 009) resuelve el lateral. Sin score, las
// columnas llegan a NULL y Scan las deja nil (null explícito en el JSON).
func ListWatchlist(ctx context.Context, q DBTX) ([]WatchlistItem, error) {
	rows, err := q.Query(ctx, `
SELECT `+watchlistColumns+`
FROM watchlist w
JOIN securities s ON s.id = w.security_id
LEFT JOIN LATERAL (
    SELECT score, signal FROM scores
    WHERE security_id = s.id
    ORDER BY as_of DESC, id DESC
    LIMIT 1
) ls ON TRUE
ORDER BY w.created_at ASC, w.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("storage: list watchlist: %w", err)
	}
	defer rows.Close()

	out := make([]WatchlistItem, 0, 8)
	for rows.Next() {
		var it WatchlistItem
		if err := rows.Scan(&it.ID, &it.Ticker, &it.Name, &it.Exchange, &it.Sector, &it.Industry, &it.CreatedAt, &it.Score, &it.Signal); err != nil {
			return nil, fmt.Errorf("storage: list watchlist scan: %w", err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list watchlist rows: %w", err)
	}
	return out, nil
}

// ListWatchlistTickers returns the tickers of the personal watchlist in
// addition order (created_at ASC, s.ticker ASC as tie-break). Never returns nil
// on success.
//
// M5.1: la usa pipeline.RefreshUniverse para que el force-refresh cubra los
// tickers guardados por el usuario aunque todavía no tengan precios en la BD
// (el caso del reporte que originó el hito). Es una lectura, no un mutador:
// cualquier caller con DBTX puede usarla.
func ListWatchlistTickers(ctx context.Context, q DBTX) ([]string, error) {
	rows, err := q.Query(ctx, `
SELECT s.ticker
FROM watchlist w
JOIN securities s ON s.id = w.security_id
ORDER BY w.created_at ASC, s.ticker ASC`)
	if err != nil {
		return nil, fmt.Errorf("storage: list watchlist tickers: %w", err)
	}
	defer rows.Close()

	out := make([]string, 0, 8)
	for rows.Next() {
		var ticker string
		if err := rows.Scan(&ticker); err != nil {
			return nil, fmt.Errorf("storage: list watchlist tickers scan: %w", err)
		}
		out = append(out, ticker)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list watchlist tickers rows: %w", err)
	}
	return out, nil
}
