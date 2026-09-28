// Helpers de formato compartidos por las vistas del dashboard.

const number2 = new Intl.NumberFormat('es-ES', {
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
});

/**
 * Locale de los NÚMEROS: es-ES en TODO el dashboard (no se toca en M5.2 —
 * decisión D11 del plan). Existe como constante para que el locale no quede
 * disperso por las vistas.
 */
const NUMBER_LOCALE = 'es-ES';

/**
 * Locale del RELOJ: es-BO (decisión D11 del plan M5.2, el de test3). El formato
 * es idéntico al de es-ES (HH:MM:SS); la decisión es inocua y reversible aquí.
 */
const TIME_LOCALE = 'es-BO';

const integer0 = new Intl.NumberFormat(NUMBER_LOCALE, { maximumFractionDigits: 0 });

/**
 * Formatea un número con 2 decimales (es-ES). Null/undefined/no-numérico → "—".
 */
export function fmtNumber(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) {
    return '—';
  }
  return number2.format(value);
}

/**
 * Formatea un valor YA expresado en puntos porcentuales (upside_pct, fcf_yield,
 * total_return_pct…): 15.5 → "15,50%". Los ratios en fracción (roe, cagr,
 * volatility_annual…) deben multiplicarse ×100 por el llamador.
 * Null/undefined/no-numérico → "—".
 */
export function fmtPct(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) {
    return '—';
  }
  return `${number2.format(value)}%`;
}

/**
 * Convierte una fecha ISO (YYYY-MM-DD o timestamp completo) a DD/MM/YYYY.
 * Se extrae la parte de fecha del string para evitar corrimientos por zona
 * horaria local. Null/undefined → "—".
 */
export function fmtDate(iso: string | null | undefined): string {
  if (!iso) {
    return '—';
  }
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(iso);
  if (!m) {
    return iso;
  }
  return `${m[3]}/${m[2]}/${m[1]}`;
}

/**
 * M5.2 (página Health): formatea un ENTERO (conteos de filas de `tables`) con
 * separador de miles es-ES y sin decimales — `fmtNumber` añade 2 decimales y no
 * sirve para un conteo de registros. Null/undefined/no finito → "—".
 */
export function fmtInt(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) {
    return '—';
  }
  return integer0.format(value);
}

/**
 * M5.2 (página Health): hora local de la última comprobación en es-BO
 * (decisión D11: el usuario es de mikylab.com y test3 formatea así; el formato
 * HH:MM:SS es el mismo que en es-ES). Sin fecha aún → "—".
 */
export function fmtTime(d: Date | null): string {
  return d ? d.toLocaleTimeString(TIME_LOCALE) : '—';
}
