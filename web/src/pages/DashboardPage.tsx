import { useCallback, useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { apiErrorMessage, getJSON, postJSON } from '../api/client';
import type {
  PipelineStatus,
  ScoresHistoryItem,
  Security,
  ValuationResponse,
  WatchlistItem,
  WatchlistResponse,
} from '../api/types';
import ScoreBadge from '../components/ScoreBadge';
import Spinner from '../components/Spinner';
import { fmtNumber, fmtPct } from '../lib/format';
import {
  isTickerInPipeline,
  pollPipelineUntilDone,
  postRefresh,
  useFetch,
  type RefreshResult,
} from '../lib/useFetch';

// /scores (sin filtro) incluye las 2 fixtures de integración (M3TST/T4BSC)
// presentes en la BD de prueba; se excluyen del catálogo del dashboard.
const TEST_FIXTURE_TICKERS = new Set<string>(['M3TST', 'T4BSC']);

// M5.1: nombres de las etapas del pipeline, en el orden real, para el progreso
// del botón "Pipeline completo" (la API publica steps por etapa).
const PIPELINE_STEPS = ['edgar', 'prices', 'sector', 'metrics', 'scores'] as const;

interface CatalogRow {
  security: Security;
  score: ScoresHistoryItem;
  valuation?: ValuationResponse;
}

/**
 * T2 — Dashboard: catálogo con score y señal.
 *
 * Datos: GET /scores (todas las filas con score, la BD tiene ~10k securities
 * de las que solo 11 tienen score) + GET /securities para resolver el detalle
 * (ticker/nombre) por security_id. El enriquecimiento con /valuation/{ticker}
 * (precio, P/E, ROE) es no bloqueante: la fila se muestra con "—" mientras
 * llega o si falla.
 *
 * M4c — Refresh desde el dashboard: "Recalcular" ejecuta POST /refresh
 * (métricas+scores, sin red, síncrono) y "Pipeline" ejecuta POST /force-refresh.
 *
 * M5.1 — (1) el pipeline completo pasa a ser ASÍNCRONO: POST /force-refresh
 * devuelve 202 y su progreso se sigue con polling a /pipeline/status (1.5 s),
 * de modo que recargar o navegar ya no lo aborta; (2) nueva sección "Mi
 * watchlist" con score/signal (nullable) y enlace al ticker, alimentada por
 * GET /watchlist (que ya trae el último score: sin N+1). La política
 * loopback-only del API responde 403 desde un cliente remoto (el deploy local
 * :8082 es loopback).
 *
 * UX: los dos botones de refresh ("Recalcular métricas" y "Pipeline completo")
 * añaden la rueda `Spinner` mientras su acción está en curso, junto al texto de
 * estado. No cambia la lógica: aparece con `refreshing` y desaparece cuando
 * `runRefresh` termina (resolución de /refresh o fin del polling de
 * /pipeline/status, que ya lo hacía en el `finally`).
 */
export default function DashboardPage() {
  const navigate = useNavigate();
  const [rows, setRows] = useState<CatalogRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState<'refresh' | 'force' | null>(null);
  const [refreshStatus, setRefreshStatus] = useState<{ ok: boolean; text: string } | null>(null);
  // M5.1: estado en vivo del pipeline en segundo plano (null = sin job).
  const [pipeline, setPipeline] = useState<PipelineStatus | null>(null);
  const watchlist = useFetch<WatchlistResponse>('/watchlist');

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [catalog, scored] = await Promise.all([
        getJSON<Security[]>('/securities?limit=20000'),
        getJSON<ScoresHistoryItem[]>('/scores'),
      ]);
      const byId = new Map<number, Security>(catalog.map((s) => [s.id, s] as [number, Security]));

      // /scores viene ordenado por as_of DESC → primer ítem por security_id
      // es el score más reciente.
      const latestBySecurity = new Map<number, ScoresHistoryItem>();
      for (const item of scored) {
        if (!latestBySecurity.has(item.security_id)) latestBySecurity.set(item.security_id, item);
      }

      const base: CatalogRow[] = [];
      for (const [securityId, score] of latestBySecurity) {
        const security = byId.get(securityId);
        if (!security || TEST_FIXTURE_TICKERS.has(security.ticker)) continue;
        base.push({ security, score });
      }
      base.sort((a, b) => a.security.ticker.localeCompare(b.security.ticker));
      setRows(base);
      setLoading(false);

      // Precio/P/E/ROE: no bloquea la tabla; se rellenan al resolverse.
      await Promise.allSettled(
        base.map((row) =>
          getJSON<ValuationResponse>(`/valuation/${row.security.ticker}`).then(
            (valuation) => {
              setRows((prev) =>
                prev.map((r) =>
                  r.security.ticker === row.security.ticker ? { ...r, valuation } : r,
                ),
              );
            },
          ),
        ),
      );
    } catch (err) {
      setError(apiErrorMessage(err));
      setLoading(false);
    }
  }, []);

  // M4c/M5.1: "Recalcular" es síncrono (POST /refresh); "Pipeline completo" es
  // asíncrono (POST /force-refresh → 202 + polling de /pipeline/status).
  const runRefresh = useCallback(
    async (kind: 'refresh' | 'force') => {
      if (refreshing) return; // ya hay uno en curso (el API también responde 409)
      if (kind === 'force' && !window.confirm('¿Re-ingestar el pipeline completo? Toma ~1-2 min.')) {
        return;
      }
      setRefreshing(kind);
      setRefreshStatus(null);
      setError(null);
      try {
        if (kind === 'force') {
          // 202 Accepted: el job ya está corriendo en el servidor con contexto
          // propio; aquí solo se sigue su progreso y se recarga al terminar.
          setPipeline(await postJSON<PipelineStatus>('/force-refresh'));
          const final = await pollPipelineUntilDone(setPipeline);
          if (final.status === 'error') {
            setRefreshStatus({
              ok: false,
              text: `Pipeline falló: ${final.error ?? 'sin detalle'}`,
            });
          } else {
            setRefreshStatus({
              ok: true,
              text: `Pipeline completado (${final.tickers.length} tickers, ${
                Object.keys(final.steps).length
              } etapas)`,
            });
            await load();
            watchlist.reload();
          }
        } else {
          const res: RefreshResult = await postRefresh('/refresh');
          setRefreshStatus({
            ok: true,
            text: `Datos recalculados (${res.tickers} tickers en ${res.duration_ms} ms)`,
          });
          await load();
          watchlist.reload();
        }
      } catch (err) {
        setRefreshStatus({ ok: false, text: `Refresh falló: ${apiErrorMessage(err)}` });
      } finally {
        setRefreshing(null);
      }
    },
    [load, refreshing, watchlist],
  );

  useEffect(() => {
    void load();
  }, [load]);

  // Las tarjetas de la watchlist (sin fixtures de integración).
  const watchlistItems = (watchlist.data ?? []).filter(
    (item) => !TEST_FIXTURE_TICKERS.has(item.ticker),
  );

  return (
    // M5.2: el wrapper min-h-screen/bg-slate-50 y el fondo/tema los aporta el
    // <Layout/> (ruta padre en App.tsx); aquí solo queda el contenido con su
    // max-w intacto. El link "Watchlist" de la cabecera se quitó: ahora lo da el
    // sidebar (se mantiene el CTA dentro de la sección cuando está vacía, que es
    // una acción contextual, no navegación global).
    <main className="mx-auto max-w-6xl px-4 py-6">
      <header className="mb-6 flex flex-wrap items-center justify-between gap-4">
        <div>
          <h1 className="text-2xl font-bold">Dashboard</h1>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
            Catálogo con score y señal — {rows.length} tickers
          </p>
        </div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => void runRefresh('refresh')}
            disabled={loading || refreshing !== null}
            className="rounded-lg border border-emerald-300 bg-emerald-50 px-3 py-1.5 text-sm font-medium text-emerald-800 hover:bg-emerald-100 disabled:opacity-50 dark:border-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-300 dark:hover:bg-emerald-900/40"
          >
            {refreshing === 'refresh' ? (
              <>
                <Spinner className="mr-2" />
                Recalculando…
              </>
            ) : (
              'Recalcular métricas'
            )}
          </button>
          <button
            type="button"
            onClick={() => void runRefresh('force')}
            disabled={loading || refreshing !== null}
            className="rounded-lg border border-amber-300 bg-amber-50 px-3 py-1.5 text-sm font-medium text-amber-800 hover:bg-amber-100 disabled:opacity-50 dark:border-amber-800 dark:bg-amber-950/40 dark:text-amber-300 dark:hover:bg-amber-900/40"
          >
            {refreshing === 'force' ? (
              <>
                <Spinner className="mr-2" />
                Pipeline en ejecución…
              </>
            ) : (
              'Pipeline completo'
            )}
          </button>
          <button
            type="button"
            onClick={() => void load()}
            disabled={loading || refreshing !== null}
            className="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium hover:bg-slate-100 disabled:opacity-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200 dark:hover:bg-slate-800"
          >
            {loading ? 'Cargando…' : 'Actualizar'}
          </button>
        </div>
      </header>

      {pipelineStepsLine(pipeline) ? (
        <div className="mb-4 rounded-xl border border-amber-200 bg-amber-50 p-3 text-xs text-amber-800 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-300">
          {pipelineStepsLine(pipeline)}
        </div>
      ) : null}

      {refreshStatus ? (
        <div
          className={`mb-4 rounded-xl border p-4 text-sm ${
            refreshStatus.ok
              ? 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-300'
              : 'border-red-200 bg-red-50 text-red-700 dark:border-red-900 dark:bg-red-950/40 dark:text-red-300'
          }`}
        >
          {refreshStatus.text}
        </div>
      ) : null}

      {error ? (
        <div className="rounded-xl border border-red-200 bg-red-50 p-5 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/40 dark:text-red-300">
          <p>No se pudo cargar el catálogo: {error}</p>
          <button
            type="button"
            onClick={() => void load()}
            className="mt-3 rounded border border-current px-3 py-1 text-xs font-medium hover:bg-red-100 dark:hover:bg-red-900/40"
          >
            Reintentar
          </button>
        </div>
      ) : loading ? (
        <TableSkeleton />
      ) : rows.length === 0 ? (
        <div className="rounded-xl border border-slate-200 bg-white p-10 text-center text-sm text-slate-500 dark:border-slate-800 dark:bg-slate-900">
          Sin tickers con score disponible en la API.
        </div>
      ) : (
        <div className="overflow-x-auto rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-slate-200 text-left text-xs uppercase tracking-wide text-slate-500 dark:border-slate-700 dark:text-slate-400">
                <th className="px-4 py-3">Ticker</th>
                <th className="px-4 py-3">Nombre</th>
                <th className="px-4 py-3 text-right">Precio</th>
                {/* Escenario BASE de cada método (2.0.0): los tres escenarios
                    y el bloque completo están en la ficha del ticker. */}
                <th className="px-4 py-3 text-right" title="Escenario base de Graham">
                  Graham base
                </th>
                <th className="px-4 py-3 text-right" title="Escenario base del DCF">
                  DCF base
                </th>
                <th className="px-4 py-3 text-right">Score</th>
                <th className="px-4 py-3">Señal</th>
                <th className="px-4 py-3 text-right">P/E</th>
                <th className="px-4 py-3 text-right">ROE</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => {
                const valuation = row.valuation;
                const pe = valuation?.metrics?.pe_ratio;
                const roe = valuation?.metrics?.roe;
                return (
                  <tr
                    key={row.security.id}
                    onClick={() => navigate(`/ticker/${row.security.ticker}`)}
                    className="cursor-pointer border-t border-slate-100 transition-colors hover:bg-slate-50 dark:border-slate-800 dark:hover:bg-slate-800/50"
                  >
                    <td className="px-4 py-2.5 font-mono font-semibold">
                      {row.security.ticker}
                    </td>
                    <td className="px-4 py-2.5 text-slate-600 dark:text-slate-300">
                      {row.security.name}
                    </td>
                    <td className="px-4 py-2.5 text-right tabular-nums">
                      {valuation?.price != null ? fmtNumber(valuation.price) : '—'}
                    </td>
                    {/* `graham.base`/`dcf.base` del contrato 2.0.0: un método
                        `unavailable` no tiene bloque, así que se pinta "—" y no
                        se cae al otro método para rellenarlo. */}
                    <td className="px-4 py-2.5 text-right tabular-nums">
                      {valuation?.value?.graham.base != null
                        ? fmtNumber(valuation.value.graham.base)
                        : '—'}
                    </td>
                    <td className="px-4 py-2.5 text-right tabular-nums">
                      {valuation?.value?.dcf.base != null ? fmtNumber(valuation.value.dcf.base) : '—'}
                    </td>
                    <td className="px-4 py-2.5 text-right font-semibold tabular-nums">
                      {fmtNumber(row.score.score)}
                    </td>
                    <td className="px-4 py-2.5">
                      <ScoreBadge score={row.score.score} signal={row.score.signal} />
                    </td>
                    <td className="px-4 py-2.5 text-right tabular-nums">
                      {pe != null ? fmtNumber(pe) : '—'}
                    </td>
                    <td className="px-4 py-2.5 text-right tabular-nums">
                      {roe != null ? fmtPct(roe * 100) : '—'}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      <WatchlistSection
        items={watchlistItems}
        loading={watchlist.loading}
        error={watchlist.error}
        pipeline={pipeline}
        onRetry={watchlist.reload}
      />
    </main>
  );
}

// pipelineStepsLine resume el progreso en vivo del job de pipeline (M5.1):
// etapas conocidas con su conteo y la cola pendiente. "" cuando no hay job.
function pipelineStepsLine(st: PipelineStatus | null): string {
  if (!st || st.status === 'idle') return '';
  const done = Object.keys(st.steps).length;
  if (st.status === 'running') {
    const parts = PIPELINE_STEPS.filter((s) => s in st.steps).map(
      (s) => `${s}: ${st.steps[s]}`,
    );
    const cola = st.pending.length > 0 ? ` · en cola: ${st.pending.join(', ')}` : '';
    return `Pipeline ${st.kind ?? ''} en curso (${done}/${PIPELINE_STEPS.length} etapas${
      parts.length > 0 ? ` — ${parts.join(', ')}` : ''
    })${cola}`;
  }
  if (st.status === 'error') {
    return `Pipeline falló: ${st.error ?? 'sin detalle'}`;
  }
  return '';
}

// WatchlistSection (M5.1): "Mi watchlist" con score/signal nullable, enlace al
// ticker y CTA a /watchlist cuando está vacía. El resto del dashboard queda
// intacto (la sección va después de la tabla principal).
function WatchlistSection({
  items,
  loading,
  error,
  pipeline,
  onRetry,
}: {
  items: WatchlistItem[];
  loading: boolean;
  error: string | null;
  pipeline: PipelineStatus | null;
  onRetry: () => void;
}) {
  return (
    <section id="mi-watchlist" className="mt-8">
      <div className="mb-3 flex items-baseline justify-between">
        <h2 className="text-lg font-semibold">Mi watchlist</h2>
        <span className="text-xs text-slate-500 dark:text-slate-400">
          {items.length} {items.length === 1 ? 'valor' : 'valores'}
        </span>
      </div>

      {loading ? (
        <div className="grid animate-pulse gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {[0, 1, 2].map((i) => (
            <div
              key={i}
              className="h-24 rounded-xl border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900"
            />
          ))}
        </div>
      ) : error ? (
        <div className="rounded-xl border border-red-200 bg-red-50 p-5 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/40 dark:text-red-300">
          <p>No se pudo cargar la watchlist: {error}</p>
          <button
            type="button"
            onClick={onRetry}
            className="mt-3 rounded border border-current px-3 py-1 text-xs font-medium hover:bg-red-100 dark:hover:bg-red-900/40"
          >
            Reintentar
          </button>
        </div>
      ) : items.length === 0 ? (
        <div className="rounded-xl border border-slate-200 bg-white p-8 text-center text-sm text-slate-500 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-400">
          <p>Aún no has guardado valores.</p>
          <Link
            to="/watchlist"
            className="mt-3 inline-block rounded-lg border border-indigo-300 bg-indigo-50 px-3 py-1.5 text-sm font-medium text-indigo-800 hover:bg-indigo-100 dark:border-indigo-800 dark:bg-indigo-950/40 dark:text-indigo-300 dark:hover:bg-indigo-900/40"
          >
            Ir a mi watchlist
          </Link>
        </div>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {items.map((item) => (
            <Link
              key={item.id}
              to={`/ticker/${item.ticker}`}
              className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm transition-colors hover:border-indigo-300 hover:bg-slate-50 dark:border-slate-800 dark:bg-slate-900 dark:hover:border-indigo-800 dark:hover:bg-slate-800/50"
            >
              <div className="flex items-center justify-between gap-2">
                <span className="font-mono font-semibold">{item.ticker}</span>
                <ScoreBadge signal={item.signal} />
              </div>
              <p className="mt-1 truncate text-sm text-slate-600 dark:text-slate-300">
                {item.name}
              </p>
              <p className="mt-1 truncate text-xs text-slate-400">
                {[item.sector, item.exchange].filter(Boolean).join(' · ') || '—'}
              </p>
              <div className="mt-2 flex items-center justify-between">
                <span className="text-lg font-semibold tabular-nums">
                  {item.score != null ? fmtNumber(item.score) : '—'}
                </span>
                {isTickerInPipeline(pipeline, item.ticker) ? (
                  <span className="text-xs text-amber-600">procesando…</span>
                ) : null}
              </div>
            </Link>
          ))}
        </div>
      )}
    </section>
  );
}

function TableSkeleton() {
  return (
    <div className="rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
      <div className="animate-pulse space-y-4 p-4">
        {[0, 1, 2, 3, 4, 5].map((i) => (
          <div key={i} className="flex items-center gap-4">
            <div className="h-4 w-16 rounded bg-slate-200 dark:bg-slate-700" />
            <div className="h-4 w-48 rounded bg-slate-200 dark:bg-slate-700" />
            <div className="ml-auto h-4 w-20 rounded bg-slate-200 dark:bg-slate-700" />
          </div>
        ))}
      </div>
    </div>
  );
}