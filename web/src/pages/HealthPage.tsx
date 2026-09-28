import { useCallback, useEffect, useRef, useState } from 'react';
import { apiErrorMessage, getJSONAllowing } from '../api/client';
import type { HealthResponse } from '../api/types';
import Spinner from '../components/Spinner';
import { fmtInt, fmtNumber, fmtTime } from '../lib/format';

/**
 * M5.2 (CA-M5.2-3) — Página /health: estado del backend y de la base de datos
 * con el diseño de la pestaña Health de test3.mikylab.com, construido solo con
 * los componentes del proyecto (decisión D6: sin lucide-react ni react-query).
 *
 * Decisiones del plan (B7):
 * - Estado propio (D10) en vez de `useFetch`: el hook está atado a `getJSON`, que
 *   lanza en el 503 de /health, y ese 503 es INFORMACIÓN (BD caída), no un error.
 *   Se usa `getJSONAllowing('/health', [503])` (D9), que devuelve el cuerpo con
 *   su status.
 * - El flag `alive` evita setState tras desmontaje (patrón de lib/useFetch.ts).
 * - Sin auto-refresh ni polling: cada carga son ~10 queries sobre la BD (riesgo
 *   R8), igual que en test3, donde el refresco es manual.
 * - 503 / `status !== 'ok'` se pintan como "degradado": tarjeta de conexión en
 *   rojo, "—" en el resto y un aviso ámbar; NO como pantalla de error (que se
 *   reserva para no poder hablar con la API).
 */
interface HealthState {
  data: HealthResponse | null;
  error: string | null;
  loading: boolean;
  lastCheck: Date | null;
  degraded: boolean;
}

const INITIAL: HealthState = {
  data: null,
  error: null,
  loading: true,
  lastCheck: null,
  degraded: false,
};

/**
 * Type guard local: si el body no tiene `status`/`database` string no es un
 * HealthResponse (p. ej. el envelope {error} de un 500 del panic-recovery) y se
 * trata como error en vez de pintar tarjetas vacías.
 */
function isHealthResponse(v: unknown): v is HealthResponse {
  if (typeof v !== 'object' || v === null) return false;
  const o = v as Record<string, unknown>;
  return typeof o.status === 'string' && typeof o.database === 'string';
}

export default function HealthPage() {
  const [state, setState] = useState<HealthState>(INITIAL);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  const load = useCallback(async () => {
    setState((s) => ({ ...s, loading: true, error: null }));
    try {
      const { httpStatus, data } = await getJSONAllowing<HealthResponse>('/health', [503]);
      if (!isHealthResponse(data)) {
        throw new Error('respuesta inesperada de GET /health');
      }
      if (!alive.current) return;
      setState({
        data,
        error: null,
        loading: false,
        lastCheck: new Date(),
        degraded: httpStatus === 503 || data.status !== 'ok',
      });
    } catch (err) {
      if (!alive.current) return;
      // Se conservan data/lastCheck anteriores: la vista muestra el error con
      // "Reintentar" encima de la última lectura buena (igual que el dashboard).
      setState((s) => ({ ...s, error: apiErrorMessage(err), loading: false }));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const { data, error, loading, lastCheck, degraded } = state;
  const connected = data?.database === 'connected';
  const tables = data?.tables;
  const totalRows = tables?.reduce((acc, t) => acc + (Number.isFinite(t.rows) ? t.rows : 0), 0);

  return (
    <div className="mx-auto max-w-6xl space-y-5">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-bold">Salud del sistema</h1>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
            {lastCheck ? `Última comprobación: ${fmtTime(lastCheck)}` : 'Métricas en vivo del backend y la base de datos.'}
            {data?.version ? ` · API ${data.version}` : ''}
          </p>
        </div>
        <button
          type="button"
          onClick={() => void load()}
          disabled={loading}
          className="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium hover:bg-slate-100 disabled:opacity-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200 dark:hover:bg-slate-800"
        >
          {loading ? (
            <>
              <Spinner className="mr-2" />
              Actualizando…
            </>
          ) : (
            'Refrescar'
          )}
        </button>
      </header>

      {error && !data ? (
        <div className="rounded-xl border border-red-200 bg-red-50 p-5 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/40 dark:text-red-300">
          <p className="font-medium">No se pudo consultar el estado del servidor.</p>
          <p className="mt-1">Detalle: {error}</p>
          <button
            type="button"
            onClick={() => void load()}
            className="mt-3 rounded border border-current px-3 py-1 text-xs font-medium hover:bg-red-100 dark:hover:bg-red-900/40"
          >
            Reintentar
          </button>
        </div>
      ) : (
        <>
          {error ? (
            <div className="rounded-xl border border-red-200 bg-red-50 p-3 text-xs text-red-700 dark:border-red-900 dark:bg-red-950/40 dark:text-red-300">
              Última lectura no actualizable: {error}{' '}
              <button
                type="button"
                onClick={() => void load()}
                className="rounded border border-current px-2 py-0.5 font-medium hover:bg-red-100 dark:hover:bg-red-900/40"
              >
                Reintentar
              </button>
            </div>
          ) : null}

          {loading && !data ? (
            <>
              <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
                {[0, 1, 2, 3].map((i) => (
                  <div
                    key={i}
                    className="h-24 w-full animate-pulse rounded-xl bg-slate-200 dark:bg-slate-800"
                  />
                ))}
              </div>
              <TableSkeleton />
            </>
          ) : (
            <>
              <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
                <Card
                  titulo="Conexión"
                  ok={connected}
                  valor={data ? (connected ? 'Conectado' : 'Sin conexión') : '—'}
                />
                <Card
                  titulo="Latencia"
                  valor={data?.latency_ms != null ? `${fmtNumber(data.latency_ms)} ms` : '—'}
                  nota="ping a PostgreSQL"
                />
                <Card titulo="Versión PostgreSQL" valor={data?.postgres_version ?? '—'} />
                <Card titulo="Tamaño de la BD" valor={data?.db_size ?? '—'} />
              </div>

              {degraded ? (
                <div className="rounded-xl border border-amber-200 bg-amber-50 p-3 text-xs text-amber-800 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-300">
                  La base de datos está desconectada; las métricas no están disponibles.
                </div>
              ) : null}

              <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
                <h2 className="text-sm font-medium">Tablas ({tables?.length ?? 0})</h2>
                {!tables ? (
                  <p className="mt-3 text-sm text-slate-500 dark:text-slate-400">
                    No se pudieron obtener los conteos de tablas.
                  </p>
                ) : tables.length === 0 ? (
                  <p className="mt-3 text-sm text-slate-500 dark:text-slate-400">
                    Sin tablas en el esquema público.
                  </p>
                ) : (
                  <div className="mt-3 overflow-x-auto">
                    <table className="w-full text-sm">
                      <thead>
                        <tr className="border-b border-slate-200 text-left text-xs uppercase tracking-wide text-slate-500 dark:border-slate-700 dark:text-slate-400">
                          <th className="py-2 pr-3">Tabla</th>
                          <th className="py-2 text-right">Registros</th>
                        </tr>
                      </thead>
                      <tbody>
                        {tables.map((t) => (
                          <tr key={t.name} className="border-t border-slate-100 dark:border-slate-800">
                            <td className="py-1.5 pr-3 font-mono text-sm">{t.name}</td>
                            <td className="py-1.5 text-right tabular-nums">{fmtInt(t.rows)}</td>
                          </tr>
                        ))}
                        <tr className="border-t border-slate-200 dark:border-slate-700">
                          <td className="py-2 pr-3 text-sm font-semibold">Total</td>
                          <td className="py-2 text-right font-semibold tabular-nums">
                            {fmtInt(totalRows)}
                          </td>
                        </tr>
                      </tbody>
                    </table>
                  </div>
                )}
              </section>
            </>
          )}
        </>
      )}
    </div>
  );
}

/**
 * Tarjeta de métrica (misma estructura que en test3): título, valor en
 * `text-xl font-semibold tabular-nums` y, en la de conexión, el badge
 * OK/Error con su aria-label.
 */
function Card({
  titulo,
  valor,
  ok,
  nota,
}: {
  titulo: string;
  valor: string;
  /** undefined = tarjeta sin badge (solo la de conexión lo lleva). */
  ok?: boolean;
  nota?: string;
}) {
  return (
    <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
      <p className="pb-2 text-sm font-medium text-slate-500 dark:text-slate-400">{titulo}</p>
      <div className="flex items-center gap-2">
        <p
          className={`text-xl font-semibold tabular-nums${
            ok === undefined
              ? ''
              : ok
                ? ' text-emerald-600 dark:text-emerald-400'
                : ' text-red-600 dark:text-red-400'
          }`}
        >
          {valor}
        </p>
        {ok !== undefined ? (
          <span
            role="img"
            aria-label={ok ? 'OK' : 'Error'}
            className={`inline-block h-2.5 w-2.5 shrink-0 rounded-full ${
              ok ? 'bg-emerald-500' : 'bg-red-500'
            }`}
          />
        ) : null}
      </div>
      {nota ? <p className="mt-1 text-xs text-slate-400">{nota}</p> : null}
    </div>
  );
}

/** Skeleton local de la tabla (mismo criterio que TableSkeleton del dashboard:
 *  no se crea un componente compartido para un solo uso). */
function TableSkeleton() {
  return (
    <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
      <div className="animate-pulse space-y-4">
        {[0, 1, 2, 3, 4].map((i) => (
          <div key={i} className="flex items-center gap-4">
            <div className="h-4 w-40 rounded bg-slate-200 dark:bg-slate-700" />
            <div className="ml-auto h-4 w-20 rounded bg-slate-200 dark:bg-slate-700" />
          </div>
        ))}
      </div>
    </div>
  );
}
