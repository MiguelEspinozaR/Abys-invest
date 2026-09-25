import { useEffect, useState } from 'react';
import { apiErrorMessage, getJSON, postJSON } from '../api/client';

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
 * Contrato de los endpoints de refresh (plan M4c): recalcula métricas+scores
 * (POST /refresh, sin red) o ejecuta el pipeline completo (POST /force-refresh).
 */
export interface RefreshResult {
  ok: boolean;
  tickers: number;
  duration_ms: number;
  steps?: Record<string, number>;
}

/**
 * Ejecuta un refresh mutador con estado loading/error. Devuelve el resultado
 * de la API y deja la decisión de recargar el dashboard al llamador.
 */
export async function postRefresh(
  path: '/refresh' | '/force-refresh',
): Promise<RefreshResult> {
  return postJSON<RefreshResult>(path);
}