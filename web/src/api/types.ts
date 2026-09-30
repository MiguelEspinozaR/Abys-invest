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

/** Una fila de `tables` en GET /health (conteo exacto de registros). */
export interface HealthTableCount {
  name: string;
  rows: number;
}

/**
 * GET /health (M5.2, ampliación ADITIVA del contrato M1: `status`, `database` y
 * `version` no cambian). Los 4 campos nuevos solo vienen con la BD conectada;
 * `undefined` = no disponible (la API los OMITE, no los manda null) y la UI
 * pinta "—". `latency_ms` es la latencia del ping a PostgreSQL, no la del
 * request completo.
 */
export interface HealthResponse {
  status: string; // "ok" | "degraded"
  database: string; // "connected" | "disconnected"
  version: string;
  latency_ms?: number;
  postgres_version?: string; // "18.6"
  db_size?: string; // "21 MB"
  tables?: HealthTableCount[]; // 9 entradas
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

/**
 * GET /securities/search?q=&limit= → array de Security del catálogo completo,
 * rankeado (ticker exacto → prefijo de ticker → coincidencia de nombre).
 */
export type SearchResults = Security[];

/**
 * GET /watchlist → fila de la watchlist con el detalle del catálogo
 * (storage.WatchlistItem). Hay exactamente una fila de watchlist por security
 * (UNIQUE security_id).
 *
 * `id` es el `securities.id` del valor en el catálogo (NO el id de la fila de
 * watchlist): es el mismo id que devuelve GET /securities/search para ese
 * ticker, y es estable si se borra y se vuelve a añadir el valor.
 * `created_at` es el orden de adición que muestra la lista (sí viene de la fila
 * de watchlist).
 *
 * M5.1: `score` y `signal` son el último score persistido del security
 * (LEFT JOIN LATERAL en storage) y llegan SIEMPRE como clave: `null` significa
 * "todavía no hay score para este valor", no campo ausente. El dashboard los
 * usa para las tarjetas de "Mi watchlist" sin pedir /score/{ticker} por item.
 */
export interface WatchlistItem {
  id: number;
  ticker: string;
  name: string;
  exchange?: string;
  sector?: string;
  industry?: string;
  created_at: string;
  score: number | null;
  signal: Signal | null;
}

/** GET /watchlist → lista en orden de adición. */
export type WatchlistResponse = WatchlistItem[];

/** PUT/DELETE /watchlist/{ticker} → {"ok":true} (idempotentes). */
export interface WatchlistAck {
  ok: boolean;
}

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

/**
 * Bloque `growth` de /valuation/{ticker} (M6a, api.GrowthDetail).
 * Todos los rates son PORCENTAJES (10.5 = 10.5%/año). Ausente = la security
 * todavía no pasó por el motor; normalized_growth_rate ausente = datos
 * insuficientes (nunca 0 de relleno).
 */
export interface GrowthDetail {
  normalized_growth_rate?: number; // % (ausente = sin datos)
  source: string; // eps_fcf_3y | revenue_3y | insufficient_data | ...
  confidence: 'high' | 'medium' | 'low';
  revenue_cagr_3y?: number;
  revenue_cagr_5y?: number;
  eps_cagr_3y?: number;
  eps_cagr_5y?: number;
  fcf_cagr_3y?: number;
  fcf_cagr_5y?: number;
  clamped: boolean; // se aplicó el clamp [-10, 25]%
  revenue_discrepancy: boolean; // revenue divergió de EPS/FCF
  as_of: string;
  model_version: string;
}

/**
 * Bloque `wacc` de /valuation/{ticker} (M6a, api.WaccDetail). Rates en
 * PORCENTAJES. `beta_observed: false` significa beta asumida por configuración
 * (wacc_source = 'configured_fallback'), no medida.
 */
export interface WaccDetail {
  wacc?: number; // % (ausente = sin estructura de capital)
  cost_of_equity?: number; // % Ke = Rf + beta x ERP
  cost_of_debt_after_tax?: number; // % Kd x (1 - tax)
  risk_free_rate?: number;
  equity_risk_premium?: number;
  beta?: number;
  beta_observed: boolean;
  source: 'capm_individual' | 'capm_hybrid' | 'configured_fallback';
  confidence: 'high' | 'medium';
  as_of: string;
  model_version: string;
}

/** GET /valuation/{ticker} (api.ValuationDetail). */
export interface ValuationResponse {
  ticker: string;
  price?: number;
  currency?: string;
  value: IntrinsicValue;
  upside_pct?: number; // downside/upside vs. consenso
  growth?: GrowthDetail; // M6a, aditivo y opcional
  wacc?: WaccDetail; // M6a, aditivo y opcional
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

/**
 * M5.1 — estado del pipeline en segundo plano (`GET /pipeline/status` y el
 * cuerpo del 202 de `POST /force-refresh`; internal/api/jobs.go). Las 8 claves
 * están siempre presentes: `kind`, `started_at`, `finished_at` y `error` llegan
 * como `null` cuando no aplican, y `tickers`/`steps`/`pending` como `[]`/`{}`.
 */
export type PipelineStatusState = 'idle' | 'running' | 'done' | 'error';

export type PipelineStatusKind = 'watchlist' | 'force';

export interface PipelineStatus {
  status: PipelineStatusState;
  kind: PipelineStatusKind | null;
  /** tickers del job en curso (o del último job, si ya terminó) */
  tickers: string[];
  /** conteos por etapa: edgar|prices|sector|metrics|scores */
  steps: Record<string, number>;
  started_at: string | null;
  finished_at: string | null;
  error: string | null;
  /** cola FIFO de tickers esperando un job watchlist (M5.1) */
  pending: string[];
}
