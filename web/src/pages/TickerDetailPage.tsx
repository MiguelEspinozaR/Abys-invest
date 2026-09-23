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
  ValuationResponse,
} from '../api/types';
import MetricsTable, { METRIC_DEFS } from '../components/MetricsTable';
import ScoreBadge from '../components/ScoreBadge';
import { fmtDate, fmtNumber, fmtPct } from '../lib/format';
import { useFetch } from '../lib/useFetch';
import type { FetchState } from '../lib/useFetch';

const DIM_LABELS: Record<string, string> = {
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
    <div className="min-h-screen bg-slate-50 text-slate-900 dark:bg-slate-950 dark:text-slate-100">
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
    </div>
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
        <span className="capitalize">{data.signal}</span> · {fmtNumber(data.score)}/100 ·{' '}
        {fmtDate(data.as_of)}
      </p>
    </div>
    <p className="mt-3 text-sm leading-relaxed text-slate-700 dark:text-slate-300">
      {data.justification}
    </p>
    {data.dimensions.length > 0 && (
      <div className="mt-5 space-y-4">
        {data.dimensions.map((dim) => (
          <DimensionBar key={dim.name} name={dim.name} score={dim.score} weight={dim.weight} />
        ))}
      </div>
    )}
  </div>
);

function DimensionBar({ name, score, weight }: { name: string; score: number; weight: number }) {
  const clamped = Math.min(100, Math.max(0, score));
  const tone = score >= 66 ? 'bg-emerald-500' : score >= 33 ? 'bg-amber-500' : 'bg-red-500';
  return (
    <div>
      <div className="mb-1 flex items-baseline justify-between gap-3 text-sm">
        <span className="font-medium">{DIM_LABELS[name] ?? name}</span>
        <span className="text-xs tabular-nums text-slate-500 dark:text-slate-400">
          {fmtNumber(score)}/100 · peso {fmtPct(weight * 100)}
        </span>
      </div>
      <div className="h-2 overflow-hidden rounded-full bg-slate-200 dark:bg-slate-700">
        <div
          className={`h-full rounded-full ${tone}`}
          style={{ width: `${clamped}%` }}
        />
      </div>
    </div>
  );
}

const renderValuation = (score: FetchState<ScoreResponse>) => (data: ValuationResponse) => {
  const snapshot = decodeScoreSnapshot(score.data?.inputs_snapshot);
  const items: ReadonlyArray<{ label: string; value: string }> = [
    { label: 'Precio', value: data.price != null ? fmtNumber(data.price) : '—' },
    ...(data.value.graham != null
      ? [{ label: 'Graham', value: fmtNumber(data.value.graham) }]
      : []),
    ...(data.value.dcf != null ? [{ label: 'DCF', value: fmtNumber(data.value.dcf) }] : []),
    {
      label: 'Consenso',
      value: data.value.consensus != null ? fmtNumber(data.value.consensus) : '—',
    },
    { label: 'Upside', value: data.upside_pct != null ? fmtPct(data.upside_pct) : '—' },
    ...(snapshot?.margin_of_safety != null
      ? [{ label: 'Margen objetivo', value: fmtPct(snapshot.margin_of_safety) }]
      : []),
  ];
  return (
    <dl className="grid grid-cols-2 gap-3 sm:grid-cols-3">
      {items.map((item) => (
        <div key={item.label} className="rounded-lg bg-slate-50 p-3 dark:bg-slate-800/60">
          <dt className="text-xs uppercase tracking-wide text-slate-500 dark:text-slate-400">
            {item.label}
          </dt>
          <dd className="mt-1 text-lg font-semibold tabular-nums">{item.value}</dd>
        </div>
      ))}
      {data.currency && (
        <p className="col-span-full text-xs text-slate-400 dark:text-slate-500">
          Moneda: {data.currency} · upside vs. consenso (Graham/DCF)
        </p>
      )}
    </dl>
  );
};

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