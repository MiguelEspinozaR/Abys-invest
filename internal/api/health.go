// Body ampliado de GET /health (plan M5.2 §A1): tipos, constantes y helpers
// puros de las métricas de PostgreSQL. El handler sigue en middleware.go
// (deuda R11) y solo orquesta ping + collectHealthMetrics.
package api

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// HealthTableCount es una fila de la tabla `tables` de la respuesta: el nombre
// de la tabla y su conteo EXACTO de filas (no una estimación de pg_class).
type HealthTableCount struct {
	Name string `json:"name"`
	Rows int64  `json:"rows"`
}

// HealthResponse es el body de GET /health (SPEC §6, M5.2). Los tres campos
// base son el contrato M1 (intactos); los cuatro nuevos son aditivos y se
// OMITEN cuando no hay BD o cuando su query falla (decisión D3) — así el 503
// serializa exactamente las mismas 3 claves que en M1 (nada de `null`).
//
// `latency_ms` es la latencia del Ping() a PostgreSQL, NO el tiempo total del
// request (decisión D2, riesgo R10 documentado en el README).
type HealthResponse struct {
	Status   string `json:"status"`   // "ok" | "degraded"
	Database string `json:"database"` // "connected" | "disconnected"
	Version  string `json:"version"`  // api.Version

	LatencyMS       *float64           `json:"latency_ms,omitempty"`       // puntero: 0.00 debe salir
	PostgresVersion string             `json:"postgres_version,omitempty"` // "18.6"
	DBSize          string             `json:"db_size,omitempty"`          // "21 MB"
	Tables          []HealthTableCount `json:"tables,omitempty"`           // 9 entradas
}

// healthTableNames: esquema público de main_tables (SPEC §6 M5.2). Fuente única
// de verdad en Go: alimenta el SQL de conteos y el test de integración que lo
// compara con information_schema (anti-deriva, riesgo R2). Verificado
// 2026-09-27: exactamente estas 9 tablas, sin más.
var healthTableNames = []string{
	"securities", "daily_prices", "fundamentals", "derived_metrics",
	"scores", "watchlist", "macro_series", "edgar_staging", "xbrl_concept_map",
}

// healthTimeout es el presupuesto de tiempo de TODO el handler (ping + las 2
// queries de métricas, decisión D4): muy por debajo de los timeouts habituales
// de un probe de readiness, y cubre la (~20 ms) operación real medida. Con la
// BD colgada la respuesta nunca se queda colgada.
const healthTimeout = 3 * time.Second

// buildCountQuery construye la query de conteos EXACTOS: UNA sola sentencia
// UNION ALL con una rama count(*) por tabla (decisión D1) → 1 round-trip,
// snapshot consistente entre ramas y orden determinista. Medido: ~18 ms para
// las 9 tablas.
//
// Los nombres provienen de constantes internas (healthTableNames), nunca de la
// petición, así que no hay entrada de usuario en el SQL; aun así se filtran
// con healthTableNameRe (identificador SQL sobrio) y se cualifican con `public.`
// para no depender del search_path del servidor.
func buildCountQuery(names []string) string {
	var b strings.Builder
	for i, name := range names {
		if !healthTableNameRe.MatchString(name) {
			continue
		}
		if i > 0 {
			b.WriteString(" UNION ALL ")
		}
		b.WriteString("SELECT '")
		b.WriteString(name)
		b.WriteString("' AS name, count(*) AS rows FROM public.")
		b.WriteString(name)
	}
	return b.String()
}

// healthTableNameRe valida un identificador antes de interpolarlo en el SQL.
var healthTableNameRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// postgresVersionRe extrae el prefijo numérico de current_setting('server_version').
var postgresVersionRe = regexp.MustCompile(`^(\d+)(?:\.(\d+))?`)

// parsePostgresVersion deja solo el número de versión que devuelve el servidor
// (decisión D5): "18.6 (Ubuntu 18.6-1.pgdg26.04+2)" → "18.6", "16.4" → "16.4".
// Si el regex no casa se devuelve el valor crudo recortado (no se pierde el dato
// por un formato inesperado). Es una función pura: se testea sin BD.
func parsePostgresVersion(raw string) string {
	trimmed := strings.TrimSpace(raw)
	m := postgresVersionRe.FindStringSubmatch(trimmed)
	if m == nil {
		return trimmed
	}
	if m[2] == "" {
		return m[1]
	}
	return m[1] + "." + m[2]
}

// msSince devuelve los milisegundos transcurridos desde t con resolución de
// microsegundos (decisión D2: la latencia mostrada es la del Ping, p. ej. 0.43).
func msSince(t time.Time) float64 {
	return float64(time.Since(t).Microseconds()) / 1000
}

// collectHealthMetrics rellena las métricas ampliadas de /health de forma
// BEST-EFFORT (decisión D3): escribe en resp lo que consiga, deja `slog.Warn`
// en cada fallo y NUNCA degrada el status HTTP. Motivo: /health es el probe de
// readiness (systemd, plan M4) y una métrica secundaria no puede degradarlo;
// el frontend pinta "—" donde falte el campo.
//
//  1. versión del servidor + tamaño de la BD (1 query);
//  2. conteos exactos por tabla (1 query UNION ALL, decisión D1).
func collectHealthMetrics(ctx context.Context, pool *pgxpool.Pool, resp *HealthResponse) {
	var rawVersion, dbSize string
	err := pool.QueryRow(ctx,
		`SELECT current_setting('server_version') AS server_version, `+
			`pg_size_pretty(pg_database_size(current_database())) AS db_size`,
	).Scan(&rawVersion, &dbSize)
	if err != nil {
		slog.Warn("health: no se pudo leer la versión/tamaño de PostgreSQL", "error", err)
	} else {
		resp.PostgresVersion = parsePostgresVersion(rawVersion)
		resp.DBSize = dbSize
	}

	rows, err := pool.Query(ctx, buildCountQuery(healthTableNames))
	if err != nil {
		slog.Warn("health: no se pudieron obtener los conteos de tablas", "error", err)
		return
	}
	defer rows.Close()

	byName := make(map[string]int64, len(healthTableNames))
	for rows.Next() {
		var name string
		var n int64
		if err := rows.Scan(&name, &n); err != nil {
			slog.Warn("health: fila de conteo ilegible; se omite `tables`", "error", err)
			return
		}
		byName[name] = n
	}
	if err := rows.Err(); err != nil {
		slog.Warn("health: lectura de conteos incompleta; se omite `tables`", "error", err)
		return
	}

	// Se reconstruye en el orden de healthTableNames: la respuesta es
	// determinista aunque la query devuelva las filas en otro orden.
	tables := make([]HealthTableCount, 0, len(healthTableNames))
	for _, name := range healthTableNames {
		if n, ok := byName[name]; ok {
			tables = append(tables, HealthTableCount{Name: name, Rows: n})
		}
	}
	if len(tables) > 0 {
		resp.Tables = tables
	}
}

// ptr devuelve un puntero al valor (para los campos *float64 de HealthResponse:
// nil = omitido, puntero a 0 = 0.00, que `omitempty` sobre un float borraría).
func ptr[T any](v T) *T { return &v }
