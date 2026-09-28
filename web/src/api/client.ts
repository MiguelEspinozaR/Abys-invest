import type { ApiErrorEnvelope } from './types';

/**
 * Base URL de la API (decisión T1, plan M4 D1/D3).
 * - Dev (Vite): el proxy de `vite.config.ts` expone la API Go (sin prefijo)
 *   bajo `/api` con rewrite quitando el prefijo → base `/api`. Esto evita
 *   depender de variables de entorno en desarrollo.
 * - Prod (build estático servido por el API Go, misma-origin): base '' por
 *   defecto, sobreescribible con VITE_API_BASE para despliegues alternativos.
 */
const DEV_BASE = '/api';

export const API_BASE: string =
  import.meta.env.DEV
    ? DEV_BASE
    : (import.meta.env.VITE_API_BASE as string | undefined) || '';

/** Error de API con el envelope estructurado de `errors.go`. */
export class ApiError extends Error {
  readonly code: string;
  readonly status: number;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
  }
}

/** parsea el envelope {error:{code,message}}; respuesta no-JSON → defaults. */
async function parseError(res: Response): Promise<ApiError> {
  let code = `http_${res.status}`;
  let message = res.statusText || `HTTP ${res.status}`;
  try {
    const body = (await res.json()) as Partial<ApiErrorEnvelope>;
    if (body?.error?.code) code = body.error.code;
    if (body?.error?.message) message = body.error.message;
  } catch {
    // respuesta no-JSON: mantener defaults
  }
  return new ApiError(res.status, code, message);
}

/**
 * Mensaje plano para la UI a partir de un error desconocido (ApiError del
 * wrapper, Error genérico o valor arbitrario).
 */
export function apiErrorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return String(err);
}

/**
 * GET tipado: `getJSON<T>('/securities')` → Promise<T> resolved del JSON,
 * o ApiError con el envelope de la API.
 */
export async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    headers: { Accept: 'application/json' },
  });
  if (!res.ok) {
    throw await parseError(res);
  }
  return (await res.json()) as T;
}

/**
 * Resultado de getJSONAllowing: el status HTTP recibido y el cuerpo parseado.
 */
export interface AllowedGet<T> {
  httpStatus: number;
  data: T;
}

/**
 * M5.2 (decisión D9): GET que NO lanza para los estados aceptados y devuelve el
 * cuerpo parseado. Existe para `GET /health`, cuyo 503 **no es un error de API
 * para esa vista** sino información ("degradado") con los campos base dentro
 * (status/database/version) — getJSON lanzaría y la página no podría pintar la
 * BD caída. Para cualquier otro estado no-2xx se comporta exactamente como
 * getJSON (lanza ApiError con el envelope), y un cuerpo no-JSON en un estado
 * aceptado también lanza ApiError (mismo contrato de error que el resto).
 *
 * Reusa API_BASE y el `Accept: application/json` de getJSON: en dev la llamada
 * es `/api/health`, en prod `/health` por XHR (nunca navegación de documento,
 * así que nunca llega HTML del fallback SPA).
 */
export async function getJSONAllowing<T>(path: string, allowed: number[]): Promise<AllowedGet<T>> {
  const res = await fetch(`${API_BASE}${path}`, {
    headers: { Accept: 'application/json' },
  });
  if (!res.ok && !allowed.includes(res.status)) {
    throw await parseError(res);
  }
  try {
    return { httpStatus: res.status, data: (await res.json()) as T };
  } catch {
    // Estado aceptado pero cuerpo no-JSON: no hay nada que mostrar.
    throw new ApiError(res.status, `http_${res.status}`, res.statusText || `HTTP ${res.status}`);
  }
}

/**
 * POST tipado con el mismo contrato de errores que getJSON. Usado por los
 * endpoints mutadores del dashboard (refresh, force-refresh y el arranque del
 * pipeline en M5.1).
 *
 * M5.1: `POST /force-refresh` responde **202 Accepted**, que también es `res.ok`,
 * así que el cuerpo (el PipelineStatus inicial) se decodifica igual que un 200.
 * `path` ya incluye cualquier prefijo de `API_BASE`.
 */
export async function postJSON<T>(path: string): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    method: 'POST',
    headers: { Accept: 'application/json' },
  });
  if (!res.ok) {
    throw await parseError(res);
  }
  return decode<T>(res);
}

/**
 * PUT tipado (plan M5: alta idempotente en la watchlist). Mismo contrato de
 * errores que getJSON/postJSON. Sin cuerpo: los endpoints de watchlist no lo
 * necesitan.
 */
export async function putJSON<T>(path: string): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    method: 'PUT',
    headers: { Accept: 'application/json' },
  });
  if (!res.ok) {
    throw await parseError(res);
  }
  return decode<T>(res);
}

/**
 * DELETE tipado (plan M5: baja idempotente de la watchlist). Mismo contrato de
 * errores que getJSON/postJSON.
 */
export async function deleteJSON<T>(path: string): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    method: 'DELETE',
    headers: { Accept: 'application/json' },
  });
  if (!res.ok) {
    throw await parseError(res);
  }
  return decode<T>(res);
}

/**
 * Decodifica un 2xx con cuerpo JSON. Tolera 204/vacío devolviendo undefined
 * (por si un mutador se sirve sin contenido) en lugar de romper el parseo.
 */
async function decode<T>(res: Response): Promise<T> {
  if (res.status === 204 || res.headers.get('Content-Length') === '0') {
    return undefined as T;
  }
  const text = await res.text();
  if (text === '') {
    return undefined as T;
  }
  return JSON.parse(text) as T;
}
