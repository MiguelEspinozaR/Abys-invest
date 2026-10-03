-- 016: upgrade the 'conservative' parameter set to include top-level weight overrides
-- (SPEC §26, ADR D28). The seed in 015 used ON CONFLICT DO NOTHING, so a pre-existing
-- row with the OLD seed (without top-level weights) is preserved. This migration
-- updates ONLY the row that still has the OLD content, leaving operator edits alone.
--
-- OLD seed (015): {"target_margin_of_safety":40,"quality_sub_weights":{"profitability":0.20,"growth":0.10,"margins":0.10,"stability":0.30,"debt_solvency":0.30}}
-- NEW seed (015 round 2): adds top-level weights summing to 0.90 (graham .10, dcf .15, quality .40, relative .20, market_context .05)
--
-- The WHERE clause matches the EXACT old JSON to avoid overwriting any manual edit.

UPDATE parameter_sets
SET parameters = '{"target_margin_of_safety":40,
    "graham_weight":0.10,"dcf_weight":0.15,"quality_weight":0.40,
    "relative_weight":0.20,"market_context_weight":0.05,
    "quality_sub_weights":{"profitability":0.20,"growth":0.10,"margins":0.10,
                           "stability":0.30,"debt_solvency":0.30}}'::jsonb
WHERE name = 'conservative'
  AND parameters = '{"target_margin_of_safety":40,"quality_sub_weights":{"profitability":0.20,"growth":0.10,"margins":0.10,"stability":0.30,"debt_solvency":0.30}}'::jsonb;