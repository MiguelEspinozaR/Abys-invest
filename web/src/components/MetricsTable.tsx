import type { DerivedMetric } from '../api/types';
import { fmtDate, fmtNumber, fmtPct } from '../lib/format';

/**
 * Las 8 métricas del Anexo §13 (EPS, P/E, P/B, P/FCF, PEG, ROE, D/E,
 * FCF Yield) con la etiqueta ES y el formateo de su unidad tal como las
 * sirve el API: fcf_yield en puntos porcentuales, roe como fracción (0.31
 * → 31,34%), el resto como ratios/times.
 */
export const METRIC_DEFS: ReadonlyArray<{
  key: string;
  label: string;
  format: (value: number) => string;
}> = [
  { key: 'eps', label: 'EPS', format: fmtNumber },
  { key: 'pe_ratio', label: 'P/E', format: fmtNumber },
  { key: 'pb_ratio', label: 'P/B', format: fmtNumber },
  { key: 'pcf_ratio', label: 'P/FCF', format: fmtNumber },
  { key: 'peg_ratio', label: 'PEG', format: fmtNumber },
  { key: 'roe', label: 'ROE', format: (v) => fmtPct(v * 100) },
  { key: 'de_ratio', label: 'D/E', format: fmtNumber },
  { key: 'fcf_yield', label: 'FCF Yield', format: fmtPct },
];

/**
 * Tabla de métricas derivadas (GET /metrics/{ticker}): muestra solo las
 * métricas del Anexo §13 presentes en el JSON, con el valor más reciente
 * por métrica (el handler devuelve el último as_of por security/metric).
 */
export default function MetricsTable({ metrics }: { metrics: DerivedMetric[] }) {
  const byKey = new Map<string, DerivedMetric>();
  for (const m of metrics) {
    const prev = byKey.get(m.metric);
    if (!prev || m.as_of >= prev.as_of) byKey.set(m.metric, m);
  }
  const rows = METRIC_DEFS.filter((def) => byKey.has(def.key));
  const latestAsOf = [...byKey.values()]
    .sort((a, b) => b.as_of.localeCompare(a.as_of))[0]?.as_of;

  if (rows.length === 0) {
    return <p className="text-sm text-slate-500">Sin métricas disponibles.</p>;
  }

  return (
    <div>
      <p className="mb-2 text-xs text-slate-500 dark:text-slate-400">
        Datos al {fmtDate(latestAsOf)}
      </p>
      <table className="w-full text-sm">
        <tbody>
          {rows.map((def) => {
            const metric = byKey.get(def.key);
            const raw = metric?.value;
            return (
              <tr
                key={def.key}
                className="border-t border-slate-100 first:border-t-0 dark:border-slate-800"
              >
                <td className="py-1.5 pr-3 text-slate-500 dark:text-slate-400">
                  {def.label}
                </td>
                <td className="py-1.5 text-right font-semibold tabular-nums">
                  {raw !== null && raw !== undefined ? def.format(raw) : '—'}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}