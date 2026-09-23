// Tipos TypeScript para las respuestas de la API M3 (contrato §6 de la SPEC).
// Los nombres de campo son fieles al wire format real:
//   - internal/storage/models.go (Security, DailyPrice, DerivedMetric, Score)
//   - internal/api/loader.go (ValuationDetail, IntrinsicValue/Inputs)
//   - internal/backtest/backtest.go (BacktestResult, Trade)
//   - internal/compare/normalize.go (DataPoint, RiskMetrics) y compare.go
// Verificado T2/T3 (2026-09-23) contra la API real con la BD poblada:
//   - /score/{ticker} y /scores?ticker= emiten `dimensions` siempre que hay
//     inputs_snapshot (los 11 tickers con datos lo tienen) → REQUERIDO aquí.
//   - /valuation/{ticker}: price, value.{graham,dcf,consensus,inputs},
//     upside_pct y metrics[metric|null] (coinciden con este archivo).
//   - /compare/comparables?ticker=: peers[].metrics[].value puede faltar
//     (claves omitidas, `json:"value,omitempty"`) → tipado como opcional.

/** Envelope de error estructurado de la API (internal/api/errors.go). */
export interface ApiErrorEnvelope {
  error: { code: string; message: string };
}

/** GET /health */
export interface HealthResponse {
  status: string; // "ok" | "degraded"
  database: string; // "connected" | "disconnected"
  version: string;
}

/** Fila del catálogo `securities` (GET /securities, GET /securities/{ticker}). */
export interface Security {
  id: number;
  ticker: string;
  cik: string;
  name: string;
  type: string;
  currency: string;
  status: string;
  exchange?: string;
  sector?: string;
  industry?: string;
  created_at: string;
  updated_at: string;
}

/** GET /securities/{ticker} — mismo shape que Security (storage.Security). */
export type SecurityDetail = Security;

/** Una barra diaria OHLCV (storage.DailyPrice) — GET /prices/{ticker}. */
export interface PricePoint {
  id: number;
  security_id: number;
  date: string;
  open?: number;
  high?: number;
  low?: number;
  close: number;
  adjusted_close: number;
  volume?: number;
  source: string;
  created_at: string;
}

/** Una métrica materializada (storage.DerivedMetric). */
export interface DerivedMetric {
  id: number;
  security_id: number;
  as_of: string;
  metric: string;
  value?: number;
  inputs_snapshot?: string; // JSON base64… solo informativo
  model_version: string;
  created_at: string;
}

/** GET /metrics/{ticker} → array de DerivedMetric (el más reciente por as_of). */
export type MetricsResponse = DerivedMetric[];

/** Inputs de la valoración (valuation.IntrinsicInputs). */
export interface IntrinsicInputs {
  growth_rate: number;
  eps?: number;
  free_cash_flow?: number;
  dcf_discount_rate: number;
  dcf_horizon_years: number;
  terminal_growth: number;
  shares_outstanding?: number;
  net_debt?: number;
}

/** Resultado de valoración (valuation.IntrinsicValue). */
export interface IntrinsicValue {
  graham?: number; // nil si inputs insuficientes (conservador)
  dcf?: number;
  consensus?: number; // promedio de Graham y DCF, o el único disponible
  inputs: IntrinsicInputs;
  model_version: string;
}

/** GET /valuation/{ticker} (api.ValuationDetail). */
export interface ValuationResponse {
  ticker: string;
  price?: number;
  currency?: string;
  value: IntrinsicValue;
  upside_pct?: number; // downside/upside vs. consenso
  metrics?: Record<string, number | null>; // metric → valor (null si no calculable)
  as_of: string; // última fecha de precio
  computed_at: string;
}

/** Una dimensión del score (score.DimensionScore) — ver TODO en ScoreResponse. */
export interface ScoreDimension {
  name: string; // valuation | fundamentals | comparables | trend
  score: number; // 0-100
  weight: number; // 0.35 / 0.30 / 0.20 / 0.15
}

/** Señal del score tal como la sirve la API (es-ES, minúsculas). */
export type Signal = 'comprar' | 'mantener' | 'vender';

/** Fila persistida de `scores` (storage.Score) — base de /score y /scores. */
export interface ScoreRow {
  id: number;
  security_id: number;
  as_of: string;
  score: number; // 0-100
  signal: Signal; // comprar | mantener | vender
  justification: string;
  inputs_snapshot?: string; // JSON base64 del snapshot de inputs (ADR-0004)
  model_version: string;
  created_at: string;
}

/**
 * GET /score/{ticker}: storage.Score enriquecido con `dimensions`. La API M3
 * (T4b) las emite siempre que hay inputs_snapshot (los tickers con score lo
 * tienen) → REQUERIDO. dimensionesFromSnapshot recomputa las cuatro
 * dimensiones del contrato (valoración 35 / métricas 30 / comparables 20 /
 * tendencia 15) — verificado contra la API real.
 */
export interface ScoreResponse extends ScoreRow {
  dimensions: ScoreDimension[];
}

/** GET /scores?ticker= → historial de filas (orden as_of DESC); cada ítem
 * lleva `dimensions` (mismo enriquecimiento que /score/{ticker}). */
export type ScoresHistoryItem = ScoreResponse;

/**
 * Decodificación PARCIAL de ScoreRow.inputs_snapshot: JSON en base64 del
 * snapshot de inputs del score (ADR-0004). El snapshot es opaco por contrato;
 * aquí solo se tipan los campos que la UI consume (el resto se ignora).
 */
export interface ScoreSnapshot {
  ticker?: string;
  price?: number;
  margin_of_safety?: number; // 0-100, objetivo de margen del score (default 30)
}

/** Un trade del backtest (backtest.Trade). */
export interface BacktestTrade {
  entry_date: string;
  entry_price: number;
  exit_date?: string;
  exit_price?: number;
  return_pct?: number;
  open?: boolean; // señal sin salida al final del histórico
}

/** Resultado de un backtest por ticker (backtest.BacktestResult). */
export interface BacktestResult {
  ticker: string;
  strategy: string;
  fast: number;
  slow: number;
  start_date: string;
  end_date: string;
  initial_capital: number;
  final_capital: number;
  total_return: number; // ratio (0.26 = +26%)
  total_return_pct: number;
  cagr: number; // ratio
  sharpe: number;
  max_drawdown: number; // negativo (decimal)
  trades: BacktestTrade[];
  total_trades: number;
}

/** GET /backtest/sma?tickers= (api.backtestResponse). */
export interface SmaBacktestResponse {
  strategy: string; // "sma"
  params: { fast: number; slow: number };
  from?: string;
  to?: string;
  results: Record<string, BacktestResult>;
}

/** Un punto de la serie normalizada base 100 (compare.DataPoint). */
export interface CompareDataPoint {
  date: string; // YYYY-MM-DD
  index: number; // base 100
}

/** Métricas de riesgo de un activo (compare.RiskMetrics). */
export interface CompareRiskMetrics {
  volatility_annual: number; // decimal (0.28 = 28%)
  max_drawdown: number; // negativo (decimal)
  sharpe: number;
}

/** GET /compare?tickers= (api.handleCompareAssets). */
export interface CompareResponse {
  tickers: string[];
  from: string;
  to: string;
  normalized_performance: Record<string, CompareDataPoint[]>;
  risk_metrics: Record<string, CompareRiskMetrics>;
}

/** GET /compare/comparables?ticker=&limit= (compare.ComparablesResult). */
export interface ComparableMetricValue {
  metric: string; // de_ratio | eps | fcf_yield | pb_ratio | pcf_ratio | peg_ratio | pe_ratio | roe
  value?: number; // puede faltar cuando no se pudo calcular (json omitempty)
}

export interface ComparablesPeer {
  id: number;
  ticker: string;
  metrics: ComparableMetricValue[];
}

export interface ComparablesResponse {
  ticker: string;
  sector?: string;
  peers: ComparablesPeer[];
  peer_count: number;
  medians?: Record<string, number | null>;
  security_id: number;
}