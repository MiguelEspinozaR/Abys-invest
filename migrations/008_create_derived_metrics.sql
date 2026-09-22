-- 008_create_derived_metrics.sql
-- Métricas derivadas materializadas (motor M2, ADR-0004). Cada fila es una
-- métrica (MetricInput -> CalculateMetrics) para (security_id, as_of) con
-- versión de fórmula y snapshot JSONB de los inputs usados (trazabilidad).
--
-- Tabla normal (no hypertable): el volumen es pequeño (8 filas por ticker
-- por fecha de cálculo) y el patrón de acceso es por security/as_of.
--
-- Idempotente: IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS derived_metrics (
    id              BIGSERIAL PRIMARY KEY,
    security_id     BIGINT NOT NULL REFERENCES securities(id),
    as_of           DATE NOT NULL,
    metric          VARCHAR(30) NOT NULL,
    value           NUMERIC(18,6),
    inputs_snapshot JSONB,
    model_version   TEXT NOT NULL DEFAULT '1.0.0',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_derived_metrics UNIQUE (security_id, as_of, metric, model_version)
);

CREATE INDEX IF NOT EXISTS idx_derived_metrics_security_asof
    ON derived_metrics(security_id, as_of DESC);
CREATE INDEX IF NOT EXISTS idx_derived_metrics_metric
    ON derived_metrics(metric, as_of DESC);