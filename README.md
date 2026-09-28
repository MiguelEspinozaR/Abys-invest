# Abys-Invest

**Value investing analysis engine** — SEC EDGAR fundamentals → score 0-100 with buy/hold/sell signal, Graham/DCF intrinsic value, comparables & SMA backtest.

> **M1 ✔ (core) completed.** **M2 ✔ (prices & metrics) completed.** **M3 ✔ (valuation, score, comparables, backtest, API) completed** (hotfix M3 + calibración 2026-09-23). **M4 ✔ (dashboard + static serving + deploy systemd) completed**; alertas (CA M4-2) diferidas a M6. **M4b ✔ (consenso promedio + señal textual) completed**. **M4c ✔ (refresh de datos desde el dashboard + fix mapeo XBRL) completed**. **M4d ✔ (Air live-reload dev + deploy opcional) completed**. **M5 ✔ (buscador + watchlist) completed.** **M5.1 ✔ (watchlist integración: pipeline asíncrono, GET /pipeline/status, ingesta automática en 2º plano, Mi watchlist en dashboard) completed.** **M5.2 ✔ (página Health: GET /health ampliado + sidebar con secciones Finanzas/Sistema) completed** (2026-09-27; suite 276/0/1).

---

## What is Abys-Invest?

Abys-Invest is a personal value investing application. It fetches financial statements of NYSE/NASDAQ-listed companies from the SEC EDGAR public filings (XBRL 10-K/10-Q), computes fundamental metrics (profitability, leverage, liquidity, valuation), queries historical daily prices (Yahoo Finance) and macro series (BLS CPI), and calculates derived valuation metrics (EPS, P/E, P/B, P/FCF, PEG, ROE, D/E, FCF Yield). Future: score 0-100 with buy/hold/sell signal and Graham/DCF intrinsic value.

**Status by milestone:**

| Milestone | Status | Description |
|-----------|--------|-------------|
| M1 — Foundation | ✅ Done | Repo, DB schema + migrations, collector (SEC EDGAR), API health check |
| M2 — Prices & Metrics | ✅ Done | Yahoo Finance v8 chart adapter, BLS CPI macro, engine de métricas (EPS, P/E, P/B, P/FCF, PEG, ROE, D/E, FCF Yield) |
| M3 — Valuation & Score | ✅ Done | Graham `(2×g)+8.5` × EPS último FY, DCF simplificado (WACC 10%, 5a, g_term 2.5%), margin of safety, score 0-100 (pesos 35/30/20/15, umbrales ≥70/40-69/<40), comparables sectoriales, backtest SMA 50/200, API REST completa (11 endpoints). Calibración 2026-09-23 con pares sectoriales reales; hotfix M3 (aislamiento security_id + guardado de métricas NULL). |
| M4 — UI & Deploy | ✅ Done | Dashboard React + Vite + Tailwind en `web/`, estáticos servidos por API Go (FileServer + SPA fallback), deploy systemd. Alertas (CA M4-2) diferidas a M6 por decisión usuario 2026-09-23. |
| M5 — Buscador + Watchlist | ✅ Done | `GET /securities/search?q=&limit=`, `GET /watchlist` (content negotiation: text/html→SPA, application/json→JSON, `Vary: Accept`), `PUT/DELETE /watchlist/{ticker}` (loopback-only, 403 fuera de localhost, idempotentes, 404 ticker no catalogado); página `/watchlist` con buscador (debounce 300ms, min 2 chars). Watchlist permite delistados. Suite 248/0/1. |
| M5.1 — Watchlist integración | ✅ Done | `POST /force-refresh` ahora **asíncrono** (202 Accepted + estado inicial; job en 2º plano con `context.Background()` + timeout 30 min, NO el contexto del HTTP request); nuevo `GET /pipeline/status` (8 claves: `status` idle\|running\|done\|error, `kind` watchlist\|force, `tickers`, `steps` {edgar,prices,sector,metrics,scores}, `started_at`, `finished_at`, `error`, `pending`); `PUT /watchlist/{ticker}` dispara ingesta en 2º plano (prices→sector→metrics→scores, SIN EDGAR), cola FIFO con dedup por ticker si hay job corriendo; `GET /watchlist` ampliado con `score` (number\|null) y `signal` (string\|null) del último score por LEFT JOIN LATERAL; dashboard con sección "Mi watchlist" (tarjetas ticker/nombre/score/signal, enlace /ticker/{ticker}, "procesando…" mientras job corre). El SPA hace polling de `GET /pipeline/status` (intervalo 1.5s). Navegar/recargar YA NO aborta el pipeline (era el bug "failed to fetch"). Suite 269/0/1; bundle `index-BDERmpRv.js`. |
| M5.2 — Página Health | ✅ Done | `GET /health` ampliado **aditivamente** (4 campos nuevos sin tocar el contrato M1): response 200 (BD conectada) `{status, database, version, latency_ms (ms ping), postgres_version (e.g. "18.6"), db_size (e.g. "21 MB"), tables: [{name, rows}...]}` con 9 tablas: securities, daily_prices, fundamentals, derived_metrics, scores, watchlist, macro_series, edgar_staging, xbrl_concept_map. 503 sin BD: SOLO `{status:degraded, database:disconnected, version}` (3 claves; los campos ampliados van omitted). Timeout de contexto 3s; status HTTP depende solo del Ping; si metadata/conteos fallan → 200 con campos presentes + `slog.Warn`. Frontend: nuevo sidebar/layout (`web/src/components/Layout.tsx`) con secciones "Finanzas" (Dashboard, Watchlist) y "Sistema" (Health); nueva página `/health` (HealthPage.tsx) con 4 tarjetas (Conexión, Latencia, Versión PostgreSQL, Tamaño) + tabla de tablas con conteos + botón Refrescar con spinner + "Última comprobación" localizada es-BO. Bundle nuevo: `index-BeqNKrtA.js` (CSS `index-CD-PyuE9.css`). Suite 276/0/1 (165 unit/0/0 + integración). F5 en `/health` muestra JSON crudo (aceptado por diseño). |
| M4b — Consenso promedio + señal textual | ✅ Done | Señal en UI como palabra `comprar\|mantener\|vender` (sin duplicar el score); columnas Graham y DCF por ticker; consenso = promedio `(graham+dcf)/2`; `model_version` 1.1.0. Decisión usuario 2026-09-23. |
| M4c — Refresh de datos + fix mapeo XBRL | ✅ Done | Botones "Recalcular métricas" (`POST /refresh`) y "Pipeline completo" (`POST /force-refresh`) en el dashboard; fix del diccionario XBRL con 6 variantes GAAP nuevas; DCF operativo para NVDA (65.72), QCOM (188.9), ADBE (394.3), CRM (245.5), CSCO (46.9), IBM (162.2). Suite 195/0/0. |

---

## Architecture (high level)

```
┌─────────────────────────────────────────────────────────────────┐
│                     Monorepo Go                                     │
│                                                                    │
│  ┌──────────┐    ┌──────────┐    ┌──────────────────────┐      │
│  │  cmd/api  │    │ cmd/collect │  ┌──────────────────┐ │      │
│  │  HTTP REST│◄──│  worker    │──│  internal/storage  │ │      │
│  │  /health  │    │  (edgar,   │  │  (pgx/v5, SQL)     │ │      │
│  └──────────┘    │  prices,   │  │  - securities        │ │      │
│                   │  macro)    │  │  - fundamentals      │ │      │
│  ┌──────────┐    └──────────┘  │  internal/pipeline   │ │      │
│  │  cmd/    │                  │  (jobs: edgar,       │ │      │
│  │  analytics│  (M2+M3)         │  prices, sector,     │ │      │
│  │  jobs    │                  │  metrics, scores)    │ │      │
│  └──────────┘                  │  internal/storage  │ │      │
│                                  │  - securities        │ │      │
│  ┌──────────┐                  │  - fundamentals      │ │      │
│  │  web/    │  (React 18 + Vite + TS + Tailwind)  │  - edgar_staging     │ │      │
│  └──────────┘                  │  - daily_prices        │ │      │
│                                  │  - macro_series        │ │      │
│                                  │  - derived_metrics     │ │      │
│                                  │  - scores (M3)         │ │      │
│                                  │  - migrations/         │ │      │
│                                  │  - xbrl_concept_map    │ │      │
│                                  │  - hypertables (TSDB)  │ │      │
│  └──────────┘                   └──────────────────────┘ │      │
│                                                                    │
│  internal/collect/        # adaptadores por proveedor (ADR-0003)   │
│    edgar/                 # SEC EDGAR client + XBRL parser         │
│    yahoo/                 # Yahoo Finance v8 chart + quoteSummary  │
│    macro/                 # BLS Public API v2 adapter (CPI)        │
│  internal/metrics/        # Motor de métricas derivadas (ADR-0004)  │
│    formulas.go            # EPS, P/E, P/B, P/FCF, PEG, ROE, D/E   │
│    engine.go              # CalculateMetrics, BuildDerivedMetrics  │
│  internal/valuation/      # Graham (2×g+8.5)×EPS + DCF simplificado│
│    valuation.go           # CalcIntrinsicValue, Graham, DCF        │
│    graham.go              # Graham intrinsic value                 │
│    dcf.go                 # Simplified DCF (WACC, horizon, term)   │
│  internal/score/          # Score 0-100 con señal buy/hold/sell    │
│    score.go               # CalculateScore, pesos 35/30/20/15      │
│    dimensions.go          # valuation/fundamentals/comparables/trend│
│    justification.go       # Plantillas de justificación textual     │
│  internal/compare/        # Comparables sectoriales + riesgo       │
│    compare.go             # ComputeComparables, CompareAssets      │
│    normalize.go           # Base-100, vol anualizada, maxDD, Sharpe│
│  internal/backtest/       # Backtest SMA 50/200 sin lookahead      │
│    backtest.go            # RunSMABacktest, golden-rule crossover  │
│  internal/api/            # HTTP REST M3: 11 endpoints + errores   │
│    router.go              # ServeMux: /securities, /prices, etc.   │
│    handlers.go            # Handlers por ruta                      │
│    loader.go              # Parámetros val/score desde env         │
│    errors.go              # JSON error envelope {error:{code,msg}} │
│    middleware.go          # Logging, panic recovery, /health        │
│  cmd/analytics/           # CLI wrapper (jobs)                      │
│  cmd/collector/           # CLI wrapper (ingesta)                   │
│  internal/pipeline/       # Jobs de pipeline                       │
└─────────────────────────────────────────────────────────────────┘

Flujo de datos:
  SEC EDGAR → edgar/adapter → edgar_staging → fundamentals → pipeline (jobs) → analytics
  Yahoo v8  → yahoo/adapter → daily_prices (hypertable si TSDB)
  BLS CPI   → macro/adapter → macro_series  (hypertable si TSDB)
  daily_prices + fundamentals → metrics/engine → derived_metrics
  derived_metrics + fundamentals + prices → valuation → intrinsic value
  derived_metrics + sector peers → score/engine → score 0-100 + señal
  daily_prices → backtest/sma → backtest results (SMA 50/200)
  daily_prices → compare → base-100, vol, maxDD, Sharpe
  daily_prices → yahoo/quoteSummary → sector/industry enrichment
```

**Módulos implementados (M1+M2+M3+M4):**

| Módulo | Ruta | Propósito |
|--------|------|-----------|
| `cmd/api` | `cmd/api/main.go` | Servidor HTTP con 11 endpoints REST + `GET /health` ampliado (M5.2: `status`, `database`, `version`, `latency_ms`, `postgres_version`, `db_size`, `tables`) |
| `cmd/collector` | `cmd/collector/main.go` | CLI wrapper: worker de ingesta (SEC EDGAR, Yahoo prices, BLS macro, **sector/industry enrichment**) (`-job edgar|prices|macro|sector|all`) |
| `cmd/analytics` | `cmd/analytics/main.go` | CLI wrapper: cálculo batch de métricas derivadas (`-tickers`, `-g`, `-dry-run`) y **job scores** (`-job scores`) |
| `internal/pipeline` | `internal/pipeline/` | Jobs de pipeline: edgar, prices, sector, metrics, scores (wrappers CLI sobre `cmd/collector`/`cmd/analytics`); contratos de invocación intactos |
| `internal/collect/edgar` | `internal/collect/edgar/` | Cliente SEC EDGAR, parser XBRL companyfacts, mapeo canónico |
| `internal/collect/yahoo` | `internal/collect/yahoo/` | Adaptador Yahoo Finance v8 chart + **quoteSummary** (sector/industry) |
| `internal/collect/macro` | `internal/collect/macro/` | Adaptador BLS Public API v2 (serie CPI-U CUSR0000SA0) |
| `internal/metrics` | `internal/metrics/` | Motor de métricas: 8 fórmulas Anexo §13 (EPS, P/E, P/B, P/FCF, PEG, ROE, D/E, FCF Yield) |
| `internal/valuation` | `internal/valuation/` | **Valoración intrínseca**: Graham `(2×g)+8.5` × EPS último FY; DCF simplificado WACC 10%, 5 años, g_terminal 2.5% |
| `internal/score` | `internal/score/` | **Score 0-100**: 4 dimensiones ponderadas (35/30/20/15), señal comprar/mantener/vender, justificación por plantillas |
| `internal/compare` | `internal/compare/` | **Comparables sectoriales** (peer set, medianas) + **comparativa de activos** (base 100, vol anualizada, maxDD, Sharpe) |
| `internal/backtest` | `internal/backtest/` | **Backtest SMA 50/200** sin lookahead, golden-rule crossover |
| `internal/api` | `internal/api/` | **HTTP REST M3**: 11 endpoints + error envelope estructurado |
| `internal/storage` | `internal/storage/` | Capa de persistencia (pgx/v5): queries, pool, modelos, migraciones |
| `migrations/` | `migrations/*.sql` | 9 migraciones SQL: extensiones, schema, staging, concept map, daily_prices, macro_series, derived_metrics, **scores (009)** |
| `docker-compose.yml` | — | PostgreSQL 16 + TimescaleDB para desarrollo local |
| `web/` | `web/src/`, `web/dist/` | Dashboard React 18 + TS + Vite + Tailwind + react-router; consumen la API M3; build estático servido por Go FileServer (SPA fallback) |
| `Makefile` | — | Build, test, migrate, run targets (precios, macro, analytics, scores, sector, API, **build-web**, **build-all**, **deploy-local**) |

**Módulos implementados en M4:** `web/` (React 18 + TS + Vite + Tailwind + react-router), `cmd/api` sirve estáticos de `web/dist` (FileServer + SPA fallback).

**Diferido a M6:** `cmd/alerts/`, `internal/alerts/` — CA M4-2 diferida a M6 por decisión del usuario 2026-09-23; el prefijo `alerts` está reservado en `apiRouteSegments` de `internal/api/static.go`.

### Metodología del score y calibración (2026-09-23)

**1. Naturaleza del modelo**

El score es un modelo de valor (no momentum). La dimensión de valoración es binaria (0 o 100): se calcula el valor intrínseco mediante la fórmula de Graham `(2×g)+8.5` × EPS del último FY y un DCF simplificado (WACC 10%, horizonte 5 años, g_terminal 2.5%), y se compara con el precio de mercado aplicando un margen de seguridad del 30%. Si el upside es ≥ margen, la dimensión valoración vale 100; en caso contrario, 0. Los parámetros por defecto son conservadores por diseño: `g=7` (`GROWTH_RATE_DEFAULT`), WACC=10, g_terminal=2.5. Todos los parámetros son ajustables mediante variables de entorno documentadas en la sección [Configuration](#configuration).

**2. Contraste con analistas (AAPL, Sept 2026)**

A fecha de septiembre de 2026, AAPL cotiza a $339.75. El consenso de analistas (S&P Global Market Intelligence / WSJ, 44 analistas) apunta a un objetivo medio de $322-327 (por debajo del precio actual), con mediana de $335 y rango de $215-250 (low) a $400 (high). Los analistas utilizan EPS forward ~$8.80 y un descuento implícito del ~6-7%. En contraste, el modelo de Abys-Invest usa EPS del último FY (no forward) y parámetros conservadores, lo que genera intrínsecos de $106-246 en todas las permutaciones razonables (g=7-12%, WACC=8.5-10%, g_term=2.5-3%), con un upside de −47% a −64%. El modelo marca "vender" porque el precio está muy por encima del valor intrínseco basado en fundamentales FY; esto refleja la postura conservadora por diseño del modelo, no una opinión de momentum de mercado.

**3. Resultados con pares sectoriales reales (Technology)**

Scores corregidos post-calibración con 10 pares sectoriales reales:

| Ticker | Score | Señal |
|--------|-------|-------|
| ADBE | 74 | COMPRAR |
| INTC | 50 | MANTENER |
| CRM | 40 | MANTENER |
| AAPL | 28 | VENDER |
| ORCL | 28 | VENDER |
| MSFT | 37 | VENDER |
| QCOM | 26 | VENDER |
| AMD | 20 | VENDER (parcial) |

**4. Nota de entrega (M4b, 2026-09-23) — consenso promedio + señal textual**

Cambio solicitado por el usuario sobre el dashboard y las páginas individuales de cada ticker:

- **Señal textual en la UI**: el badge de señal muestra la palabra `comprar|mantener|vender` (ya no se repite el número del score dentro del badge; el score numérico se conserva en su columna/p. ej. "74/100").
- **Valor intrínseco por ambas formas + consenso promedio**: el dashboard y la página de ticker muestran **Graham** y **DCF** por separado; el **consenso** ya no es el menor de ambos (conservador) sino el **promedio** `(graham+dcf)/2` (decisión del usuario 2026-09-23). La dimensión valoración del score puntúa el precio contra ese promedio (antes: mezcla 60/40 de sub-scores).
- **`model_version` 1.0.0 → 1.1.0** en `internal/valuation` e `internal/score` (refleja el cambio de fórmula; el golden AAPL recalcula 36 → 36 sin cambio de señal en ese caso).
- **Impacto práctico**: 4 de 11 tickers cambiaron de score por el nuevo promedio (p. ej. IBM 37 → 31); señales: ADBE comprar · INTC/CRM mantener · resto vender.
- **Suite M4b**: 178/0/0 (igual que M4; mismo conteo, valores recalculados) — evidencia `test-results/tests/abys-m4b-consenso-promedio.json` y `test-results/security/abys-m4b-consenso-promedio.json`.

**5. Nota de entrega (M4c, 2026-09-23) — refresh de datos desde el dashboard + fix mapeo XBRL**

**Fix del diccionario XBRL** (`internal/collect/edgar/concepts.go`): el mapeo solo cubría `PaymentsToAcquirePropertyPlantAndEquipment` (capex) y `ShortTermBorrowings`/`ShortTermDebt`/`CommercialPaper` (short_term_debt). NVDA/QCOM reportan `PaymentsToAcquireProductiveAssets` (QCOM además `PaymentsToAcquireOtherProductiveAssets`); ADBE/CRM/ORCL/QCOM reportan `DebtCurrent` (CRM `LongTermDebtCurrent`); ORCL reporta cash como `CashAndCashEquivalentsAtCarryingValue` y deuda LP como `LongTermNotesAndLoans`. Sin esos mapeos, FCF derivado o NetDebt quedaban nil → DCF desaparecía.

| Concepto XBRL | Campo canonical | Tickers afectados |
|----------------|-----------------|-------------------|
| `PaymentsToAcquireProductiveAssets` | capex | NVDA, QCOM |
| `PaymentsToAcquireOtherProductiveAssets` | capex | QCOM |
| `DebtCurrent` | short_term_debt | ADBE, CRM, ORCL, QCOM |
| `LongTermDebtCurrent` | short_term_debt | CRM |
| `CashAndCashEquivalentsAtCarryingValue` | cash | ORCL |
| `LongTermNotesAndLoans` | long_term_debt | ORCL |

**Impacto en DCF**: NVDA dcf 0.61→65.72; QCOM, ADBE, CRM, CSCO, IBM ya tienen DCF (188.9, 394.3, 245.5, 46.9, 162.2). ORCL/INTC siguen con DCF nulo LEGÍTIMO (FCF FY2026 negativo real del SEC).

**Endpoints de refresh** (nuevos, `internal/api`):
- `POST /refresh` — recalcula `derived_metrics` + `scores` de las securities activas con precio actual, sin red (~0.26s). Respuesta: `{"ok":true,"tickers":N,"duration_ms":D}`.
- `POST /force-refresh` — pipeline completo: edgar con re-ingesta fresca → prices → sector → metrics → scores (~59s para 11 tickers). Respuesta: `{"ok":true,"tickers":N,"duration_ms":D,"steps":[...]}`.
- Ambos son **loopback-only** (403 desde no-loopback) con **anti-concurrencia** (409 si ya corre uno).

**Dashboard**: 2 botones nuevos — "Recalcular métricas" (llama `POST /refresh`) y "Pipeline completo" (llama `POST /force-refresh`).

**Scores**: sin cambios de señal (`model_version` 1.1.0; AAPL 28 vender, ADBE 74 comprar, INTC 50 mantener, CRM 40 mantener, resto vender).

**Suite**: 195/0/0 (178 baseline M4b + 17 nuevos). Build web determinista.

**6. Nota de entrega (hotfix M3, 2026-09-23)**

- **Fix 1 — Aislamiento de métricas por company**: `GetLatestMetrics` incorpora filtro `security_id` en el WHERE clause, evitando fuga de datos entre empresas (cross-company metric leakage).
- **Fix 2 — Guardado de métricas NULL**: `scoreFundamentals` ahora verifica `ok && v != nil` en 6 métricas (pe_ratio, pb_ratio, fcf_yield, roe, de_ratio, peg_ratio), evitando SIGSEGV/DoS ante métricas NULL y degradando la dimensión a neutral (score 50).
- **Suite final**: 154/154 tests verdes (15 paquetes, ejecución serial `-p 1`).
- **Evidencia**: `test-results/tests/abys-m3-hotfix-getlatestmetrics.json` y `test-results/security/abys-m3-hotfix-getlatestmetrics.json`.

**7. Nota de entrega (M5, 2026-09-25) — buscador + watchlist**

M5 implementa el buscador de securities y la watchlist de usuario:

- **Endpoints nuevos**: `GET /securities/search?q=&limit=` (busca por prefijo de ticker o ILIKE en nombre, ranking exacto→prefijo→nombre), `GET /watchlist` (lista con detalle; **negociación de contenido**: `Accept: text/html` → SPA del dashboard, `application/json` → JSON con `Vary: Accept`), `PUT /watchlist/{ticker}` (añade, idempotente 200 `{"ok":true}`, 404 si ticker no catalogado), `DELETE /watchlist/{ticker}` (elimina, idempotente, 404/403 iguales).
- **Loopback-only**: `PUT`/`DELETE /watchlist/{ticker}` solo aceptan conexiones localhost (403 fuera), como `/refresh`.
- **Delistados permitidos**: la watchlist puede contener securities deslistadas (no se filtran por estatus).
- **Contrato**: `WatchlistItem.id` = `securities.id` (mismo id que `/securities/search`).
- **Suite**: 248/0/1. Build web OK (`npm run build`, hash `index-PmMvGtRy.js`).
- **Mantenimiento**: la suite de integración (`make integration`, `-tags=integration`) se ejecuta contra **`abys_test`** (`TEST_DATABASE_URL` con sufijo `_test`, validado por el guard `EnsureTestDatabase` en el Makefile → aborta si apunta a la BD de producción). Los tests **no truncan la BD de despliegue** (securities/watchlist del usuario quedan intactos). El `storage` de la suite trunca tablas solo dentro de `abys_test` entre ejecuciones.

**8. Nota de entrega (M5.1, 2026-09-26) — watchlist integración**

M5.1 completa la experiencia de watchlist con integración asíncrona y dashboard mejorado:

- **`POST /force-refresh` ahora es asíncrono**: responde **202 Accepted** con el estado inicial del job; el pipeline corre en 2º plano usando `context.Background()` con timeout hardcoded de **30 minutos** (NO el contexto del request HTTP, que ya no bloquea). El SPA hace polling de `GET /pipeline/status` cada 1.5 s. Navegar o recargar la página YA NO aborta el pipeline — se corrige el bug "failed to fetch" que ocurría al abandonar la petición síncrona.
- **`GET /pipeline/status`** (nuevo): devuelve el estado del job en curso (o el último terminal). 8 claves JSON: `status` (`idle`|`running`|`done`|`error`), `kind` (`watchlist`|`force`), `tickers`, `steps` (`{edgar, prices, sector, metrics, scores}`), `started_at`, `finished_at`, `error`, `pending` (cola FIFO). JSON puro — **no es ruta del SPA**; con `Accept: text/html` responde JSON igualmente (sin servir `index.html`). Siempre 200 (incluso en `idle`). Consultable sin restricción (igual que `/watchlist`).
- **`PUT /watchlist/{ticker}`** ahora dispara la ingesta del ticker en 2º plano (prices → sector → metrics → scores, **SIN EDGAR**) justo después del alta; responde `200 {"ok":true}` al instante. Si hay un job corriendo, el ticker entra en **cola FIFO** con dedup por ticker (no se duplica). Loopback-only (403 fuera) e idempotente (sin cambios → 200).
- **`GET /watchlist` ampliado**: cada item incluye `score` (number|null) y `signal` (string|null) del último score si existe, mediante `LEFT JOIN LATERAL`. Mantiene `id=securities.id`, `created_at`, negociación de contenido (`text/html` → `index.html`) y `Vary: Accept`.
- **Dashboard — sección "Mi watchlist"**: tarjetas con ticker/nombre/score/signal, enlace a `/ticker/{ticker}`, CTA a `/watchlist` cuando está vacía, y texto "procesando…" mientras el job corre. El botón "Pipeline completo" ya no espera en bloque: dispara 202, hace polling, y recarga la vista al terminar. "Recalcular métricas" (`POST /refresh`) sigue síncrono.
- **WatchlistPage**: al añadir un valor muestra indicador de procesado con polling; la lista muestra chips de score/signal por ticker.
- **Mutadores protegidos**: `POST /force-refresh`, `PUT`/`DELETE /watchlist/*` siguen loopback-only; `GET /watchlist` y `GET /pipeline/status` abiertos. Pool nil → 503.
- **Suite**: **269/0/1** (248 baseline M5 + 21 nuevos). Build web determinista, bundle `index-BDERmpRv.js`.
- **Infra (mismos commits M5.1, F4 del reviewer)**: `make integration` / guard `EnsureTestDatabase` ahora corren contra **`abys_test`** (`TEST_DATABASE_URL`, sufijo `_test`) — los tests **ya NO truncan la BD de despliegue**. Puerto por defecto de BD en `.env.example`/Makefile/deploy/setup.sh: **55432** (reportar deuda en `docker-compose.yml` que sigue en 5432).

---

## Requirements

- **Go 1.25+** (module `github.com/miky/abys-invest`)
- **PostgreSQL 16+** (vía `docker-compose` o instancia local/remota en puerto 55432)
- **TimescaleDB** (opcional — las migraciones 006-007 crean tablas normales con fallback si la extensión no está cargada)
- **Docker** (opcional — usado por `make docker-up` para DB local)
- `SEC_EDGAR_USER_AGENT` — requerido por SEC EDGAR fair-access policy; necesario en el entorno del servicio para `force-refresh` (M4c)
- `DATABASE_URL` — PostgreSQL connection string
- `BLS_API_KEY` — opcional; BLS permite 25 queries/día sin key (suficiente para CPI batch)
- `GROWTH_RATE_DEFAULT` — tasa `g` por defecto para PEG, Graham y DCF (default: `7`)
- `MARGIN_OF_SAFETY` — margen de seguridad para score de valoración (default: `30`)
- `DCF_DISCOUNT_RATE` — tasa WACC para DCF (default: `10`)
- `DCF_HORIZON_YEARS` — años de proyección para DCF (default: `5`)
- `DCF_TERMINAL_GROWTH` — tasa de crecimiento terminal para DCF (default: `2.5`)
- `COMPARABLES_MIN_SECURITIES` — mínimo de pares de sector para comparables (default: `5`)
- `COMPARABLES_HISTORY_YEARS` — años de histórico para medianas propias en comparables (default: `5`)
- `API_PORT` — puerto para el servidor HTTP API (default: `8080`)

> **Security:** Secrets are read exclusively from environment variables. `.env` is gitignored. No credentials are hardcoded in source code.

> **Alertas:** El sistema de alertas se implementará íntegramente en **M4** (decisión del usuario 2026-09-22, documentado en CA §10.1). No existen alertas en M3.

---

## Quickstart

### 1. Clone and set up environment

```bash
cp .env.example .env
# Edit .env con sus credenciales DB
```

### 2. Start PostgreSQL (via Docker o instancia local)

```bash
make docker-up
```

O use una instancia PostgreSQL local en puerto 55432 (el entorno de test usa esta).

### 3. Apply migrations

```bash
make migrate
```

Esto ejecuta las 9 migraciones SQL en `migrations/` contra la DB configurada por `DATABASE_URL`. Las migraciones 006-007 crean hypertables TimescaleDB si la extensión está disponible; si no, crean tablas normales (sin compresión) — funcionalidad completa sin degradación de datos.

### 4. Run collector (ingest SEC EDGAR fundamentals)

```bash
make run-collector
# O con empresas específicas:
SEC_EDGAR_USER_AGENT="YourName/1.0 (your@email.com)" go run ./cmd/collector -job edgar -companies AAPL,MSFT
```

El collector:
1. Fetches el catálogo de empresas SEC EDGAR → persiste en `securities`
2. Descarga AAPL companyfacts (XBRL 10-K) → normaliza → persiste en `fundamentals`
3. Idempotente: re-ejecutar no duplica filas

### 5. Ingest Yahoo prices

```bash
make run-prices
# O con tickers específicos:
make run-prices TICKERS=AAPL,MSFT
```

El job `prices`:
1. Para cada ticker: obtiene histórico 5y diario (Yahoo v8 chart) → upserta en `daily_prices`
2. También ingesta el precio actual (quote) para ese ticker
3. Individual ticker errors no abortan el job

### 6. Ingest macro series (CPI)

```bash
make run-macro
# O con series específicas:
make run-macro MACRO_SERIES=CPI
```

El job `macro`:
1. Ingesta la serie BLS CPI-U (`CUSR0000SA0`) para los últimos N años (default 5)
2. Persiste en `macro_series` con metadatos de unidad, frecuencia y fuente
3. `BLS_API_KEY` se lee del entorno (opcional)

### 7. Calculate derived metrics

```bash
make run-analytics
# O con parámetros:
make run-analytics TICKERS=AAPL G=8
```

El binario `analytics`:
1. Lee fundamentales FY más recientes de `fundamentals` + precio más reciente de `daily_prices`
2. Calcula las 8 métricas del Anexo §13 (EPS, P/E, P/B, P/FCF, PEG, ROE, D/E, FCF Yield)
3. Persiste en `derived_metrics` (idempotente por UNIQUE constraint)
4. `GROWTH_RATE_DEFAULT` (o flag `-g`) controla la tasa `g` para PEG
5. `-dry-run` imprime sin persistir

### 8. Enrich sector/industry

```bash
make run-sector
# O con tickers específicos:
make run-sector TICKERS=AAPL,MSFT
```

El job `sector`:
1. Para cada ticker: consulta Yahoo Finance quoteSummary para sector/industry
2. Fallback a Finviz si Yahoo no devuelve sector
3. Persiste en `securities(sector, industry)` — idempotente

### 9. Calculate valuation & score (M3)

```bash
make run-scores
# O con parámetros:
make run-scores TICKERS=AAPL,MSFT MARGIN_OF_SAFETY=30 DCF_DISCOUNT_RATE=10
```

El job `scores`:
1. Para cada ticker: calcula valoración intrínseca (Graham + DCF) y score 0-100
2. Graham: `(2×g)+8.5` × EPS último FY
3. DCF simplificado: WACC configurable (`DCF_DISCOUNT_RATE`, default 10%), horizonte 5a (`DCF_HORIZON_YEARS`), g_terminal 2.5% (`DCF_TERMINAL_GROWTH`)
4. Score 0-100 con 4 dimensiones: valuation (35%), fundamentals (30%), comparables (20%), trend (15%)
5. Señal: ≥70 comprar / 40-69 mantener / <40 vender
6. Persiste en `scores` (idempotente por security_id + as_of + model_version)
7. Env vars: `MARGIN_OF_SAFETY` (30), `DCF_DISCOUNT_RATE` (10), `DCF_HORIZON_YEARS` (5), `DCF_TERMINAL_GROWTH` (2.5), `COMPARABLES_MIN_SECURITIES` (5), `COMPARABLES_HISTORY_YEARS` (5)
8. `-dry-run` imprime sin persistir

### 10. Run the API

```bash
make run-api
```

Then check health:

```bash
curl http://localhost:8080/health
# {"status":"ok","database":"connected","version":"0.1.0"}
```

**Endpoints API (M3):**

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/health` | Estado de la BD, versión y métricas ampliadas (M5.2). **200 con BD**: `{status:"ok", database:"connected", version, latency_ms (ms del Ping), postgres_version (e.g. "18.6"), db_size (e.g. "21 MB"), tables:[{name,rows}...]}` con 9 tablas. **503 sin BD**: SOLO `{status:"degraded", database:"disconnected", version}` (3 claves; los campos ampliados se omiten). El `status` HTTP depende solo del Ping; si metadata o conteos fallan → 200 con los campos disponibles + `slog.Warn`. Timeout de contexto 3s. |
| GET | `/securities?limit=&offset=` | Catálogo de empresas |
| GET | `/securities/{ticker}` | Detalle de una empresa |
| GET | `/prices/{ticker}?from=&to=` | Precios diarios (default 5a) |
| GET | `/metrics/{ticker}` | Métricas derivadas más recientes |
| GET | `/valuation/{ticker}` | Valoración intrínseca (Graham + DCF + consensus) |
| GET | `/score/{ticker}?as_of=` | Score 0-100 + señal + justificación |
| GET | `/scores?ticker=&from=&to=&limit=` | Historial de scores |
| GET | `/compare?tickers=&from=&to=&rf=` | Comparativa de activos (base 100, riesgo) |
| GET | `/compare/comparables?ticker=&limit=` | Pares del sector y medianas |
| GET | `/compare/history?ticker=&years=` | Histórico de precios del ticker |
| GET | `/backtest/sma?tickers=&fast=&slow=&initial_capital=` | Backtest SMA 50/200 |

**Endpoints de refresh (M4c + M5.1 — asíncrono):**

| Method | Endpoint | Description | Loopback |
|--------|----------|-------------|----------|
| POST | `/refresh` | Recalcula `derived_metrics` + `scores` de securities activas con precio actual (~0.26s). **Sigue síncrono.** | Solo loopback (403 otherwise) |
| POST | `/force-refresh` | Pipeline completo: edgar → prices → sector → metrics → scores. **Ahora asíncrono**: responde **202 Accepted** con estado inicial; el job corre en 2º plano (`context.Background()` + timeout 30 min, NO el contexto del HTTP request). El estado se consulta con `GET /pipeline/status`. | Solo loopback (403 otherwise) |
| GET | `/pipeline/status` | Estado del job de pipeline en curso (o último terminal). 8 claves JSON: `status` (idle\|running\|done\|error), `kind` (watchlist\|force), `tickers`, `steps` {edgar, prices, sector, metrics, scores}, `started_at`, `finished_at`, `error`, `pending` (cola FIFO). JSON puro — no es ruta del SPA (`Accept: text/html` → JSON igual). Siempre 200. Consultable sin restricción. | No (abierto) |

`POST /force-refresh` y `PUT /watchlist/*` son **mutadores protegidos**: loopback-only (403 fuera) y anti-concurrencia (409 si ya hay un refresh). Con `POST /force-refresh` el anti-concurrencia encola el ticker si el job ya corre (FIFO, dedup). El SPA hace polling de `GET /pipeline/status` cada 1.5 s; navegar/recargar YA NO aborta el pipeline (era el bug "failed to fetch").

Requiere `SEC_EDGAR_USER_AGENT` definido en el entorno del servicio para `force-refresh` (fallback de dev si falta).

**Endpoints de M5/M5.1 (búsqueda, watchlist y pipeline):**

| Method | Endpoint | Description | Loopback |
|--------|----------|-------------|----------|
| GET | `/securities/search?q=<2+ chars>&limit=<1-50, default 10>` | Busca en catálogo completo por prefijo de ticker o nombre (ILIKE); ranking exacto→prefijo→nombre; array de securities | No |
| GET | `/watchlist` | Lista con detalle de la watchlist; cada item incluye `score` (number\|null) y `signal` (string\|null) del último score (LEFT JOIN LATERAL); **content negotiation**: `text/html` → `index.html` del SPA; `application/json` → JSON; lleva `Vary: Accept` | No |
| PUT | `/watchlist/{ticker}` | Añade ticker a la watchlist; responde `200 {"ok":true}` al instante y dispara la ingesta en 2º plano (prices→sector→metrics→scores, SIN EDGAR). Si hay un job corriendo, el ticker entra en **cola FIFO** con dedup por ticker. Idempotente; 404 si el ticker no está catalogado | Solo localhost (403 fuera) |
| DELETE | `/watchlist/{ticker}` | Elimina ticker de la watchlist; idempotente; 404/403 iguales | Solo localhost (403 fuera) |

**Pipeline en segundo plano (M5.1):**

- `POST /force-refresh` ya no bloquea: responde 202 Accepted y el pipeline corre en 2º plano.
- El job usa `context.Background()` (no el del request HTTP) con **timeout hardcoded de 30 minutos**. Si expira, el estado pasa a `error`.
- El estado del job vive **en memoria**: un reinicio del servicio pierde el job en curso y `/pipeline/status` vuelve a `idle` (sin recuperar el job). Los tickers en cola se pierden también.
- El SPA consulta `GET /pipeline/status` cada **1.5 s** para actualizar la UI ("procesando…" → tarjetas con resultados).
- La cola FIFO de ingesta de watchlist (activada por `PUT /watchlist/{ticker}` cuando hay un job corriendo) está **acotada por dedup + catálogo**: un ticker solo se encola una vez y solo si está catalogado. No hay tope numérico explícito más allá de esos filtros.
- `POST /refresh` ("Recalcular métricas") **sigue síncrono** y no cambia.

**Página `/watchlist`**: buscador con debounce de 300ms (mínimo 2 caracteres), resultados clicables que navegan al ticker seleccionado; botón para añadir el security a la watchlist (muestra indicador de procesado con polling); lista con chips de `score`/`signal` por entrada y botón de eliminar. Los datos se persisten en PostgreSQL.

**Ejemplos curl:**

```bash
# Health (200 con BD)
curl http://localhost:8080/health
# {"status":"ok","database":"connected","version":"0.1.0","latency_ms":0.43,"postgres_version":"18.6","db_size":"21 MB","tables":[{"name":"securities","rows":10428},… 9 entradas …]}

# Health (503 sin BD): solo 3 claves
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/health  # 503
curl http://localhost:8080/health
# {"status":"degraded","database":"disconnected","version":"0.1.0"}

# Valoración
curl http://localhost:8080/valuation/AAPL
# {"ticker":"AAPL","price":185.50,"value":{"graham":172.30,"dcf":195.10,"consensus":183.70,"inputs":{...},"model_version":"1.1.0"},"upside_pct":-1.0,...}

# Score
curl http://localhost:8080/score/AAPL
# {"score":72,"signal":"COMPRAR","justification":"APPLE: score 72/100 — COMPRAR. Valoración: ...","dimensions":[{...}],"model_version":"1.1.0"}

# Comparables
curl http://localhost:8080/compare/comparables?ticker=AAPL&limit=5
# {"ticker":"AAPL","sector":"Technology","peers":[{...}],"peer_count":48,"medians":{...},"security_id":1}

# Comparativa de activos
curl 'http://localhost:8080/compare?tickers=AAPL,MSFT&from=2024-01-01'
# {"tickers":["AAPL","MSFT"],"normalized_performance":{...},"risk_metrics":{"AAPL":{"volatility_annual":0.28,"max_drawdown":-0.18,"sharpe":1.2},...}}

# Backtest SMA
curl 'http://localhost:8080/backtest/sma?tickers=AAPL&fast=50&slow=200&initial_capital=10000'
# {"strategy":"sma","params":{"fast":50,"slow":200},"results":{"AAPL":{"ticker":"AAPL","total_return":0.2631,"cagr":0.0891,"sharpe":1.15,"max_drawdown":-0.22,"total_trades":14,...}}}

# Errores (envelope estructurado)
curl http://localhost:8080/score/UNKNOWN
# {"error":{"code":"not_found","message":"sin score para \"UNKNOWN\" (ejecuta analytics -job scores primero)"}}
```

**Formato de errores:** Todos los endpoints responden con `{"error":{"code":"<code>","message":"<msg>"}}` en caso de error. Códigos: `not_found`, `bad_request`, `validation_error`, `internal_error`, `service_unavailable`, `unsupported`.

> **Nota:** `/alerts` está reservado en `internal/api/static.go` (`apiRouteSegments`) para **M6**; diferido a M6 por decisión del usuario 2026-09-23 (CA M4-2). No existe endpoint de alertas en M4.

### 11. Run tests

```bash
make test          # Unit tests: go test ./... -count=1
make integration   # Integration tests with -tags=integration (serial -p 1)
make lint          # go vet ./...
```

Suite M1-M5.2: **276 pass / 0 fail / 1 skip** (271 baseline M1-M5.1 + 5 nuevos de M5.2: 3 unit en `internal/api/health_test.go`, 2 integración en `internal/api/health_integration_test.go` — más el desfase de base 271→276 documentado en la evidencia). La suite de integración corre contra `abys_test` y **no trunca la BD de despliegue**.

### 12. Run the dashboard (M4)

**Build del frontend:**

```bash
make build-web          # npm ci + vite build → web/dist
```

El dashboard se sirve automáticamente cuando el API Go detecta `web/dist/` (default `STATIC_DIR=./web/dist`). Tras `make build`, arranca el API:

```bash
make run-api
# El dashboard está disponible en http://localhost:8080/
# (mismo origin que la API; el frontend usa fetch nativo a /api)
```

**Desarrollo con Vite (HMR):**

```bash
cd web && npm run dev
# Vite en :5173 con proxy /api → http://localhost:8080
```

**Página `/health` (M5.2):** nueva página del SPA con 4 tarjetas (Conexión, Latencia, Versión PostgreSQL, Tamaño de la BD) + tabla de conteos por tabla (9: securities, daily_prices, fundamentals, derived_metrics, scores, watchlist, macro_series, edgar_staging, xbrl_concept_map). Botón "Refrescar" con `Spinner` (sin auto-refresh/polling). Muestra "Última comprobación" con hora en locale `es-BO`. Sin BD: muestra "Sin conexión" + aviso ámbar + "—" en las demás tarjetas (503 degradado, no pantalla de error).

**Sidebar de navegación (M5.2):** nuevo layout compartido (`web/src/components/Layout.tsx`) con `<Outlet/>` para rutas anidadas. Secciones: **"Finanzas"** (Dashboard, Watchlist) y **"Sistema"** (Health). Ruta activa marcada con `NavLink` (indigo, `aria-current="page"`). Fijo en `lg+` (240 px); barra con hamburguesa en `<lg`. Las páginas existentes (Dashboard, Watchlist, TickerDetail) perdieron su wrapper `min-h-screen` duplicado y links de navegación redundantes (el sidebar los aporta).

**Nota:** `/health` con F5 o bookmark en el navegador muestra el **JSON crudo** (200/503), no la página SPA. Es aceptado por diseño: `/health` es el probe de readiness de `deploy/setup.sh` y una respuesta HTML 200 enmascararía una BD caída. La página se alcanza navegando desde el link del sidebar.

### 13. Deploy systemd (M4)

Requisitos: **root**, `systemd`, y `make build-all` previo.

```bash
make build-all                          # Go binaries + web/dist
sudo bash deploy/setup.sh               # instalar + systemd + health check
```

**Pasos:**
1. `make build-all` compila `bin/api`, `bin/collector`, `bin/analytics` y `web/dist`.
2. Editar `/etc/abys-invest/secrets.env` con la `DATABASE_URL` real (el setup.sh genera un placeholder CAMBIAR_* si no existe; **no sobrescribe** si ya hay archivo).
3. `sudo bash deploy/setup.sh` instala: usuario `abys` (nologin), binario en `/opt/abys-invest/bin/api`, frontend en `/opt/abys-invest/web/dist`, unit en `/etc/systemd/system/abys-invest-api.service`.

**Guardias:**
- **Placeholder no sobrescribe:** `secrets.env` existente se conserva intacto.
- **Enable guard:** el servicio NO se habilita/arranca hasta que `DATABASE_URL` no contenga `CAMBIAR`/`CHANGEME`/`PLACEHOLDER`.
- **Rollback:** `bin/api.prev` se preserva antes de instalar el nuevo binario; ante fallo de arranque, `setup.sh` restaura y reinicia.
- **Health check:** `curl -sf http://localhost:8080/health` (JSON `.status=ok`) y `curl -sf http://localhost:8080/ | grep -q '<div id="root">'` (SPA cargado).

**Endpoints de refresh (M4c):** `POST /refresh` y `POST /force-refresh` son mutadores protegidos (solo loopback, anti-concurrencia). El servicio debe tener `SEC_EDGAR_USER_AGENT` definido en `/etc/abys-invest/secrets.env` para que `force-refresh` funcione (fallback de dev si falta).

**Archivos de deploy:** `deploy/abys-invest-api.service` (unit systemd, `Type=simple`, `User=abys`, `EnvironmentFile=/etc/abys-invest/secrets.env`, `Restart=on-failure`, hardening `ProtectSystem=strict`) y `deploy/setup.sh` (script de instalación idempotente, `set -euo pipefail`).

**Deploy con auto-recompilación (variante Air, opcional):**

- `sudo bash deploy/setup.sh --with-air` además copia `cmd/`, `internal/`, `go.mod`/`go.sum` a `/opt/abys-invest/src`, instala `air` en `/usr/local/bin` (vía `GOBIN`) y habilita la unit ALTERNA `abys-invest-api-air.service` (air recompila y reinicia al detectar cambios en .go).
- Default sin `--with-air` = modo estático actual (sin cambios; `abys-invest-api.service` con binario `/opt/abys-invest/bin/api`).
- **Requisitos:** toolchain Go en el servidor; el primer `go build` descarga módulos (egress a `proxy.golang.org`, caché de abys vacía).
- **Hardening:** la unit air usa `ProtectSystem=full` + `ReadWritePaths` en `src/` y `bin/` (necesario para `go build`), sin write a `data/`.
- **Reinicio por rebuild:** `send_interrupt=false` → SIGKILL sin drain en cada cambio (aceptable para dev; el unit estático sigue siendo el recomendado para prod sin cambios frecuentes).

---

## Desarrollo con Air (live-reload)

Air permite el auto-reload del servicio API al detectar cambios en archivos `.go` (debounce 500ms, configurado en `.air.toml` raíz). El frontend continúa con su propio HMR de Vite (`cd web && npm run dev`).

### Instalación de la toolchain Air

```bash
make air-install   # go install github.com/air-verse/air@latest (v1.67.4+)
```

> Air es una herramienta de desarrollo, **no** una dependencia de runtime. No se agrega a `go.mod`.

### Levantar el API con live-reload

```bash
export DATABASE_URL="..."   # mismo contrato que make run-api
export API_PORT=8080
make dev-api               # air lanza cmd/api con auto-rebuild+restart
```

- La configuración está en `.air.toml` (raíz del repo): `build.cmd = "go build -o ./tmp/api ./cmd/api"`, `build.delay = "500ms"`, `build.exclude_dir` = `tmp`, `web`, `.git`, `test-results`, `.ai`, `node_modules`; `build.include_ext = ["go", "tpl", "tmpl", "html"]` (excluye JS/TS del frontend).
- El binario hereda las variables de entorno del shell que lanza `air`.
- **Jobs one-shot** (`collector`, `analytics`) **NO usan air** — continúan con `make run-collector`, `make run-analytics`, etc.

### Deploy con auto-recompilación (variante Air, opcional)

Ver arriba en la sección §13. Resumen:
- `sudo bash deploy/setup.sh --with-air` configura el entorno con sources en `/opt/abys-invest/src`, `air` en `/usr/local/bin`, y la unit `abys-invest-api-air.service`.
- Requiere toolchain Go en el servidor; primer build descarga módulos de `proxy.golang.org`.
- La unit alternativa usa `EnvironmentFile=/etc/abys-invest/secrets.env`, `API_PORT=8082`, `ProtectSystem=full` + `ReadWritePaths` en `src/` y `bin/`.
- El modo estático (`abys-invest-api.service`) sigue siendo el recomendado para producción sin cambios frecuentes.

---

## Configuration

### Environment variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `DATABASE_URL` | Yes (for collector/analytics/API) | `postgres://abys:abys@localhost:55432/abys?sslmode=disable` | PostgreSQL connection string |
| `SEC_EDGAR_USER_AGENT` | Yes (for collector) | `AbysInvest/1.0 (dev)` (fallback only) | User-Agent per SEC EDGAR policy |
| `BLS_API_KEY` | No | (empty) | API key for BLS (25 queries/día sin key) |
| `GROWTH_RATE_DEFAULT` | No | `7` | Tasa `g` por defecto para PEG, Graham y DCF (%) |
| `API_PORT` | No | `8080` | Puerto para el servidor HTTP API |
| `POSTGRES_USER` | No (docker-compose) | `abys` | DB user para docker-compose |
| `POSTGRES_PASSWORD` | No (docker-compose) | `abys` | DB password para docker-compose |
| `POSTGRES_DB` | No (docker-compose) | `abys` | DB name para docker-compose |
| `MARGIN_OF_SAFETY` | No | `30` | Margen de seguridad para score de valoración (%) |
| `DCF_DISCOUNT_RATE` | No | `10` | Tasa WACC para DCF (%) |
| `DCF_HORIZON_YEARS` | No | `5` | Años de proyección para DCF |
| `DCF_TERMINAL_GROWTH` | No | `2.5` | Tasa de crecimiento terminal para DCF (%) |
| `COMPARABLES_MIN_SECURITIES` | No | `5` | Mínimo de pares de sector para comparables |
| `COMPARABLES_HISTORY_YEARS` | No | `5` | Años de histórico para medianas propias en comparables |
| `STATIC_DIR` | No | `./web/dist` | Directorio de estáticos del frontend (servido por Go FileServer + SPA fallback) |
| `VITE_API_BASE` | No | `/` | Base URL de la API para el build del frontend (misma-origin en producción) |

> **Security:** Secrets are read exclusively from environment variables. `.env` is gitignored. No credentials are hardcoded in source code.

> **Nota:** El nombre de la variable de entorno para el horizonte DCF es `DCF_HORIZON_YEARS` (documentado en `internal/api/loader.go` y `cmd/analytics/main.go`).

### Makefile targets

| Target | Description |
|--------|-------------|
| `make build` | Compile `bin/api`, `bin/collector`, `bin/analytics` |
| `make test` | Run all unit tests (`go test ./... -count=1`) |
| `make integration` | Run integration tests (`go test -p 1 ./... -count=1 -tags=integration`) |
| `make lint` | Run `go vet ./...` |
| `make vet` | Same as `lint` |
| `make docker-up` | Start PostgreSQL 16 + TimescaleDB via Docker |
| `make docker-down` | Stop Docker services |
| `make migrate` | Apply SQL migrations using `psql` |
| `make run-api` | Run the API server with env vars |
| `make run-collector` | Run EDGAR collector (default: AAPL) |
| `make run-prices` | Ingest Yahoo daily prices for configured tickers (`-job prices -tickers`) |
| `make run-macro` | Ingest macro series (default CPI) (`-job macro -macro-series`) |
| `make run-analytics` | Calculate derived metrics (`-tickers`, `GROWTH_RATE_DEFAULT`) |
| `make run-sector` | Enrich sector/industry via Yahoo quoteSummary (`-tickers`) |
| `make run-scores` | Calculate score 0-100 + valuation (`-tickers`, env vars M3) |
| `make run-all-data` | Pipeline E2E M3 completo (migrate → edgar → prices → macro → sector → analytics → scores) |
| `make build-web` | Compile frontend (`cd web && npm ci && npm run build` → `web/dist`) |
| `make build-all` | Build Go binaries + frontend (`build` + `build-web`) |
| `make deploy-local` | Build completo + instalación systemd via `deploy/setup.sh` (requiere root) |
| `make clean` | Remove `bin/` directory |
| `make air-install` | Instalar la toolchain Air (`go install github.com/air-verse/air@latest`, v1.67.4+) |
| `make dev-api` | Levantar `cmd/api` con Air (live-reload, auto-rebuild+restart al cambiar .go) |

---

## Database schema (M1+M2)

| Table | Purpose |
|-------|---------|
| `securities` | Catalog of listed companies (ticker, CIK, name, type, sector, industry) |
| `fundamentals` | Normalized XBRL facts (canonical dictionary) |
| `edgar_staging` | Raw SEC EDGAR payloads awaiting normalization |
| `xbrl_concept_map` | XBRL → canonical concept mapping dictionary (20 concepts) |
| `daily_prices` | Daily OHLCV series from Yahoo Finance (source='yahoo'); hypertable si TimescaleDB |
| `macro_series` | Macro observations (series_code, date, value, unit, frequency, source); hypertable si TimescaleDB |
| `derived_metrics` | Materialized metrics (EPS, P/E, P/B, P/FCF, PEG, ROE, D/E, FCF Yield) con inputs_snapshot JSONB |
| `scores` | Score 0-100 + señal + justificación + inputs_snapshot JSONB (M3, migración 009) |

Derived concepts computed at normalization time (M1): `total_debt`, `net_debt`, `ebitda`, `free_cash_flow`.

### Tabla `scores` (migración 009, M3)

```sql
CREATE TABLE scores (
    id              BIGSERIAL PRIMARY KEY,
    security_id     BIGINT NOT NULL REFERENCES securities(id),
    as_of           DATE NOT NULL,
    score           SMALLINT NOT NULL CHECK (score >= 0 AND score <= 100),
    signal          VARCHAR(10) NOT NULL CHECK (signal IN ('comprar', 'mantener', 'vender')),
    justification   TEXT NOT NULL,
    inputs_snapshot JSONB,
    model_version   TEXT NOT NULL DEFAULT '1.0.0',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_scores UNIQUE (security_id, as_of, model_version)
);
```

Idempotente por `(security_id, as_of, model_version)`. Índices en `(security_id, as_of DESC)` y `(signal, as_of DESC)`.

### Hypertables y compresión (ADR-0002)

- **`daily_prices`**: hypertable sobre `date`, segmentada por `security_id`, compresión después de 30 días.
- **`macro_series`**: hypertable sobre `date`, segmentada por `series_code`, compresión después de 90 días.
- **`derived_metrics`**: tabla normal (volumen pequeño: 8 filas por ticker/fecha de cálculo).
- **`scores`**: tabla normal (volumen pequeño: 1 fila por ticker/fecha).

**Degradación sin TimescaleDB:** Las migraciones 006-007 usan `DO $$ ... $$` blocks que verifican la existencia de la extensión `timescaledb`. Si no está cargada, crean tablas normales con índices y constraints idénticos. La conversión a hypertable posterior es posible sin pérdida de datos (`create_hypertable` con `migrate_data => true`). Entornos de prueba sin TimescaleDB usan este fallback documentado.

---

## Roadmap

- **M1 ✔** — Foundation: DB schema, migrations, SEC EDGAR adapter, collector, API health check. 55/55 tests.
- **M2 ✔** — Prices & Metrics: Yahoo v8 chart adapter, BLS CPI macro, engine de métricas §13 (EPS, P/E, P/B, P/FCF, PEG, ROE, D/E, FCF Yield). 82/82 tests.
- **M3 ✔** — Valuation & Score: Graham `(2×g)+8.5` × EPS, DCF simplificado (WACC 10%, 5a, g_term 2.5%), margin of safety, score 0-100 (pesos 35/30/20/15, umbrales ≥70/40-69/<40), comparables sectoriales, backtest SMA 50/200, API REST completa (11 endpoints), migración 009.
- **M4 ✔** — UI & Deploy: Dashboard React 18 + TS + Vite + Tailwind en `web/`, estáticos servidos por API Go (FileServer + SPA fallback, `STATIC_DIR`), deploy systemd (`abys-invest-api.service`). Alertas (CA M4-2) diferidas a M6 por decisión usuario 2026-09-23.
- **M4b ✔** — Consenso promedio y señal textual (2026-09-23): badge de señal muestra la palabra `comprar|mantener|vender`; columnas Graham y DCF en dashboard/ticker; consenso = promedio `(graham+dcf)/2` usado para score y upside; `model_version` 1.1.0. 178/0/0.
- **M4c ✔** — Refresh de datos + fix mapeo XBRL (2026-09-23): botones `Recalcular métricas` y `Pipeline completo` en dashboard; endpoints `POST /refresh` y `POST /force-refresh` (loopback-only, anti-concurrencia); fix del diccionario XBRL con 6 conceptos GAAP nuevos; DCF operativo para NVDA, QCOM, ADBE, CRM, CSCO, IBM. Suite 195/0/0.
- **M4d ✔** — Air live-reload (2026-09-24): `make air-install` + `make dev-api` con auto-rebuild/restart al cambiar .go (`.air.toml` raíz, delay 500ms); frontend Vite HMR aparte; deploy opcional con `--with-air` (unit alterna `abys-invest-api-air.service`, sources en `/opt/abys-invest/src`, air en `/usr/local/bin`). 195/0/0 sin cambios.
- **M5 ✔** — Buscador + Watchlist (2026-09-25): `GET /securities/search?q=<2+ chars>&limit=<1-50>` (ranking exacto→prefijo→nombre), `GET /watchlist` (content negotiation: text/html→SPA, application/json→JSON con `Vary: Accept`), `PUT/DELETE /watchlist/{ticker}` (loopback-only, 403 fuera de localhost, idempotentes, 404 si ticker no catalogado); página `/watchlist` con buscador debounce 300ms (min 2 chars), resultados clicables + añadir, lista con eliminar. Permite guardar delistados. Suite 248/0/1.
- **M5.1 ✔** — Watchlist integración (2026-09-26): `POST /force-refresh` asíncrono (202 + job en 2º plano, timeout 30 min); nuevo `GET /pipeline/status` (8 claves JSON puro); `PUT /watchlist/{ticker}` dispara ingesta en 2º plano (prices→sector→metrics→scores, SIN EDGAR) con cola FIFO dedup; `GET /watchlist` ampliado con `score`/`signal` (LEFT JOIN LATERAL); dashboard con sección "Mi watchlist" (tarjetas + polling); build `index-BDERmpRv.js`. Suite 269/0/1. Infra: suite contra `abys_test` (guard `EnsureTestDatabase`), puerto BD 55432.
- **M5.2 ✔** — Página Health (2026-09-27): `GET /health` ampliado aditivamente con `latency_ms` (ms del Ping), `postgres_version` (e.g. "18.6"), `db_size` (e.g. "21 MB") y `tables` (9 entradas: securities, daily_prices, fundamentals, derived_metrics, scores, watchlist, macro_series, edgar_staging, xbrl_concept_map). 503 sin BD: solo 3 claves (`status`,`database`,`version`). Contexto con timeout 3s; status HTTP depende solo del Ping; best-effort de metadata con `slog.Warn`. Nuevo sidebar/layout con secciones "Finanzas" (Dashboard, Watchlist) y "Sistema" (Health); página `/health` con 4 tarjetas + tabla de conteos + Refrescar con spinner + "Última comprobación" es-BO. Bundle `index-BeqNKrtA.js`. Suite 276/0/1 (165/0/0 unit + integración). F5 en `/health` muestra JSON crudo (aceptado por diseño).
- **M6 🔲** — Alertas: `cmd/alerts/`, `internal/alerts/`, endpoint `/alerts` (prefijo reservado). Diferido a M6 por decisión del usuario (SPEC §7).

---

## Deuda técnica y limitaciones conocidas

| ID | Limitación | Detalle | Estado |
|----|-----------|---------|--------|
| **F1** | `kind=force` en `/pipeline/status` devuelve `tickers:[]` | El job de tipo `force` no registra los tickers en el campo `tickers` de la respuesta de estado; la UI no puede listar los tickers del pipeline force. **Fix futuro**: resolver el universo de tickers antes de `start` del job. | Pendiente |
| **F2** | Mensaje "Pipeline completado (0 tickers)" inexacto | Mientras F1 esté abierto, el dashboard puede mostrar "0 tickers" al finalizar un `force-refresh`, aunque el pipeline sí procesó datos. | Pendiente (depende de F1) |
| **L1** | Timeout 30 min hardcoded | El timeout del job asíncrono está fijado en el código (`30 * time.Minute`); no es configurable por entorno. Un pipeline largo puede expirar sin opción de extensión dinámica. | Pendiente |
| **L2** | Estado en memoria | El estado del job vive solo en memoria. Un **reinicio del servicio pierde el job en curso** (y la cola FIFO): `/pipeline/status` vuelve a `idle` sin recuperar progreso. Sin persistencia → no hay recuperación ante crash. | Pendiente |
| **L3** | Cola FIFO sin tope numérico | La cola de ingesta de watchlist está acotada solo por dedup (un ticker entra una vez) y por el catálogo (solo tickers existentes). No hay límite máximo explícito de entries en cola. | Monitorear |
| **D1** | `docker-compose.yml` expone puerto **5432** | El compose sigue mapeando `"5432:5432"` en `db`. Las referencias de `.env.example`, `Makefile` y `deploy/setup.sh` usan **55432** para la BD real de Abys y 55432 para `abys_test`. El compose puede estar mapeando al puerto del cluster del sistema (5432) o a la BD incorrecta — verificar si aplica al flujo de desarrollo local. | Deuda documentada; fuera de alcance de este task |
| **F3** | `make integration` sin `export TEST_DATABASE_URL` se salta la suite en verde | Confirmado como hallazgo F1 (2026-09-27): el guard de la línea 130 del Makefile evalúa `$(TEST_DATABASE_URL)` con el valor por defecto de la línea 16 (una BD *_test), y la línea 131 propaga al sub-make con `TEST_DATABASE_URL=$$TEST_DATABASE_URL` que el shell expande a vacío (anulando el `?=`). Los 7 TestMain hacen `if dsn == ""; os.Exit(0)` → 0 tests ejecutados, make exit 0. **Falso-visible real**, no hipótesis. Protección de datos intacta (EnsureTestDatabase nunca se relaja). | Confirmado (preexistente, no causado por M5.2) |
| **L4** | Lista de 9 tablas hardcodeada en `internal/api/health.go` | `healthTableNames` es una constante Go con las 9 tablas del esquema público. Si una migración futura añade o renombra una tabla, la query de conteos falla (→ `tables` ausente en 200, sin cambio de status HTTP) o la página muestra conteos incompletos. **Mitigado** por `TestHealthTablesCoincidenConEsquema` (anti-deriva): compara `healthTableNames` con `SELECT table_name FROM information_schema.tables WHERE table_schema='public'` en la suite de integración; si hay deriva, el test falla y obliga a actualizar la lista. | Mitigado (test anti-deriva); sin fix automático |
| **D2** | GET /health con navegador directo muestra JSON crudo | La URL `/health` es ruta API reservada en `internal/api/static.go` (`apiRouteSegments`). Un F5 o bookmark en `/health` muestra el JSON crudo (200 con BD / 503 sin BD), **no** la página SPA del HealthPage. Es **aceptado por diseño** (D12, CA-M5.2-4): `/health` es el probe de readiness de `deploy/setup.sh` (`curl -sf .../health`) y una respuesta HTML 200 enmascararía una BD caída. La página se alcanza por cliente (link del sidebar). Si el usuario quisiera negociación de contenido como `/watchlist`, es un task aparte con su SPEC. | Aceptado por diseño; no se prevé fix |
| **F4** | `buildCountQuery` usa `i > 0` como separador UNION ALL — riesgo de SQL inválido si el primer nombre de tabla falla el regex | `internal/api/health.go` `buildCountQuery` construye la query UNION ALL con `i > 0` como separador entre ramas `count(*)`; si el PRIMER nombre de tabla fallara el filtro regex, el SQL terminaría con `UNION ALL` inicial y sería error de sintaxis. Hoy inalcanzable (las 9 constantes pasan el regex; test fija el SQL exacto); efecto benigno si ocurriera (D3 → 200 con `tables` omitido). Fix de 1 línea: contador `written`. | Latente; fix trivial |
| **L5** | `<main>` anidado y padding duplicado en Layout.tsx y páginas | `web/src/components/Layout.tsx` + páginas: `<main>` anidado (landmark inválido) y padding duplicado (px-4 py-6 en layout y en cada página). Del plan. | Del plan |
| **D3** | `es-BO` con `toLocaleTimeString` emite formato 12h (e.g. "8:45:12 p. m."), no HH:MM:SS | `es-BO` con `toLocaleTimeString` emite formato 12h (e.g. "8:45:12 p. m."), no HH:MM:SS; conforme a SPEC/CA (que exigen es-BO) pero a confirmar por el usuario si prefiere 12h estilo test3 o `hour12:false`. | A confirmar por usuario |
| **R10** | `latency_ms` es latencia del PING a PostgreSQL, no del request HTTP | `latency_ms` es la latencia del PING a PostgreSQL, no del request HTTP. | Nota documentada |
| **Divulgación** | /health expone versión PostgreSQL, tamaño de BD y conteos por tabla en probe público sin auth | /health expone versión PostgreSQL, tamaño de BD y conteos por tabla en un probe público sin auth — aceptado por diseño (así lo acordaron scanner/reviewer), junto a la nota de que todo el API es anónimo ya. | Aceptado por diseño |

---

## License

Personal project. See `.ai/specs/abys-foundation.md` for the full specification.
