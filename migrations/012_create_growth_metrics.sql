-- 012_create_growth_metrics.sql
-- Growth Engine individual por empresa (SPEC v2 §5, plan M6a). Una fila por
-- (security_id, as_of, model_version) con los 6 CAGR (3y/5y de revenue, EPS y
-- FCF), la tasa normalizada y su confidence/source/flags.
--
-- Tabla normal (no hypertable), igual que scores (009): el resultado es un
-- informe estructurado (6 CAGR + confianza + source + flags), no una métrica
-- escalar; meterlo en derived_metrics destruiría la trazabilidad.
--
-- Trazabilidad §4/§26: as_of = fecha del último precio (valuation_price),
-- available_at = max(filing_date) de los hechos usados (corte sin look-ahead),
-- fundamentals_as_of = period_end del último ejercicio usado, e
-- inputs_snapshot = JSONB con los puntos anuales, la config y el resultado.
--
-- Idempotente: IF NOT EXISTS.
CREATE TABLE IF NOT EXISTS growth_metrics (
    id                     BIGSERIAL PRIMARY KEY,
    security_id            BIGINT NOT NULL REFERENCES securities(id),
    as_of                  DATE NOT NULL,
    available_at           DATE,
    fundamentals_as_of     DATE,
    revenue_cagr_3y        NUMERIC(10,4),
    revenue_cagr_5y        NUMERIC(10,4),
    eps_cagr_3y            NUMERIC(10,4),
    eps_cagr_5y            NUMERIC(10,4),
    fcf_cagr_3y            NUMERIC(10,4),
    fcf_cagr_5y            NUMERIC(10,4),
    normalized_growth_rate NUMERIC(10,4),
    growth_confidence      VARCHAR(6) NOT NULL CHECK (growth_confidence IN ('high','medium','low')),
    growth_source          VARCHAR(32) NOT NULL,
    growth_clamped         BOOLEAN NOT NULL DEFAULT FALSE,
    revenue_discrepancy    BOOLEAN NOT NULL DEFAULT FALSE,
    inputs_snapshot        JSONB,
    model_version          TEXT NOT NULL DEFAULT '1.0.0',
    calculation_timestamp  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_growth_metrics UNIQUE (security_id, as_of, model_version)
);

-- No se añade CHECK de NaN: en PostgreSQL 'NaN'::numeric = 'NaN'::numeric es
-- TRUE (NaN ordena por encima de todo número), así que un `x = x` no filtraría
-- nada. La garantía de "no NaN" la da el motor (CAGR devuelve nil si el
-- resultado no es finito) más los tests unitarios.
CREATE INDEX IF NOT EXISTS idx_growth_metrics_security_asof
    ON growth_metrics(security_id, as_of DESC);
