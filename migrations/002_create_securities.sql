-- 002_create_securities.sql
-- Tipos enum y tabla securities (catálogo de valores).

-- PostgreSQL no tiene CREATE TYPE IF NOT EXISTS; se usa DO block idempotente.
DO $$
BEGIN
    CREATE TYPE security_type AS ENUM ('stock', 'etf', 'bond', 'fund', 'other');
EXCEPTION
    WHEN duplicate_object THEN NULL;
END
$$;

DO $$
BEGIN
    CREATE TYPE security_status AS ENUM ('active', 'delisted', 'unknown');
EXCEPTION
    WHEN duplicate_object THEN NULL;
END
$$;

CREATE TABLE IF NOT EXISTS securities (
    id              BIGSERIAL PRIMARY KEY,
    ticker          VARCHAR(20) NOT NULL,
    cik             VARCHAR(12) NOT NULL,
    name            TEXT NOT NULL,
    type            security_type NOT NULL DEFAULT 'stock',
    currency        VARCHAR(3) NOT NULL DEFAULT 'USD',
    status          security_status NOT NULL DEFAULT 'active',
    exchange        TEXT,
    sector          TEXT,
    industry        TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_securities_ticker UNIQUE (ticker)
);

-- idx_securities_cik: el CIK es corporativo y varias clases de acciones pueden
-- compartirlo (p.ej. GOOG/GOOGL -> 0001652044); NO es único. La identidad del
-- valor es el ticker (uq_securities_ticker).
CREATE INDEX IF NOT EXISTS idx_securities_cik ON securities(cik);

CREATE INDEX IF NOT EXISTS idx_securities_status ON securities(status) WHERE status = 'active';