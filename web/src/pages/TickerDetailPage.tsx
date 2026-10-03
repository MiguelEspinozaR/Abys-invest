import { useMemo } from 'react';
import type { ReactNode } from 'react';
import { Link, useParams } from 'react-router-dom';
import { decodeScoreSnapshot } from '../api/snapshot';
import type {
  ComparablesResponse,
  MetricsResponse,
  PricePoint,
  ScoreResponse,
  ScoresHistoryItem,
  SecurityDetail,
  ValuationMethod,
  ValuationResponse,
} from '../api/types';
import MetricsTable, { METRIC_DEFS } from '../components/MetricsTable';
import ScoreBadge from '../components/ScoreBadge';
import { fmtDate, fmtNumber, fmtPct } from '../lib/format';
import { useFetch } from '../lib/useFetch';
import type { FetchState } from '../lib/useFetch';

/**
 * Etiquetas de las dimensiones del score 2.0.0. El bloque 1.x "valuation" 35%
 * son DOS dimensiones independientes (Graham 15% + DCF 20%, §18): Graham y DCF
 * pueden estar disponibles por separado, así que la UI las nombra por separado
 * en lugar de agruparlas bajo una "Valoración" que ya no existe.
 *
 * `valuation` se conserva como clave de compatibilidad con las filas de score
 * persistidas por la versión 1.x del modelo (esas dimensiones no se exponen en
 * la API, pero la etiqueta evita un "undefined" si alguna arrives).
 */
const DIM_LABELS: Record<string, string> = {
  graham: 'Graham',
  dcf: 'DCF',
  valuation: 'Valoración',
  fundamentals: 'Métricas',
  comparables: 'Comparables',
  trend: 'Tendencia',
};

/**
 * T3 — Detalle de ticker: header, score con dimensiones + justificación,
 * valoración, métricas (Anexo §13), histórico de scores y comparables.
 * Cada sección carga su dato con loading/error/reintento propio; el fallo de
 * una sección no rompe las demás, y comparables se oculta si falla.
 */
export default function TickerDetailPage() {
  const { ticker: paramTicker } = useParams<{ ticker: string }>();
  const ticker = useMemo(() => paramTicker?.trim().toUpperCase() ?? null, [paramTicker]);

  // Ventana reciente de precios para el precio actual (último cierre).
  const pricePath = useMemo(() => {
    if (!ticker) return null;
    const to = new Date();
    const from = new Date(to.getTime() - 14 * 24 * 60 * 60 * 1000);
    const iso = (d: Date) => d.toISOString().slice(0, 10);
    return `/prices/${ticker}?from=${iso(from)}&to=${iso(to)}`;
  }, [ticker]);

  const security = useFetch<SecurityDetail>(ticker ? `/securities/${ticker}` : null);
  const prices = useFetch<PricePoint[]>(pricePath);
  const score = useFetch<ScoreResponse>(ticker ? `/score/${ticker}` : null);
  const valuation = useFetch<ValuationResponse>(ticker ? `/valuation/${ticker}` : null);
  const metrics = useFetch<MetricsResponse>(ticker ? `/metrics/${ticker}` : null);
  const history = useFetch<ScoresHistoryItem[]>(ticker ? `/scores?ticker=${ticker}` : null);
  const comparables = useFetch<ComparablesResponse>(
    ticker ? `/compare/comparables?ticker=${ticker}&limit=8` : null,
  );

  const lastClose =
    prices.data && prices.data.length > 0
      ? prices.data[prices.data.length - 1].close
      : undefined;
  const currentPrice = lastClose ?? valuation.data?.price;

  return (
    // M5.2: el wrapper min-h-screen/bg-slate-50 y el fondo/tema los aporta el
    // <Layout/> (ruta padre en App.tsx). El "← Dashboard" del header se mantiene
    // por decisión del plan (B3): es la vuelta al origen del clic, no navegación
    // global.
    <main className="mx-auto max-w-5xl px-4 py-6">
      <section className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div>
            <Link
              to="/"
              className="text-sm text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-300"
            >
              ← Dashboard
            </Link>
            <h1 className="mt-1 text-2xl font-bold">{ticker ?? '—'}</h1>
            {security.loading && (
              <p className="mt-1 text-sm text-slate-500">Cargando detalles…</p>
            )}
            {security.error && (
              <p className="mt-1 text-sm text-red-600 dark:text-red-400">
                {security.error}
                <button
                  type="button"
                  onClick={security.reload}
                  className="ml-2 rounded border border-current px-2 py-0.5 text-xs hover:bg-red-50 dark:hover:bg-red-950/30"
                >
                  Reintentar
                </button>
              </p>
            )}
            {security.data && (
              <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
                {security.data.name}
                {(security.data.sector || security.data.industry) && (
                  <> · {[security.data.sector, security.data.industry].filter(Boolean).join(' · ')}</>
                )}
              </p>
            )}
          </div>
          <div className="text-right">
            <p className="text-xs uppercase tracking-wide text-slate-400">Precio actual</p>
            <p className="text-2xl font-bold tabular-nums">
              {currentPrice != null ? fmtNumber(currentPrice) : '—'}
            </p>
            {prices.error && (
              <p className="mt-1 text-xs text-red-500 dark:text-red-400">
                Sin precio: {prices.error}
              </p>
            )}
          </div>
        </div>
      </section>

      <div className="mt-6 space-y-6">
        <Section title="Score" state={score} render={renderScore} />

        <Section title="Valoración" state={valuation} empty="Sin valoración para este ticker." render={renderValuation(score)} />

        <Section title="Métricas" state={metrics} empty="Sin métricas disponibles." render={(data) => <MetricsTable metrics={data} />} />

        <Section title="Histórico de scores" state={history} empty="Sin histórico de scores." render={renderHistory} />

        {comparables.data && comparables.data.peers.length > 0 && (
          <Section
            title={`Comparables · ${comparables.data.sector ?? 'sector'}`}
            state={comparables}
            render={renderComparables}
          />
        )}
      </div>
    </main>
  );
}

/** Tarjeta de sección con estados loading/error/data compartidos. */
function Section<T>({
  title,
  state,
  empty = 'Sin datos.',
  render,
}: {
  title: string;
  state: FetchState<T> & { reload: () => void };
  empty?: string;
  render: (data: T) => ReactNode;
}) {
  return (
    <section className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
      <h2 className="mb-3 text-lg font-semibold">{title}</h2>
      {state.loading ? (
        <p className="text-sm text-slate-500">Cargando…</p>
      ) : state.error ? (
        <p className="flex flex-wrap items-center gap-2 text-sm text-red-600 dark:text-red-400">
          {state.error}
          <button
            type="button"
            onClick={state.reload}
            className="rounded border border-current px-2 py-0.5 text-xs hover:bg-red-50 dark:hover:bg-red-950/30"
          >
            Reintentar
          </button>
        </p>
      ) : state.data ? (
        render(state.data)
      ) : (
        <p className="text-sm text-slate-500">{empty}</p>
      )}
    </section>
  );
}

const renderScore = (data: ScoreResponse) => (
  <div>
    <div className="flex flex-wrap items-center gap-3">
      <ScoreBadge score={data.score} signal={data.signal} size="lg" />
      <p className="text-sm text-slate-500 dark:text-slate-400">
        <span>{fmtNumber(data.score)}/100</span> · {fmtDate(data.as_of)}
      </p>
    </div>
    <p className="mt-3 text-sm leading-relaxed text-slate-700 dark:text-slate-300">
      {data.justification}
    </p>
    {data.dimensions && data.dimensions.length > 0 && (
      <div className="mt-5 space-y-4">
        {data.dimensions.map((dim) => (
          <DimensionBar
            key={dim.name}
            name={dim.name}
            score={dim.score}
            valid={dim.valid}
            weight={dim.weight}
            reason={dim.reason}
          />
        ))}
        {/* §18: si el score se renormalizó, decirlo. Un score más bajo del
            esperado sin esta nota parece un bug; con ella es el contrato. */}
        {data.weight_used != null &&
          data.weight_configured != null &&
          data.weight_used < data.weight_configured && (
            <p className="text-xs text-amber-600 dark:text-amber-400">
              Score renormalizado: se aplicó el {fmtPct(data.weight_used * 100)} de los{' '}
              {fmtPct(data.weight_configured * 100)} pesos configurados (las
              dimensiones sin dato no aportan).
            </p>
          )}
      </div>
    )}
    {/* §13 (D26/Az3): los sub-bloques de quality son el PORQUÉ del número, no un
        detalle. Sin ellos la UI muestra un 84 sin decir que ROIC pesa más que
        márgenes. Sólo 2.1.0 los trae; 1.1.0/2.0.0 no los tienen y no se pintan. */}
    {data.quality && (
      <div className="mt-5 rounded-lg border border-slate-200 p-4 dark:border-slate-700">
        <h4 className="text-sm font-semibold text-slate-700 dark:text-slate-200">
          Quality {data.quality.score != null ? fmtNumber(data.quality.score) : 'n/d'}/100
          <span className="ml-2 font-normal text-xs text-slate-500">
            cobertura {fmtPct(data.quality.coverage * 100)} · confianza {data.quality.confidence}
            {data.quality.tax_rate_source
              ? ` · tipo impositivo ${data.quality.tax_rate_source}`
              : ''}
          </span>
        </h4>
        {data.quality.sub_scores && (
          <div className="mt-3 space-y-2">
            {Object.values(data.quality.sub_scores).map((sub) => (
              <div key={sub.name} className="flex items-center justify-between text-xs">
                <span className="text-slate-600 dark:text-slate-300">
                  {sub.name}
                  <span className="ml-1 text-slate-400">
                    peso {fmtPct(sub.weight * 100)} · cobertura {fmtPct(sub.coverage * 100)}
                  </span>
                </span>
                <span className="font-medium text-slate-700 dark:text-slate-200">
                  {sub.score != null ? fmtNumber(sub.score) : 'n/d'}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    )}
    {/* §16: sector e historial van por separado; su desacuerdo es información. */}
    {data.relative && (
      <div className="mt-4 rounded-lg border border-slate-200 p-4 dark:border-slate-700">
        <h4 className="text-sm font-semibold text-slate-700 dark:text-slate-200">
          Relative {data.relative.score != null ? fmtNumber(data.relative.score) : 'n/d'}/100
          <span className="ml-2 font-normal text-xs text-slate-500">
            cobertura {fmtPct(data.relative.coverage * 100)} · confianza {data.relative.confidence}
          </span>
        </h4>
        <div className="mt-2 grid grid-cols-2 gap-2 text-xs text-slate-600 dark:text-slate-300">
          <span>
            vs sector: {data.relative.sector_score != null ? fmtNumber(data.relative.sector_score) : 'n/d'}
          </span>
          <span>
            vs historial:{' '}
            {data.relative.historical_score != null ? fmtNumber(data.relative.historical_score) : 'n/d'}
          </span>
        </div>
      </div>
    )}
  </div>
);

/**
 * Barra de una dimensión 2.0.0. `score` es opcional a propósito: una dimensión
 * INVÁLIDA (sin dato suficiente) se pinta como indisponible con su reason, en
 * lugar de dibujar una barra en 0 que se leería como "puntúa 0".
 */
function DimensionBar({
  name,
  score,
  valid,
  weight,
  reason,
}: {
  name: string;
  score?: number;
  valid: boolean;
  weight: number;
  reason?: string;
}) {
  const hasScore = valid && score != null;
  const value = hasScore ? Math.min(100, Math.max(0, score)) : 0;
  const tone = !hasScore
    ? 'bg-slate-300 dark:bg-slate-600'
    : value >= 66
      ? 'bg-emerald-500'
      : value >= 33
        ? 'bg-amber-500'
        : 'bg-red-500';
  return (
    <div>
      <div className="mb-1 flex items-baseline justify-between gap-3 text-sm">
        <span className="font-medium">
          {DIM_LABELS[name] ?? name}
          {!hasScore && (
            <span className="ml-2 text-xs font-normal text-slate-400">(sin dato)</span>
          )}
        </span>
        <span className="text-xs tabular-nums text-slate-500 dark:text-slate-400">
          {hasScore ? `${fmtNumber(score)}/100` : '—'} · peso {fmtPct(weight * 100)}
        </span>
      </div>
      <div className="h-2 overflow-hidden rounded-full bg-slate-200 dark:bg-slate-700">
        <div className={`h-full rounded-full ${tone}`} style={{ width: `${value}%` }} />
      </div>
      {reason != null && (
        <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">{reason}</p>
      )}
    </div>
  );
}

/**
 * Sección de valoración 2.0.0.
 *
 * Reglas de presentación que NO son cosméticas:
 *  - No hay "consenso" ni "upside": el sistema no tiene consenso ni precio
 *    objetivo, y M6b los eliminó del contrato. La UI no puede reintroducirlos.
 *  - Graham y DCF son independientes: cada uno muestra sus TRES escenarios
 *    (bear/base/bull) o, si está `unavailable`, sus reasons. Un método ausente
 *    no se rellena con el otro.
 *  - `confidence` se pinta SIEMPRE. Cuando es LOW la UI muestra el literal
 *    "LOW CONFIDENCE" del contrato: es la información que evita que el usuario
 *    lea un escenario como una prediction.
 *  - Todo número ausente se pinta "—". `value` ausente (security sin datos) no
 *    es un 0.
 */
const renderValuation = (score: FetchState<ScoreResponse>) => (data: ValuationResponse) => {
  const snapshot = decodeScoreSnapshot(score.data?.inputs_snapshot);
  const value = data.value;
  const inputs = value?.inputs;

  if (!value) {
    return (
      <div className="space-y-3 text-sm text-slate-500 dark:text-slate-400">
        <p>
          Sin valoración disponible
          {data.valuation_source === 'computed' ? ' (recalculada al vuelo)' : ''}.
        </p>
        <p>Precio: {data.price != null ? fmtNumber(data.price) : '—'}</p>
      </div>
    );
  }

  const items: ReadonlyArray<{ label: string; value: string }> = [
    { label: 'Precio', value: data.price != null ? fmtNumber(data.price) : '—' },
    ...scenarioItems('Graham', value.graham),
    ...scenarioItems('DCF', value.dcf),
    { label: 'MOS Graham', value: fmtPctOrDash(value.margin_of_safety.graham_base) },
    { label: 'MOS DCF base', value: fmtPctOrDash(value.margin_of_safety.dcf_base) },
    { label: 'WACC usado', value: fmtPctOrDash(inputs?.wacc_used) },
    { label: 'Crecimiento', value: fmtPctOrDash(inputs?.normalized_growth_rate) },
    { label: 'PEG', value: fmtNumberOrDash(value.peg) },
    { label: 'P/FCF', value: fmtNumberOrDash(value.p_fcf) },
    ...(snapshot?.margin_of_safety != null
      ? [{ label: 'Margen objetivo', value: fmtPct(snapshot.margin_of_safety) }]
      : []),
  ];

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <span
          className={`rounded px-2 py-0.5 text-xs font-semibold uppercase tracking-wide ${
            value.confidence === 'low'
              ? 'bg-amber-100 text-amber-800 dark:bg-amber-950/60 dark:text-amber-300'
              : 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300'
          }`}
        >
          {value.confidence === 'low' ? 'LOW CONFIDENCE' : 'HIGH CONFIDENCE'}
        </span>
        <span className="text-xs text-slate-500 dark:text-slate-400">
          {value.status === 'available' ? 'valoración disponible' : 'sin valoración'} ·
          origen: {data.valuation_source === 'persisted' ? 'persistida' : 'recalculada'} ·
          modelo {value.model_version}
        </span>
      </div>

      {value.reasons?.length && (
        <div className="mb-3 text-xs text-slate-600 dark:text-slate-400">
          <span className="font-medium">Motivos de confianza:</span>{' '}
          {value.reasons!.map((r, i) => (
            <span key={r} className={i > 0 ? 'mx-1' : ''}>
              {r}{i < value.reasons!.length - 1 ? ',' : ''}
            </span>
          ))}
        </div>
      )}

      <dl className="grid grid-cols-2 gap-3 sm:grid-cols-3">
        {items.map((item) => (
          <div key={item.label} className="rounded-lg bg-slate-50 p-3 dark:bg-slate-800/60">
            <dt className="text-xs uppercase tracking-wide text-slate-500 dark:text-slate-400">
              {item.label}
            </dt>
            <dd className="mt-1 text-lg font-semibold tabular-nums">{item.value}</dd>
          </div>
        ))}
      </dl>

      {(value.graham.status === 'unavailable' || value.dcf.status === 'unavailable') && (
        <ul className="mt-3 space-y-1 text-xs text-slate-500 dark:text-slate-400">
          {value.graham.status === 'unavailable' && (
            <li>Graham no disponible: {(value.graham.reasons ?? []).join(', ') || 'sin reasons'}</li>
          )}
          {value.dcf.status === 'unavailable' && (
            <li>DCF no disponible: {(value.dcf.reasons ?? []).join(', ') || 'sin reasons'}</li>
          )}
        </ul>
      )}

      {data.currency && (
        <p className="mt-3 text-xs text-slate-400 dark:text-slate-500">
          Moneda: {data.currency} · escenarios independientes (Graham/DCF), sin consenso ni
          precio objetivo
        </p>
      )}
    </div>
  );
};

/** bear/base/bull de un método; nada si el método no tiene valores. */
function scenarioItems(
  label: string,
  method: ValuationMethod,
): ReadonlyArray<{ label: string; value: string }> {
  return [
    { label: `${label} bear`, value: fmtNumberOrDash(method.bear) },
    { label: `${label} base`, value: fmtNumberOrDash(method.base) },
    { label: `${label} bull`, value: fmtNumberOrDash(method.bull) },
  ];
}

/** Ausente/null → "—": nunca 0 de relleno (nil significa "sin dato"). */
function fmtNumberOrDash(v?: number): string {
  return v != null ? fmtNumber(v) : '—';
}

function fmtPctOrDash(v?: number): string {
  return v != null ? fmtPct(v) : '—';
}

const renderHistory = (data: ScoresHistoryItem[]) =>
  data.length === 0 ? (
    <p className="text-sm text-slate-500">Sin histórico de scores.</p>
  ) : (
    <table className="w-full text-sm">
      <thead>
        <tr className="border-b border-slate-200 text-left text-xs uppercase tracking-wide text-slate-500 dark:border-slate-700 dark:text-slate-400">
          <th className="py-2 pr-3">Fecha</th>
          <th className="py-2 pr-3">Score</th>
          <th className="py-2">Señal</th>
        </tr>
      </thead>
      <tbody>
        {data.map((row) => (
          <tr key={row.id} className="border-t border-slate-100 dark:border-slate-800">
            <td className="py-2 pr-3 tabular-nums">{fmtDate(row.as_of)}</td>
            <td className="py-2 pr-3 font-medium tabular-nums">{fmtNumber(row.score)}</td>
            <td className="py-2">
              <ScoreBadge score={row.score} signal={row.signal} />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );

const renderComparables = (data: ComparablesResponse) => {
  const medians = data.medians ?? {};
  const metricKeys = METRIC_DEFS.map((def) => def.key).filter((key) => medians[key] !== undefined);
  return (
    <div>
      <p className="mb-3 text-xs text-slate-500 dark:text-slate-400">
        Mediana del sector ({data.peer_count} empresas) vs. peers listados.
      </p>
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-slate-200 text-left text-xs uppercase tracking-wide text-slate-500 dark:border-slate-700 dark:text-slate-400">
              <th className="py-2 pr-3">Métrica</th>
              {data.peers.map((peer) => (
                <th key={peer.id} className="py-2 pr-3">
                  {peer.ticker}
                </th>
              ))}
              <th className="py-2">Mediana</th>
            </tr>
          </thead>
          <tbody>
            {metricKeys.map((key) => {
              const def = METRIC_DEFS.find((d) => d.key === key);
              if (!def) return null;
              const median = medians[key] ?? undefined;
              return (
                <tr key={key} className="border-t border-slate-100 dark:border-slate-800">
                  <td className="py-1.5 pr-3 text-slate-500 dark:text-slate-400">{def.label}</td>
                  {data.peers.map((peer) => {
                    const val = peer.metrics.find((m) => m.metric === key)?.value;
                    return (
                      <td key={peer.id} className="py-1.5 pr-3 tabular-nums">
                        {val != null ? def.format(val) : '—'}
                      </td>
                    );
                  })}
                  <td className="py-1.5 font-semibold tabular-nums">
                    {median != null ? def.format(median) : '—'}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
};