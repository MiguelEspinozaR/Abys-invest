# Abys-Invest M1+M2+M3+M4 — Makefile (fundación + precios + métricas + score/API
# + dashboard web + deploy systemd). Los secretos (DATABASE_URL,
# SEC_EDGAR_USER_AGENT, BLS_API_KEY) se leen del entorno, nunca están
# hardcodeados en el repo.

BIN_DIR        := bin
API_BIN        := $(BIN_DIR)/api
COLLECTOR_BIN  := $(BIN_DIR)/collector
ANALYTICS_BIN  := $(BIN_DIR)/analytics

# BD real de Abys-Invest = instancia dedicada abys-postgres (55432). El
# cluster del sistema (5432) pertenece a otros proyectos; no tiene el esquema.
DATABASE_URL      ?= postgres://abys:abys@localhost:55432/abys?sslmode=disable
# BD dedicada para la suite de integración (make integration usa esta, nunca
# la de datos; el guard exige un nombre *_test).
TEST_DATABASE_URL ?= postgres://abys:abys@localhost:55432/abys_test?sslmode=disable
SEC_EDGAR_UA   ?= AbysInvest/1.0 (contact@abys-invest.dev)
API_PORT       ?= 8080

# Configuración de jobs M2/M3
TICKERS          ?= AAPL
MACRO_SERIES     ?= CPI
G                ?= 7
MARGIN_SAFETY    ?= 30
DCF_DISCOUNT     ?= 10
COMP_MIN_SEC     ?= 5

.PHONY: build build-web build-all deploy-local test lint vet docker-up docker-down migrate air-install dev-api run-api run-collector reingest-fundamentals run-prices run-macro run-sector run-growth run-valuation run-analytics run-scores run-quality run-relative run-backtest run-all-data integration integration-run clean

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
## OJO: corre EDGAR con fresh=false, y InsertStaging es idempotente por
## (cik, source): una empresa YA ingerida NO vuelve a pasar por el canonizador.
## Para que un cambio del catálogo XBRL se vea, usa reingest-fundamentals.
run-collector:
	DATABASE_URL=$(DATABASE_URL) SEC_EDGAR_USER_AGENT="$(SEC_EDGAR_UA)" go run ./cmd/collector -job edgar -companies AAPL

## reingest-fundamentals: RE-canoniza los fundamentals ya ingeridos (plan
## M6c-T1 W3). Es OBLIGATORIO tras cambiar el catálogo XBRL
## (internal/collect/edgar/concepts.go) porque `make run-collector` corre con
## fresh=false y, al ser InsertStaging idempotente por (cik, source), las
## empresas ya ingeridas nunca vuelven a pasar por el canonizador: sin esto, el
## catálogo nuevo no cambiaría NI UNA fila de `fundamentals`.
##
## Qué hace, en orden y con el DSN redactado (mismo patrón que `integration`):
##   1. imprime el DSN redactado con bin/redactdsn (nunca el DSN en crudo);
##   2. resuelve la lista de empresas y la MUESTRA con su tamaño ANTES de tocar
##      nada. Sin lista explícita, el universo POR OMISIÓN es el selector
##      `staged` = las empresas con un payload `company_facts` YA NORMALIZADO en
##      `edgar_staging` (selector SQL de SOLO LECTURA,
##      `storage.StagedNormalizedCIKsSQL`; medido 2026-10-05: 42 empresas).
##      Deliberadamente NO es el catálogo `securities` (10.461 filas, de las que
##      ~42 se han ingerido): re-canonizar lo nunca descargado sería trabajo inútil
##      y una descarga masiva contra EDGAR. Las empresas con normalized=false
##      (normalización fallida) tampoco entran por defecto: reintentarlas es una
##      decisión de diagnóstico, no el comportamiento por omisión;
##   3. VALIDA la lista (nada de entradas vacías o malformadas) y, si no hay
##      nada que re-canonizar, ABORTA con exit 2 sin escribir nada;
##   4. corre el collector con `-fresh`, que borra el staging por CIK y
##      reinserta, de modo que cada payload vuelve a pasar por dedupeCanonical.
##
## Idempotente: `fundamentals` se upserta por
## (security_id, concept, period_type, period_end, fiscal_year, fiscal_period)
## con COALESCE, así que una 2.ª pasada no cambia filas (CA-9).
##
## PRECEDENCIA (explícita). Para verla con un dry-run, USA UN DSN REDACTADO:
##
##     make -n reingest-fundamentals DATABASE_URL='postgres://***@localhost:55432/abys'
##
## AVISO DE SEGURIDAD: `make -n` EXPANDE e IMPRIME la receta, y la última
## línea de la receta es `sh "$(DATABASE_URL)" "$(REINGEST_TICKERS)" ...`, así
## que `make -n` escribe el VALOR de `DATABASE_URL` en la terminal (y en los
## logs de CI). El `@` de la receta no lo evita: con `-n` make imprime la
## línea igual. Por eso el ejemplo de arriba lleva un DSN redactado explícito y
## el `make -n` NUNCA debe usarse con el DSN real: si `DATABASE_URL` viene del
## entorno con una contraseña real, eso la imprimiría.
## En ejecución NORMAL el contrato se cumple: la receta lleva `@` (no se
## repite la línea de comando) y el DSN que se imprime es SIEMPRE el que
## devuelve bin/redactdsn. Ver S1/W3E7.
##
##   1. REINGEST_TICKERS= ... lista explícita. Es el nombre PROPIO de este
##      target y gana sobre cualquier otra cosa.
##   2. TICKERS= ... SÓLO si viene de la línea de comandos
##      (`make reingest-fundamentals TICKERS="NVDA,WMT"`), y es un alias de
##      (1). NO se hereda ni el `TICKERS` del Makefile (=AAPL, para
##      precios/sector/scores) ni uno exportado en el entorno, y lo decide
##      `$(origin TICKERS)`: sin ese filtro, un `TICKERS` de otro target (o un
##      export en el shell) haría que `make reingest-fundamentals` re-canonizara
##      1 empresa pensando el operador que re-canoniza 42, o al revés.
##   3. UNIVERSE= ... sólo si 1 y 2 están vacíos; hoy el único selector es
##      `staged`. `UNIVERSE=` explícitamente vacío es un error (exit 2), no una
##      forma de pedir "el de por defecto": para eso se omite la variable.
##
## Variables:
##   REINGEST_TICKERS= lista explícita de tickers/CIKs (CSV). Tiene prioridad
##                sobre TICKERS y sobre UNIVERSE.
##   TICKERS=        alias aceptado SÓLO desde la línea de comandos (ver
##                la precedencia). En el Makefile vale AAPL y no se hereda aquí.
##   UNIVERSE=       selector del universo por omisión (sólo `staged` hoy).
##   DATABASE_URL=    BD destino (la de los demás targets; se redacta al
##                imprimir). POR OMISIÓN es la BD de DESARROLLO (`abys`), no
##                la de test: ver "BD destino y ausencia de guard" más abajo.
##   SEC_EDGAR_UA=   User-Agent para EDGAR (mismo que el resto de targets).
##
## BD DESTINO POR OMISIÓN = LA DE DESARROLLO, Y AQUÍ NO HAY GUARD `*_test`.
## No es un descuido de quien lo lea como una contradicción con `integration`:
## son contratos distintos. `integration` corre contra TEST_DATABASE_URL y ABORTA
## (exit 1) si el nombre no acaba en `_test`, porque lo que hace es TRUNCAR
## tablas compartidas. Este target NO lleva ese guard y su `DATABASE_URL` por
## omisión es la BD de DESARROLLO (`abys`, puerto 55432), porque Az6 exige
## ejecutar la re-ingesta AHÍ: es el único modo de que el trío
## interest_expense/income_tax_expense/pretax_income aparezca en `fundamentals`
## y la cobertura de interés deje de ser nil en el universo. El target ESCRIBE
## (borra el staging por CIK y reinserta), así que un guard de nombre de BD no
## sería una protección real: quien lo lance sobre otra base lo hace a
## sabiendas, pasando el `DATABASE_URL` que corresponda.
##
## DEUDA CONOCIDA,documentada aquí porque este target es el punto más cercano a
## su uso: `edgar.NormalizePending` —la rama que repasa pendientes cuando el
## staging YA existe, o sea el camino NO fresco de `make run-collector`— es
## GLOBAL: reprocesa hasta 5 filas de staging de CUALQUIER empresa, sin acotar
## por ticker. En el camino `-fresh` de este target NO se llega a él (el staging
## del CIK se borra, el `INSERT` devuelve id y la normalización va por
## `NormalizeStaging`, que procesa UN payload); la re-ingesta, por tanto, está
## pensada para BD de desarrollo/test, y acotar `NormalizePending` por ticker
## queda PENDIENTE (task propia).
REINGEST_TICKERS ?= $(if $(filter command line,$(origin TICKERS)),$(TICKERS),)
UNIVERSE         ?= staged
# De dónde salió la lista, para que el mensaje no mienta: `REINGEST_TICKERS`
# si el operador la pasó, `TICKERS` si vino de la línea de comandos, `-` si se
# usa el universo por omisión.
#
# OJO, CÓMO FUNCIONA EL `filter` DE ARRIBA (para que nadie lo lea como si
# comparara el origin entero contra la cadena "command line environment
# override"). `$(filter pattern...,text)` devuelve las PALABRAS de `text` que
# casan con el PATRÓN, y aquí `text` es UN solo origin. `$(origin V)` sólo
# puede devolver uno de: undefined, default, environment, environment override,
# file, command line, override, automatic. Desglosado:
#   * `environment` y `override` son origins válidos y cada uno casa por sí solo
#     (el patrón `override` no casa con `environment override`: son palabras
#     distintas, y `filter` no es un "empieza por").
#   * `command line` NO es un origin: es el único patrón de DOS PALABRAS de la
#     lista, y casa porque `filter` desglosa el patrón en `command` y `line` y
#     `command line` también desglosa su origin en esas dos. O sea que casa
#     de rebote, por token, y no porque el origin se llame así.
# Consecuencia práctica: hoy el resultado es correcto en los 4 casos
# verificados (línea de comandos, entorno, entorno con override, y `file`/sin
# origen), y el filtro NO filtra por valor, sólo por origen. Es deliberadamente
# frágil por lectura, no por error: si algún día hay que aceitar o rechazar un
# origin más, hay que añadir su palabra exacta a la lista, y un origin
# compuesto por varias palabras tiene que Casar token a token.
REINGEST_TICKERS_SOURCE := $(if $(filter command line environment override,$(origin REINGEST_TICKERS)),REINGEST_TICKERS,$(if $(filter command line,$(origin TICKERS)),TICKERS,-))
reingest-fundamentals: $(COLLECTOR_BIN) $(BIN_DIR)/redactdsn
	@sh -c 'set -eu; \
	dsn=$$1; tickers=$$2; universe=$$3; collector=$$4; ua=$$5; redactor=$$6; source=$$7; \
	if [ -z "$$dsn" ]; then echo "ERROR: DATABASE_URL vacio; reingest-fundamentals necesita una BD (no se ha escrito nada)" >&2; exit 2; fi; \
	case "$$universe" in "") echo "ERROR: UNIVERSE vacio. Omite la variable para usar el de por defecto (staged); no se ha escrito nada" >&2; exit 2;; esac; \
	redacted=$$("$$redactor" "$$dsn"); \
	echo "==> re-ingesta de fundamentals contra $$redacted"; \
	if [ -n "$$tickers" ]; then targets=$$tickers; \
		echo "==> universo: lista EXPLICITA ($$source); tiene prioridad sobre UNIVERSE=$$universe (que queda sin efecto)"; \
	else targets=$$(DATABASE_URL="$$dsn" $$collector -job universe -universe "$$universe") || exit $$?; \
		echo "==> universo: POR OMISION (selector UNIVERSE=$$universe = empresas con companyfacts ya normalizado en edgar_staging; no es el catalogo securities)"; \
	fi; \
	verdict=$$(printf "%s" "$$targets" | awk -F, " \
	BEGIN { n = 0; bad = \"\" } \
	{ for (i = 1; i <= NF; i++) { \
		e = \$$i; sub(/^[ \t\r]+/, \"\", e); sub(/[ \t\r]+\$$/, \"\", e); \
		if (e == \"\") { if (bad == \"\") bad = \"entrada #\" i \" VACIA\"; continue } \
		if (e ~ /[ \t]/) { if (bad == \"\") bad = \"entrada con ESPACIOS INTERNALES: \" e; continue } \
		if (e !~ /^[0-9A-Za-z._^-]+\$$/) { if (bad == \"\") bad = \"entrada MALFORMADA: \" e; continue } \
		n++ } } \
	END { if (bad != \"\") printf \"0\\t%s\\n\", bad; else printf \"%d\\t\\n\", n }"); \
	n=$$(printf "%s" "$$verdict" | cut -f1); problem=$$(printf "%s" "$$verdict" | cut -f2); \
	if [ "$$n" = "0" ]; then \
		if [ -z "$$problem" ]; then problem="universo VACIO: el selector $$universe no devuelve ninguna empresa (no hay companyfacts normalizado en edgar_staging)"; fi; \
		echo "ERROR: $$problem; no se ha escrito nada (reingest-fundamentals aborted)" >&2; exit 2; \
	fi; \
	echo "==> empresas a re-canonizar con -fresh: $$n"; \
	echo "==> lista: $$targets"; \
	echo "==> todavia NO se ha escrito nada; se llama al collector con -fresh a continuacion"; \
	DATABASE_URL="$$dsn" SEC_EDGAR_USER_AGENT="$$ua" $$collector -job edgar -fresh -companies "$$targets"' \
	sh "$(DATABASE_URL)" "$(REINGEST_TICKERS)" "$(UNIVERSE)" "$(COLLECTOR_BIN)" "$(SEC_EDGAR_UA)" "$(BIN_DIR)/redactdsn" "$(REINGEST_TICKERS_SOURCE)"

## run-prices: ingesta de precios Yahoo para tickers configurados
run-prices:
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/collector -job prices -tickers $(TICKERS)

## run-macro: ingesta de series macro (default: CPI)
run-macro:
	DATABASE_URL=$(DATABASE_URL) BLS_API_KEY="$(BLS_API_KEY)" go run ./cmd/collector -job macro -macro-series $(MACRO_SERIES)

## run-growth: motor de crecimiento y WACC por security (M6a: growth_metrics +
## wacc_metrics). Sin red: lee fundamentals/precios y la beta observada.
run-growth:
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/analytics -job growth -tickers $(TICKERS)

## run-valuation: motor de valoración 2.0.0 (M6b) → valuation_results.
## Consume el growth/wacc ya persistido y los FY as-of del último precio.
run-valuation:
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/analytics -job valuation -tickers $(TICKERS)

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

## run-quality: evalúa el motor de quality §13 (no persiste: su forma durable es
## el trace del score). Diagnóstico tras un backfill de fundamentals.
run-quality:
	DATABASE_URL=$(DATABASE_URL) GROWTH_RATE_DEFAULT=$(G) \
		go run ./cmd/analytics -job quality -tickers $(TICKERS)

## run-relative: igual que run-quality para el motor de relative §16.
run-relative:
	DATABASE_URL=$(DATABASE_URL) GROWTH_RATE_DEFAULT=$(G) \
		COMPARABLES_MIN_SECURITIES=$(COMP_MIN_SEC) \
		go run ./cmd/analytics -job relative -tickers $(TICKERS)

## run-backtest: replay de §28 — reproduce cada score 2.1.0 desde su trace y mide
## los retornos forward 20d/60d/365d sobre daily_prices.adjusted_close.
## NO persiste nada (no hay tablas de resultados). Variables:
##   TICKERS=         vacío = todos los tickers con score
##   BACKTEST_AS_OF=  vacío = el último score de cada ticker
##   PARAMETER_SET=   vacío = reproducción exacta (pesos del trace)
##   BACKTEST_JSON=1  salida JSON
run-backtest:
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/analytics -job backtest \
		-tickers "$(TICKERS)" -as-of "$(BACKTEST_AS_OF)" \
		-parameter-set "$(PARAMETER_SET)" $(if $(BACKTEST_JSON),-json,)

## run-all-data: pipeline E2E M3 + M6a + M6b (migrate → ingesta → sector →
## growth/wacc → valuation → métricas → score). El orden NO es cosmético:
## cada etapa consume la fila persistida por la anterior (M6b C2).
run-all-data:
	$(MAKE) migrate run-collector run-prices run-macro run-sector run-growth run-valuation run-analytics run-scores

## integration: tests de integración end-to-end (requiere BD de pruebas).
## Sin -p 1: la fase de BD de cada TestMain se serializa con un
## pg_advisory_lock de sesión (clave compartida 'abys_integration_lock'), de modo
## que los TRUNCATE compartidos no se deadlockean al correr paquetes en paralelo.
## Guard de seguridad: se ejecuta SIEMPRE contra TEST_DATABASE_URL (que debe
## apuntar a una BD con sufijo *_test). Si apunta a la BD de producción
## (p. ej. /abys o /5432/abys) se aborta para no truncar datos reales.
## El DSN se redacta con cmd/redactdsn (testsupport.RedactDSN), NO con un `sed`:
## el patrón `://[^/@]*@` era evadible con un `/` en la contraseña.
integration: $(BIN_DIR)/redactdsn
	@sh -c 'case "$(TEST_DATABASE_URL)" in */*_test?*) echo "==> integración contra BD de test: $$($(BIN_DIR)/redactdsn "$(TEST_DATABASE_URL)")";; *) echo "ERROR: TEST_DATABASE_URL debe apuntar a una BD *_test (no a la de producción); DSN: $$($(BIN_DIR)/redactdsn "$(TEST_DATABASE_URL)")" 1>&2; exit 1;; esac'
	@TEST_DATABASE_URL='$(TEST_DATABASE_URL)' $(MAKE) integration-run

integration-run:
	@DATABASE_URL=$(TEST_DATABASE_URL) go test ./... -count=1 -tags=integration

$(BIN_DIR)/redactdsn:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/redactdsn ./cmd/redactdsn

## bin/collector como prerrequisito de los targets que lo invocan (igual que
## bin/redactdsn): `reingest-fundamentals` necesita el binario para el listado
## read-only del universo y para la ingesta con -fresh.
##
## DEFECTO QUE ESTA REGLA EVITA (incidente M6c-T1-W3): la regla NO declaraba
## prerequisitos, así que make sólo construía el binario si el archivo NO
## existía. Con `bin/collector` ya presente (p. ej. de ayer), `make
## reingest-fundamentals` ejecutaba en SILENCIO un binario OBSOLETO. El
## 2026-10-06 el fix de resolución CIK->clase "no surtió efecto" por esto: hubo
## que recompilar a mano (`go build -o bin/collector ./cmd/collector`) porque el
## binario era de un día antes que las fuentes. Ahora cualquier fuente Go
## (cmd/**/*.go, internal/**/*.go) y go.mod/go.sum son prerequisitos: tocar una
## reconstruye. `find` existe en Linux y macOS; `2>/dev/null` evita que un error
## deje la regla sin prerequisitos (muda, como el defecto), y si `find` no
## devuelve nada se fuerza el rebuild con un prerequisito PHONY: nunca más un
## binario obsoleto silencioso.
COLLECTOR_SRCS := $(shell find cmd internal -name '*.go' 2>/dev/null)
COLLECTOR_DEPS := $(COLLECTOR_SRCS) go.mod go.sum
ifeq ($(strip $(COLLECTOR_SRCS)),)
FORCE_COLLECTOR_REBUILD := force-collector-rebuild
.PHONY: force-collector-rebuild
force-collector-rebuild:
endif
$(COLLECTOR_BIN): $(COLLECTOR_DEPS) $(FORCE_COLLECTOR_REBUILD)
	@mkdir -p $(BIN_DIR)
	go build -o $(COLLECTOR_BIN) ./cmd/collector

clean:
	rm -rf $(BIN_DIR)