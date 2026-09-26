import { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { apiErrorMessage, deleteJSON, putJSON } from '../api/client';
import type { SearchResults, WatchlistAck, WatchlistItem, WatchlistResponse } from '../api/types';
import { fmtDate } from '../lib/format';
import { useFetch } from '../lib/useFetch';

// M5 (SPEC §11bis CA-M5-3): página dedicada /watchlist = buscador sobre todo el
// catálogo (GET /securities/search, mínimo 2 caracteres, debounce de 300 ms)
// + lista persistida en BD (GET/PUT/DELETE /watchlist, idempotentes). Nada se
// guarda en localStorage: la lista vive en PostgreSQL (migración 010).
//
// El buscador es solo lectura del catálogo: cada resultado navega a
// /ticker/:ticker o se añade a la watchlist con "+". La lista guardada
// permite eliminar con "✕". Los estados de error y el resultado de la última
// acción se muestran en una vela inline (mismo patrón que el dashboard M4c).

const MIN_QUERY_LEN = 2;
const SEARCH_DEBOUNCE_MS = 300;

export default function WatchlistPage() {
  const navigate = useNavigate();
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  // Debounce del término: evita una petición por pulsación.
  const [debounced, setDebounced] = useState('');
  useEffect(() => {
    const id = window.setTimeout(() => setDebounced(query.trim()), SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(id);
  }, [query]);

  const searchPath = useMemo(() => {
    if (debounced.length < MIN_QUERY_LEN) return null;
    return `/securities/search?q=${encodeURIComponent(debounced)}&limit=10`;
  }, [debounced]);

  const search = useFetch<SearchResults>(searchPath);
  const watchlist = useFetch<WatchlistResponse>('/watchlist');

  const saved = useMemo(() => new Set((watchlist.data ?? []).map((item) => item.ticker)), [watchlist.data]);

  // add/remove son idempotentes en la API; tras la acción se recarga la lista
  // (el servidor es la única fuente de verdad).
  const mutate = async (ticker: string, kind: 'add' | 'remove') => {
    if (busy !== null) return;
    setBusy(ticker);
    setStatus(null);
    try {
      const path = `/watchlist/${encodeURIComponent(ticker)}`;
      if (kind === 'add') {
        await putJSON<WatchlistAck>(path);
        setStatus({ ok: true, text: `${ticker} añadida a la watchlist` });
      } else {
        await deleteJSON<WatchlistAck>(path);
        setStatus({ ok: true, text: `${ticker} eliminada de la watchlist` });
      }
      watchlist.reload();
    } catch (err) {
      setStatus({ ok: false, text: `No se pudo ${kind === 'add' ? 'añadir' : 'eliminar'} ${ticker}: ${apiErrorMessage(err)}` });
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="min-h-screen bg-slate-50 text-slate-900 dark:bg-slate-950 dark:text-slate-100">
      <main className="mx-auto max-w-4xl px-4 py-6">
        <header className="mb-6">
          <Link to="/" className="text-sm text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-300">
            ← Dashboard
          </Link>
          <h1 className="mt-1 text-2xl font-bold">Watchlist</h1>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
            Busca por ticker o nombre en todo el catálogo y guarda tus valores (persistido en la base de datos).
          </p>
        </header>

        <section className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <label htmlFor="watchlist-search" className="text-sm font-medium">
            Buscar valor
          </label>
          <input
            id="watchlist-search"
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="AAPL, Apple, Tesla… (mínimo 2 caracteres)"
            className="mt-2 w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm outline-none focus:border-emerald-500 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
          />
          {query.trim().length > 0 && query.trim().length < MIN_QUERY_LEN ? (
            <p className="mt-2 text-xs text-slate-500 dark:text-slate-400">
              Escribe al menos {MIN_QUERY_LEN} caracteres para buscar.
            </p>
          ) : null}

          {search.loading ? (
            <p className="mt-3 text-sm text-slate-500">Buscando…</p>
          ) : search.error ? (
            <p className="mt-3 text-sm text-red-600 dark:text-red-400">
              {search.error}
              <button
                type="button"
                onClick={search.reload}
                className="ml-2 rounded border border-current px-2 py-0.5 text-xs hover:bg-red-50 dark:hover:bg-red-950/30"
              >
                Reintentar
              </button>
            </p>
          ) : search.data && search.data.length > 0 ? (
            <ul className="mt-3 divide-y divide-slate-100 dark:divide-slate-800">
              {search.data.map((sec) => (
                <li key={sec.id} className="flex items-center gap-3 py-2">
                  <button
                    type="button"
                    onClick={() => navigate(`/ticker/${sec.ticker}`)}
                    className="min-w-0 flex-1 text-left"
                  >
                    <span className="font-mono font-semibold">{sec.ticker}</span>
                    <span className="ml-2 text-slate-600 dark:text-slate-300">{sec.name}</span>
                    {sec.sector ? (
                      <span className="ml-2 text-xs text-slate-400">{sec.sector}</span>
                    ) : null}
                  </button>
                  <button
                    type="button"
                    onClick={() => void mutate(sec.ticker, 'add')}
                    disabled={saved.has(sec.ticker) || busy !== null}
                    title={saved.has(sec.ticker) ? 'Ya está en la watchlist' : 'Añadir a la watchlist'}
                    className="rounded-lg border border-emerald-300 bg-emerald-50 px-2.5 py-1 text-sm font-semibold text-emerald-700 hover:bg-emerald-100 disabled:opacity-40 dark:border-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-300 dark:hover:bg-emerald-900/40"
                  >
                    {saved.has(sec.ticker) ? '✓' : '+'}
                  </button>
                </li>
              ))}
            </ul>
          ) : search.data ? (
            <p className="mt-3 text-sm text-slate-500">Sin resultados para “{debounced}”.</p>
          ) : null}
        </section>

        {status ? (
          <div
            className={`mt-4 rounded-xl border p-4 text-sm ${
              status.ok
                ? 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-300'
                : 'border-red-200 bg-red-50 text-red-700 dark:border-red-900 dark:bg-red-950/40 dark:text-red-300'
            }`}
          >
            {status.text}
          </div>
        ) : null}

        <section className="mt-6 rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <h2 className="text-lg font-semibold">Mi watchlist</h2>
          {watchlist.loading ? (
            <p className="mt-3 text-sm text-slate-500">Cargando…</p>
          ) : watchlist.error ? (
            <p className="mt-3 flex flex-wrap items-center gap-2 text-sm text-red-600 dark:text-red-400">
              {watchlist.error}
              <button
                type="button"
                onClick={watchlist.reload}
                className="rounded border border-current px-2 py-0.5 text-xs hover:bg-red-50 dark:hover:bg-red-950/30"
              >
                Reintentar
              </button>
            </p>
          ) : (watchlist.data?.length ?? 0) === 0 ? (
            <p className="mt-3 text-sm text-slate-500">
              Aún no hay valores guardados. Usa el buscador de arriba para añadir el primero.
            </p>
          ) : (
            <ul className="mt-3 divide-y divide-slate-100 dark:divide-slate-800">
              {(watchlist.data ?? []).map((item) => (
                <WatchlistRow
                  key={item.id}
                  item={item}
                  disabled={busy !== null}
                  onOpen={() => navigate(`/ticker/${item.ticker}`)}
                  onRemove={() => void mutate(item.ticker, 'remove')}
                />
              ))}
            </ul>
          )}
        </section>
      </main>
    </div>
  );
}

function WatchlistRow({
  item,
  disabled,
  onOpen,
  onRemove,
}: {
  item: WatchlistItem;
  disabled: boolean;
  onOpen: () => void;
  onRemove: () => void;
}) {
  return (
    <li className="flex items-center gap-3 py-2.5">
      <button type="button" onClick={onOpen} className="min-w-0 flex-1 text-left">
        <span className="font-mono font-semibold">{item.ticker}</span>
        <span className="ml-2 text-slate-600 dark:text-slate-300">{item.name}</span>
        <span className="ml-2 text-xs text-slate-400">
          {[item.sector, item.exchange].filter(Boolean).join(' · ')}
        </span>
        <span className="ml-2 text-xs text-slate-400">· añadida {fmtDate(item.created_at)}</span>
      </button>
      <button
        type="button"
        onClick={onRemove}
        disabled={disabled}
        title={`Eliminar ${item.ticker} de la watchlist`}
        aria-label={`Eliminar ${item.ticker}`}
        className="rounded-lg border border-red-200 bg-red-50 px-2.5 py-1 text-sm font-semibold text-red-600 hover:bg-red-100 disabled:opacity-40 dark:border-red-900 dark:bg-red-950/40 dark:text-red-300 dark:hover:bg-red-900/40"
      >
        ✕
      </button>
    </li>
  );
}
