-- 011_add_securities_beta.sql
-- Beta observada de Yahoo (defaultKeyStatistics) como dato de REFERENCIA por
-- security, junto a sector/industry, que ya persiste el mismo fetch en la misma
-- llamada quoteSummary (plan D17). Es catálogo, NO un resultado de cálculo: el
-- WACC evaluado vive en wacc_metrics (migración 013).
-- beta_updated_at fecha la observación (trazabilidad §26: el CAPM solo es
-- reproducible si se sabe con qué beta se calculó y cuándo se obtuvo).
--
-- Idempotente: ADD COLUMN IF NOT EXISTS. No requiere backfill: las betas se
-- rellenan en el siguiente RunSectorJob (job de sector, el único camino de red
-- con sesión crumb), y una respuesta de Yahoo sin defaultKeyStatistics NO borra
-- la última beta conocida (COALESCE en UpdateSecurityReference).

ALTER TABLE securities
    ADD COLUMN IF NOT EXISTS beta            NUMERIC(8,4),
    ADD COLUMN IF NOT EXISTS beta_updated_at TIMESTAMPTZ;

-- Una beta solo es creíble si es positiva y está acotada (Yahoo puede devolver
-- valores absurdos); KeyStatsFor ya lo garantiza, pero el CHECK blinda la
-- columna contra escrituras directas. 0 y negativos se guardan como NULL, no
-- como un 0 que luego parecería una beta observada.
ALTER TABLE securities
    DROP CONSTRAINT IF EXISTS ck_securities_beta_positive;
ALTER TABLE securities
    ADD CONSTRAINT ck_securities_beta_positive CHECK (beta IS NULL OR (beta > 0 AND beta <= 10));
