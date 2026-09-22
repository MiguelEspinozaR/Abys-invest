package yahoo

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/collect/finviz"
	"github.com/miky/abys-invest/internal/storage"
)

// SectorEnricher resolves the sector/industry of a ticker.
type SectorEnricher interface {
	// GetSectorInfo returns the sector/industry or an error (the caller
	// falls back to the next source).
	GetSectorInfo(ctx context.Context, ticker string) (*SectorInfo, error)
}

// SectorInfo is the sector/industry resolved from any source.
type SectorInfo struct {
	Sector   *string
	Industry *string
}

// EnrichSectors resolves sector/industry for each ticker (Yahoo quoteSummary
// primary, Finviz fallback; plan D1 + risk table) and persists it in
// securities.sector/industry. It returns the number of tickers successfully
// enriched; individual failures are logged, never fatal.
func EnrichSectors(ctx context.Context, pool *pgxpool.Pool, tickers []string) (int, error) {
	yc := NewClient()
	if ua := os.Getenv("SEC_EDGAR_USER_AGENT"); ua != "" {
		yc = NewClient(WithUserAgent(ua))
	}
	return enrichSectors(ctx, pool, tickers, yc, finviz.NewClient())
}

// enrichSectors is the dependency-injectable implementation.
func enrichSectors(ctx context.Context, pool *pgxpool.Pool, tickers []string, yc *Client, fz *finviz.Client) (int, error) {
	if len(tickers) == 0 {
		return 0, nil
	}
	slog.Info("enriquecimiento de sector", "tickers", len(tickers), "fuente_primaria", "yahoo quoteSummary", "fallback", "finviz")
	updated := 0
	for _, raw := range tickers {
		ticker := strings.ToUpper(strings.TrimSpace(raw))
		if ticker == "" {
			continue
		}
		info, err := resolveSectorInfo(ctx, ticker, yc, fz)
		if err != nil {
			slog.Warn("sector no resoluble (queda NULL; comparables degradan)", "ticker", ticker, "error", err)
			continue
		}
		if err := storage.UpdateSecuritySector(ctx, pool, ticker, info.Sector, info.Industry); err != nil {
			slog.Warn("persistir sector falló", "ticker", ticker, "error", err)
			continue
		}
		updated++
		slog.Info("sector actualizado", "ticker", ticker, "sector", deref(info.Sector), "industry", deref(info.Industry))
	}
	return updated, nil
}

// sectorFallback abstracts the non-primary sector source (Finviz) so
// resolveSectorInfo is testable without network.
type sectorFallback interface {
	GetSectorInfo(ctx context.Context, ticker string) (*finviz.SectorInfo, error)
}

// resolveSectorInfo tries Yahoo quoteSummary first and Finviz as fallback.
func resolveSectorInfo(ctx context.Context, ticker string, yc *Client, fz sectorFallback) (*SectorInfo, error) {
	if qs, err := yc.GetQuoteSummary(ctx, ticker); err == nil && len(qs.QuoteSummary.Result) > 0 {
		ap := qs.QuoteSummary.Result[0].AssetProfile
		if ap.Sector != "" || ap.Industry != "" {
			return &SectorInfo{Sector: optional(ap.Sector), Industry: optional(ap.Industry)}, nil
		}
		return nil, fmt.Errorf("quoteSummary sin sector para %s", ticker)
	} else if err != nil {
		slog.Debug("yahoo quoteSummary no disponible, probando finviz", "ticker", ticker, "error", err)
	}

	if fi, err := fz.GetSectorInfo(ctx, ticker); err == nil {
		return &SectorInfo{Sector: optional(fi.Sector), Industry: optional(fi.Industry)}, nil
	} else {
		return nil, fmt.Errorf("finviz fallback falló para %s: %w", ticker, err)
	}
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
