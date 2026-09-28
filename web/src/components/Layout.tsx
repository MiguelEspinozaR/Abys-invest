import { useState } from 'react';
import { NavLink, Outlet } from 'react-router-dom';

/**
 * M5.2 (CA-M5.2-2) — Layout compartido del SPA: sidebar de navegación con
 * secciones y ruta activa marcada, al estilo de la pestaña Health de
 * test3.mikylab.com.
 *
 * Decisiones del plan (B1):
 * - D7: el layout va en la RUTA PADRE de App.tsx (`<Route element={<Layout/>}>`
 *   con las páginas como rutas hijas + `<Outlet/>`), así hay una sola fuente de
 *   verdad para el fondo/tema y para la navegación, y las páginas solo pierden el
 *   wrapper duplicado (sin tocar su lógica ni su `max-w-*`).
 * - D8: sidebar FIJO en `lg+` (`fixed inset-y-0 left-0 w-60` + `lg:pl-60` en el
 *   contenido) y, por debajo de `lg`, una barra superior con hamburguesa que
 *   despliega el mismo panel. Sin estado de colapso persistente: el servidor de
 *   estáticos solo sirve `index.html`, no habría dónde persistirlo.
 * - Sin dependencias nuevas (D6): sin lucide-react, el icono de menú es un SVG
 *   inline de tres líneas y el botón de la cabecera del dashboard ya marca el
 *   patrón de `Spinner`.
 */
interface NavItem {
  to: string;
  label: string;
  /** `end` para que "/" no quede activo en todas las rutas (react-router). */
  end?: boolean;
}

const NAV: ReadonlyArray<{ title: string; items: ReadonlyArray<NavItem> }> = [
  {
    title: 'Finanzas',
    items: [
      { to: '/', label: 'Dashboard', end: true },
      { to: '/watchlist', label: 'Watchlist' },
    ],
  },
  {
    title: 'Sistema',
    items: [{ to: '/health', label: 'Health' }],
  },
];

const LINK_BASE =
  'block rounded-lg px-3 py-2 text-sm transition-colors';
const LINK_ACTIVE =
  'bg-indigo-500/10 font-medium text-indigo-700 dark:bg-indigo-400/10 dark:text-indigo-300';
const LINK_IDLE =
  'text-slate-600 hover:bg-slate-200/70 dark:text-slate-400 dark:hover:bg-slate-800';

export default function Layout() {
  // Panel móvil: se cierra al navegar (onNavigate) para no tapar la página.
  const [open, setOpen] = useState(false);

  return (
    <div className="min-h-screen bg-slate-50 text-slate-900 dark:bg-slate-950 dark:text-slate-100">
      {/* Sidebar fijo de escritorio (lg+). */}
      <aside className="fixed inset-y-0 left-0 z-30 hidden w-60 flex-col border-r border-slate-200 bg-white/70 p-4 lg:flex dark:border-slate-800 dark:bg-slate-900/60">
        <Brand />
        <Nav onNavigate={() => setOpen(false)} />
      </aside>

      {/* Barra móvil (<lg) con hamburguesa. */}
      <div className="sticky top-0 z-20 flex items-center justify-between border-b border-slate-200 bg-slate-50/90 px-4 py-3 backdrop-blur lg:hidden dark:border-slate-800 dark:bg-slate-950/90">
        <Brand compact />
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-label="Abrir navegación"
          aria-expanded={open}
          aria-controls="nav-movil"
          className="inline-flex items-center justify-center rounded-lg border border-slate-300 p-2 text-slate-600 hover:bg-slate-200/70 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
        >
          {/* hamburguesa (SVG inline: sin lucide-react, decisión D6) */}
          <svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
            <path d="M4 7h16M4 12h16M4 17h16" />
          </svg>
        </button>
      </div>
      {open ? (
        <nav
          id="nav-movil"
          aria-label="Navegación principal (móvil)"
          className="border-b border-slate-200 bg-white px-4 pb-4 pt-2 lg:hidden dark:border-slate-800 dark:bg-slate-900"
        >
          <Nav onNavigate={() => setOpen(false)} />
        </nav>
      ) : null}

      {/* Contenido: el margen izquierdo reserva el hueco del sidebar en lg+; el
          `max-w-*` de cada página se mantiene intacto. */}
      <div className="lg:pl-60">
        <main className="px-4 py-6">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

function Brand({ compact = false }: { compact?: boolean } = {}) {
  return (
    <div className={compact ? '' : 'mb-6'}>
      <p className="text-base font-bold tracking-tight">Abys-Invest</p>
      <p className="text-xs text-slate-500 dark:text-slate-400">Panel</p>
    </div>
  );
}

/** Secciones de navegación. `onNavigate` cierra el panel móvil al pulsar. */
function Nav({ onNavigate }: { onNavigate: () => void }) {
  return (
    <nav aria-label="Navegación principal" className="space-y-6">
      {NAV.map((section) => (
        <div key={section.title}>
          <p className="px-3 pb-2 text-xs font-semibold uppercase tracking-wide text-slate-400 dark:text-slate-500">
            {section.title}
          </p>
          <ul className="space-y-1">
            {section.items.map((item) => (
              <li key={item.to}>
                <NavLink
                  to={item.to}
                  end={item.end}
                  onClick={onNavigate}
                  // NavLink añade aria-current="page" en la ruta activa
                  // (accesibilidad de CA-M5.2-2).
                  className={({ isActive }) => `${LINK_BASE} ${isActive ? LINK_ACTIVE : LINK_IDLE}`}
                >
                  {item.label}
                </NavLink>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </nav>
  );
}
