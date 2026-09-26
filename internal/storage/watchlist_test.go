//go:build integration

package storage

import (
	"context"
	"testing"
)

// M5 integration coverage for the watchlist table (migration 010): idempotent
// add/remove, listing in addition order, FK integrity and ON DELETE CASCADE.
//
// Run with: DATABASE_URL=... go test ./internal/storage/... -tags=integration -count=1

// seedSecurity inserta un security de fixture y devuelve su id.
func seedSecurity(t *testing.T, ticker, name string) int64 {
	t.Helper()
	sec, err := UpsertSecurity(context.Background(), testPool, &Security{
		Ticker: ticker, CIK: "0000000000", Name: name,
		Type: "stock", Currency: "USD", Status: "active",
	})
	if err != nil {
		t.Fatalf("UpsertSecurity(%s): %v", ticker, err)
	}
	return sec.ID
}

// securityIDByTicker resuelve el id del catálogo por ticker (la semántica real
// contra la que se compara WatchlistItem.ID).
func securityIDByTicker(t *testing.T, ticker string) int64 {
	t.Helper()
	sec, err := GetSecurityByTicker(context.Background(), testPool, ticker)
	if err != nil {
		t.Fatalf("GetSecurityByTicker(%s): %v", ticker, err)
	}
	return sec.ID
}

// watchlistRowID devuelve la PK de la fila de watchlist (w.id) del security. Se
// usa para comprobar que WatchlistItem.ID NO es ese valor (si lo fuera, ambos
// coincidirían solo por casualidad de secuencias tras el TRUNCATE ... RESTART
// IDENTITY del helper de la suite).
func watchlistRowID(t *testing.T, securityID int64) int64 {
	t.Helper()
	var id int64
	if err := testPool.QueryRow(context.Background(),
		`SELECT id FROM watchlist WHERE security_id = $1`, securityID).Scan(&id); err != nil {
		t.Fatalf("SELECT id FROM watchlist WHERE security_id=%d: %v", securityID, err)
	}
	return id
}

func TestWatchlistAddIdempotent(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	// Security señuelo que queda en el catálogo pero fuera de la watchlist: hace
	// que securities.id y watchlist.id NO coincidan, así la aserción de abajo
	// discrimina el contrato real en vez de pasar por coincidencia de
	// secuencias (el TRUNCATE ... RESTART IDENTITY reinicia ambas).
	seedSecurity(t, "M5WTF", "M5 Watchlist Decoy")
	idAAPL := seedSecurity(t, "M5WTA", "M5 Watchlist A")
	if err := AddToWatchlist(ctx, pool, idAAPL); err != nil {
		t.Fatalf("AddToWatchlist: %v", err)
	}
	// Re-alta: no duplica ni erroriza (ON CONFLICT DO NOTHING).
	if err := AddToWatchlist(ctx, pool, idAAPL); err != nil {
		t.Fatalf("AddToWatchlist repetido falló (debe ser idempotente): %v", err)
	}

	items, err := ListWatchlist(ctx, pool)
	if err != nil {
		t.Fatalf("ListWatchlist: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("se esperaba 1 entrada, hay %d", len(items))
	}
	if items[0].Ticker != "M5WTA" || items[0].Name != "M5 Watchlist A" {
		t.Fatalf("entrada inesperada: %+v", items[0])
	}
	// Contrato F1: ID es el securities.id (semántica real, no coincidencia de
	// ids) y explícitamente no el id de la fila de watchlist.
	wantID := securityIDByTicker(t, "M5WTA")
	if items[0].ID != wantID {
		t.Fatalf("WatchlistItem.ID debe ser securities.id: got %d want %d", items[0].ID, wantID)
	}
	rowID := watchlistRowID(t, wantID)
	if rowID == wantID {
		t.Fatalf("fixture inválido: watchlist.id y securities.id coinciden (%d), la aserción no discrimina", rowID)
	}
	if items[0].ID == rowID {
		t.Fatalf("WatchlistItem.ID no debe ser el id de la fila de watchlist: %+v (watchlist.id=%d)", items[0], rowID)
	}
	if items[0].CreatedAt.IsZero() {
		t.Fatal("created_at no debe venir vacío")
	}
}

// TestWatchlistItemIDStableAcrossReAdd (hallazgo F1 de la REVIEW de M5):
// WatchlistItem.ID es el securities.id, así que sobrevive a una baja + re-alta.
// Con el id de la fila de watchlist (w.id) este test fallaría: el re-alta
// insertaría una fila nueva con otra PK.
func TestWatchlistItemStableIDAcrossReAdd(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	idTarget := seedSecurity(t, "M5WTA", "M5 Watchlist A")
	if err := AddToWatchlist(ctx, pool, idTarget); err != nil {
		t.Fatalf("AddToWatchlist: %v", err)
	}
	first, err := ListWatchlist(ctx, pool)
	if err != nil {
		t.Fatalf("ListWatchlist (antes): %v", err)
	}
	if len(first) != 1 || first[0].ID != idTarget {
		t.Fatalf("precondición: 1 entrada con ID=securities.id, got %+v", first)
	}
	firstRowID := watchlistRowID(t, idTarget)

	// Baja + re-alta del mismo valor.
	if err := RemoveFromWatchlist(ctx, pool, idTarget); err != nil {
		t.Fatalf("RemoveFromWatchlist: %v", err)
	}
	if err := AddToWatchlist(ctx, pool, idTarget); err != nil {
		t.Fatalf("AddToWatchlist (re-alta): %v", err)
	}
	second, err := ListWatchlist(ctx, pool)
	if err != nil {
		t.Fatalf("ListWatchlist (después): %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("se esperaba 1 entrada tras la re-alta, hay %d", len(second))
	}
	if second[0].ID != first[0].ID {
		t.Fatalf("el id debe ser estable entre baja y re-alta: %d → %d", first[0].ID, second[0].ID)
	}
	if second[0].ID != securityIDByTicker(t, "M5WTA") {
		t.Fatalf("el id debe seguir siendo securities.id, got %d", second[0].ID)
	}
	if watchlistRowID(t, idTarget) == firstRowID {
		t.Fatalf("la PK de la fila de watchlist no cambió (%d): la re-alta no reinsertó, el test no discrimina w.id", firstRowID)
	}
}

func TestWatchlistRemoveIdempotent(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	idAAPL := seedSecurity(t, "M5WTA", "M5 Watchlist A")
	if err := AddToWatchlist(ctx, pool, idAAPL); err != nil {
		t.Fatalf("AddToWatchlist: %v", err)
	}
	if err := RemoveFromWatchlist(ctx, pool, idAAPL); err != nil {
		t.Fatalf("RemoveFromWatchlist: %v", err)
	}
	// Baja repetida de algo ya ausente: no erroriza (idempotente).
	if err := RemoveFromWatchlist(ctx, pool, idAAPL); err != nil {
		t.Fatalf("RemoveFromWatchlist repetido falló (debe ser idempotente): %v", err)
	}

	items, err := ListWatchlist(ctx, pool)
	if err != nil {
		t.Fatalf("ListWatchlist: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("se esperaba watchlist vacía, hay %d entradas", len(items))
	}
}

// TestWatchlistListOrder: el listado respeta el orden de adición
// (created_at ASC, con id ASC como desempate).
func TestWatchlistListOrder(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	order := []struct{ ticker, name string }{
		{"M5WTA", "M5 Watchlist A"},
		{"M5WTB", "M5 Watchlist B"},
		{"M5WTC", "M5 Watchlist C"},
	}
	for _, s := range order {
		if err := AddToWatchlist(ctx, pool, seedSecurity(t, s.ticker, s.name)); err != nil {
			t.Fatalf("AddToWatchlist(%s): %v", s.ticker, err)
		}
	}

	items, err := ListWatchlist(ctx, pool)
	if err != nil {
		t.Fatalf("ListWatchlist: %v", err)
	}
	if len(items) != len(order) {
		t.Fatalf("se esperaban %d entradas, hay %d", len(order), len(items))
	}
	for i, want := range order {
		if items[i].Ticker != want.ticker {
			t.Fatalf("posición %d: se esperaba %s, hay %s (orden de adición no respetado: %+v)",
				i, want.ticker, items[i].Ticker, items)
		}
	}
}

// TestWatchlistForeignKey: security_id debe existir en el catálogo (FK) y el
// borrado de un security limpia su entrada (ON DELETE CASCADE).
func TestWatchlistForeignKey(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	idAAPL := seedSecurity(t, "M5WTA", "M5 Watchlist A")
	if err := AddToWatchlist(ctx, pool, idAAPL); err != nil {
		t.Fatalf("AddToWatchlist: %v", err)
	}

	// FK violada: security inexistente → error (la API lo evita con un 404).
	if err := AddToWatchlist(ctx, pool, 999999999); err == nil {
		t.Fatal("AddToWatchlist con security_id inexistente debe fallar por FK")
	}

	if _, err := pool.Exec(ctx, `DELETE FROM securities WHERE id = $1`, idAAPL); err != nil {
		t.Fatalf("delete security: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM watchlist WHERE security_id = $1`, idAAPL).Scan(&n); err != nil {
		t.Fatalf("count watchlist: %v", err)
	}
	if n != 0 {
		t.Fatalf("ON DELETE CASCADE no limpió la watchlist: quedan %d filas", n)
	}
}
