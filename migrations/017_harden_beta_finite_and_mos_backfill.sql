-- 017_harden_beta_finite_and_mos_backfill.sql
--
-- Dos correcciones de deuda técnica del hito M6c, en una sola migración
-- idempotente (todo con IF EXISTS / IF NOT EXISTS / DO block):
--
-- 1) CHECK beta FINITO (securities.beta y beta_history.beta).
--    Los CHECK de PostgreSQL son NaN-PERMEABLES: 'NaN'::numeric > 0 es UNKNOWN,
--    no FALSE, así que un CHECK (beta > 0 AND beta <= 10) ACEPTA un NaN. La
--    migración 015 documentaba el problema y confiaba en el guard de Go
--    (storage.UpdateSecurityReference) — defensa en profundidad que no era
--    defensa en profundidad, porque una escritura SQL directa (psql, un job
--    futuro, un test) lo colaba. Un NaN en beta_history es CANÓNICO (ADR D29):
--    envenena el CAPM, el WACC y el score hacia abajo, y NaN no se ve en un
--    SELECT a ojo.
--    El guard nuevo usa `beta = beta`, que es FALSE para NaN y TRUE para todo
--    número real (NaN no es igual ni a sí mismo en PostgreSQL). NOT NULL en
--    beta_history se mantiene: la columna nunca admitió NULL.
--
-- 2) Backfill de margin_of_safety en las filas de scores 2.1.0.
--    Trace21.MarginOfSafety tenía `omitempty`, así que una fila escrita con
--    MOS=0 no persistió el campo y el replay no podía distinguir "MOS
--    desactivado" de "campo ausente": tenía que inventar el default 30. El
--    campo ya NO lleva omitempty (internal/score/score21.go) y toda fila nueva
--    persiste el valor explícito. Esta migración vuelve EXPLÍCITO lo que el
--    parse ya aplicaba de forma implícita: pone 30 donde el campo no está. NO
--    cambia el resultado de ningún replay — calculateScore21 ya caía al 30
--    cuando el campo faltaba o era 0 — pero deja el trace fiel a lo que se
--    aplicó, que es lo que exige §26 (reproducibilidad/auditoría).
--    Solo toca filas 2.1.0: son las únicas cuyo trace tiene este campo.

-- ---------------------------------------------------------------------------
-- 1) CHECK beta FINITO
-- ---------------------------------------------------------------------------

-- securities.beta (constraint de la migración 011: ck_securities_beta_positive).
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint c
        JOIN pg_class t ON t.oid = c.conrelid
        WHERE t.relname = 'securities' AND c.conname = 'ck_securities_beta_positive'
    ) THEN
        ALTER TABLE securities DROP CONSTRAINT ck_securities_beta_positive;
    END IF;

    -- beta = beta es FALSE para NaN (el único valor que se auto-rechaza) y TRUE
    -- para todo número real; se combina con el rango ya documentado en 011
    -- (0 < beta <= 10). El nombre explícito permite auditar/referenciar el
    -- invariant desde el código y desde una migración futura.
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint c
        JOIN pg_class t ON t.oid = c.conrelid
        WHERE t.relname = 'securities' AND c.conname = 'securities_beta_finite_check'
    ) THEN
        ALTER TABLE securities
            ADD CONSTRAINT securities_beta_finite_check
            CHECK (beta IS NULL OR (beta = beta AND beta > 0 AND beta <= 10));
    END IF;
END
$$;

-- beta_history.beta (CHECK inline de la migración 015, sin nombre explícito).
DO $$
DECLARE
    ck_name TEXT;
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'beta_history') THEN
        -- Localiza el CHECK de la columna beta (el de la 015: beta > 0 AND
        -- beta <= 10) sea cual sea su nombre.
        SELECT c.conname INTO ck_name
        FROM pg_constraint c
        JOIN pg_class t ON t.oid = c.conrelid
        JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY (c.conkey)
        WHERE t.relname = 'beta_history'
          AND c.contype = 'c'
          AND a.attname = 'beta'
        LIMIT 1;

        IF ck_name IS NOT NULL AND ck_name <> 'beta_history_beta_finite_check' THEN
            EXECUTE format('ALTER TABLE beta_history DROP CONSTRAINT %I', ck_name);
        END IF;

        IF NOT EXISTS (
            SELECT 1 FROM pg_constraint c
            JOIN pg_class t ON t.oid = c.conrelid
            WHERE t.relname = 'beta_history' AND c.conname = 'beta_history_beta_finite_check'
        ) THEN
            -- NOT NULL se conserva (beta es NOT NULL desde la 015).
            ALTER TABLE beta_history
                ADD CONSTRAINT beta_history_beta_finite_check
                CHECK (beta = beta AND beta > 0 AND beta <= 10);
        END IF;
    END IF;
END
$$;

-- ---------------------------------------------------------------------------
-- 2) Backfill margin_of_safety = 30 en filas de scores 2.1.0 sin el campo
-- ---------------------------------------------------------------------------
--
-- Condición de fila: model_version = '2.1.0' (las únicas con trace 2.1.0) Y el
-- JSONB inputs_snapshot NO tiene la clave margin_of_safety. jsonb_set la
-- escribe al nivel raíz con el MISMO tipo que ya tenía (número), así que el
-- resto del trace no se toca. create_missing IF NOT EXISTS por si una fila
-- traía el valor a null (imposible tras quitar omitempty, pero inocuo).
--
-- Idempotente: tras aplicarse, toda fila 2.1.0 tiene la clave, así que un
-- segundo UPDATE no afecta ninguna fila.
UPDATE scores
SET inputs_snapshot = jsonb_set(
        COALESCE(inputs_snapshot, '{}'::jsonb),
        '{margin_of_safety}',
        '30'::jsonb,
        true
    )
WHERE model_version = '2.1.0'
  AND inputs_snapshot IS NOT NULL
  AND NOT (inputs_snapshot ? 'margin_of_safety');