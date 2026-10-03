-- 015: parameter sets (SPEC §26, ADR D20/D28) and beta with history (ADR D29, Az8).
--
-- IDEMPOTENT by construction: every statement is IF NOT EXISTS / ON CONFLICT DO
-- NOTHING / DROP-then-ADD, so the file can be applied twice (and on a partially
-- migrated database) without error.
--
-- PARTIAL ROLLBACK ORDER (as the plan requires, sections 1-3 independent of 4):
--   · sections 1-3 are about the IDENTITY of a score and can be reverted with
--     `ALTER TABLE scores DROP COLUMN parameter_set_id` + restoring `uq_scores`.
--   · section 4 (beta_history) is independent of 1-3 and can be dropped alone
--     (`DROP TABLE beta_history`) without touching parameter_sets.
--   Section 4 does NOT depend on 1-3, and 1-3 do NOT depend on 4: apply order is
--   irrelevant, which is what makes a partial revert possible.

-- ---------------------------------------------------------------------------
-- 1) Parameter Sets
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS parameter_sets (
    id            BIGSERIAL PRIMARY KEY,
    name          VARCHAR(64) NOT NULL UNIQUE,
    model_version TEXT NOT NULL,
    parameters    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- NULL on both: no backfill. scores 1.1.0/2.0.0 and the three valuation_results
-- 2.0.0 PREDATE parameter sets, and rewriting their identity would rewrite history
-- (ADR: "M6c agrega versiones, no reescribe").
ALTER TABLE scores            ADD COLUMN IF NOT EXISTS parameter_set_id BIGINT REFERENCES parameter_sets(id);
ALTER TABLE valuation_results ADD COLUMN IF NOT EXISTS parameter_set_id BIGINT REFERENCES parameter_sets(id);

-- ---------------------------------------------------------------------------
-- 2) §26: the parameter set is part of the IDENTITY of the result
--
-- The UNIQUE gains a 4th column so two parameter sets can coexist in the SAME
-- as_of without overwriting each other.
--
-- PostgreSQL treats NULLs as DISTINCT in a UNIQUE constraint, so the 4-column
-- constraint ALONE would let two legacy rows (parameter_set_id IS NULL) collide
-- freely. `uq_scores_legacy_null` closes exactly that hole with a partial unique
-- index: 1.1.0/2.0.0 keep their (security_id, as_of, model_version) uniqueness.
-- ---------------------------------------------------------------------------
ALTER TABLE scores DROP CONSTRAINT IF EXISTS uq_scores;
ALTER TABLE scores ADD CONSTRAINT uq_scores
    UNIQUE (security_id, as_of, model_version, parameter_set_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_scores_legacy_null
    ON scores (security_id, as_of, model_version) WHERE parameter_set_id IS NULL;

-- ---------------------------------------------------------------------------
-- 3) seeds (ADR D28)
--
-- base         = "use env/defaults": parameters {} means NO overrides, so the
--                deployed configuration is not frozen into the database.
-- conservative = explicit overrides: a higher margin of safety, a stability/
--                solvency tilt inside quality, AND top-level weight overrides
--                that sum to 0.90 (§18 strict: graham .10, dcf .15, quality .40,
--                relative .20, market_context .05). With AAPL (graham 0, dcf 0)
--                the score moves via quality/relative/market_context (P0-2).
-- ON CONFLICT (name) DO NOTHING: an operator who edited a set keeps their edit.
-- ---------------------------------------------------------------------------
INSERT INTO parameter_sets (name, model_version, parameters) VALUES
  ('base', '2.1.0', '{}'::jsonb),
  ('conservative', '2.1.0', '{"target_margin_of_safety":40,
      "graham_weight":0.10,"dcf_weight":0.15,"quality_weight":0.40,
      "relative_weight":0.20,"market_context_weight":0.05,
      "quality_sub_weights":{"profitability":0.20,"growth":0.10,"margins":0.10,
                             "stability":0.30,"debt_solvency":0.30}}'::jsonb)
ON CONFLICT (name) DO NOTHING;

-- ---------------------------------------------------------------------------
-- 4) beta with history (ADR D29, Az8)
--
-- `securities.beta` stays as the CACHE of the last observation (read by M6a
-- code paths untouched); beta_history is the canonical series. The invariant
-- cache == last row is maintained by UpdateSecurityReference in ONE transaction
-- and has its own test.
--
-- CHECK (beta > 0 AND beta <= 10) replicates the policy of securities.beta
-- (M6a-F6): a negative or absurd beta is a provider bug, and storing it would
-- silently corrupt every CAPM downstream.
-- NOTE: PostgreSQL CHECK constraints are NaN-permeable (NaN > 0 is UNKNOWN,
-- not FALSE). The Go guard in storage.UpdateSecurityReference rejects NaN/Inf
-- before insert, so the DB never sees them. The CHECK is defense-in-depth for
-- direct SQL inserts.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS beta_history (
    id          BIGSERIAL PRIMARY KEY,
    security_id BIGINT NOT NULL REFERENCES securities(id),
    as_of       DATE NOT NULL,
    beta        NUMERIC(10,6) NOT NULL CHECK (beta > 0 AND beta <= 10),
    source      VARCHAR(32) NOT NULL DEFAULT 'yahoo',
    fetched_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_beta_history UNIQUE (security_id, as_of)
);
CREATE INDEX IF NOT EXISTS idx_beta_history_security_asof
    ON beta_history (security_id, as_of DESC);
