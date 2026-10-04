// Tipos TypeScript para las respuestas de la API M3 (contrato §6 de la SPEC).
// Los nombres de campo son fieles al wire format real:
//   - internal/storage/models.go (Security, DailyPrice, DerivedMetric, Score)
//   - internal/api/loader.go (ValuationDetail) + internal/valuation
//     (Inputs, Method, Result) — contrato de valoración 2.0.0
//   - internal/backtest/backtest.go (BacktestResult, Trade)
//   - internal/compare/normalize.go (DataPoint, RiskMetrics) y compare.go
// Verificado T2/T3 (2026-09-23) contra la API real con la BD poblada:
//   - /score/{ticker} y /scores?ticker= emiten `dimensions` siempre que hay
//     inputs_snapshot (los 11 tickers con datos lo tienen) → REQUERIDO aquí.
//   - /valuation/{ticker}: price, valuation_source, value.{status,graham,dcf,
//     margin_of_safety,uncertainty,confidence,reasons,sensitivity,inputs,
//     peg,p_fcf,model_version} y metrics[metric|null] (coinciden con este
//     archivo). El 1.x `consensus`/`upside_pct` ya NO existen (M6b): no hay
//     consenso ni precio objetivo en este sistema.
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
  /** B7/B15: presente desde 2.1.0; null en las filas históricas. */
  parameter_set_id?: number | null;
  created_at: string;
}

/** GET /metrics/{ticker} → array de DerivedMetric (el más reciente por as_of). */
export type MetricsResponse = DerivedMetric[];

/**
 * Inputs de la valoración 2.0.0 (valuation.Inputs).
 *
 * OJO con los `null`: `eps`, `free_cash_flow`, `net_debt`, `wacc` o
 * `normalized_growth_rate` AUSENTES significan "dato insuficiente" y la API los
 * OMITE (nunca 0). Un campo ausente no es un 0 que la UI pueda formatear como
 * si fuera un dato real.
 *
 * `wacc_used` + `discount_source` + `discount_reason` documentan la tasa que el
 * motor REALMENTE usó (D9): el discount rate nunca es invisible.
 */
export interface ValuationInputs {
  ticker?: string;
  as_of?: string;
  price?: number;
  eps?: number;
  free_cash_flow?: number;
  shares_outstanding?: number;
  net_debt?: number; // total debt − cash; ausente = desconocido
  normalized_growth_rate?: number; // % persistido por M6a
  growth_confidence?: ValuationConfidence;
  growth_source?: string;
  growth_model_version?: string;
  wacc?: number; // % persistido por M6a
  cost_of_equity?: number;
  wacc_source?: string;
  wacc_confidence?: ValuationConfidence;
  wacc_model_version?: string;
  beta_observed?: boolean;
  wacc_used?: number; // % realmente aplicado en el DCF
  discount_source?: string; // wacc_metrics | cost_of_equity | wacc_fallback | dcf_discount_rate
  discount_reason?: string; // vacío en nivel 1 (sin degradación)
  discount_level?: number; // 1..4 (D9)
  growth_fallback_used?: boolean; // A1: se usó el 7% legacy
}

/** Estado de un método de valoración (available | unavailable). */
export type ValuationStatus = 'available' | 'unavailable';

/** Confianza de la valoración (§20): high | medium | low. */
export type ValuationConfidence = 'high' | 'medium' | 'low';

/**
 * Un método de valoración con sus TRES escenarios (2.0.0 §10).
 * `status: 'unavailable'` ⇒ todos los escenarios AUSENTES y `reasons` explica
 * por qué. Graham y DCF son independientes: uno puede estar disponible y el
 * otro no.
 */
export interface ValuationMethod {
  status: ValuationStatus;
  bear?: number;
  base?: number;
  bull?: number;
  confidence?: ValuationConfidence;
  reasons?: string[];
}

/** Margen de seguridad §11 (%). `reason` viene cuando no se pudo calcular. */
export interface MarginOfSafety {
  graham_base?: number;
  dcf_bear?: number;
  dcf_base?: number;
  dcf_bull?: number;
  target_margin_of_safety: number;
  reason?: string;
}

/** Incertidumbre §19: media, desviación típica POBLACIONAL y dispersión. */
export interface ValuationUncertainty {
  dispersion?: number; // std/mean, adimensional
  mean?: number;
  std_dev?: number;
  components: number; // 0..4 (valores componentes disponibles)
}

/** Una celda del grid de sensibilidad §8 (WACC × growth). */
export interface SensitivityPoint {
  growth: number; // %
  wacc: number; // %
  dcf_base: number; // 0 cuando la celda no es computable
}

/**
 * Bloque `value` de /valuation/{ticker} (valuation.Result, 2.0.0).
 * `confidence` es a NIVEL DE VALUACIÓN; cada método trae la suya.
 */
export interface ValuationValue {
  ticker?: string;
  as_of?: string;
  status: ValuationStatus;
  graham: ValuationMethod;
  dcf: ValuationMethod;
  margin_of_safety: MarginOfSafety;
  uncertainty: ValuationUncertainty;
  confidence: ValuationConfidence;
  reasons?: string[];
  sensitivity?: SensitivityPoint[];
  peg?: number; // aditivo §15: growth/PE. Ausente = no computable
  p_fcf?: number; // aditivo §15: precio / FCF por acción
  inputs: ValuationInputs;
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
  confidence: 'high' | 'medium' | 'low';
  as_of: string;
  model_version: string;
}

/**
 * GET /valuation/{ticker} (api.ValuationDetail, 2.0.0).
 *
 * `valuation_source` declara qué camino produjo `value`: `persisted` (fila de
 * valuation_results, la del pipeline diario) o `computed` (la API recalculó
 * porque el pipeline aún no corrió para esa security). `value` es OPCIONAL: una
 * security sin precio ni datos no tiene bloque de valoración, y la UI lo pinta
 * como "—" en lugar de inventar un valor.
 */
export interface ValuationResponse {
  ticker: string;
  price?: number;
  currency?: string;
  valuation_source?: 'persisted' | 'computed';
  value?: ValuationValue;
  growth?: GrowthDetail; // M6a, aditivo y opcional
  wacc?: WaccDetail; // M6a, aditivo y opcional
  metrics?: Record<string, number | null>; // metric → valor (null si no calculable)
  as_of: string; // última fecha de precio
  computed_at: string;
}

/** Una dimensión del score (score.DimensionScore) — ver TODO en ScoreResponse. */
/**
 * Una dimensión del score 2.0.0 (score.DimensionScore).
 *
 * `score` es OPCIONAL: ausente = dimensión inválida (pesos renormalizados,
 * §18). `weight` es el peso CONFIGURADO de la dimensión (graham=0.15, dcf=0.20,
 * fundamentals=0.30, comparables=0.20, trend=0.15). La suma de los pesos de las
 * dimensiones válidas se expone en `ScoreResponse.weight_used` (active_weight_sum
 * de §18), de modo que un cliente puede explicar por qué el score se renormalizó.
 */
export interface ScoreDimension {
  /**
   * graham | dcf | quality | relative | market_context  (score 2.1.0)
   * graham | dcf | fundamentals | comparables | trend   (score 2.0.0)
   *
   * El NOMBRE depende de la revisión: la UI debe comprobar `model_version`
   * antes de assuming el taxonomía de §18, porque 2.0.0 sigue sirviendo sus
   * cinco dimensiones viejas y ambas son válidas a la vez (gate por versión).
   */
  name: string;
  score?: number; // 0-100 (ausente = inválida)
  valid: boolean;
  /** Peso CONFIGURADO. Alias de 2.0.0 conservado por compatibilidad M4b. */
  weight: number;
  /** Peso configurado de 2.1.0 (mismo valor que `weight`). */
  weight_configured?: number;
  weight_used?: number;
  reason?: string;
}

/** Un sub-bloque de quality de §13 (D26/Az3: la UI los muestra, no un número). */
export interface QualitySubBlock {
  name: string; // profitability | growth | margins | stability | solvency
  score?: number; // 0-100 (ausente = sub-bloque sin datos)
  weight: number;
  coverage: number; // 0-1
  metrics?: Record<string, number | null>;
}

/** Bloque `quality` del score 2.1.0 (§13). */
export interface QualityBlock {
  score?: number;
  coverage: number;
  /** high | medium | low. Con `tax_rate_source: 'configured'` el tope es
   *  `medium` por ADR D26: la UI no debe insinuar más confianza de la que hay. */
  confidence: 'high' | 'medium' | 'low';
  tax_rate_source?: string;
  sub_scores?: Record<string, QualitySubBlock>;
  reasons?: string[];
}

/** Bloque `relative` del score 2.1.0 (§16): los dos lados van SEPARADOS. */
export interface RelativeBlock {
  score?: number;
  sector_score?: number;
  historical_score?: number;
  coverage: number;
  confidence: 'high' | 'medium' | 'low';
  reasons?: string[];
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
  /** B7/B15: presente desde 2.1.0; null en las filas históricas. */
  parameter_set_id?: number | null;
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
  /** AUSENTE en 1.1.0 (la revisión no tenía dimensiones) y presente en 2.0.0
   *  y 2.1.0 con la taxonomía que le corresponde a `model_version`. */
  dimensions?: ScoreDimension[];
  // §18: sumas de pesos (aditivos; ausentes si no hay dimensiones, p. ej. fila
  // de otra versión del modelo). weight_used < weight_configured ⇒ el score se
  // renormalizó sobre menos dimensiones.
  weight_configured?: number;
  weight_used?: number;
  /** §26: identidad de la configuración que produjo el score (null = codes). */
  parameter_set_id?: number | null;
  /** §26: NOMBRE del parameter set que produjo el score (2.1.0; ausente si la
   *  fila es de codes o de una revisión que no lo emitía). La API ya lo envía
   *  (`parameter_set`): sin él, dos scores de la misma empresa no se pueden
   *  distinguir en la UI y el número no es auditable. */
  parameter_set?: string;
  /** Solo 2.1.0. */
  quality?: QualityBlock;
  /** Solo 2.1.0. */
  relative?: RelativeBlock;
  /** Layout del trace del que se decodificaron dimensions/quality/relative. */
  trace_version?: string;
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
