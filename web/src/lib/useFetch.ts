import { useEffect, useState } from 'react';
import { apiErrorMessage, getJSON, postJSON } from '../api/client';
import type { PipelineStatus } from '../api/types';

export interface FetchState<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
}

/**
 * Fetch tipado de un path de la API con estados loading/error/data y `reload`
 * (reintento). `path` null ⇒ no se ejecuta ninguna petición (útil cuando el
 * parámetro de ruta falta). Evita setState tras desmontaje con un flag.
 */
export function useFetch<T>(path: string | null): FetchState<T> & { reload: () => void } {
  const [nonce, setNonce] = useState(0);
  const [state, setState] = useState<FetchState<T>>({
    data: null,
    error: null,
    loading: path !== null,
  });

  useEffect(() => {
    if (!path) {
      setState({ data: null, error: null, loading: false });
      return;
    }
    let alive = true;
    setState({ data: null, error: null, loading: true });
    getJSON<T>(path)
      .then((data) => {
        if (alive) setState({ data, error: null, loading: false });
      })
      .catch((err: unknown) => {
        if (alive) setState({ data: null, error: apiErrorMessage(err), loading: false });
      });
    return () => {
      alive = false;
    };
  }, [path, nonce]);

  return { ...state, reload: () => setNonce((n) => n + 1) };
}

/**
 * Contrato de POST /refresh (plan M4c, sin cambios en M5.1): recalcula
 * métricas+scores de forma SÍNCRONA y devuelve el resultado en la propia
 * respuesta.
 *
 * M5.1: /force-refresh ya NO pasa por aquí — es asíncrono (202) y su progreso se
 * sigue con pollPipelineUntilDone. El tipo `steps` se conserva por el contrato
 * histórico del endpoint; el job de fondo publica sus etapas en
 * PipelineStatus.steps.
 */
export interface RefreshResult {
  ok: boolean;
  tickers: number;
  duration_ms: number;
  steps?: Record<string, number>;
}

/**
 * Ejecuta el refresh rápido (síncrono) con estado loading/error. Devuelve el
 * resultado de la API y deja la decisión de recargar el dashboard al llamador.
 */
export async function postRefresh(path: '/refresh'): Promise<RefreshResult> {
  return postJSON<RefreshResult>(path);
}

/** Cadencia del polling del pipeline en segundo plano (M5.1). */
export const PIPELINE_POLL_MS = 1500;

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

/**
 * M5.1: sondea GET /pipeline/status cada PIPELINE_POLL_MS hasta que `isDone(st)`
 * sea true y devuelve ese estado terminal. `onTick` opcional para pintar el
 * progreso en vivo (el dashboard lo usa para las etapas y el chips
 * "procesando…" de la watchlist).
 *
 * Por defecto termina cuando el job deja de estar 'running' (usa así el
 * dashboard para el force-refresh). La página /watchlist pasa además un
 * predicado propio: al añadir un ticker este puede quedar en cola detrás de
 * otro job, y en la rendija entre dos jobs el estado pasa por 'done' antes de
 * volver a 'running' — terminar ahí dejaría el score sin pintar.
 *
 * Termina siempre: si el API se reinicia, el estado vuelve a 'idle' y el bucle
 * sale. Lanza ApiError si la petición falla (el llamador lo muestra en su vela
 * de estado).
 */
export async function pollPipelineUntilDone(
  onTick?: (st: PipelineStatus) => void,
  isDone: (st: PipelineStatus) => boolean = (st) => st.status !== 'running',
): Promise<PipelineStatus> {
  for (;;) {
    const st = await getJSON<PipelineStatus>('/pipeline/status');
    onTick?.(st);
    if (isDone(st)) return st;
    await sleep(PIPELINE_POLL_MS);
  }
}

/**
 * ¿El ticker está siendo procesado ahora o esperando en cola? Usa `tickers` ∪
 * `pending` del estado: sin `pending` la UI no podría distinguir "procesando" de
 * "en cola" (decisión D6 del plan M5.1).
 */
export function isTickerInPipeline(st: PipelineStatus | null, ticker: string): boolean {
  if (!st) return false;
  return st.tickers.includes(ticker) || st.pending.includes(ticker);
}
