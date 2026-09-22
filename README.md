# Abys-Invest

**Value investing analysis engine** — SEC EDGAR fundamentals → score 0-100 with buy/hold/sell signal.

> **M1 (core) completed.** M2–M4 pending.

---

## What is Abys-Invest?

Abys-Invest is a personal value investing application. It fetches financial statements of NYSE/NASDAQ-listed companies from the SEC EDGAR public filings (XBRL 10-K/10-Q), computes fundamental metrics (profitability, leverage, liquidity, valuation), and generates a **score 0–100** with a **buy / hold / sell** signal and textual justification based on Benjamin Graham's methodology and margin of safety.

**Status by milestone:**

| Milestone | Status | Description |
|-----------|--------|-------------|
| M1 — Foundation | ✅ Done | Repo, DB schema + migrations, collector (SEC EDGAR), API health check |
| M2 — Prices & Metrics | ⏳ Pending | Yahoo Finance adapter, macro data, derived metrics (EPS, P/E, P/B, etc.) |
| M3 — Valuation & Score | ⏳ Pending | Graham, DCF simplified, margin of safety, score 0-100, full REST API |
| M4 — UI & Alerts | ⏳ Pending | React dashboard, alert engine, systemd deployment |

---

## Architecture (high level)

```
┌─────────────────────────────────────────────────────────────┐
│                     Monorepo Go                              │
│                                                             │
│  ┌──────────┐    ┌──────────┐    ┌──────────────────────┐  │
│  │  cmd/api  │    │ cmd/collector │  ┌──────────────────┐ │  │
│  │  HTTP REST│◄──│  M1 worker  │──│  internal/storage  │ │  │
│  │  /health  │    │  SEC EDGAR  │  │  (pgx/v5, SQL)     │ │  │
│  └──────────┘    └──────────┘  │  - securities        │ │  │
│                                │  - fundamentals      │ │  │
│  ┌──────────┐                  │  - edgar_staging     │ │  │
│  │  web/    │  (future React)  │  - xbrl_concept_map  │ │  │
│  │  Vite    │                  │  - migrations/       │ │  │
│  └──────────┘                  └──────────────────────┘  │
│                                                             │
│  internal/collect/edgar/    # SEC EDGAR adapter (ADR-0003)  │
│    client.go, companyfacts.go, concepts.go, normalize.go    │
└─────────────────────────────────────────────────────────────┘

Data flow: SEC EDGAR → adapter → edgar_staging → normalized → fundamentals → API/Web
Future: Yahoo Finance, macro series, analytics, score, alerts, React dashboard
```

**Current modules (M1):**

| Module | Path | Purpose |
|--------|------|---------|
| `cmd/api` | `cmd/api/main.go` | HTTP server with `GET /health` (DB status check) |
| `cmd/collector` | `cmd/collector/main.go` | Ingestion worker: SEC EDGAR catalog + company facts → DB |
| `internal/collect/edgar` | `internal/collect/edgar/` | SEC EDGAR HTTP client, XBRL companyfacts parser, canonical concept mapper |
| `internal/storage` | `internal/storage/` | PostgreSQL persistence layer (pgx/v5): queries, connection pool, migrations |
| `migrations/` | `migrations/*.sql` | 5 versioned SQL migrations: extensions, schema, staging, concept map |
| `docker-compose.yml` | — | PostgreSQL 16 + TimescaleDB for local dev |
| `Makefile` | — | Build, test, migrate, run targets |

**Future modules (not yet implemented):** `cmd/analytics/`, `cmd/alerts/`, `internal/metrics/`, `internal/score/`, `internal/valuation/`, `internal/backtest/`, `internal/compare/`, `internal/alerts/`, `web/` (React + Vite dashboard).

---

## Requirements

- **Go 1.25+** (module `github.com/miky/abys-invest`)
- **PostgreSQL 16+ with TimescaleDB** (via `docker-compose`), OR a local/remote PostgreSQL instance
- **Docker** (optional — used by `make docker-up` for local DB; PG on port 55432 also works as an alternative)
- `SEC_EDGAR_USER_AGENT` — required by SEC EDGAR fair-access policy
- `DATABASE_URL` — PostgreSQL connection string

---

## Quickstart

### 1. Clone and set up environment

```bash
cp .env.example .env
# Edit .env with your local DB credentials
```

### 2. Start PostgreSQL + TimescaleDB (via Docker)

```bash
make docker-up
```

Or use a local PostgreSQL instance on port 55432 (the test environment uses this).

### 3. Apply migrations

```bash
make migrate
```

This runs all SQL migrations in `migrations/` against the DB configured by `DATABASE_URL`.

### 4. Run the collector (ingest SEC EDGAR data)

```bash
make run-collector
# Or with specific companies:
SEC_EDGAR_USER_AGENT="YourName/1.0 (your@email.com)" go run ./cmd/collector -companies AAPL,MSFT
```

The collector:
1. Fetches the SEC EDGAR company tickers catalog → persists in `securities`
2. Downloads AAPL companyfacts (XBRL 10-K) → normalizes to canonical dictionary → persists in `fundamentals`
3. Idempotent: re-running does not duplicate rows

### 5. Run the API

```bash
make run-api
```

Then check health:

```bash
curl http://localhost:8080/health
# {"status":"ok","database":"connected","version":"0.1.0"}
```

### 6. Run tests

```bash
make test          # Unit tests: go test ./... -count=1
make integration   # Integration tests with -tags=integration
make lint          # go vet ./...
```

All M1 tests pass: **55/55** (30 unit + 25 integration). Evidence in `test-results/tests/abys-foundation.json`.

---

## Configuration

### Environment variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `DATABASE_URL` | Yes (for collector/API) | `postgres://abys:abys@localhost:5432/abys?sslmode=disable` | PostgreSQL connection string |
| `SEC_EDGAR_USER_AGENT` | Yes (for collector) | `AbysInvest/1.0 (dev)` (fallback only) | User-Agent per SEC EDGAR policy |
| `API_PORT` | No | `8080` | Port for the HTTP API server |
| `POSTGRES_USER` | No (docker-compose) | `abys` | DB user for docker-compose |
| `POSTGRES_PASSWORD` | No (docker-compose) | `abys` | DB password for docker-compose |
| `POSTGRES_DB` | No (docker-compose) | `abys` | DB name for docker-compose |

> **Security:** Secrets are read exclusively from environment variables. `.env` is gitignored. No credentials are hardcoded in source code. See `test-results/security/abys-foundation.json` for the security scan report.

### Makefile targets

| Target | Description |
|--------|-------------|
| `make build` | Compile `bin/api` and `bin/collector` |
| `make test` | Run all unit tests (`go test ./... -count=1`) |
| `make integration` | Run integration tests (`go test ./... -count=1 -tags=integration`) |
| `make lint` | Run `go vet ./...` |
| `make vet` | Same as `lint` |
| `make docker-up` | Start PostgreSQL 16 + TimescaleDB via Docker |
| `make docker-down` | Stop Docker services |
| `make migrate` | Apply SQL migrations using `psql` |
| `make run-api` | Run the API server with env vars |
| `make run-collector` | Run the collector worker (default: AAPL) |
| `make clean` | Remove `bin/` directory |

---

## Database schema (M1)

| Table | Purpose |
|-------|---------|
| `securities` | Catalog of listed companies (ticker, CIK, name, type, sector) |
| `fundamentals` | Normalized XBRL facts (canonical dictionary: revenues, net_earnings, EPS, etc.) |
| `edgar_staging` | Raw SEC EDGAR payloads awaiting normalization |
| `xbrl_concept_map` | XBRL → canonical concept mapping dictionary (20 concepts) |

Derived concepts computed at normalization time: `total_debt`, `net_debt`, `ebitda`, `free_cash_flow`.

TimescaleDB extension enabled (M1) for future hypertables (`daily_prices`, `macro_series` — M2).

---

## Roadmap

- **M1 ✅** — Foundation: DB schema, migrations, SEC EDGAR adapter, collector, API health check. 55/55 tests passing.
- **M2** — Prices and metrics: Yahoo Finance adapter, macro data, derived metrics (EPS, P/E, P/B, P/FCF, PEG, ROE, D/E, FCF Yield).
- **M3** — Valuation and scoring: Graham intrinsic value, DCF simplified, margin of safety, sector comparables, score 0-100 with signal, full REST API.
- **M4** — UI and alerts: React dashboard, alert engine, systemd deployment.

---

## License

Personal project. See `.ai/specs/abys-foundation.md` for the full specification.
