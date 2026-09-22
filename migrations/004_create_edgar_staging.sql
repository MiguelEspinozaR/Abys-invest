-- 004_create_edgar_staging.sql
-- Staging de datos crudos SEC EDGAR (JSONB).

CREATE TABLE IF NOT EXISTS edgar_staging (
    id              BIGSERIAL PRIMARY KEY,
    cik             VARCHAR(12) NOT NULL,
    ticker          VARCHAR(20),
    accession       TEXT NOT NULL,
    form_type       VARCHAR(20) NOT NULL,
    filing_date     DATE NOT NULL,
    period_end      DATE,
    payload         JSONB NOT NULL,
    payload_type    VARCHAR(30) NOT NULL,
    ingested_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    normalized      BOOLEAN NOT NULL DEFAULT false,
    CONSTRAINT uq_edgar_staging_accession
        UNIQUE (accession, form_type, payload_type)
);

CREATE INDEX IF NOT EXISTS idx_edgar_staging_pending ON edgar_staging(normalized)
    WHERE normalized = false;