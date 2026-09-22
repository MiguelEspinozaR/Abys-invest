# Abys-Invest

**Value investing analysis engine** — SEC EDGAR fundamentals → score 0-100 with buy/hold/sell signal.

> **M1 ✔ (core) completed.** **M2 ✔ (prices & metrics) completed.** M3–M4 pending.

---

## What is Abys-Invest?

Abys-Invest is a personal value investing application. It fetches financial statements of NYSE/NASDAQ-listed companies from the SEC EDGAR public filings (XBRL 10-K/10-Q), computes fundamental metrics (profitability, leverage, liquidity, valuation), queries historical daily prices (Yahoo Finance) and macro series (BLS CPI), and calculates derived valuation metrics (EPS, P/E, P/B, P/FCF, PEG, ROE, D/E, FCF Yield). Future: score 0-100 with buy/hold/sell signal and Graham/DCF intrinsic value.

**Status by milestone:**

| Milestone | Status | Description |
|-----------|--------|-------------|
| M1 — Foundation | ✅ Done | Repo, DB schema + migrations, collector (SEC EDGAR), API health check |
| M2 — Prices & Metrics | ✅ Done | Yahoo Finance v8 chart adapter, BLS CPI macro, engine de métricas (EPS, P/E, P/B, P/FCF, PEG, ROE, D/E, FCF Yield) |
| M3 — Valuation & Score | ⏳ Pending | Graham, DCF simplificado, margin of safety, score 0-100, API completa |
| M4 — UI & Alerts | ⏳ Pending | React dashboard, alert engine, systemd deployment |

---

## Architecture (high level)

```
┌─────────────────────────────────────────────────────────────────┐
│                     Monorepo Go                                    │
│                                                                    │
│  ┌──────────┐    ┌──────────┐    ┌──────────────────────┐      │
│  │  cmd/api  │    │ cmd/collector │  ┌──────────────────┐ │      │
│  │  HTTP REST│◄──│  worker    │──│  internal/storage  │ │      │
│  │  /health  │    │  (edgar,   │  │  (pgx/v5, SQL)     │ │      │
│  └──────────┘    │  prices,   │  │  - securities        │ │      │
│                   │  macro)    │  │  - fundamentals      │ │      │
│  ┌──────────┐    └──────────┘  │  - edgar_staging     │ │      │
│  │  cmd/    │                  │  - daily_prices        │ │      │
│  │  analytics│  (M2)           │  - macro_series        │ │      │
│  │  jobs    │                  │  - derived_metrics     │ │      │
│  └──────────┘                  │  - migrations/         │ │      │
│                                  │  - xbrl_concept_map    │ │      │
│  ┌──────────┐                  │  - hypertables (TSDB)  │ │      │
│  │  web/    │  (future React)  └──────────────────────┘ │      │
│  │  Vite    │                                           │      │
│  └──────────┘                                           │      │
│                                                                    │
│  internal/collect/        # adaptadores por proveedor (ADR-0003)    │
│    edgar/                 # SEC EDGAR client + XBRL parser         │
│    yahoo/                 # Yahoo Finance v8 chart adapter          │
│    macro/                 # BLS Public API v2 adapter (CPI)         │
│  internal/metrics/        # Motor de métricas derivadas (ADR-0004)   │
│    formulas.go            # EPS, P/E, P/B, P/FCF, PEG, ROE, D/E    │
│    engine.go              # CalculateMetrics, BuildDerivedMetrics  │
│  cmd/analytics/           # Binario: cálculo batch de métricas      │
└─────────────────────────────────────────────────────────────────┘

Flujo de datos:
  SEC EDGAR → edgar/adapter → edgar_staging → fundamentals → analytics
  Yahoo v8  → yahoo/adapter → daily_prices (hypertable si TSDB)
  BLS CPI   → macro/adapter → macro_series  (hypertable si TSDB)
  daily_prices + fundamentals → metrics/engine → derived_metrics
```

**Módulos implementados (M1+M2):**

| Módulo | Ruta | Propósito |
|--------|------|-----------|
| `cmd/api` | `cmd/api/main.go` | Servidor HTTP con `GET /health` (DB status) |
| `cmd/collector` | `cmd/collector/main.go` | Worker de ingesta: SEC EDGAR, Yahoo prices, BLS macro (`-job edgar|prices|macro|all`) |
| `cmd/analytics` | `cmd/analytics/main.go` | Binario: cálculo batch de métricas derivadas (`-tickers`, `-g`, `-dry-run`) |
| `internal/collect/edgar` | `internal/collect/edgar/` | Cliente SEC EDGAR, parser XBRL companyfacts, mapeo canónico |
| `internal/collect/yahoo` | `internal/collect/yahoo/` | Adaptador Yahoo Finance v8 chart (histórico + quote) |
| `internal/collect/macro` | `internal/collect/macro/` | Adaptador BLS Public API v2 (serie CPI-U CUSR0000SA0) |
| `internal/metrics` | `internal/metrics/` | Motor de métricas: 8 fórmulas Anexo §13 (EPS, P/E, P/B, P/FCF, PEG, ROE, D/E, FCF Yield) |
| `internal/storage` | `internal/storage/` | Capa de persistencia (pgx/v5): queries, pool, modelos, migraciones |
| `migrations/` | `migrations/*.sql` | 8 migraciones SQL: extensiones, schema, staging, concept map, daily_prices, macro_series, derived_metrics |
| `docker-compose.yml` | — | PostgreSQL 16 + TimescaleDB para desarrollo local |
| `Makefile` | — | Build, test, migrate, run targets (precios, macro, analytics) |

**Módulos futuros (no implementados):** `cmd/alerts/`, `internal/score/`, `internal/valuation/`, `internal/backtest/`, `internal/compare/`, `internal/alerts/`, `web/` (React + Vite dashboard).

---

## Requirements

- **Go 1.25+** (module `github.com/miky/abys-invest`)
- **PostgreSQL 16+** (vía `docker-compose` o instancia local/remota en puerto 55432)
- **TimescaleDB** (opcional — las migraciones 006-007 crean tablas normales con fallback si la extensión no está cargada)
- **Docker** (opcional — usado por `make docker-up` para DB local)
- `SEC_EDGAR_USER_AGENT` — requerido por SEC EDGAR fair-access policy
- `DATABASE_URL` — PostgreSQL connection string
- `BLS_API_KEY` — opcional; BLS permite 25 queries/día sin key (suficiente para CPI batch)
- `GROWTH_RATE_DEFAULT` — tasa `g` por defecto para PEG (default: `7`)

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

Esto ejecuta las 8 migraciones SQL en `migrations/` contra la DB configurada por `DATABASE_URL`. Las migraciones 006-007 crean hypertables TimescaleDB si la extensión está disponible; si no, crean tablas normales (sin compresión) — funcionalidad completa sin degradación de datos.

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

### 8. Run the API

```bash
make run-api
```

Then check health:

```bash
curl http://localhost:8080/health
# {"status":"ok","database":"connected","version":"0.1.0"}
```

### 9. Run tests

```bash
make test          # Unit tests: go test ./... -count=1
make integration   # Integration tests with -tags=integration (serial -p 1)
make lint          # go vet ./...
```

All M1+M2 tests pass: **82/82** (7 paquetes). Evidence in `test-results/tests/abys-m2-prices-metrics.json`.

---

## Configuration

### Environment variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `DATABASE_URL` | Yes (for collector/analytics/API) | `postgres://abys:abys@localhost:5432/abys?sslmode=disable` | PostgreSQL connection string |
| `SEC_EDGAR_USER_AGENT` | Yes (for collector) | `AbysInvest/1.0 (dev)` (fallback only) | User-Agent per SEC EDGAR policy |
| `BLS_API_KEY` | No | (empty) | API key for BLS (25 queries/día sin key) |
| `GROWTH_RATE_DEFAULT` | No | `7` | Tasa `g` por defecto para PEG (%) |
| `API_PORT` | No | `8080` | Puerto para el servidor HTTP API |
| `POSTGRES_USER` | No (docker-compose) | `abys` | DB user para docker-compose |
| `POSTGRES_PASSWORD` | No (docker-compose) | `abys` | DB password para docker-compose |
| `POSTGRES_DB` | No (docker-compose) | `abys` | DB name para docker-compose |

> **Security:** Secrets are read exclusively from environment variables. `.env` is gitignored. No credentials are hardcoded in source code.

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
| `make clean` | Remove `bin/` directory |

---

## Database schema (M1+M2)

| Table | Purpose |
|-------|---------|
| `securities` | Catalog of listed companies (ticker, CIK, name, type, sector) |
| `fundamentals` | Normalized XBRL facts (canonical dictionary) |
| `edgar_staging` | Raw SEC EDGAR payloads awaiting normalization |
| `xbrl_concept_map` | XBRL → canonical concept mapping dictionary (20 concepts) |
| `daily_prices` | Daily OHLCV series from Yahoo Finance (source='yahoo'); hypertable si TimescaleDB |
| `macro_series` | Macro observations (series_code, date, value, unit, frequency, source); hypertable si TimescaleDB |
| `derived_metrics` | Materialized metrics (EPS, P/E, P/B, P/FCF, PEG, ROE, D/E, FCF Yield) con inputs_snapshot JSONB |

Derived concepts computed at normalization time (M1): `total_debt`, `net_debt`, `ebitda`, `free_cash_flow`.

### Hypertables y compresión (ADR-0002)

- **`daily_prices`**: hypertable sobre `date`, segmentada por `security_id`, compresión después de 30 días.
- **`macro_series`**: hypertable sobre `date`, segmentada por `series_code`, compresión después de 90 días.
- **`derived_metrics`**: tabla normal (volumen pequeño: 8 filas por ticker/fecha de cálculo).

**Degradación sin TimescaleDB:** Las migraciones 006-007 usan `DO $$ ... $$` blocks que verifican la existencia de la extensión `timescaledb`. Si no está cargada, crean tablas normales con índices y constraints idénticos. La conversión a hypertable posterior es posible sin pérdida de datos (`create_hypertable` con `migrate_data => true`). Entornos de prueba sin TimescaleDB usan este fallback documentado.

---

## Roadmap

- **M1 ✔** — Foundation: DB schema, migrations, SEC EDGAR adapter, collector, API health check. 55/55 tests.
- **M2 ✔** — Prices & Metrics: Yahoo v8 chart adapter, BLS CPI macro, engine de métricas §13 (EPS, P/E, P/B, P/FCF, PEG, ROE, D/E, FCF Yield). 82/82 tests.
- **M3** — Valuation & Score: Graham intrinsic value, DCF simplificado, margin of safety, comparables por sector, score 0-100 con señal, API REST completa.
- **M4** — UI & Alerts: React dashboard, alert engine, systemd deployment.

---

## License

Personal project. See `.ai/specs/abys-foundation.md` for the full specification.
