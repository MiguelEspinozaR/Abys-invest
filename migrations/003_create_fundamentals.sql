-- 003_create_fundamentals.sql
-- Hechos XBRL normalizados al diccionario canónico.

CREATE TABLE IF NOT EXISTS fundamentals (
    id              BIGSERIAL PRIMARY KEY,
    security_id     BIGINT NOT NULL REFERENCES securities(id),
    concept         TEXT NOT NULL,
    value           NUMERIC,
    unit            VARCHAR(20),
    period_type     VARCHAR(10) NOT NULL,
    period_start    DATE,
    period_end      DATE NOT NULL,
    fiscal_year     SMALLINT,
    fiscal_period   VARCHAR(4),
    filing_date     DATE,
    source          TEXT NOT NULL DEFAULT 'sec_edgar',
    source_fact_id  TEXT,
    raw_value       TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_fundamentals_canonical
        UNIQUE (security_id, concept, period_type, period_end, fiscal_year, fiscal_period)
);

CREATE INDEX IF NOT EXISTS idx_fundamentals_security ON fundamentals(security_id, period_end DESC);
CREATE INDEX IF NOT EXISTS idx_fundamentals_concept  ON fundamentals(concept, period_end DESC);