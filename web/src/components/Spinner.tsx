/**
 * M5.1 (UX): rueda girando mínima para indicar una acción en curso.
 *
 * Es un <span> decorativo con el borde en `currentColor`: hereda el color del
 * texto del botón donde se inserta (y su variante oscura) sin props de color.
 * La animación es la utilidad `animate-spin` del tema por defecto de Tailwind
 * (`spin 1s linear infinite`), no hace falta CSS adicional ni @apply.
 *
 * No aporta texto: el estado "en curso" lo comunica el propio botón, así que va
 * `aria-hidden` para no duplicar el anuncio del lector de pantalla.
 */
interface SpinnerProps {
  /** Clases extra (tamaño, margen, alineación) que sobrescriben las por defecto. */
  className?: string;
}

const SPINNER_BASE =
  'inline-block h-4 w-4 shrink-0 rounded-full border-2 border-current border-r-transparent align-[-0.125em] animate-spin';

export default function Spinner({ className }: SpinnerProps = {}) {
  return (
    <span
      aria-hidden="true"
      className={className ? `${SPINNER_BASE} ${className}` : SPINNER_BASE}
    />
  );
}
