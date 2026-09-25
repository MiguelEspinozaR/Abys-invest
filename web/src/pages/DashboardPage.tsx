import { useCallback, useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { apiErrorMessage, getJSON } from '../api/client';
import type { ScoresHistoryItem, Security, ValuationResponse } from '../api/types';
import ScoreBadge from '../components/ScoreBadge';
import { fmtNumber, fmtPct } from '../lib/format';
import { postRefresh, type RefreshResult } from '../lib/useFetch';

// /scores (sin filtro) incluye las 2 fixtures de integración (M3TST/T4BSC)
// presentes en la BD de prueba; se excluyen del catálogo del dashboard.
const TEST_FIXTURE_TICKERS = new Set<string>(['M3TST', 'T4BSC']);

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
 * (métricas+scores, sin red) y "Pipeline" ejecuta POST /force-refresh
 * (edgar → prices → sector → metrics → scores, con confirmación previa).
 * Ambos recargan el catálogo al terminar; la política loopback-only del API
 * responde 403 desde un cliente remoto (el deploy local :8082 es loopback).
 */
export default function DashboardPage() {
  const navigate = useNavigate();
  const [rows, setRows] = useState<CatalogRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState<'refresh' | 'force' | null>(null);
  const [refreshStatus, setRefreshStatus] = useState<{ ok: boolean; text: string } | null>(null);

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

  // M4c: ejecuta un refresh (rápido o pipeline completo) y recarga el catálogo.
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
        const res: RefreshResult = await postRefresh(kind === 'force' ? '/force-refresh' : '/refresh');
        const msgs: Record<string, string> = {
          refresh: `Datos recalculados (${res.tickers} tickers en ${res.duration_ms} ms)`,
          force: `Pipeline completado (${res.tickers} tickers, ${res.duration_ms} ms)`,
        };
        setRefreshStatus({ ok: true, text: msgs[kind] });
        await load();
      } catch (err) {
        setRefreshStatus({ ok: false, text: `Refresh falló: ${apiErrorMessage(err)}` });
      } finally {
        setRefreshing(null);
      }
    },
    [load, refreshing],
  );

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div className="min-h-screen bg-slate-50 text-slate-900 dark:bg-slate-950 dark:text-slate-100">
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
              {refreshing === 'refresh' ? 'Recalculando…' : 'Recalcular métricas'}
            </button>
            <button
              type="button"
              onClick={() => void runRefresh('force')}
              disabled={loading || refreshing !== null}
              className="rounded-lg border border-amber-300 bg-amber-50 px-3 py-1.5 text-sm font-medium text-amber-800 hover:bg-amber-100 disabled:opacity-50 dark:border-amber-800 dark:bg-amber-950/40 dark:text-amber-300 dark:hover:bg-amber-900/40"
            >
              {refreshing === 'force' ? 'Pipeline en ejecución…' : 'Pipeline completo'}
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
                  <th className="px-4 py-3 text-right">Graham</th>
                  <th className="px-4 py-3 text-right">DCF</th>
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
                      <td className="px-4 py-2.5 text-right tabular-nums">
                        {valuation?.value?.graham != null ? fmtNumber(valuation.value.graham) : '—'}
                      </td>
                      <td className="px-4 py-2.5 text-right tabular-nums">
                        {valuation?.value?.dcf != null ? fmtNumber(valuation.value.dcf) : '—'}
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
      </main>
    </div>
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