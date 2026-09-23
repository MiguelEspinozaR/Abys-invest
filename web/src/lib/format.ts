// Helpers de formato compartidos por las vistas del dashboard.

const number2 = new Intl.NumberFormat('es-ES', {
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
});

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
