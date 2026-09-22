-- 009_create_scores.sql
-- Scores de valoración persistidos (motor M3, ADR-0004). Cada fila es el
-- resultado determinista del score 0-100 para (security_id, as_of,
-- model_version): score, señal comprar/mantener/vender, justificación
-- textual y snapshot JSONB de los inputs usados (trazabilidad).
--
-- Tabla normal (no hypertable): volumen pequeño (1 fila por ticker/fecha).
-- Las alertas se implementan íntegramente en M4 (decisión del usuario
-- 2026-09-22); aquí solo el score, sin tabla alerts.
--
-- Idempotente: IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS scores (
    id              BIGSERIAL PRIMARY KEY,
    security_id     BIGINT NOT NULL REFERENCES securities(id),
    as_of           DATE NOT NULL,
    score           SMALLINT NOT NULL CHECK (score >= 0 AND score <= 100),
    signal          VARCHAR(10) NOT NULL CHECK (signal IN ('comprar', 'mantener', 'vender')),
    justification   TEXT NOT NULL,
    inputs_snapshot JSONB,
    model_version   TEXT NOT NULL DEFAULT '1.0.0',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_scores UNIQUE (security_id, as_of, model_version)
);

CREATE INDEX IF NOT EXISTS idx_scores_security_asof
    ON scores(security_id, as_of DESC);
CREATE INDEX IF NOT EXISTS idx_scores_signal
    ON scores(signal, as_of DESC);