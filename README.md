# Abys-Invest

**Value investing analysis engine** — SEC EDGAR fundamentals → score 0-100 with buy/hold/sell signal, Graham/DCF intrinsic value, comparables & SMA backtest.

> **M1 ✔ (core) completed.** **M2 ✔ (prices & metrics) completed.** **M3 ✔ (valuation, score, comparables, backtest, API) completed** (hotfix M3 + calibración 2026-09-23). M4 pending.

---

## What is Abys-Invest?

Abys-Invest is a personal value investing application. It fetches financial statements of NYSE/NASDAQ-listed companies from the SEC EDGAR public filings (XBRL 10-K/10-Q), computes fundamental metrics (profitability, leverage, liquidity, valuation), queries historical daily prices (Yahoo Finance) and macro series (BLS CPI), and calculates derived valuation metrics (EPS, P/E, P/B, P/FCF, PEG, ROE, D/E, FCF Yield). Future: score 0-100 with buy/hold/sell signal and Graham/DCF intrinsic value.

**Status by milestone:**

| Milestone | Status | Description |
|-----------|--------|-------------|
| M1 — Foundation | ✅ Done | Repo, DB schema + migrations, collector (SEC EDGAR), API health check |
| M2 — Prices & Metrics | ✅ Done | Yahoo Finance v8 chart adapter, BLS CPI macro, engine de métricas (EPS, P/E, P/B, P/FCF, PEG, ROE, D/E, FCF Yield) |
| M3 — Valuation & Score | ✅ Done | Graham `(2×g)+8.5` × EPS último FY, DCF simplificado (WACC 10%, 5a, g_term 2.5%), margin of safety, score 0-100 (pesos 35/30/20/15, umbrales ≥70/40-69/<40), comparables sectoriales, backtest SMA 50/200, API REST completa (11 endpoints). Calibración 2026-09-23 con pares sectoriales reales; hotfix M3 (aislamiento security_id + guardado de métricas NULL). |
| M4 — UI & Alerts | ⏳ Pending | React dashboard, alert engine, systemd deployment |

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
│  ┌──────────┐    └──────────┘  │  - edgar_staging     │ │      │
│  │  cmd/    │                  │  - daily_prices        │ │      │
│  │  analytics│  (M2+M3)         │  - macro_series        │ │      │
│  │  jobs    │                  │  - derived_metrics     │ │      │
│  └──────────┘                  │  - scores (M3)         │ │      │
│                                  │  - migrations/         │ │      │
│  ┌──────────┐                  │  - xbrl_concept_map    │ │      │
│  │  web/    │  (future React)  │  - hypertables (TSDB)  │ │      │
│  │  Vite    │                   │                     │ │      │
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
│  cmd/analytics/           # Binario: métricas + job scores          │
│  cmd/collector/           # Binario: ingesta + job sector           │
└─────────────────────────────────────────────────────────────────┘

Flujo de datos:
  SEC EDGAR → edgar/adapter → edgar_staging → fundamentals → analytics
  Yahoo v8  → yahoo/adapter → daily_prices (hypertable si TSDB)
  BLS CPI   → macro/adapter → macro_series  (hypertable si TSDB)
  daily_prices + fundamentals → metrics/engine → derived_metrics
  derived_metrics + fundamentals + prices → valuation → intrinsic value
  derived_metrics + sector peers → score/engine → score 0-100 + señal
  daily_prices → backtest/sma → backtest results (SMA 50/200)
  daily_prices → compare → base-100, vol, maxDD, Sharpe
  daily_prices → yahoo/quoteSummary → sector/industry enrichment
```

**Módulos implementados (M1+M2+M3):**

| Módulo | Ruta | Propósito |
|--------|------|-----------|
| `cmd/api` | `cmd/api/main.go` | Servidor HTTP con 11 endpoints REST + `GET /health` (DB status) |
| `cmd/collector` | `cmd/collector/main.go` | Worker de ingesta: SEC EDGAR, Yahoo prices, BLS macro, **sector/industry enrichment** (`-job edgar|prices|macro|sector|all`) |
| `cmd/analytics` | `cmd/analytics/main.go` | Binario: cálculo batch de métricas derivadas (`-tickers`, `-g`, `-dry-run`) y **job scores** (`-job scores`) |
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
| `Makefile` | — | Build, test, migrate, run targets (precios, macro, analytics, scores, sector, API) |

**Módulos futuros (no implementados):** `web/` (React + Vite dashboard), `cmd/alerts/`, `internal/alerts/` (alertas íntegramente en M4).

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

**4. Nota de entrega (hotfix M3, 2026-09-23)**

- **Fix 1 — Aislamiento de métricas por company**: `GetLatestMetrics` incorpora filtro `security_id` en el WHERE clause, evitando fuga de datos entre empresas (cross-company metric leakage).
- **Fix 2 — Guardado de métricas NULL**: `scoreFundamentals` ahora verifica `ok && v != nil` en 6 métricas (pe_ratio, pb_ratio, fcf_yield, roe, de_ratio, peg_ratio), evitando SIGSEGV/DoS ante métricas NULL y degradando la dimensión a neutral (score 50).
- **Suite final**: 154/154 tests verdes (15 paquetes, ejecución serial `-p 1`).
- **Evidencia**: `test-results/tests/abys-m3-hotfix-getlatestmetrics.json` y `test-results/security/abys-m3-hotfix-getlatestmetrics.json`.

---

## Requirements

- **Go 1.25+** (module `github.com/miky/abys-invest`)
- **PostgreSQL 16+** (vía `docker-compose` o instancia local/remota en puerto 55432)
- **TimescaleDB** (opcional — las migraciones 006-007 crean tablas normales con fallback si la extensión no está cargada)
- **Docker** (opcional — usado por `make docker-up` para DB local)
- `SEC_EDGAR_USER_AGENT` — requerido por SEC EDGAR fair-access policy
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
| GET | `/health` | Estado de la BD y versión |
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

**Ejemplos curl:**

```bash
# Health
curl http://localhost:8080/health

# Valoración
curl http://localhost:8080/valuation/AAPL
# {"ticker":"AAPL","price":185.50,"value":{"graham":172.30,"dcf":195.10,"consensus":172.30,"inputs":{...},"model_version":"1.0.0"},"upside_pct":-7.1,...}

# Score
curl http://localhost:8080/score/AAPL
# {"score":72,"signal":"COMPRAR","justification":"APPLE: score 72/100 — COMPRAR. Valoración: ...","dimensions":[{...}],"model_version":"1.0.0"}

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

> **Nota:** `/alerts` se implementará en **M4**. No existe endpoint de alertas en M3.

### 11. Run tests

```bash
make test          # Unit tests: go test ./... -count=1
make integration   # Integration tests with -tags=integration (serial -p 1)
make lint          # go vet ./...
```

Suite M1-M3 (con fixes de aislamiento): **154/154** tests verdes — evidencia en `test-results/tests/abys-m3-hotfix-getlatestmetrics.json` (y `test-results/tests/abys-m2-prices-metrics.json` para M2).

---

## Configuration

### Environment variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `DATABASE_URL` | Yes (for collector/analytics/API) | `postgres://abys:abys@localhost:5432/abys?sslmode=disable` | PostgreSQL connection string |
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
| `make clean` | Remove `bin/` directory |

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
- **M4** — UI & Alerts: React dashboard, alert engine (alertas íntegramente en M4, CA §10.1), systemd deployment.

---

## License

Personal project. See `.ai/specs/abys-foundation.md` for the full specification.
