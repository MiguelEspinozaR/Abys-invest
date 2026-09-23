import type { Signal } from '../api/types';

const KNOWN_SIGNALS: ReadonlyArray<Signal> = ['comprar', 'mantener', 'vender'];

const SIGNAL_WORDS: Record<Signal, string> = {
  comprar: 'Comprar',
  mantener: 'Mantener',
  vender: 'Vender',
};

type BadgeTone = Signal | 'unknown';

const TONES: Record<BadgeTone, string> = {
  comprar:
    'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/50 dark:text-emerald-300',
  mantener:
    'bg-amber-100 text-amber-800 dark:bg-amber-900/50 dark:text-amber-300',
  vender: 'bg-red-100 text-red-800 dark:bg-red-900/50 dark:text-red-300',
  unknown:
    'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300',
};

interface ScoreBadgeProps {
  /** score 0-100; null/undefined → "s/d" */
  score?: number | null;
  /** señal del API (es-ES); ausente/desconocida → gris "s/d" */
  signal?: string | null;
  size?: 'sm' | 'lg';
}

/**
 * Badge de score coloreado por señal: comprar=verde, mantener=ámbar,
 * vender=rojo; señal ausente → gris con "s/d". El score se muestra como
 * entero redondeado (0-100).
 */
export default function ScoreBadge({ score, signal, size = 'sm' }: ScoreBadgeProps) {
  const tone: BadgeTone = KNOWN_SIGNALS.includes(signal as Signal)
    ? (signal as Signal)
    : 'unknown';
  const dims = size === 'lg' ? 'px-3 py-1 text-base' : 'px-2.5 py-0.5 text-xs';
  const text = score !== null && score !== undefined ? String(Math.round(score)) : 's/d';
  return (
    <span
      className={`inline-flex items-center rounded-full font-semibold tabular-nums ${TONES[tone]} ${dims}`}
      title={tone === 'unknown' ? 'Sin señal' : `Señal: ${SIGNAL_WORDS[tone]}`}
    >
      {text}
    </span>
  );
}