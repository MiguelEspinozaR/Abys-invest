-- 010_create_watchlist.sql
-- Watchlist personal persistida en PostgreSQL (plan M5, SPEC §11bis CA-M5-2).
-- App de un solo usuario (SPEC §2.3 no-goals: sin multi-usuario ni auth) →
-- lista única global, sin columna user_id.
--
-- Cada fila referencia un valor del catálogo (FK ON DELETE CASCADE: si el
-- security desaparece del catálogo, su entrada de watchlist desaparece con él)
-- y es única por security (uq_watchlist_security) para que el alta sea
-- idempotente (INSERT ... ON CONFLICT DO NOTHING).
--
-- Idempotente: IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS watchlist (
    id          BIGSERIAL PRIMARY KEY,
    security_id BIGINT NOT NULL REFERENCES securities(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_watchlist_security UNIQUE (security_id)
);

CREATE INDEX IF NOT EXISTS idx_watchlist_created ON watchlist(created_at);
