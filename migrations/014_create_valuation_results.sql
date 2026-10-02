-- 014_create_valuation_results.sql
-- Valuation results per security (SPEC v2 §6-11, §19-21, plan M6b ADR D4). One
-- row per (security_id, as_of, model_version) with the SIX scenarios
-- (graham/dcf x bear/base/bull), the four MOS, valuation uncertainty,
-- valuation confidence and the reasons behind them.
--
-- NEW table: valuation has never been persisted (M3/M4b computed it on the
-- fly in the API and in the scores job), so there is NO backfill and NO
-- ALTER of any 1.1.0 artifact. Historical scores 1.1.0 stay untouched.
--
-- The wacc_source CHECK here is a NEW taxonomy (D9: it also admits the
-- cost_of_equity and legacy_discount_env fallbacks used as discount rate). It
-- does NOT change the 3-value CHECK of wacc_metrics (M6a taxonomy, reused
-- as-is).
--
-- Idempotent: IF NOT EXISTS.
CREATE TABLE IF NOT EXISTS valuation_results (
    id                    BIGSERIAL PRIMARY KEY,
    security_id           BIGINT NOT NULL REFERENCES securities(id),
    as_of                 DATE NOT NULL,
    available_at          DATE,          -- max(filing_date) of the facts used (§4)
    fundamentals_as_of    DATE,          -- period_end of the FY used
    price                 NUMERIC(20,4), -- valuation_price, the MOS denominator input
    currency              VARCHAR(8),

    -- Inputs echoed for auditability (M6a rows are the source of truth).
    normalized_growth_rate NUMERIC(8,4),
    growth_source         VARCHAR(32),   -- growth_metrics.source, verbatim
    growth_confidence     VARCHAR(6) CHECK (growth_confidence IN ('high','medium','low')),
    wacc                  NUMERIC(8,4),  -- WACC actually used as discount rate
    wacc_source           VARCHAR(24) NOT NULL
        CHECK (wacc_source IN ('capm_individual','capm_hybrid','cost_of_equity',
                               'configured_fallback','legacy_discount_env')),
    wacc_confidence       VARCHAR(6) CHECK (wacc_confidence IN ('high','medium','low')),

    -- §6/§8 scenarios. NULL = not computable, NEVER 0 (§21).
    graham_bear  NUMERIC(20,4) CHECK (graham_bear  IS NULL OR graham_bear  > 0),
    graham_base  NUMERIC(20,4) CHECK (graham_base  IS NULL OR graham_base  > 0),
    graham_bull  NUMERIC(20,4) CHECK (graham_bull  IS NULL OR graham_bull  > 0),
    dcf_bear     NUMERIC(20,4) CHECK (dcf_bear     IS NULL OR dcf_bear     > 0),
    dcf_base     NUMERIC(20,4) CHECK (dcf_base     IS NULL OR dcf_base     > 0),
    dcf_bull     NUMERIC(20,4) CHECK (dcf_bull     IS NULL OR dcf_bull     > 0),

    -- §11 margin of safety, in percent.
    graham_mos   NUMERIC(10,4),
    dcf_bear_mos NUMERIC(10,4),
    dcf_base_mos NUMERIC(10,4),
    dcf_bull_mos NUMERIC(10,4),
    target_mos   NUMERIC(6,2) NOT NULL DEFAULT 30.00,

    -- §19 uncertainty (population std / mean over the 4 §19 components).
    valuation_mean        NUMERIC(20,4),
    valuation_stddev      NUMERIC(20,4),
    valuation_dispersion  NUMERIC(10,6),
    valuation_components  SMALLINT NOT NULL DEFAULT 0 CHECK (valuation_components BETWEEN 0 AND 4),

    -- §10/§20/§21 status + confidence.
    valuation_status     VARCHAR(12) NOT NULL
        CHECK (valuation_status IN ('available','unavailable')),
    valuation_confidence VARCHAR(6) NOT NULL CHECK (valuation_confidence IN ('high','medium','low')),
    graham_status        VARCHAR(12) NOT NULL CHECK (graham_status IN ('available','unavailable')),
    graham_confidence    VARCHAR(6) CHECK (graham_confidence IN ('high','medium','low')),
    dcf_status           VARCHAR(12) NOT NULL CHECK (dcf_status IN ('available','unavailable')),
    dcf_confidence       VARCHAR(6) CHECK (dcf_confidence IN ('high','medium','low')),

    reasons              TEXT[] NOT NULL DEFAULT '{}',
    sensitivity          JSONB,  -- §8 WACC x growth grid
    inputs_snapshot      JSONB,  -- full Inputs + Config actually used (§26)
    model_version        TEXT NOT NULL DEFAULT '2.0.0',
    calculation_timestamp TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_valuation_results UNIQUE (security_id, as_of, model_version),
    -- A row with no single scenario would be a row with nothing to say.
    CONSTRAINT ck_valuation_results_has_value
        CHECK (graham_base IS NOT NULL OR graham_bear IS NOT NULL OR graham_bull IS NOT NULL
            OR dcf_base    IS NOT NULL OR dcf_bear    IS NOT NULL OR dcf_bull    IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_valuation_results_security_asof
    ON valuation_results(security_id, as_of DESC);