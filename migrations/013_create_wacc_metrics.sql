-- 013_create_wacc_metrics.sql
-- WACC individual CAPM por empresa (SPEC v2 §7, plan M6a). Una fila por
-- (security_id, as_of, model_version) con los componentes del CAPM, los pesos
-- E/D observados y la marca de procedencia. Tabla normal, como scores.
--
-- Taxonomía de 3 niveles (plan D7): capm_individual (todo observado) /
-- capm_hybrid (beta y E/D observados, Rf/ERP/Kd/tax de configuración — el caso
-- normal de M6a, porque no hay proveedor de Rf/ERP ni conceptos EDGAR de
-- impuestos/intereses) / configured_fallback (sin beta observada o sin E/D →
-- WACC_FALLBACK). beta + beta_observed + beta_updated_at dejan auditable de
-- dónde salió el input que manda en el CAPM.
--
-- Idempotente: IF NOT EXISTS.
CREATE TABLE IF NOT EXISTS wacc_metrics (
    id                     BIGSERIAL PRIMARY KEY,
    security_id            BIGINT NOT NULL REFERENCES securities(id),
    as_of                  DATE NOT NULL,
    available_at           DATE,
    equity_value           NUMERIC(20,4),
    debt_value             NUMERIC(20,4),
    beta                   NUMERIC(8,4),
    beta_observed          BOOLEAN NOT NULL DEFAULT FALSE,
    beta_updated_at        TIMESTAMPTZ,
    cost_of_equity         NUMERIC(8,4),
    cost_of_debt_pretax    NUMERIC(8,4),
    cost_of_debt_after_tax NUMERIC(8,4),
    tax_rate               NUMERIC(8,4),
    risk_free_rate         NUMERIC(8,4),
    equity_risk_premium    NUMERIC(8,4),
    wacc                   NUMERIC(8,4),
    weight_equity          NUMERIC(8,6),
    weight_debt            NUMERIC(8,6),
    wacc_source            VARCHAR(24) NOT NULL
        CHECK (wacc_source IN ('capm_individual','capm_hybrid','configured_fallback')),
    wacc_confidence        VARCHAR(6) NOT NULL CHECK (wacc_confidence IN ('high','medium','low')),
    inputs_snapshot        JSONB,
    model_version          TEXT NOT NULL DEFAULT '1.0.0',
    calculation_timestamp  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Coherencia de la taxonomía con la procedencia real de los inputs: no se
    -- puede declarar capm_hybrid/capm_individual con una beta no observada, que
    -- es exactamente lo que la degradación a configured_fallback significa.
    CONSTRAINT ck_wacc_source_beta
        CHECK (wacc_source = 'configured_fallback' OR beta_observed),
    CONSTRAINT uq_wacc_metrics UNIQUE (security_id, as_of, model_version)
);

CREATE INDEX IF NOT EXISTS idx_wacc_metrics_security_asof
    ON wacc_metrics(security_id, as_of DESC);
