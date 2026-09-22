// Command analytics computes the 8 MVP valuation metrics (SPEC §13) for the
// selected securities and materializes them into derived_metrics.
//
// Inputs: latest FY fundamentals (M1), latest daily price (M2), and the
// user growth parameter g (GROWTH_RATE_DEFAULT, default 7%).
//
// Usage:
//
//	go run ./cmd/analytics -tickers AAPL
//	go run ./cmd/analytics -g 8 -dry-run
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/metrics"
	"github.com/miky/abys-invest/internal/storage"
)

const (
	migrationsDir = "migrations"
	defaultGrowth = 7.0
)

// latestFYFundamentals fetches the newest FY row of every required concept.
// Missing concepts simply stay nil (conservative engine rule).
func latestFYFundamentals(ctx context.Context, pool *pgxpool.Pool, securityID int64, concepts []string) (map[string]*float64, error) {
	out := map[string]*float64{}
	rows, err := pool.Query(ctx, `SELECT DISTINCT ON (concept) concept, value
		FROM fundamentals
		WHERE security_id = $1 AND concept = ANY($2) AND fiscal_period = 'FY'
		ORDER BY concept, period_end DESC`, securityID, concepts)
	if err != nil {
		return nil, fmt.Errorf("analytics: fundamentales FY security %d: %w", securityID, err)
	}
	defer rows.Close()

	for rows.Next() {
		var concept string
		var value *float64
		if err := rows.Scan(&concept, &value); err != nil {
			return nil, fmt.Errorf("analytics: scan fundamentales: %w", err)
		}
		out[concept] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics: rows fundamentales: %w", err)
	}
	return out, nil
}

func main() {
	var tickersCSV string
	var growth float64
	var dryRun bool

	flag.StringVar(&tickersCSV, "tickers", "", "tickers a calcular (CSV); vacío = todos los active con precio")
	flag.Float64Var(&growth, "g", defaultGrowth, "tasa de crecimiento g para PEG (porcentaje)")
	flag.BoolVar(&dryRun, "dry-run", false, "calcula e imprime sin persistir en BD")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if v := os.Getenv("GROWTH_RATE_DEFAULT"); v != "" && growth == defaultGrowth {
		if parsed, err := parseGrowth(v); err == nil {
			growth = parsed
		} else {
			slog.Warn("GROWTH_RATE_DEFAULT inválido, usando default", "value", v, "error", err)
		}
	}
	if growth <= 0 {
		growth = defaultGrowth
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		slog.Error("DATABASE_URL requerido")
		os.Exit(1)
	}
	pool, err := storage.Connect(ctx, dsn)
	if err != nil {
		slog.Error("conexión a Postgres", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := storage.RunMigrations(ctx, pool, migrationsDir); err != nil {
		slog.Error("aplicación de migraciones", "error", err)
		os.Exit(1)
	}

	securities, err := resolveTargets(ctx, pool, tickersCSV)
	if err != nil {
		slog.Error("resolución de tickers", "error", err)
		os.Exit(1)
	}
	if len(securities) == 0 {
		slog.Warn("sin securities objetivo")
		return
	}

	concepts := []string{"net_earnings", "shares_outstanding", "shareholders_equity", "total_liabilities", "free_cash_flow"}

	succeeded := 0
	for _, sec := range securities {
		funds, err := latestFYFundamentals(ctx, pool, sec.ID, concepts)
		if err != nil {
			slog.Error("fundamentales fallaron (continúa)", "ticker", sec.Ticker, "error", err)
			continue
		}

		priceRow, err := storage.GetLatestPrice(ctx, pool, sec.ID)
		if err != nil {
			slog.Warn("sin precio para ticker (métricas price-based serán NULL o se omiten)", "ticker", sec.Ticker, "error", err)
			continue
		}

		input := metrics.MetricInput{
			SecurityID:         sec.ID,
			Ticker:             sec.Ticker,
			AsOf:               priceRow.Date,
			NetEarnings:        funds["net_earnings"],
			SharesOutstanding:  funds["shares_outstanding"],
			Price:              priceRow.Close,
			ShareholdersEquity: funds["shareholders_equity"],
			TotalLiabilities:   funds["total_liabilities"],
			FreeCashFlow:       funds["free_cash_flow"],
			GrowthRate:         growth,
		}

		rows, err := metrics.BuildDerivedMetrics(input, metrics.DefaultModelVersion)
		if err != nil {
			slog.Error("build de métricas falló (continúa)", "ticker", sec.Ticker, "error", err)
			continue
		}

		if dryRun {
			for _, m := range rows {
				logger.Info("métrica (dry-run)",
					"ticker", sec.Ticker, "as_of", m.AsOf.Format("2006-01-02"),
					"metric", m.Metric, "value", formatValue(m.Value))
			}
			succeeded++
			continue
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			slog.Error("begin tx métricas falló (continúa)", "ticker", sec.Ticker, "error", err)
			continue
		}
		if err := storage.UpsertDerivedMetrics(ctx, tx, rows); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			slog.Error("persistir métricas falló (continúa)", "ticker", sec.Ticker, "error", err)
			continue
		}
		if err := tx.Commit(ctx); err != nil {
			slog.Error("commit métricas falló (continúa)", "ticker", sec.Ticker, "error", err)
			continue
		}
		succeeded++
		slog.Info("métricas calculadas y persistidas", "ticker", sec.Ticker, "as_of", priceRow.Date.Format("2006-01-02"), "métricas", len(rows))
	}
	slog.Info("job analytics terminado", "exitosos", succeeded, "total", len(securities), "dry_run", dryRun)
}

// resolveTargets: explicit tickers or all active securities with a price.
func resolveTargets(ctx context.Context, pool *pgxpool.Pool, tickersCSV string) ([]storage.Security, error) {
	if strings.TrimSpace(tickersCSV) != "" {
		var out []storage.Security
		for _, t := range strings.Split(tickersCSV, ",") {
			t = strings.TrimSpace(strings.ToUpper(t))
			if t == "" {
				continue
			}
			sec, err := storage.GetSecurityByTicker(ctx, pool, t)
			if err != nil {
				return nil, fmt.Errorf("security %q: %w", t, err)
			}
			out = append(out, *sec)
		}
		return out, nil
	}

	ids, err := storage.ListSecuritiesWithPrices(ctx, pool)
	if err != nil {
		return nil, err
	}
	var out []storage.Security
	for _, id := range ids {
		sec, err := getSecurityByID(ctx, pool, id)
		if err != nil {
			slog.Warn("security omitida (id no resoluble)", "id", id, "error", err)
			continue
		}
		if sec.Status == "active" {
			out = append(out, *sec)
		}
	}
	return out, nil
}

// getSecurityByID fetches a security row by its primary key.
func getSecurityByID(ctx context.Context, pool *pgxpool.Pool, id int64) (*storage.Security, error) {
	row := pool.QueryRow(ctx, `SELECT id, ticker, cik, name, type, currency, status, exchange, sector, industry, created_at, updated_at
		FROM securities WHERE id = $1`, id)
	s := &storage.Security{}
	if err := row.Scan(&s.ID, &s.Ticker, &s.CIK, &s.Name, &s.Type, &s.Currency, &s.Status,
		&s.Exchange, &s.Sector, &s.Industry, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, fmt.Errorf("analytics: get security by id %d: %w", id, err)
	}
	return s, nil
}

func parseGrowth(v string) (float64, error) {
	var f float64
	if _, err := fmt.Sscanf(v, "%f", &f); err != nil {
		return 0, err
	}
	return f, nil
}

func formatValue(v *float64) string {
	if v == nil {
		return "NULL"
	}
	return fmt.Sprintf("%.6f", *v)
}
