-- 007_create_macro_series.sql
-- Series macro US (fuente: BLS Public API v2, M2). Serie inicial: CPI-U
-- CUSR0000SA0. El esquema es genérico: una fila por (series_code, date),
-- con metadatos de unidad, frecuencia y procedencia (ADR-0003).
--
-- Hypertable TimescaleDB sobre `date` con compresión después de 90 días
-- (ADR-0002; plan §2.3). Igual fallback que 006 en PostgreSQL puro:
-- tabla normal, convertible a hypertable más adelante sin perder datos.
--
-- Idempotente: IF NOT EXISTS + DO block con guarda de extensión.

CREATE TABLE IF NOT EXISTS macro_series (
    id              BIGSERIAL PRIMARY KEY,
    series_code     VARCHAR(50) NOT NULL,
    date            DATE NOT NULL,
    value           NUMERIC(16,4) NOT NULL,
    unit            VARCHAR(30) NOT NULL DEFAULT 'index',
    frequency       VARCHAR(20) NOT NULL DEFAULT 'monthly',
    source          TEXT NOT NULL DEFAULT 'bls',
    source_id       TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_macro_series UNIQUE (series_code, date)
);

CREATE INDEX IF NOT EXISTS idx_macro_series_code_date
    ON macro_series(series_code, date DESC);
CREATE INDEX IF NOT EXISTS idx_macro_series_date
    ON macro_series(date);

-- Hypertable + compresión (solo si TimescaleDB está cargada).
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        PERFORM create_hypertable('macro_series', 'date', if_not_exists => TRUE);
        ALTER TABLE macro_series SET (
            timescaledb.compress,
            timescaledb.compress_segmentby = 'series_code',
            timescaledb.compress_orderby = 'date DESC'
        );
        PERFORM add_compression_policy('macro_series', INTERVAL '90 days', if_not_exists => TRUE);
        RAISE NOTICE 'macro_series: hypertable + compresión (90 días) creadas';
    ELSE
        RAISE NOTICE 'timescaledb no disponible: macro_series es tabla normal (hypertable/compresión al activar la extensión, plan §7)';
    END IF;
END
$$;