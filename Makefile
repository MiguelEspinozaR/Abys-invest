# Abys-Invest M1+M2+M3+M4 — Makefile (fundación + precios + métricas + score/API
# + dashboard web + deploy systemd). Los secretos (DATABASE_URL,
# SEC_EDGAR_USER_AGENT, BLS_API_KEY) se leen del entorno, nunca están
# hardcodeados en el repo.

BIN_DIR        := bin
API_BIN        := $(BIN_DIR)/api
COLLECTOR_BIN  := $(BIN_DIR)/collector
ANALYTICS_BIN  := $(BIN_DIR)/analytics

DATABASE_URL   ?= postgres://abys:abys@localhost:5432/abys?sslmode=disable
SEC_EDGAR_UA   ?= AbysInvest/1.0 (contact@abys-invest.dev)
API_PORT       ?= 8080

# Configuración de jobs M2/M3
TICKERS          ?= AAPL
MACRO_SERIES     ?= CPI
G                ?= 7
MARGIN_SAFETY    ?= 30
DCF_DISCOUNT     ?= 10
COMP_MIN_SEC     ?= 5

.PHONY: build build-web build-all deploy-local test lint vet docker-up docker-down migrate air-install dev-api run-api run-collector run-prices run-macro run-sector run-analytics run-scores run-all-data integration clean

## build: compila api, collector y analytics en bin/
build:
	mkdir -p $(BIN_DIR)
	go build -o $(API_BIN) ./cmd/api
	go build -o $(COLLECTOR_BIN) ./cmd/collector
	go build -o $(ANALYTICS_BIN) ./cmd/analytics

## build-web: compila el frontend (npm ci con lockfile + vite build) en web/dist
build-web:
	cd web && npm ci && npm run build

## build-all: binarios Go (build) + frontend (build-web)
build-all: build build-web

## deploy-local: build completo + instalación systemd vía deploy/setup.sh
## (requiere sudo; el script instala y arranca solo si secrets.env tiene la
## DATABASE_URL real — ver guards en deploy/setup.sh)
deploy-local: build build-web
	sudo bash deploy/setup.sh

## test: ejecuta todos los tests (unidad)
test:
	go test ./... -count=1

## lint: go vet sobre todos los paquetes
lint: vet

vet:
	go vet ./...

## docker-up: levanta PostgreSQL 16 + TimescaleDB local
docker-up:
	docker compose up -d

## docker-down: detiene los servicios
docker-down:
	docker compose down

## migrate: aplica las migraciones SQL en orden (usa psql directamente;
## compatible con docker y con una BD local/remota vía DATABASE_URL)
migrate:
	@for f in migrations/*.sql; do \
		echo "==> $$f"; \
		psql "$(DATABASE_URL)" -v ON_ERROR_STOP=1 -q -f "$$f"; \
	done

## air-install: instala la tool de live-reload Air (go install, no es dependencia de runtime)
air-install:
	go install github.com/air-verse/air@latest

## dev-api: levanta el API con live-reload de Air (lee .air.toml de la raíz).
## Requiere DATABASE_URL y API_PORT EXPORTADOS en el entorno (mismo contrato que
## run-api). El frontend va aparte con su propio HMR: cd web && npm run dev.
## AIR resuelve `air` desde PATH y, si no está (p. ej. GOPATH/bin fuera de
## PATH), cae a $(go env GOPATH)/bin/air instalado por `make air-install`.
AIR ?= $(shell command -v air 2>/dev/null || echo $$(go env GOPATH)/bin/air)
dev-api:
	$(AIR)

## run-api: ejecuta el binario api con env de entorno
run-api:
	API_PORT=$(API_PORT) DATABASE_URL=$(DATABASE_URL) go run ./cmd/api

## run-collector: ejecuta el binario collector (empresas por defecto: AAPL)
run-collector:
	DATABASE_URL=$(DATABASE_URL) SEC_EDGAR_USER_AGENT="$(SEC_EDGAR_UA)" go run ./cmd/collector -job edgar -companies AAPL

## run-prices: ingesta de precios Yahoo para tickers configurados
run-prices:
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/collector -job prices -tickers $(TICKERS)

## run-macro: ingesta de series macro (default: CPI)
run-macro:
	DATABASE_URL=$(DATABASE_URL) BLS_API_KEY="$(BLS_API_KEY)" go run ./cmd/collector -job macro -macro-series $(MACRO_SERIES)

## run-analytics: cálculo de métricas derivadas (8 métricas §13)
run-analytics:
	DATABASE_URL=$(DATABASE_URL) GROWTH_RATE_DEFAULT=$(G) go run ./cmd/analytics -tickers $(TICKERS)

## run-sector: enriquece sector/industria de los tickers (Yahoo + fallback Finviz)
run-sector:
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/collector -job sector -tickers $(TICKERS)

## run-scores: calcula y persiste el score 0-100 para los tickers (job scores)
run-scores:
	DATABASE_URL=$(DATABASE_URL) GROWTH_RATE_DEFAULT=$(G) MARGIN_OF_SAFETY=$(MARGIN_SAFETY) \
		DCF_DISCOUNT_RATE=$(DCF_DISCOUNT) COMPARABLES_MIN_SECURITIES=$(COMP_MIN_SEC) \
		go run ./cmd/analytics -job scores -tickers $(TICKERS)

## run-all-data: pipeline E2E M3 (migrate → ingesta → sector → métricas → score)
run-all-data:
	$(MAKE) migrate run-collector run-prices run-macro run-sector run-analytics run-scores

## integration: tests de integración end-to-end (requiere BD levantada)
## Se usan -p 1 (secuencial): los paquetes comparten la misma BD de desarrollo
## y el suite storage trunca las tablas de datos en cada ejecución.
integration:
	go test -p 1 ./... -count=1 -tags=integration

clean:
	rm -rf $(BIN_DIR)