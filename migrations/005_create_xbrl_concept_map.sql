-- 005_create_xbrl_concept_map.sql
-- Diccionario canónico de fundamentales (plan §2.3): 20 conceptos canónicos.
-- La correspondencia XBRL → canónico completa vive en Go (internal/collect/edgar/concepts.go);
-- esta tabla persiste el diccionario oficial para consulta.

CREATE TABLE IF NOT EXISTS xbrl_concept_map (
    id              BIGSERIAL PRIMARY KEY,
    xbrl_concept    TEXT NOT NULL,
    canonical_name  TEXT NOT NULL,
    unit_expected   VARCHAR(20),
    notes           TEXT,
    CONSTRAINT uq_xbrl_concept_map
        UNIQUE (xbrl_concept, canonical_name)
);

CREATE INDEX IF NOT EXISTS idx_xbrl_concept_map_canonical ON xbrl_concept_map(canonical_name);

-- Seed: 20 conceptos canónicos. Idempotente (ON CONFLICT DO NOTHING).
INSERT INTO xbrl_concept_map (xbrl_concept, canonical_name, unit_expected, notes) VALUES
    ('revenues',                    'revenues',                    'USD',      'XBRL: Revenues, RevenueFromContractWithCustomerExcludingAssessedTax (duration)'),
    ('cost_of_revenue',             'cost_of_revenue',             'USD',      'XBRL: CostOfGoodsAndServicesSold, CostOfRevenue (duration)'),
    ('gross_profit',                'gross_profit',                'USD',      'XBRL: GrossProfit (duration)'),
    ('operating_income',            'operating_income',            'USD',      'XBRL: OperatingIncomeLoss (duration)'),
    ('net_earnings',                'net_earnings',                'USD',      'XBRL: NetIncomeLoss (duration)'),
    ('depreciation_amortization',   'depreciation_amortization',   'USD',      'XBRL: DepreciationDepletionAndAmortization, DepreciationAndAmortization (duration)'),
    ('total_assets',                'total_assets',                'USD',      'XBRL: Assets (instant)'),
    ('total_liabilities',           'total_liabilities',           'USD',      'XBRL: Liabilities (instant)'),
    ('long_term_debt',              'long_term_debt',              'USD',      'XBRL: LongTermDebt, LongTermDebtAndCapitalLeaseObligations (instant)'),
    ('short_term_debt',             'short_term_debt',             'USD',      'XBRL: ShortTermBorrowings, ShortTermDebt, CommercialPaper (instant)'),
    ('shareholders_equity',         'shareholders_equity',         'USD',      'XBRL: StockholdersEquity (instant)'),
    ('cash_and_equivalents',        'cash_and_equivalents',        'USD',      'XBRL: CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents (instant)'),
    ('operating_cash_flow',         'operating_cash_flow',         'USD',      'XBRL: NetCashProvidedByUsedInOperatingActivities (duration)'),
    ('capex',                       'capex',                       'USD',      'XBRL: PaymentsToAcquirePropertyPlantAndEquipment (duration)'),
    ('dividends_paid',              'dividends_paid',              'USD',      'XBRL: PaymentsOfDividends (duration)'),
    ('shares_outstanding',          'shares_outstanding',          'shares',   'XBRL: EntityCommonStockSharesOutstanding (instant)'),
    ('eps_basic',                   'eps_basic',                   'USD/shares','XBRL: EarningsPerShareBasic (duration)'),
    ('eps_diluted',                 'eps_diluted',                 'USD/shares','XBRL: EarningsPerShareDiluted (duration)'),
    ('current_assets',              'current_assets',              'USD',      'XBRL: AssetsCurrent (instant)'),
    ('current_liabilities',         'current_liabilities',         'USD',      'XBRL: LiabilitiesCurrent (instant)')
ON CONFLICT (xbrl_concept, canonical_name) DO NOTHING;