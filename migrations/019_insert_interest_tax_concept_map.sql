-- 019_insert_interest_tax_concept_map.sql
--
-- Las TRES filas documentales del trío que faltaba en el diccionario canónico:
-- `interest_expense`, `income_tax_expense` y `pretax_income`. Sube el
-- diccionario de 20 a 23 conceptos canónicos (los 20 de la migración 005 más
-- estos 3).
--
-- CAUSA. M6c cerró con `interest_coverage = nil` en TODO el universo y
-- `income_tax_expense` inexistente, y la causa no era el motor: era que el
-- catálogo XBRL (internal/collect/edgar/concepts.go) nunca mapeó los tags de
-- interés ni de impuesto. `interest_coverage` es EBIT/interés y, sin interés
-- canónico, el motor hacía bien en devolver nil en vez de inventar un
-- denominador; y sin `income_tax_expense` + `pretax_income` la tasa fiscal
-- efectiva no se puede OBSERVAR, sólo CONFIGURAR (ADR D26: el tope de
-- `quality.confidence` a `medium` justamente por `tax_rate_source`).
-- Fechado read-only sobre `edgar_staging` (44 emisores): los 10 tags de Az1
-- están presentes en el dato real — InterestExpense 38,
-- InterestExpenseNonoperating 26, InterestExpenseDebt 14, InterestAndDebtExpense 4,
-- InterestExpenseDebtExcludingAmortization 3, IncomeTaxExpenseBenefit 44,
-- IncomeTaxExpenseBenefitContinuingOperations 8,
-- …BeforeIncomeTaxesExtraordinaryItemsNoncontrollingInterest 36,
-- …BeforeIncomeTaxesMinorityInterestAndIncomeLossFromEquityMethodInvestments 31,
-- ResultsOfOperationsIncomeBeforeIncomeTaxes 2. Es decir: la cobertura que
-- falta es del CATÁLOGO, no de la FUENTE.
--
-- POR QUÉ NO HAY `ALTER TABLE`. `fundamentals` ya es genérico desde la
-- migración 003 (concept, value, unit, fiscal_year, period_start/end,
-- filing_date, source_fact_id): el trío nuevo no necesita una columna por
-- concepto, ni una vista, ni un índice propio. Añadir DDL aquí sería
-- estructura para un problema que no existe. La tabla `xbrl_concept_map` es el
-- DICCIONARIO PERSISTIDO para consulta y auditoría (`/health` la cuenta); el
-- mapeo que decide de verdad vive en Go, en `conceptMap`, y esta migración lo
-- documenta en el mismo sitio donde ya estaban los otros 20.
--
-- POR QUÉ NO HAY BACKFILL. `fundamentals` NO se toca: las empresas ya
-- ingeridas conservan su `source_fact_id` viejo y sus filas canónicas
-- actuales, y backfillear el trío aquí exigiría re-canonizar (elegir entre tags
-- por prioridad y por periodo) desde SQL, que es exactamente el camino que W3
-- descarta por trazabilidad: un hecho escrito sin pasar por `dedupeCanonical`
-- deja un `source_fact_id` que no corresponde al tag ganador, y el §26 del plan
-- exige que cada entrada sea reproducible desde su fuente. Lo puebla la
-- RE-INGESTA de W3 (`make reingest-fundamentals`, `-fresh` en cmd/collector),
-- que borra el staging por CIK y vuelve a pasar cada payload por el
-- canonizador. Es idempotente y trazable por construcción: el `source_fact_id`
-- que acaba en `fundamentals` es el del tag que el canonizador eligió.
--
-- LA FORMA DEL `ON CONFLICT`. `ON CONFLICT (xbrl_concept, canonical_name)` casa
-- EXACTAMENTE con la constraint real de la 005,
-- `uq_xbrl_concept_map UNIQUE (xbrl_concept, canonical_name)` (verificado en
-- `pg_constraint`, no supuesto): las dos columnas y en ese orden, que es lo que
-- PostgreSQL exige para que la inferencia sea válida. Un `ON CONFLICT DO
-- NOTHING` sin objetivo habría coaxializado con cualquier constraint futura
-- (una exclusion, un índice único nuevo) y es justo el tipo de acoplamiento que
-- hace que una segunda pasada de RunMigrations falle en lugar de ser un no-op.
-- `RunMigrations` (internal/storage/db.go) no lleva libro de migraciones:
-- ejecuta TODOS los `.sql` del directorio en orden, dos veces en el test de
-- idempotencia, así que la idempotencia tiene que estar EN el SQL.
--
-- EXCLUSIONES (documentadas aquí porque son decisiones, y las decisiones que no
-- están escritas se revierten sin querer):
--   * `InterestIncomeExpenseNet` (7/44) — es el resultado NETO de interés e
--     ingreso y puede ser NEGATIVO (WMT FY2024): no es un gasto bruto y daría
--     un `interest_coverage` con el signo cambiado en vez de nil.
--   * `IncomeLossFromContinuingOperationsBeforeIncomeTaxesForeign` (38/44) y
--     `…Domestic` (36/44) — desglose GEOGRÁFICO del mismo total: elegir uno es
--     un subtotal y sumarlos duplicaría el total (que ya entra por su tag).
--   * `Deferred*` / `Current*` / `Federal*` / `StateAndLocal*` / `Foreign*`
--     `IncomeTaxExpenseBenefit` — COMPONENTES del impuesto (corriente vs
--     diferido, federal vs estatal): mapear uno no es el total y abriría un
--     doble conteo.
--   * `EffectiveIncomeTaxRateContinuingOperations` (41/44) — es una TASA
--     declarada por la compañía, en unidad `pure`/`Rate`, no en USD. El
--     catálogo sólo acepta la unidad que declara cada fila, así que además de
--     ser una segunda fuente de impuestos (ADR D32: una sola fuente) no
--     podría entrar por la vía del mapa. Servirá como control de coherencia,
--     nunca como fuente.
--
-- Reversible: `DELETE FROM xbrl_concept_map WHERE xbrl_concept IN
-- ('interest_expense','income_tax_expense','pretax_income')` — y no hace falta
-- deshacer nada más, porque no hay DDL ni backfill.

INSERT INTO xbrl_concept_map (xbrl_concept, canonical_name, unit_expected, notes) VALUES
    ('interest_expense', 'interest_expense', 'USD',
     'XBRL (duration, USD), prioridad menor-gana por (concepto, periodo) según ADR D31: InterestExpense (P1), InterestExpenseNonoperating (P2), InterestAndDebtExpense (P3), InterestExpenseDebt (P4), InterestExpenseDebtExcludingAmortization (P5). EXCLUIDO: InterestIncomeExpenseNet (neto, puede ser negativo: no es gasto bruto) y las variantes de contexto/segmento. Se lee del mismo period_end que operating_income (ADR D31: alineación fiscal)'),
    ('income_tax_expense', 'income_tax_expense', 'USD',
     'XBRL (duration, USD), prioridad menor-gana por (concepto, periodo) según ADR D31: IncomeTaxExpenseBenefit (P1), IncomeTaxExpenseBenefitContinuingOperations (P2). EXCLUIDOS los COMPONENTES Deferred*/Current*/Federal*/StateAndLocal*/Foreign* (doble conteo) y las variantes de segmento/operaciones discontinuadas. Se lee del mismo period_end que pretax_income (ADR D32: una sola fuente de impuestos)'),
    ('pretax_income', 'pretax_income', 'USD',
     'XBRL (duration, USD), prioridad menor-gana por (concepto, periodo) según ADR D31: IncomeLossFromContinuingOperationsBeforeIncomeTaxesExtraordinaryItemsNoncontrollingInterest (P1), IncomeLossFromContinuingOperationsBeforeIncomeTaxesMinorityInterestAndIncomeLossFromEquityMethodInvestments (P2), ResultsOfOperationsIncomeBeforeIncomeTaxes (P3). EXCLUIDOS IncomeLossFromContinuingOperationsBeforeIncomeTaxesForeign/Domestic (desglose geográfico, no el total). Denominador de la tasa fiscal observada de ADR D32')
ON CONFLICT (xbrl_concept, canonical_name) DO NOTHING;
