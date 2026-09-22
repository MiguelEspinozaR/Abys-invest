-- 006_create_daily_prices.sql
-- Precios diarios OHLCV (fuente: Yahoo Finance v8 chart API, M2).
--
-- Hypertable TimescaleDB sobre `date` con compresión después de 30 días
-- (ADR-0002; plan §2.3). En servidores PostgreSQL puros sin la extensión
-- cargada (entorno de desarrollo, mitigación del riesgo §7) la tabla se
-- crea normal con los mismos índices y constraint UNIQUE (security_id, date);
-- la hypertable/compresión se activan posteriormente sin migración de datos,
-- ya que create_hypertable admite tablas existentes (migrate_data => true).
--
-- Idempotente: IF NOT EXISTS + DO block con guarda de extensión.

CREATE TABLE IF NOT EXISTS daily_prices (
    id              BIGSERIAL PRIMARY KEY,
    security_id     BIGINT NOT NULL REFERENCES securities(id),
    date            DATE NOT NULL,
    open            NUMERIC(12,4),
    high            NUMERIC(12,4),
    low             NUMERIC(12,4),
    close           NUMERIC(12,4) NOT NULL,
    adjusted_close  NUMERIC(12,4) NOT NULL,
    volume          BIGINT,
    source          TEXT NOT NULL DEFAULT 'yahoo',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_daily_prices UNIQUE (security_id, date)
);

CREATE INDEX IF NOT EXISTS idx_daily_prices_security_date
    ON daily_prices(security_id, date DESC);
CREATE INDEX IF NOT EXISTS idx_daily_prices_date
    ON daily_prices(date);

-- Hypertable + compresión (solo si TimescaleDB está cargada).
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        PERFORM create_hypertable('daily_prices', 'date', if_not_exists => TRUE);
        ALTER TABLE daily_prices SET (
            timescaledb.compress,
            timescaledb.compress_segmentby = 'security_id',
            timescaledb.compress_orderby = 'date DESC'
        );
        PERFORM add_compression_policy('daily_prices', INTERVAL '30 days', if_not_exists => TRUE);
        RAISE NOTICE 'daily_prices: hypertable + compresión (30 días) creadas';
    ELSE
        RAISE NOTICE 'timescaledb no disponible: daily_prices es tabla normal (hypertable/compresión al activar la extensión, plan §7)';
    END IF;
END
$$;